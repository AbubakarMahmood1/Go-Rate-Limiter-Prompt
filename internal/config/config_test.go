package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func clearOverrides(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"PORT", "STORE", "REDIS_ADDR", "REDIS_PASSWORD",
		"ALLOW_POLICY_OVERRIDES", "RESET_TOKEN",
	} {
		t.Setenv(name, "")
	}
}

func TestLoad_ParsesDurationsTiersAndExplicitFalse(t *testing.T) {
	clearOverrides(t)
	path := writeConfig(t, `
server:
  port: 9000
  read_timeout: 500ms
  decision_timeout: 750ms
store: memory
algorithms:
  default: sliding_window
api:
  allow_policy_overrides: true
limits:
  default:
    requests: 42
    window: 90s
  tiers:
    premium:
      requests: 1000
      window: 1h
      burst: 1200
metrics:
  enabled: false
`)

	cfg, err := Load(path)
	require.NoError(t, err)

	assert.Equal(t, 9000, cfg.Server.Port)
	assert.Equal(t, 500*time.Millisecond, cfg.Server.ReadTimeout.Std())
	assert.Equal(t, 750*time.Millisecond, cfg.Server.DecisionTimeout.Std())
	assert.Equal(t, AlgorithmSlidingWindow, cfg.Algorithms.Default)
	assert.True(t, cfg.API.AllowPolicyOverrides)
	assert.Equal(t, 42, cfg.Limits.Default.Requests)
	assert.Equal(t, 90*time.Second, cfg.Limits.Default.Window.Std())
	require.Contains(t, cfg.Limits.Tiers, "premium")
	assert.Equal(t, time.Hour, cfg.Limits.Tiers["premium"].Window.Std())
	assert.Equal(t, 1200, cfg.Limits.Tiers["premium"].Burst)
	assert.False(t, cfg.Metrics.Enabled)

	// Omitted values inherit defaults before decoding.
	assert.Equal(t, 10*time.Second, cfg.Server.WriteTimeout.Std())
	assert.Equal(t, "/metrics", cfg.Metrics.Path)
}

func TestLoad_EmptyFileUsesDefaults(t *testing.T) {
	clearOverrides(t)
	cfg, err := Load(writeConfig(t, "# defaults only\n"))
	require.NoError(t, err)
	assert.Equal(t, 8080, cfg.Server.Port)
	assert.Equal(t, StoreMemory, cfg.Store)
	assert.Equal(t, time.Minute, cfg.Limits.Default.Window.Std())
}

func TestLoad_RejectsInvalidConfig(t *testing.T) {
	clearOverrides(t)
	cases := map[string]string{
		"bad duration":           "limits:\n  default:\n    requests: 10\n    window: sixty",
		"numeric duration":       "server:\n  read_timeout: 5",
		"unknown key":            "server:\n  write_timout: 5s",
		"second document":        "store: memory\n---\nstore: memory",
		"unknown store":          "store: postgres",
		"unknown algorithm":      "algorithms:\n  default: leaky_bucket",
		"explicit zero port":     "server:\n  port: 0",
		"explicit zero timeout":  "server:\n  decision_timeout: 0s",
		"zero requests":          "limits:\n  default:\n    requests: 0\n    window: 1m",
		"negative requests":      "limits:\n  default:\n    requests: -1\n    window: 1m",
		"sub-micro window":       "limits:\n  default:\n    requests: 10\n    window: 1ns",
		"fractional microsecond": "limits:\n  default:\n    requests: 10\n    window: 1500ns",
		"zero tier window":       "limits:\n  tiers:\n    free:\n      requests: 10\n      window: 0s",
		"reserved tier name":     "limits:\n  tiers:\n    default:\n      requests: 10\n      window: 1m",
		"invalid tier name":      "limits:\n  tiers:\n    'bad tier':\n      requests: 10\n      window: 1m",
		"multiple redis":         "redis:\n  addresses: [redis-a:6379, redis-b:6379]",
		"bad metrics path":       "metrics:\n  path: metrics",
		"route conflict":         "metrics:\n  path: /v1/check",
		"unnormalized path":      "metrics:\n  path: /ops/../metrics",
		"metrics query":          "metrics:\n  path: /metrics?format=text",
		"metrics fragment":       "metrics:\n  path: /metrics#internal",
		"port out of range":      "server:\n  port: 70000",
		"yaml reset secret":      "api:\n  reset_token: do-not-accept-secrets-here",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeConfig(t, content))
			assert.Error(t, err)
		})
	}
}

func TestLoad_MissingFileErrors(t *testing.T) {
	clearOverrides(t)
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	assert.Error(t, err)
}

func TestEnvOverrides(t *testing.T) {
	clearOverrides(t)
	t.Setenv("PORT", "9999")
	t.Setenv("STORE", "redis")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("REDIS_PASSWORD", "secret")
	t.Setenv("ALLOW_POLICY_OVERRIDES", "true")
	t.Setenv("RESET_TOKEN", "0123456789abcdef")

	cfg, err := Default()
	require.NoError(t, err)

	assert.Equal(t, 9999, cfg.Server.Port)
	assert.Equal(t, StoreRedis, cfg.Store)
	assert.Equal(t, []string{"redis:6379"}, cfg.Redis.Addresses)
	assert.Equal(t, "secret", cfg.Redis.Password)
	assert.True(t, cfg.API.AllowPolicyOverrides)
	assert.Equal(t, "0123456789abcdef", cfg.API.ResetToken)
}

func TestEnvOverrides_RejectInvalidValues(t *testing.T) {
	clearOverrides(t)
	t.Run("port", func(t *testing.T) {
		t.Setenv("PORT", "eighty")
		_, err := Default()
		assert.Error(t, err)
	})
	t.Run("boolean", func(t *testing.T) {
		t.Setenv("ALLOW_POLICY_OVERRIDES", "sometimes")
		_, err := Default()
		assert.Error(t, err)
	})
	t.Run("short reset token", func(t *testing.T) {
		t.Setenv("RESET_TOKEN", "short")
		_, err := Default()
		assert.Error(t, err)
	})
	t.Run("comma separated redis", func(t *testing.T) {
		t.Setenv("REDIS_ADDR", "redis-a:6379,redis-b:6379")
		_, err := Default()
		assert.Error(t, err)
	})
}

func TestDefault_IsValidAndIndependent(t *testing.T) {
	clearOverrides(t)
	first, err := Default()
	require.NoError(t, err)
	second, err := Default()
	require.NoError(t, err)

	assert.Equal(t, 8080, first.Server.Port)
	assert.Equal(t, StoreMemory, first.Store)
	assert.Equal(t, AlgorithmTokenBucket, first.Algorithms.Default)
	assert.Equal(t, 100, first.Limits.Default.Requests)
	assert.Equal(t, time.Minute, first.Limits.Default.Window.Std())
	assert.Equal(t, 2*time.Second, first.Server.DecisionTimeout.Std())
	assert.True(t, first.Metrics.Enabled)
	assert.False(t, first.API.AllowPolicyOverrides)

	first.Redis.Addresses[0] = strings.Repeat("x", 10)
	assert.Equal(t, "localhost:6379", second.Redis.Addresses[0], "defaults must not share slices")
}
