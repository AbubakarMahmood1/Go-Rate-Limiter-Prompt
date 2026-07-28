package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/AbubakarMahmood/go-rate-limiter/internal/algorithms"
	"github.com/AbubakarMahmood/go-rate-limiter/internal/handlers"
	"github.com/AbubakarMahmood/go-rate-limiter/internal/metrics"
	"github.com/AbubakarMahmood/go-rate-limiter/internal/store"
	"github.com/AbubakarMahmood/go-rate-limiter/pkg/limiter"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testResetToken = "test-reset-token-123"

// newRouter builds the API exactly as main does, on a memory store with a
// small default limit (5/min, burst 5) and a "premium" tier (50/min).
func newRouter(t *testing.T) *gin.Engine {
	t.Helper()
	return newRouterWithPolicy(t, true, testResetToken)
}

func newRouterWithPolicy(t *testing.T, allowOverrides bool, resetToken string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	s := store.NewMemoryStore()
	t.Cleanup(func() { _ = s.Close() })

	defaultCfg := limiter.Config{Limit: 5, Window: time.Minute, Burst: 5}
	premiumCfg := limiter.Config{Limit: 50, Window: time.Minute, Burst: 50}

	limiters := map[string]map[string]limiter.RateLimiter{
		"token_bucket": {
			"default": algorithms.NewTokenBucket(s, defaultCfg),
			"premium": algorithms.NewTokenBucket(s, premiumCfg),
		},
		"sliding_window": {
			"default": algorithms.NewSlidingWindowCounter(s, defaultCfg),
		},
		"fixed_window": {
			"default": algorithms.NewFixedWindowCounter(s, defaultCfg),
		},
	}

	h := handlers.NewRateLimitHandler(limiters, s, metrics.New(prometheus.NewRegistry()), handlers.Options{
		DefaultAlgorithm:     "token_bucket",
		AllowPolicyOverrides: allowOverrides,
		ResetToken:           resetToken,
		DecisionTimeout:      time.Second,
	})

	router := gin.New()
	v1 := router.Group("/v1")
	v1.POST("/check", h.Check)
	v1.GET("/status", h.GetStatus)
	v1.POST("/reset", h.Reset)
	router.GET("/health", h.Health)
	return router
}

func doJSON(t *testing.T, router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func checkBody(resource, identifier string, extra string) string {
	b := `{"resource":"` + resource + `","identifier":"` + identifier + `"`
	if extra != "" {
		b += "," + extra
	}
	return b + "}"
}

func statusPath(identifier, resource string, extra url.Values) string {
	query := url.Values{
		"identifier": {identifier},
		"resource":   {resource},
	}
	for key, values := range extra {
		for _, value := range values {
			query.Add(key, value)
		}
	}
	return "/v1/status?" + query.Encode()
}

func resetRequest(identifier, resource, token string) *http.Request {
	query := url.Values{
		"identifier": {identifier},
		"resource":   {resource},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/reset?"+query.Encode(), nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func TestCheck_AllowsAndSetsHeaders(t *testing.T) {
	router := newRouter(t)

	w := doJSON(t, router, http.MethodPost, "/v1/check", checkBody("api.users.create", "u1", ""))
	require.Equal(t, http.StatusOK, w.Code)

	var resp handlers.CheckResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Allowed)
	assert.Equal(t, 5, resp.Limit)
	assert.Equal(t, 4, resp.Remaining)
	assert.Nil(t, resp.RetryAfter)

	assert.Equal(t, "5", w.Header().Get("X-RateLimit-Limit"))
	assert.Equal(t, "4", w.Header().Get("X-RateLimit-Remaining"))
	assert.NotEmpty(t, w.Header().Get("X-RateLimit-Reset"))
}

func TestCheck_DeniesWith429(t *testing.T) {
	router := newRouter(t)

	for i := 0; i < 5; i++ {
		w := doJSON(t, router, http.MethodPost, "/v1/check", checkBody("api.test", "u1", ""))
		require.Equal(t, http.StatusOK, w.Code, "request %d", i+1)
	}

	w := doJSON(t, router, http.MethodPost, "/v1/check", checkBody("api.test", "u1", ""))
	require.Equal(t, http.StatusTooManyRequests, w.Code)

	var resp handlers.CheckResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.False(t, resp.Allowed)
	assert.Equal(t, 0, resp.Remaining)
	require.NotNil(t, resp.RetryAfter)
	assert.GreaterOrEqual(t, *resp.RetryAfter, int64(1))
	assert.NotEmpty(t, w.Header().Get("Retry-After"))
}

func TestCheck_CountConsumesPermits(t *testing.T) {
	router := newRouter(t)

	w := doJSON(t, router, http.MethodPost, "/v1/check", checkBody("api.test", "u1", `"count":5,"algorithm":"fixed_window"`))
	require.Equal(t, http.StatusOK, w.Code)

	// The window is exhausted: a single further request must be denied.
	w = doJSON(t, router, http.MethodPost, "/v1/check", checkBody("api.test", "u1", `"algorithm":"fixed_window"`))
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
}

func TestCheck_BadRequests(t *testing.T) {
	router := newRouter(t)

	cases := map[string]string{
		"missing fields":     `{"resource":"api.test"}`,
		"zero count":         checkBody("api.test", "u1", `"count":0`),
		"negative count":     checkBody("api.test", "u1", `"count":-2`),
		"null count":         checkBody("api.test", "u1", `"count":null`),
		"fractional count":   checkBody("api.test", "u1", `"count":1.5`),
		"unknown algorithm":  checkBody("api.test", "u1", `"algorithm":"leaky_bucket"`),
		"unknown tier":       checkBody("api.test", "u1", `"tier":"platinum"`),
		"count over limit":   checkBody("api.test", "u1", `"count":6`),
		"unknown field":      checkBody("api.test", "u1", `"surprise":true`),
		"malformed json":     `{`,
		"multiple objects":   checkBody("api.test", "u1", "") + `{}`,
		"leading whitespace": checkBody("api.test", " u1", ""),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			w := doJSON(t, router, http.MethodPost, "/v1/check", body)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

func TestCheck_RejectsOversizedBody(t *testing.T) {
	router := newRouter(t)
	body := checkBody("api.test", strings.Repeat("x", 20<<10), "")
	w := doJSON(t, router, http.MethodPost, "/v1/check", body)
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}

func TestCheck_IdentityBoundariesAndEncoding(t *testing.T) {
	router := newRouter(t)

	exact, err := json.Marshal(map[string]string{
		"identifier": strings.Repeat("i", 256),
		"resource":   strings.Repeat("r", 512),
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, doJSON(t, router, http.MethodPost, "/v1/check", string(exact)).Code)

	cases := map[string]map[string]string{
		"identifier too long": {
			"identifier": strings.Repeat("i", 257),
			"resource":   "api.test",
		},
		"resource too long": {
			"identifier": "u1",
			"resource":   strings.Repeat("r", 513),
		},
		"identifier control character": {
			"identifier": "u1\nadmin",
			"resource":   "api.test",
		},
		"resource control character": {
			"identifier": "u1",
			"resource":   "api\ttest",
		},
	}
	for name, fields := range cases {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(fields)
			require.NoError(t, err)
			assert.Equal(t, http.StatusBadRequest, doJSON(t, router, http.MethodPost, "/v1/check", string(body)).Code)
		})
	}

	invalidUTF8 := append([]byte(`{"resource":"api.test","identifier":"u`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`"}`)...)
	req := httptest.NewRequest(http.MethodPost, "/v1/check", bytes.NewReader(invalidUTF8))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "valid UTF-8")
}

func TestCheck_TiersAreIndependent(t *testing.T) {
	router := newRouter(t)

	for i := 0; i < 5; i++ {
		doJSON(t, router, http.MethodPost, "/v1/check", checkBody("api.test", "u1", ""))
	}
	w := doJSON(t, router, http.MethodPost, "/v1/check", checkBody("api.test", "u1", ""))
	require.Equal(t, http.StatusTooManyRequests, w.Code)

	w = doJSON(t, router, http.MethodPost, "/v1/check", checkBody("api.test", "u1", `"tier":"premium"`))
	require.Equal(t, http.StatusOK, w.Code)

	var resp handlers.CheckResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 50, resp.Limit)
	assert.Equal(t, 49, resp.Remaining)
}

func TestCheck_PolicyOverridesAreDisabledByDefaultBoundary(t *testing.T) {
	router := newRouterWithPolicy(t, false, "")

	w := doJSON(t, router, http.MethodPost, "/v1/check", checkBody("api.test", "u1", `"tier":"premium"`))
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "overrides are disabled")

	w = doJSON(t, router, http.MethodPost, "/v1/check", checkBody("api.test", "u1", ""))
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestStatus_DoesNotConsume(t *testing.T) {
	router := newRouter(t)
	doJSON(t, router, http.MethodPost, "/v1/check", checkBody("api.test", "u1", ""))

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, statusPath("u1", "api.test", nil), nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var resp handlers.CheckResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, 4, resp.Remaining, "status call %d must not consume permits", i+1)
	}
}

func TestSubjectEncodingPreventsDelimiterCollisions(t *testing.T) {
	router := newRouter(t)

	first := checkBody("c", "a:b", `"count":5,"algorithm":"fixed_window"`)
	second := checkBody("b:c", "a", `"count":5,"algorithm":"fixed_window"`)
	assert.Equal(t, http.StatusOK, doJSON(t, router, http.MethodPost, "/v1/check", first).Code)
	assert.Equal(t, http.StatusOK, doJSON(t, router, http.MethodPost, "/v1/check", second).Code,
		"distinct identifier/resource pairs must never share state")
}

func TestStatusSupportsEscapedUnicodeAndSpecialCharacters(t *testing.T) {
	router := newRouter(t)
	identifier := "customer:東京/42"
	resource := "api.orders:create/v2"
	body, err := json.Marshal(map[string]string{"identifier": identifier, "resource": resource})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, doJSON(t, router, http.MethodPost, "/v1/check", string(body)).Code)

	req := httptest.NewRequest(http.MethodGet, statusPath(identifier, resource, nil), nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"remaining":4`)
}

func TestReset_RequiresTokenAndRestoresBudget(t *testing.T) {
	router := newRouter(t)

	for i := 0; i < 5; i++ {
		doJSON(t, router, http.MethodPost, "/v1/check", checkBody("api.test", "u1", ""))
	}
	w := doJSON(t, router, http.MethodPost, "/v1/check", checkBody("api.test", "u1", ""))
	require.Equal(t, http.StatusTooManyRequests, w.Code)

	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, resetRequest("u1", "api.test", "wrong-token"))
	assert.Equal(t, http.StatusUnauthorized, unauthorized.Code)

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, resetRequest("u1", "api.test", testResetToken))
	require.Equal(t, http.StatusOK, rw.Code)

	w = doJSON(t, router, http.MethodPost, "/v1/check", checkBody("api.test", "u1", ""))
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestReset_IsNotExposedWithoutToken(t *testing.T) {
	router := newRouterWithPolicy(t, false, "")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, resetRequest("u1", "api.test", "anything"))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHealth_OK(t *testing.T) {
	router := newRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"ok"`)
}

type fakeLimiter struct {
	allowN func(context.Context, string, int) (*limiter.Result, error)
	peek   func(context.Context, string) (*limiter.Result, error)
	reset  func(context.Context, string) error
}

func (f fakeLimiter) Allow(ctx context.Context, key string) (*limiter.Result, error) {
	return f.AllowN(ctx, key, 1)
}

func (f fakeLimiter) AllowN(ctx context.Context, key string, n int) (*limiter.Result, error) {
	if f.allowN == nil {
		return nil, errors.New("unexpected AllowN")
	}
	return f.allowN(ctx, key, n)
}

func (f fakeLimiter) Peek(ctx context.Context, key string) (*limiter.Result, error) {
	if f.peek == nil {
		return nil, errors.New("unexpected Peek")
	}
	return f.peek(ctx, key)
}

func (f fakeLimiter) Reset(ctx context.Context, key string) error {
	if f.reset == nil {
		return errors.New("unexpected Reset")
	}
	return f.reset(ctx, key)
}

type fakeStore struct {
	ping func(context.Context) error
}

func (fakeStore) IncrWindow(context.Context, string, time.Duration, int64, int64, bool, time.Duration) (*limiter.WindowResult, error) {
	return nil, errors.New("unexpected IncrWindow")
}

func (fakeStore) TakeTokens(context.Context, string, float64, float64, float64, time.Duration) (*limiter.TokenResult, error) {
	return nil, errors.New("unexpected TakeTokens")
}

func (fakeStore) Delete(context.Context, string) error { return errors.New("unexpected Delete") }
func (s fakeStore) Ping(ctx context.Context) error {
	if s.ping == nil {
		return nil
	}
	return s.ping(ctx)
}
func (fakeStore) Close() error { return nil }

func routerWithFakes(t *testing.T, lim limiter.RateLimiter, st limiter.Store, timeout time.Duration) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := handlers.NewRateLimitHandler(
		map[string]map[string]limiter.RateLimiter{"token_bucket": {"default": lim}},
		st,
		metrics.New(prometheus.NewRegistry()),
		handlers.Options{
			DefaultAlgorithm: "token_bucket",
			DecisionTimeout:  timeout,
			ResetToken:       testResetToken,
			Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	)
	router := gin.New()
	router.POST("/v1/check", h.Check)
	router.GET("/v1/status", h.GetStatus)
	router.POST("/v1/reset", h.Reset)
	router.GET("/health", h.Health)
	return router
}

func TestCheck_BackendFailureFailsClosedWithoutLeakingDetails(t *testing.T) {
	backendErr := errors.New("redis password=super-secret connection refused")
	router := routerWithFakes(t, fakeLimiter{
		allowN: func(context.Context, string, int) (*limiter.Result, error) {
			return nil, backendErr
		},
	}, fakeStore{}, time.Second)

	w := doJSON(t, router, http.MethodPost, "/v1/check", checkBody("api.test", "u1", ""))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "1", w.Header().Get("Retry-After"))
	assert.Contains(t, w.Body.String(), "decision unavailable")
	assert.NotContains(t, w.Body.String(), "super-secret")
}

func TestCheck_DecisionTimeoutFailsClosed(t *testing.T) {
	router := routerWithFakes(t, fakeLimiter{
		allowN: func(ctx context.Context, _ string, _ int) (*limiter.Result, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}, fakeStore{}, 5*time.Millisecond)

	w := doJSON(t, router, http.MethodPost, "/v1/check", checkBody("api.test", "u1", ""))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "1", w.Header().Get("Retry-After"))
}

func TestCheck_RoundsRateLimitHeadersUp(t *testing.T) {
	router := routerWithFakes(t, fakeLimiter{
		allowN: func(context.Context, string, int) (*limiter.Result, error) {
			return &limiter.Result{
				Allowed:    false,
				Limit:      10,
				Remaining:  0,
				ResetAt:    time.Unix(100, 1),
				RetryAfter: time.Nanosecond,
			}, nil
		},
	}, fakeStore{}, time.Second)

	w := doJSON(t, router, http.MethodPost, "/v1/check", checkBody("api.test", "u1", ""))
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, "101", w.Header().Get("X-RateLimit-Reset"))
	assert.Equal(t, "1", w.Header().Get("Retry-After"))
}

func TestHealth_FailureIsGenericAndBounded(t *testing.T) {
	router := routerWithFakes(t, fakeLimiter{}, fakeStore{ping: func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return errors.New("dial redis://user:secret@example")
		}
	}}, 10*time.Millisecond)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), `"unavailable"`)
	assert.NotContains(t, w.Body.String(), "secret")
}
