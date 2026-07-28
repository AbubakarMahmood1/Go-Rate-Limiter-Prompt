package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/AbubakarMahmood/go-rate-limiter/pkg/limiter"
	"github.com/redis/go-redis/v9"
)

// RedisStore is a Redis-backed limiter.Store for distributed deployments.
// Every decision executes as one Lua script, so concurrent checks from any
// number of service instances serialize on Redis and cannot over-admit.
// Scripts use Redis TIME, keeping decisions independent of application-clock
// skew.
//
// The verified contract is one standalone Redis endpoint. Cluster, Sentinel,
// failover, and multi-region behavior are intentionally not advertised.
type RedisStore struct {
	client *redis.Client
}

// RedisConfig holds Redis connection configuration.
type RedisConfig struct {
	Addresses []string // exactly one standalone Redis address is supported
	Password  string
	DB        int
	PoolSize  int // 0 uses go-redis's default
}

// NewRedisStore connects to one standalone Redis endpoint and verifies it.
func NewRedisStore(config RedisConfig) (*RedisStore, error) {
	if len(config.Addresses) != 1 {
		return nil, fmt.Errorf("redis requires exactly one standalone address")
	}
	address := strings.TrimSpace(config.Addresses[0])
	if address == "" || strings.Contains(address, ",") {
		return nil, fmt.Errorf("redis requires exactly one non-empty standalone address")
	}
	if config.DB < 0 {
		return nil, fmt.Errorf("redis db must not be negative")
	}
	if config.PoolSize < 0 {
		return nil, fmt.Errorf("redis pool size must not be negative")
	}

	client := redis.NewClient(&redis.Options{
		Addr:     address,
		Password: config.Password,
		DB:       config.DB,
		PoolSize: config.PoolSize,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("connecting to redis: %w", err)
	}

	return &RedisStore{client: client}, nil
}

// incrWindowScript implements fixed- and weighted sliding-window admission in
// one atomic step. Numeric fields are keyed by window start (unix
// microseconds); __last_now records the latest decision clock committed by a
// permit-consuming write, preventing a host-clock rollback from moving a key
// into an older window. Peeks and denials do not modify permit state.
//
// KEYS[1] counter key
// ARGV[1] window length, microseconds
// ARGV[2] permits requested (0 = read-only peek)
// ARGV[3] limit
// ARGV[4] 1 = weigh previous window, 0 = fixed-window semantics
// ARGV[5] TTL, seconds
//
// Returns {allowed, current, previous, windowStartMicros, nowMicros}.
var incrWindowScript = redis.NewScript(`
local window = tonumber(ARGV[1])
local n = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local weigh_prev = tonumber(ARGV[4]) == 1
local ttl = tonumber(ARGV[5])

local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000000 + tonumber(t[2])
local last_now = tonumber(redis.call('HGET', KEYS[1], '__last_now'))
if last_now ~= nil and now < last_now then
	now = last_now
end

local cur_start = now - (now % window)
local prev_start = cur_start - window

-- tostring may use scientific notation for large values; explicit formatting
-- preserves exact microsecond field names below 2^53.
local cur_field = string.format('%.0f', cur_start)
local prev_field = string.format('%.0f', prev_start)

local cur = tonumber(redis.call('HGET', KEYS[1], cur_field) or '0')
local prev = 0
if weigh_prev then
	prev = tonumber(redis.call('HGET', KEYS[1], prev_field) or '0')
end

local weighted = cur
if weigh_prev and prev > 0 then
	weighted = cur + prev * (1 - (now - cur_start) / window)
end

local allowed = 0
if weighted + n <= limit then
	allowed = 1
	if n > 0 then
		cur = redis.call('HINCRBY', KEYS[1], cur_field, n)
		redis.call('HSET', KEYS[1], '__last_now', string.format('%.0f', now))
		for _, field in ipairs(redis.call('HKEYS', KEYS[1])) do
			local field_time = tonumber(field)
			if field_time ~= nil and field_time < prev_start then
				redis.call('HDEL', KEYS[1], field)
			end
		end
		redis.call('EXPIRE', KEYS[1], ttl)
	end
end

return {allowed, cur, prev, string.format('%.0f', cur_start), string.format('%.0f', now)}
`)

// takeTokensScript implements token-bucket admission in one atomic step.
// State is the token count plus a Redis-clock timestamp in integer unix
// microseconds. A clock rollback is clamped to the last committed timestamp.
// Denials consume nothing; peeks and denials do not write state.
//
// KEYS[1] bucket key
// ARGV[1] capacity
// ARGV[2] refill rate, tokens per second
// ARGV[3] tokens requested (0 = read-only peek)
// ARGV[4] TTL, seconds
//
// Returns {allowed, tokensAfter, nowMicros}.
var takeTokensScript = redis.NewScript(`
local capacity = tonumber(ARGV[1])
local refill = tonumber(ARGV[2])
local n = tonumber(ARGV[3])
local ttl = tonumber(ARGV[4])

local t = redis.call('TIME')
local now_us = tonumber(t[1]) * 1000000 + tonumber(t[2])

-- Read the legacy fractional-second field too so upgrades do not reset
-- existing buckets.
local state = redis.call('HMGET', KEYS[1], 'tokens', 'ts_us', 'ts')
local tokens = tonumber(state[1])
local ts_us = tonumber(state[2])
if ts_us == nil and state[3] ~= false then
	ts_us = tonumber(state[3]) * 1000000
end
if tokens == nil or ts_us == nil then
	tokens = capacity
	ts_us = now_us
end
if now_us < ts_us then
	now_us = ts_us
end

local elapsed = (now_us - ts_us) / 1000000
if elapsed > 0 then
	tokens = tokens + elapsed * refill
	if tokens > capacity then
		tokens = capacity
	end
end

local allowed = 0
if n <= tokens then
	allowed = 1
	if n > 0 then
		tokens = tokens - n
		redis.call('HSET', KEYS[1],
			'tokens', string.format('%.17g', tokens),
			'ts_us', string.format('%.0f', now_us))
		redis.call('HDEL', KEYS[1], 'ts')
		redis.call('EXPIRE', KEYS[1], ttl)
	end
end

return {allowed, string.format('%.17g', tokens), string.format('%.0f', now_us)}
`)

// IncrWindow implements limiter.Store.
func (rs *RedisStore) IncrWindow(ctx context.Context, key string, window time.Duration, n, limit int64, weightPrev bool, ttl time.Duration) (*limiter.WindowResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateWindowOperation(window, n, limit, weightPrev, ttl); err != nil {
		return nil, err
	}

	weigh := 0
	if weightPrev {
		weigh = 1
	}
	raw, err := incrWindowScript.Run(ctx, rs.client, []string{"rl:" + key},
		window.Microseconds(), n, limit, weigh, ttlSeconds(ttl)).Result()
	if err != nil {
		return nil, fmt.Errorf("redis window increment: %w", err)
	}

	reply, ok := raw.([]interface{})
	if !ok || len(reply) != 5 {
		return nil, fmt.Errorf("redis window increment: unexpected reply %T", raw)
	}

	var fields [5]int64
	for i, value := range reply {
		parsed, err := replyInt(value)
		if err != nil {
			return nil, fmt.Errorf("redis window increment field %d: %w", i, err)
		}
		fields[i] = parsed
	}

	return &limiter.WindowResult{
		Allowed:     fields[0] == 1,
		Current:     fields[1],
		Previous:    fields[2],
		WindowStart: time.UnixMicro(fields[3]),
		Now:         time.UnixMicro(fields[4]),
	}, nil
}

// TakeTokens implements limiter.Store.
func (rs *RedisStore) TakeTokens(ctx context.Context, key string, capacity, refillPerSec, n float64, ttl time.Duration) (*limiter.TokenResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateTokenOperation(capacity, refillPerSec, n, ttl); err != nil {
		return nil, err
	}

	raw, err := takeTokensScript.Run(ctx, rs.client, []string{"rl:" + key},
		formatFloat(capacity), formatFloat(refillPerSec), formatFloat(n), ttlSeconds(ttl)).Result()
	if err != nil {
		return nil, fmt.Errorf("redis token take: %w", err)
	}

	reply, ok := raw.([]interface{})
	if !ok || len(reply) != 3 {
		return nil, fmt.Errorf("redis token take: unexpected reply %T", raw)
	}

	allowed, err := replyInt(reply[0])
	if err != nil {
		return nil, fmt.Errorf("redis token take allowed: %w", err)
	}
	tokens, err := replyFloat(reply[1])
	if err != nil {
		return nil, fmt.Errorf("redis token take balance: %w", err)
	}
	nowUS, err := replyInt(reply[2])
	if err != nil {
		return nil, fmt.Errorf("redis token take clock: %w", err)
	}
	return &limiter.TokenResult{
		Allowed: allowed == 1,
		Tokens:  tokens,
		Now:     time.UnixMicro(nowUS),
	}, nil
}

// Delete implements limiter.Store.
func (rs *RedisStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := rs.client.Del(ctx, "rl:"+key).Err(); err != nil {
		return fmt.Errorf("redis delete: %w", err)
	}
	return nil
}

// Ping implements limiter.Store.
func (rs *RedisStore) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := rs.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping: %w", err)
	}
	return nil
}

// Close implements limiter.Store.
func (rs *RedisStore) Close() error { return rs.client.Close() }

// ttlSeconds converts a duration to whole seconds for EXPIRE, rounding up so
// state never expires before the algorithm expects it to.
func ttlSeconds(ttl time.Duration) int64 {
	seconds := int64(ttl / time.Second)
	if ttl%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	return seconds
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func replyInt(value interface{}) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case string:
		return strconv.ParseInt(typed, 10, 64)
	case []byte:
		return strconv.ParseInt(string(typed), 10, 64)
	default:
		return 0, fmt.Errorf("unexpected reply element %T", value)
	}
}

func replyFloat(value interface{}) (float64, error) {
	switch typed := value.(type) {
	case string:
		return strconv.ParseFloat(typed, 64)
	case []byte:
		return strconv.ParseFloat(string(typed), 64)
	case int64:
		return float64(typed), nil
	case float64:
		return typed, nil
	default:
		return 0, fmt.Errorf("unexpected reply element %T", value)
	}
}
