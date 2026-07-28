// Package config loads and validates the service configuration from YAML,
// with environment-variable overrides for deployment-specific settings.
package config

import (
	"bytes"
	"fmt"
	"io"
	"math/big"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Store backends.
const (
	StoreMemory = "memory"
	StoreRedis  = "redis"
)

// Algorithm names, as used in configuration and the HTTP API.
const (
	AlgorithmTokenBucket   = "token_bucket"
	AlgorithmSlidingWindow = "sliding_window"
	AlgorithmFixedWindow   = "fixed_window"
)

const (
	maxExactCount = int64(1<<52 - 1)
	maxDuration   = time.Duration(1<<63 - 1)
)

var tierNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// Algorithms lists every implemented algorithm.
var Algorithms = []string{AlgorithmTokenBucket, AlgorithmSlidingWindow, AlgorithmFixedWindow}

// Duration wraps time.Duration so YAML values like "500ms", "5s" or "1m"
// parse; yaml.v3 has no native support for duration strings.
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var text string
	if err := value.Decode(&text); err != nil {
		return fmt.Errorf("duration must be a string like \"30s\": %w", err)
	}
	parsed, err := time.ParseDuration(text)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", text, err)
	}
	*d = Duration(parsed)
	return nil
}

// Std returns the wrapped time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Config is the root of the service configuration.
type Config struct {
	Server     ServerConfig     `yaml:"server"`
	Store      string           `yaml:"store"` // memory or redis
	Redis      RedisConfig      `yaml:"redis"`
	Algorithms AlgorithmsConfig `yaml:"algorithms"`
	API        APIConfig        `yaml:"api"`
	Limits     LimitsConfig     `yaml:"limits"`
	Metrics    MetricsConfig    `yaml:"metrics"`
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Port              int      `yaml:"port"`
	ReadHeaderTimeout Duration `yaml:"read_header_timeout"`
	ReadTimeout       Duration `yaml:"read_timeout"`
	WriteTimeout      Duration `yaml:"write_timeout"`
	IdleTimeout       Duration `yaml:"idle_timeout"`
	DecisionTimeout   Duration `yaml:"decision_timeout"`
}

// RedisConfig holds the one supported Redis deployment shape: standalone.
type RedisConfig struct {
	Addresses []string `yaml:"addresses"`
	Password  string   `yaml:"password"`
	DB        int      `yaml:"db"`
	PoolSize  int      `yaml:"pool_size"`
}

// AlgorithmsConfig selects the algorithm used when a request names none.
type AlgorithmsConfig struct {
	Default string `yaml:"default"`
}

// APIConfig controls trust-boundary behavior. ResetToken is intentionally
// environment-only so a destructive-control secret is not committed in YAML.
type APIConfig struct {
	AllowPolicyOverrides bool   `yaml:"allow_policy_overrides"`
	ResetToken           string `yaml:"-"`
}

// LimitsConfig holds the default limit plus optional named tiers that trusted
// callers may select.
type LimitsConfig struct {
	Default LimitConfig            `yaml:"default"`
	Tiers   map[string]LimitConfig `yaml:"tiers"`
}

// LimitConfig is one rate-limit definition.
type LimitConfig struct {
	Requests int      `yaml:"requests"` // permits per window
	Window   Duration `yaml:"window"`
	Burst    int      `yaml:"burst"` // token-bucket capacity; 0 means Requests
}

// MetricsConfig controls the Prometheus endpoint.
type MetricsConfig struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}

// Load reads filename into a pre-populated default configuration, applies
// environment overrides, then validates the result. Pre-population means
// omitted fields receive defaults while an explicitly supplied zero remains
// zero and fails validation. Unknown keys and extra YAML documents are errors.
func Load(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	cfg := defaultConfig()
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil && err != io.EOF {
		return nil, fmt.Errorf("parsing %s: %w", filename, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parsing %s: multiple YAML documents are not supported", filename)
		}
		return nil, fmt.Errorf("parsing %s: %w", filename, err)
	}

	if err := finish(cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", filename, err)
	}
	return cfg, nil
}

// Default returns the built-in configuration with environment overrides.
func Default() (*Config, error) {
	cfg := defaultConfig()
	if err := finish(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func defaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Port:              8080,
			ReadHeaderTimeout: Duration(2 * time.Second),
			ReadTimeout:       Duration(5 * time.Second),
			WriteTimeout:      Duration(10 * time.Second),
			IdleTimeout:       Duration(120 * time.Second),
			DecisionTimeout:   Duration(2 * time.Second),
		},
		Store: StoreMemory,
		Redis: RedisConfig{
			Addresses: []string{"localhost:6379"},
			PoolSize:  100,
		},
		Algorithms: AlgorithmsConfig{Default: AlgorithmTokenBucket},
		Limits: LimitsConfig{
			Default: LimitConfig{Requests: 100, Window: Duration(time.Minute)},
		},
		Metrics: MetricsConfig{Enabled: true, Path: "/metrics"},
	}
}

func finish(cfg *Config) error {
	if err := applyEnv(cfg); err != nil {
		return err
	}
	normalize(cfg)
	return validate(cfg)
}

func applyEnv(cfg *Config) error {
	if value := os.Getenv("PORT"); value != "" {
		port, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid PORT %q: %w", value, err)
		}
		cfg.Server.Port = port
	}
	if value := os.Getenv("STORE"); value != "" {
		cfg.Store = value
	}
	if value := os.Getenv("REDIS_ADDR"); value != "" {
		cfg.Redis.Addresses = []string{value}
	}
	if value, ok := os.LookupEnv("REDIS_PASSWORD"); ok {
		cfg.Redis.Password = value
	}
	if value := os.Getenv("ALLOW_POLICY_OVERRIDES"); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid ALLOW_POLICY_OVERRIDES %q: %w", value, err)
		}
		cfg.API.AllowPolicyOverrides = parsed
	}
	if value, ok := os.LookupEnv("RESET_TOKEN"); ok {
		cfg.API.ResetToken = value
	}
	return nil
}

func normalize(cfg *Config) {
	cfg.Store = strings.TrimSpace(cfg.Store)
	cfg.Algorithms.Default = strings.TrimSpace(cfg.Algorithms.Default)
	for i := range cfg.Redis.Addresses {
		cfg.Redis.Addresses[i] = strings.TrimSpace(cfg.Redis.Addresses[i])
	}
	cfg.Metrics.Path = strings.TrimSpace(cfg.Metrics.Path)
}

func validate(cfg *Config) error {
	if cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
		return fmt.Errorf("server.port %d out of range", cfg.Server.Port)
	}
	for name, value := range map[string]Duration{
		"server.read_header_timeout": cfg.Server.ReadHeaderTimeout,
		"server.read_timeout":        cfg.Server.ReadTimeout,
		"server.write_timeout":       cfg.Server.WriteTimeout,
		"server.idle_timeout":        cfg.Server.IdleTimeout,
		"server.decision_timeout":    cfg.Server.DecisionTimeout,
	} {
		if value <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}

	if cfg.Store != StoreMemory && cfg.Store != StoreRedis {
		return fmt.Errorf("store must be %q or %q, got %q", StoreMemory, StoreRedis, cfg.Store)
	}
	if len(cfg.Redis.Addresses) != 1 || cfg.Redis.Addresses[0] == "" || strings.Contains(cfg.Redis.Addresses[0], ",") {
		return fmt.Errorf("redis.addresses must contain exactly one non-empty standalone Redis address")
	}
	if cfg.Redis.DB < 0 {
		return fmt.Errorf("redis.db must not be negative")
	}
	if cfg.Redis.PoolSize <= 0 {
		return fmt.Errorf("redis.pool_size must be positive")
	}

	if !contains(Algorithms, cfg.Algorithms.Default) {
		return fmt.Errorf("algorithms.default must be one of %s, got %q",
			strings.Join(Algorithms, ", "), cfg.Algorithms.Default)
	}

	if cfg.API.ResetToken != "" && len(cfg.API.ResetToken) < 16 {
		return fmt.Errorf("RESET_TOKEN must contain at least 16 bytes when set")
	}

	if err := validateLimit("limits.default", cfg.Limits.Default); err != nil {
		return err
	}
	for name, tier := range cfg.Limits.Tiers {
		if name == "default" {
			return fmt.Errorf("limits.tiers must not define a tier named %q; use limits.default", name)
		}
		if !tierNamePattern.MatchString(name) {
			return fmt.Errorf("limits.tiers.%s: tier name must match %s", name, tierNamePattern.String())
		}
		if err := validateLimit("limits.tiers."+name, tier); err != nil {
			return err
		}
	}

	if cfg.Metrics.Path == "" || !strings.HasPrefix(cfg.Metrics.Path, "/") {
		return fmt.Errorf("metrics.path must be an absolute HTTP path")
	}
	if strings.ContainsAny(cfg.Metrics.Path, "?#") {
		return fmt.Errorf("metrics.path must not contain a query or fragment")
	}
	if path.Clean(cfg.Metrics.Path) != cfg.Metrics.Path {
		return fmt.Errorf("metrics.path must be normalized, got %q", cfg.Metrics.Path)
	}
	if cfg.Metrics.Path == "/health" || cfg.Metrics.Path == "/v1" || strings.HasPrefix(cfg.Metrics.Path, "/v1/") {
		return fmt.Errorf("metrics.path %q conflicts with a service route", cfg.Metrics.Path)
	}
	return nil
}

func validateLimit(name string, limit LimitConfig) error {
	if limit.Requests <= 0 || int64(limit.Requests) > maxExactCount {
		return fmt.Errorf("%s.requests must be in [1, %d], got %d", name, maxExactCount, limit.Requests)
	}
	window := limit.Window.Std()
	maxWindow := (maxDuration - time.Second) / 2
	if window < time.Microsecond || window > maxWindow {
		return fmt.Errorf("%s.window must be between 1µs and %s, got %s", name, maxWindow, window)
	}
	if window%time.Microsecond != 0 {
		return fmt.Errorf("%s.window must be a whole number of microseconds, got %s", name, window)
	}
	if limit.Burst < 0 || int64(limit.Burst) > maxExactCount {
		return fmt.Errorf("%s.burst must be in [0, %d], got %d", name, maxExactCount, limit.Burst)
	}

	capacity := limit.Burst
	if capacity == 0 {
		capacity = limit.Requests
	}
	// Token-bucket retention is ceil(window*capacity/requests)+1s. Check
	// the multiplication in integer space before algorithms construct it.
	numerator := new(big.Int).Mul(big.NewInt(int64(window)), big.NewInt(int64(capacity)))
	maximum := new(big.Int).Mul(big.NewInt(int64(maxDuration-time.Second)), big.NewInt(int64(limit.Requests)))
	if numerator.Cmp(maximum) > 0 {
		return fmt.Errorf("%s burst/window combination makes token-bucket retention overflow", name)
	}
	return nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
