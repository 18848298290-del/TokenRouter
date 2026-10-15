package middleware

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	identityhttp "github.com/TokenFlux/TokenRouter/internal/identity/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
	"github.com/TokenFlux/TokenRouter/internal/server/runtimeconfig"
)

// panel_rate_limit.go applies user and public-address rate limits to panel
// requests.
// Panel access uses separate fixed-window buckets for each authenticated user,
// each heavy query scope, and each eligible public client address. Cached
// settings decide whether a bucket applies; missing dependencies, disabled
// limits, administrator exemptions, and counter errors leave the handler chain
// available.
//
// User buckets require an authenticated positive user ID and select global or
// heavy limits independently. Public buckets use the security client address
// and bypass local or unusable addresses, which can identify shared ingress. A
// denied counter result aborts with the panel response and a rounded retry
// delay; storage errors are logged before allowing the handler chain to
// continue.

// panelRateLimitWindow supplies the one-minute counter window for both
// user-scoped and public-address panel buckets.
const panelRateLimitWindow = time.Minute

// panelRateLimitAllower supplies the admission observations consumed by user
// and public panel limits.
type panelRateLimitAllower interface {
	// Allow reports admission and retry observations for the supplied panel
	// bucket, limit, and window. Calls can arrive concurrently; failures return to
	// middleware, which keeps panel requests available.
	Allow(ctx context.Context, key string, limit int, window time.Duration) (AllowResult, error)
}

// PanelSettingsReader supplies cached settings values that panel middleware
// reads on each request.
type PanelSettingsReader interface {
	// GetPanelRateLimitSettingsCached returns the settings value used for this
	// request's bucket decision. Implementations manage cache refresh and read
	// failures internally, returning a usable value for concurrent middleware
	// consumers.
	GetPanelRateLimitSettingsCached(context.Context) runtimeconfig.PanelRateLimitSettings
}

// PanelRateLimiter combines cached settings and request counters for panel HTTP
// admission.
type PanelRateLimiter struct {
	// limiter and settingService remain available throughout installed middleware
	// use. A nil dependency bypasses admission checks, and supplied
	// implementations support concurrent request reads and counts.
	limiter        panelRateLimitAllower // panel request counter
	settingService PanelSettingsReader   // cached panel settings reader
}

// NewPanelRateLimiter retains the supplied counter and settings reader for
// panel requests. A nil counter stays a nil interface so unavailable Redis can
// bypass counting; settings and counter implementations remain usable while
// routes serve requests.
func NewPanelRateLimiter(counter *RateLimiter, settingService PanelSettingsReader) *PanelRateLimiter {
	limiter := &PanelRateLimiter{settingService: settingService}
	if counter != nil {
		limiter.limiter = counter
	}
	// A missing counter remains an absent interface here. Request checks can then
	// bypass counting when application startup has no Redis client.
	return limiter
}

// Global returns panel admission keyed by the authenticated user's ID with
// cached UserRPM. Mount it after authentication; missing valid subjects pass
// through, leaving rejection to that layer. Cached settings determine
// exemptions and whether counting applies.
func (p *PanelRateLimiter) Global() gin.HandlerFunc {
	return p.userScoped("global", func(s runtimeconfig.PanelRateLimitSettings) int {
		return s.UserRPM
	})
}

// Heavy returns a separate authenticated-user bucket using cached HeavyRPM for
// expensive panel queries. Mount it after authentication; when a request also
// passes Global, each installed limiter consumes its own scoped counter.
func (p *PanelRateLimiter) Heavy() gin.HandlerFunc {
	return p.userScoped("heavy", func(s runtimeconfig.PanelRateLimitSettings) int {
		return s.HeavyRPM
	})
}

// userScoped returns admission middleware for one user-and-scope bucket
// selected by limitOf. It bypasses missing dependencies, disabled or
// nonpositive limits, missing valid subjects, and configured administrator
// exemptions. Callers authenticate first and provide a nonnil limit selector.
//
// Counter failures are logged and the request continues. A denied count aborts
// with the panel 429 envelope and a rounded Retry-After, while other requests
// continue downstream.
func (p *PanelRateLimiter) userScoped(scope string, limitOf func(runtimeconfig.PanelRateLimitSettings) int) gin.HandlerFunc {
	return func(c *gin.Context) {
		if p == nil || p.limiter == nil || p.settingService == nil {
			c.Next()
			return
		}
		settings := p.settingService.GetPanelRateLimitSettingsCached(c.Request.Context())
		if !settings.Enabled {
			c.Next()
			return
		}
		limit := limitOf(settings)
		if limit <= 0 {
			c.Next()
			return
		}
		subject, ok := authctx.GetAuthSubjectFromContext(c)
		if !ok || subject.UserID <= 0 {
			// Authentication middleware owns rejection of requests with no valid subject.
			c.Next()
			return
		}
		if settings.ExemptAdmin {
			if role, hasRole := authctx.GetUserRoleFromContext(c); hasRole && role == identity.RoleAdmin {
				c.Next()
				return
			}
		}

		key := "panel:" + scope + ":user:" + strconv.FormatInt(subject.UserID, 10)
		result, err := p.limiter.Allow(c.Request.Context(), key, limit, panelRateLimitWindow)
		if err != nil {
			// The cached policy has selected a panel bucket, but its counter failed.
			// Keep panel access available and record the failure for diagnosis.
			slog.Warn("panel rate limit check failed, allowing request", "scope", scope, "error", err)
			c.Next()
			return
		}
		if !result.Allowed {
			abortPanelRateLimited(c, result.RetryAfter)
			return
		}
		c.Next()
	}
}

// PublicIP returns middleware for a separate public-address panel bucket using
// cached PublicIPRPM. Mount it on the intended public endpoints; it applies to
// any request reaching that chain and uses the security address resolver.
//
// Loopback, private, link-local, unspecified, invalid, and non-global-unicast
// addresses bypass counting because they can represent shared local ingress.
// Disabled limits and missing dependencies also pass through. Counter errors
// are logged and allowed; a denied count uses the panel rejection envelope.
func (p *PanelRateLimiter) PublicIP() gin.HandlerFunc {
	return func(c *gin.Context) {
		if p == nil || p.limiter == nil || p.settingService == nil {
			c.Next()
			return
		}
		settings := p.settingService.GetPanelRateLimitSettingsCached(c.Request.Context())
		if !settings.Enabled || settings.PublicIPRPM <= 0 {
			c.Next()
			return
		}
		clientIP := identityhttp.SecurityClientIP(c)
		// A local or unusable address can identify shared ingress rather than one
		// public caller. Bypass this bucket so those requests do not share an
		// unintended limit.
		if !isPubliclyRoutableClientIP(clientIP) {
			c.Next()
			return
		}

		result, err := p.limiter.Allow(c.Request.Context(), "panel:public:ip:"+clientIP, settings.PublicIPRPM, panelRateLimitWindow)
		if err != nil {
			// Public panel counting has failed before an admission observation is
			// available. Keep the endpoint reachable while logging the counter error for
			// diagnosis.
			slog.Warn("panel public rate limit check failed, allowing request", "error", err)
			c.Next()
			return
		}
		if !result.Allowed {
			abortPanelRateLimited(c, result.RetryAfter)
			return
		}
		c.Next()
	}
}

// isPubliclyRoutableClientIP reports whether a parsed literal is eligible for
// public panel counting. It requires Go's global-unicast classification and
// excludes loopback, private, link-local, and unspecified addresses; this is
// not an Internet reachability probe.
func isPubliclyRoutableClientIP(clientIP string) bool {
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	return ip.IsGlobalUnicast()
}

// abortPanelRateLimited emits the panel 429 envelope and aborts downstream
// handling. It uses the shared minute window for a nonpositive delay and rounds
// the chosen delay upward to whole seconds for Retry-After.
func abortPanelRateLimited(c *gin.Context, retryAfter time.Duration) {
	if retryAfter <= 0 {
		// A rejected count with no positive delay still needs a retry suggestion. Use
		// the same configured minute window as panel admission.
		retryAfter = panelRateLimitWindow
	}
	// Round fractional seconds upward so the advertised delay covers the counter
	// observation.
	seconds := int64(retryAfter / time.Second)
	if retryAfter%time.Second > 0 {
		seconds++
	}
	c.Header("Retry-After", strconv.FormatInt(seconds, 10))
	httpx.AbortWithError(c, http.StatusTooManyRequests, "RATE_LIMITED", "Too many requests, please slow down and try again later")
}
