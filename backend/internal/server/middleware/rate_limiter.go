package middleware

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	ippkg "github.com/TokenFlux/TokenRouter/internal/server/clientip"
)

const (
	RateLimitFailOpen RateLimitFailureMode = iota
	RateLimitFailClose
)

// FixedWindowAllower supplies counter results independently of the storage implementation.
type FixedWindowAllower interface {
	Allow(context.Context, string, int, time.Duration) (bool, int64, time.Duration, error)
}

// RateLimiter adapts fixed-window counts to HTTP middleware.
type RateLimiter struct {
	allower FixedWindowAllower
	prefix  string
}

// RateLimitFailureMode controls whether counter errors allow or reject requests.
type RateLimitFailureMode int

// RateLimitOptions configures counter failure handling.
type RateLimitOptions struct {
	FailureMode RateLimitFailureMode
}

// AllowResult describes one fixed-window rate limit decision.
type AllowResult struct {
	// Allowed reports whether the request is within the limit.
	Allowed bool
	// Count includes the current request in the window's total.
	Count int64
	// RetryAfter is the remaining window duration on rejection; the counter may return a full window if TTL lookup fails.
	RetryAfter time.Duration
}

// NewRateLimiter wraps the supplied counter for HTTP callers.
func NewRateLimiter(allower FixedWindowAllower) *RateLimiter {
	return &RateLimiter{allower: allower, prefix: "rate_limit:"}
}

// Allow returns the counter decision, or a zero result when the counter fails.
func (r *RateLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (AllowResult, error) {
	allowed, count, retry, err := r.allower.Allow(ctx, key, limit, window)
	if err != nil {
		return AllowResult{}, err
	}
	return AllowResult{Allowed: allowed, Count: count, RetryAfter: retry}, nil
}

// clientIPForRateLimit returns the client address used to key IP rate limits.
// The security resolver shares the request's forwarded-IP settings with audit,
// session binding, and API Key ACL checks. It trusts forwarding headers in
// compatibility mode and uses the configured trusted proxy chain otherwise.
// This keeps clients behind a shared proxy in separate buckets when forwarding is trusted.
func clientIPForRateLimit(c *gin.Context) string {
	if resolved := ippkg.GetSecurityClientIP(c, false); resolved != "" {
		return resolved
	}
	return c.ClientIP()
}

// LimitWithOptions limits requests by client IP and defaults to allowing requests on counter errors.
func (r *RateLimiter) LimitWithOptions(key string, limit int, window time.Duration, opts RateLimitOptions) gin.HandlerFunc {
	failureMode := opts.FailureMode
	if failureMode != RateLimitFailClose {
		failureMode = RateLimitFailOpen
	}

	return func(c *gin.Context) {
		result, err := r.Allow(c.Request.Context(), key+":"+clientIPForRateLimit(c), limit, window)
		if err != nil {
			log.Printf("[RateLimit] redis error: key=%s mode=%s err=%v", r.prefix+key, failureModeLabel(failureMode), err)
			if failureMode == RateLimitFailClose {
				abortRateLimit(c, window)
				return
			}
			// Allow requests when the configured failure mode favors availability.
			c.Next()
			return
		}

		if !result.Allowed {
			abortRateLimit(c, result.RetryAfter)
			return
		}

		c.Next()
	}
}

func abortRateLimit(c *gin.Context, retryAfter time.Duration) {
	if retryAfter > 0 {
		seconds := int64(retryAfter / time.Second)
		if retryAfter%time.Second > 0 {
			seconds++
		}
		c.Header("Retry-After", strconv.FormatInt(seconds, 10))
	}
	c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
		"error":   "rate limit exceeded",
		"message": "Too many requests, please try again later",
	})
}

func failureModeLabel(mode RateLimitFailureMode) string {
	if mode == RateLimitFailClose {
		return "fail-close"
	}
	return "fail-open"
}
