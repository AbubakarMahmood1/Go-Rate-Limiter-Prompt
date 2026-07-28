// Package handlers implements the HTTP API.
package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/AbubakarMahmood/go-rate-limiter/internal/metrics"
	"github.com/AbubakarMahmood/go-rate-limiter/pkg/limiter"
	"github.com/gin-gonic/gin"
)

const (
	// DefaultTier is the tier used when a request names none.
	DefaultTier = "default"

	maxRequestBodyBytes = 8 << 10
	maxIdentifierBytes  = 256
	maxResourceBytes    = 512
	defaultDecisionTTL  = 2 * time.Second
)

var errPolicyOverridesDisabled = errors.New("algorithm and tier overrides are disabled")

// Options controls handler behavior at the service trust boundary.
type Options struct {
	DefaultAlgorithm     string
	AllowPolicyOverrides bool
	ResetToken           string
	DecisionTimeout      time.Duration
	Logger               *slog.Logger
}

// RateLimitHandler serves the rate-limiting endpoints. Limiters are indexed
// by algorithm, then by tier.
type RateLimitHandler struct {
	limiters             map[string]map[string]limiter.RateLimiter
	store                limiter.Store
	metrics              *metrics.Metrics
	defaultAlgorithm     string
	allowPolicyOverrides bool
	resetEnabled         bool
	resetTokenHash       [sha256.Size]byte
	decisionTimeout      time.Duration
	logger               *slog.Logger
}

// NewRateLimitHandler creates the handler. The reset endpoint is disabled
// when Options.ResetToken is empty; otherwise it requires a matching bearer
// token. Algorithm/tier overrides remain disabled unless the caller is behind
// an explicitly trusted policy boundary.
func NewRateLimitHandler(
	limiters map[string]map[string]limiter.RateLimiter,
	store limiter.Store,
	m *metrics.Metrics,
	options Options,
) *RateLimitHandler {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	decisionTimeout := options.DecisionTimeout
	if decisionTimeout <= 0 {
		decisionTimeout = defaultDecisionTTL
	}

	h := &RateLimitHandler{
		limiters:             limiters,
		store:                store,
		metrics:              m,
		defaultAlgorithm:     options.DefaultAlgorithm,
		allowPolicyOverrides: options.AllowPolicyOverrides,
		decisionTimeout:      decisionTimeout,
		logger:               logger,
	}
	if options.ResetToken != "" {
		h.resetEnabled = true
		h.resetTokenHash = sha256.Sum256([]byte(options.ResetToken))
	}
	return h
}

// CheckRequest is the body of POST /v1/check.
type CheckRequest struct {
	Resource   string      `json:"resource"`   // resource being accessed, e.g. "api.users.create"
	Identifier string      `json:"identifier"` // caller identity, e.g. a user ID or API key
	Algorithm  string      `json:"algorithm"`  // optional trusted-boundary override
	Tier       string      `json:"tier"`       // optional trusted-boundary override
	Count      PermitCount `json:"count"`      // permits to consume; absent means 1
}

// PermitCount distinguishes an omitted count from an explicitly supplied
// zero or null. Only omission receives the default of one permit.
type PermitCount struct {
	Value int
	Set   bool
}

// UnmarshalJSON implements json.Unmarshaler.
func (c *PermitCount) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("count must be an integer")
	}
	var value int
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("count must be an integer: %w", err)
	}
	c.Value = value
	c.Set = true
	return nil
}

// CheckResponse is returned by check and status calls.
type CheckResponse struct {
	Allowed    bool   `json:"allowed"`
	Limit      int    `json:"limit"`
	Remaining  int    `json:"remaining"`
	ResetAt    string `json:"reset_at"`
	RetryAfter *int64 `json:"retry_after,omitempty"` // whole seconds, rounded up
}

// Check handles POST /v1/check.
func (h *RateLimitHandler) Check(c *gin.Context) {
	started := time.Now()

	var req CheckRequest
	if err := decodeJSON(c, &req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body exceeds 8 KiB"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := validateIdentity(req.Identifier, req.Resource); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	count := 1
	if req.Count.Set {
		count = req.Count.Value
	}
	if count <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": limiter.ErrInvalidCount.Error()})
		return
	}

	algorithm, tier, lim, err := h.resolveLimiter(req.Algorithm, req.Tier)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := h.decisionContext(c.Request.Context())
	defer cancel()
	result, err := lim.AllowN(ctx, composeKey(tier, req.Identifier, req.Resource), count)
	if err != nil {
		outcome := metrics.ResultError
		if errors.Is(err, limiter.ErrExceedsLimit) || errors.Is(err, limiter.ErrInvalidCount) {
			outcome = metrics.ResultInvalid
		}
		h.recordDecision(algorithm, tier, outcome, started)
		h.decisionError(c, err)
		return
	}

	outcome := metrics.ResultAllowed
	status := http.StatusOK
	if !result.Allowed {
		outcome = metrics.ResultDenied
		status = http.StatusTooManyRequests
	}
	h.recordDecision(algorithm, tier, outcome, started)
	writeRateLimitHeaders(c, result)
	c.JSON(status, toResponse(result))
}

// GetStatus handles GET /v1/status?identifier=...&resource=.... Algorithm and
// tier are optional trusted-boundary query parameters. It never consumes
// permits.
func (h *RateLimitHandler) GetStatus(c *gin.Context) {
	identifier, resource, ok := identityFromQuery(c)
	if !ok {
		return
	}
	_, tier, lim, err := h.resolveLimiter(c.Query("algorithm"), c.Query("tier"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := h.decisionContext(c.Request.Context())
	defer cancel()
	result, err := lim.Peek(ctx, composeKey(tier, identifier, resource))
	if err != nil {
		h.decisionError(c, err)
		return
	}

	writeRateLimitHeaders(c, result)
	c.JSON(http.StatusOK, toResponse(result))
}

// Reset handles POST /v1/reset?identifier=...&resource=.... It is disabled
// unless RESET_TOKEN is configured and otherwise requires a matching bearer
// token. This endpoint is an operational control, not a public client API.
func (h *RateLimitHandler) Reset(c *gin.Context) {
	if !h.resetEnabled {
		c.JSON(http.StatusNotFound, gin.H{"error": "reset endpoint is disabled"})
		return
	}
	if !h.authorized(c.GetHeader("Authorization")) {
		c.Header("WWW-Authenticate", "Bearer")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	identifier, resource, ok := identityFromQuery(c)
	if !ok {
		return
	}
	_, tier, lim, err := h.resolveLimiter(c.Query("algorithm"), c.Query("tier"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := h.decisionContext(c.Request.Context())
	defer cancel()
	if err := lim.Reset(ctx, composeKey(tier, identifier, resource)); err != nil {
		h.decisionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "reset"})
}

// Health handles GET /health. It reports 503 when the store is unreachable,
// so orchestrators stop routing to an instance that cannot decide.
func (h *RateLimitHandler) Health(c *gin.Context) {
	timeout := h.decisionTimeout
	if timeout > time.Second {
		timeout = time.Second
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()

	if err := h.store.Ping(ctx); err != nil {
		h.logger.Error("health check failed", "error", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// resolveLimiter applies safe defaults and resolves one configured limiter.
func (h *RateLimitHandler) resolveLimiter(algorithm, tier string) (string, string, limiter.RateLimiter, error) {
	if !h.allowPolicyOverrides && (algorithm != "" || tier != "") {
		return "", "", nil, errPolicyOverridesDisabled
	}
	if algorithm == "" {
		algorithm = h.defaultAlgorithm
	}
	if tier == "" {
		tier = DefaultTier
	}
	tiers, ok := h.limiters[algorithm]
	if !ok {
		return algorithm, tier, nil, fmt.Errorf("unknown algorithm %q", algorithm)
	}
	lim, ok := tiers[tier]
	if !ok {
		return algorithm, tier, nil, fmt.Errorf("unknown tier %q", tier)
	}
	return algorithm, tier, lim, nil
}

func (h *RateLimitHandler) decisionContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, h.decisionTimeout)
}

func (h *RateLimitHandler) decisionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, limiter.ErrExceedsLimit), errors.Is(err, limiter.ErrInvalidCount):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, limiter.ErrInvalidStoreOperation):
		h.logger.Error("invalid rate-limit store operation", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "rate limiter is misconfigured"})
	default:
		// Decisions fail closed: dependency and timeout errors never turn into
		// an implicit allow. Retry-After prevents a hot retry loop.
		h.logger.Error("rate-limit decision failed", "error", err)
		c.Header("Retry-After", "1")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "rate limit decision unavailable"})
	}
}

func (h *RateLimitHandler) recordDecision(algorithm, tier, result string, started time.Time) {
	if h.metrics != nil {
		h.metrics.RecordDecision(algorithm, tier, result, time.Since(started).Seconds())
	}
}

func (h *RateLimitHandler) authorized(header string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	providedHash := sha256.Sum256([]byte(strings.TrimPrefix(header, prefix)))
	return subtle.ConstantTimeCompare(providedHash[:], h.resetTokenHash[:]) == 1
}

func identityFromQuery(c *gin.Context) (string, string, bool) {
	identifier := c.Query("identifier")
	resource := c.Query("resource")
	if err := validateIdentity(identifier, resource); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return "", "", false
	}
	return identifier, resource, true
}

func validateIdentity(identifier, resource string) error {
	if err := validateIdentityPart("identifier", identifier, maxIdentifierBytes); err != nil {
		return err
	}
	return validateIdentityPart("resource", resource, maxResourceBytes)
}

func validateIdentityPart(name, value string, maxBytes int) error {
	switch {
	case value == "":
		return fmt.Errorf("%s is required", name)
	case !utf8.ValidString(value):
		return fmt.Errorf("%s must be valid UTF-8", name)
	case strings.TrimSpace(value) != value:
		return fmt.Errorf("%s must not have leading or trailing whitespace", name)
	case len(value) > maxBytes:
		return fmt.Errorf("%s must be at most %d bytes", name, maxBytes)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s must not contain control characters", name)
		}
	}
	return nil
}

func decodeJSON(c *gin.Context, dst any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBodyBytes)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if !utf8.Valid(body) {
		return fmt.Errorf("invalid JSON body: body must be valid UTF-8")
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("invalid JSON body: exactly one object is required")
		}
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

// composeKey uses byte-length framing, making the tuple injective even when
// tiers, identifiers, and resources contain colons or other delimiters.
func composeKey(tier, identifier, resource string) string {
	var b strings.Builder
	b.Grow(len(tier) + len(identifier) + len(resource) + 32)
	b.WriteString("v1|")
	writeKeyPart(&b, tier)
	writeKeyPart(&b, identifier)
	writeKeyPart(&b, resource)
	return b.String()
}

func writeKeyPart(b *strings.Builder, value string) {
	b.WriteString(strconv.Itoa(len(value)))
	b.WriteByte(':')
	b.WriteString(value)
	b.WriteByte('|')
}

func toResponse(r *limiter.Result) CheckResponse {
	resp := CheckResponse{
		Allowed:   r.Allowed,
		Limit:     r.Limit,
		Remaining: r.Remaining,
		ResetAt:   r.ResetAt.UTC().Format(time.RFC3339Nano),
	}
	if !r.Allowed {
		seconds := retryAfterSeconds(r.RetryAfter)
		resp.RetryAfter = &seconds
	}
	return resp
}

func writeRateLimitHeaders(c *gin.Context, r *limiter.Result) {
	c.Header("X-RateLimit-Limit", strconv.Itoa(r.Limit))
	c.Header("X-RateLimit-Remaining", strconv.Itoa(r.Remaining))
	c.Header("X-RateLimit-Reset", strconv.FormatInt(unixSecondsCeil(r.ResetAt), 10))
	if !r.Allowed {
		c.Header("Retry-After", strconv.FormatInt(retryAfterSeconds(r.RetryAfter), 10))
	}
}

// retryAfterSeconds rounds up to whole seconds, the granularity of the
// Retry-After header; a denied request never advertises "retry now".
func retryAfterSeconds(d time.Duration) int64 {
	seconds := int64(math.Ceil(d.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	return seconds
}

func unixSecondsCeil(t time.Time) int64 {
	seconds := t.Unix()
	if t.Nanosecond() > 0 {
		seconds++
	}
	return seconds
}
