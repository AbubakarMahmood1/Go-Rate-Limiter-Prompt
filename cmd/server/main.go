// Command server runs the rate-limiter HTTP service.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/AbubakarMahmood/go-rate-limiter/internal/algorithms"
	"github.com/AbubakarMahmood/go-rate-limiter/internal/config"
	"github.com/AbubakarMahmood/go-rate-limiter/internal/handlers"
	"github.com/AbubakarMahmood/go-rate-limiter/internal/metrics"
	"github.com/AbubakarMahmood/go-rate-limiter/internal/store"
	"github.com/AbubakarMahmood/go-rate-limiter/pkg/limiter"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("service stopped with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := loadConfig(logger)
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	logger.Info("configuration loaded",
		"store", cfg.Store,
		"default_algorithm", cfg.Algorithms.Default,
		"default_requests", cfg.Limits.Default.Requests,
		"default_window", cfg.Limits.Default.Window.Std(),
		"tiers", len(cfg.Limits.Tiers),
		"policy_overrides", cfg.API.AllowPolicyOverrides,
		"reset_enabled", cfg.API.ResetToken != "",
	)

	st, err := newStore(cfg)
	if err != nil {
		return fmt.Errorf("store initialization: %w", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			logger.Error("store close failed", "error", err)
		}
	}()

	limiters, err := buildLimiters(cfg, st)
	if err != nil {
		return fmt.Errorf("limiter initialization: %w", err)
	}
	var m *metrics.Metrics
	if cfg.Metrics.Enabled {
		m = metrics.New(prometheus.DefaultRegisterer)
	}
	handler := handlers.NewRateLimitHandler(limiters, st, m, handlers.Options{
		DefaultAlgorithm:     cfg.Algorithms.Default,
		AllowPolicyOverrides: cfg.API.AllowPolicyOverrides,
		ResetToken:           cfg.API.ResetToken,
		DecisionTimeout:      cfg.Server.DecisionTimeout.Std(),
		Logger:               logger,
	})

	if os.Getenv("GIN_MODE") == "" {
		gin.SetMode(gin.ReleaseMode)
	}
	router := gin.New()
	router.Use(requestLogger(logger), recoveryLogger(logger))

	v1 := router.Group("/v1")
	{
		v1.POST("/check", handler.Check)
		v1.GET("/status", handler.GetStatus)
		v1.POST("/reset", handler.Reset)
	}
	router.GET("/health", handler.Health)
	if cfg.Metrics.Enabled {
		router.GET(cfg.Metrics.Path, gin.WrapH(promhttp.Handler()))
		logger.Info("metrics enabled", "path", cfg.Metrics.Path)
	}

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:           router,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.Std(),
		ReadTimeout:       cfg.Server.ReadTimeout.Std(),
		WriteTimeout:      cfg.Server.WriteTimeout.Std(),
		IdleTimeout:       cfg.Server.IdleTimeout.Std(),
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("server listening", "address", srv.Addr)
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)

	select {
	case sig := <-quit:
		logger.Info("shutdown requested", "signal", sig.String())
	case err := <-serverErr:
		if err != nil {
			return fmt.Errorf("HTTP server: %w", err)
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	if err := <-serverErr; err != nil {
		return fmt.Errorf("HTTP server after shutdown: %w", err)
	}
	logger.Info("server stopped")
	return nil
}

// loadConfig loads CONFIG_FILE if set (failing hard when it is unreadable or
// invalid), falls back to ./config.yaml when present, and otherwise runs on
// built-in defaults. Environment overrides apply in every case.
func loadConfig(logger *slog.Logger) (*config.Config, error) {
	if file := os.Getenv("CONFIG_FILE"); file != "" {
		return config.Load(file)
	}
	if _, err := os.Stat("config.yaml"); err == nil {
		return config.Load("config.yaml")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("checking config.yaml: %w", err)
	}
	logger.Info("no config file found; using built-in defaults")
	return config.Default()
}

func newStore(cfg *config.Config) (limiter.Store, error) {
	switch cfg.Store {
	case config.StoreMemory:
		return store.NewMemoryStore(), nil
	case config.StoreRedis:
		return store.NewRedisStore(store.RedisConfig{
			Addresses: cfg.Redis.Addresses,
			Password:  cfg.Redis.Password,
			DB:        cfg.Redis.DB,
			PoolSize:  cfg.Redis.PoolSize,
		})
	default:
		return nil, fmt.Errorf("unsupported store %q", cfg.Store)
	}
}

// buildLimiters constructs one limiter per algorithm and tier, all sharing
// the same store.
func buildLimiters(cfg *config.Config, st limiter.Store) (map[string]map[string]limiter.RateLimiter, error) {
	tiers := map[string]config.LimitConfig{handlers.DefaultTier: cfg.Limits.Default}
	for name, tier := range cfg.Limits.Tiers {
		tiers[name] = tier
	}

	limiters := make(map[string]map[string]limiter.RateLimiter, len(config.Algorithms))
	for _, algorithm := range config.Algorithms {
		limiters[algorithm] = make(map[string]limiter.RateLimiter, len(tiers))
		for name, tier := range tiers {
			limiterConfig := limiter.Config{Limit: tier.Requests, Window: tier.Window.Std(), Burst: tier.Burst}
			var instance limiter.RateLimiter
			switch algorithm {
			case config.AlgorithmTokenBucket:
				instance = algorithms.NewTokenBucket(st, limiterConfig)
			case config.AlgorithmSlidingWindow:
				instance = algorithms.NewSlidingWindowCounter(st, limiterConfig)
			case config.AlgorithmFixedWindow:
				instance = algorithms.NewFixedWindowCounter(st, limiterConfig)
			default:
				return nil, fmt.Errorf("unsupported algorithm %q", algorithm)
			}
			limiters[algorithm][name] = instance
		}
	}
	return limiters, nil
}

func requestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = c.Request.URL.Path
		}
		logger.Info("HTTP request",
			"method", c.Request.Method,
			"route", route,
			"status", c.Writer.Status(),
			"bytes", c.Writer.Size(),
			"duration_ms", float64(time.Since(started).Microseconds())/1000,
		)
	}
}

func recoveryLogger(logger *slog.Logger) gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered any) {
		logger.Error("panic recovered", "panic", fmt.Sprint(recovered), "route", c.FullPath())
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
	})
}
