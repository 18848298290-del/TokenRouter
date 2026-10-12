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

// panelRateLimitWindow is the fixed window shared by all panel rate limits.
const panelRateLimitWindow = time.Minute

// panelRateLimitAllower supplies the counter used by panel middleware.
type panelRateLimitAllower interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (AllowResult, error)
}

// PanelSettingsReader supplies cached panel rate limit settings.
type PanelSettingsReader interface {
	GetPanelRateLimitSettingsCached(context.Context) runtimeconfig.PanelRateLimitSettings
}

// PanelRateLimiter applies panel API limits and allows requests when counters fail.
type PanelRateLimiter struct {
	limiter        panelRateLimitAllower
	settingService PanelSettingsReader
}

// NewPanelRateLimiter creates panel middleware using the supplied counter and settings.
func NewPanelRateLimiter(counter *RateLimiter, settingService PanelSettingsReader) *PanelRateLimiter {
	limiter := &PanelRateLimiter{settingService: settingService}
	if counter != nil {
		limiter.limiter = counter
	}
	// A missing counter leaves requests unrestricted when Redis is unavailable at startup.
	return limiter
}

// Global limits authenticated panel requests by user ID, keeping users behind a shared proxy independent.
func (p *PanelRateLimiter) Global() gin.HandlerFunc {
	return p.userScoped("global", func(s runtimeconfig.PanelRateLimitSettings) int {
		return s.UserRPM
	})
}

// Heavy limits aggregate queries such as usage and dashboard requests by user ID.
// When installed with Global, a request consumes both limits.
func (p *PanelRateLimiter) Heavy() gin.HandlerFunc {
	return p.userScoped("heavy", func(s runtimeconfig.PanelRateLimitSettings) int {
		return s.HeavyRPM
	})
}

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
			// Counter failures leave panel access available.
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

// PublicIP limits unauthenticated endpoints using the security client IP resolver.
// Loopback, private, and link-local addresses are skipped because they may identify
// a shared reverse proxy whose users would otherwise consume a single bucket.
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
		if !isPubliclyRoutableClientIP(clientIP) {
			c.Next()
			return
		}

		result, err := p.limiter.Allow(c.Request.Context(), "panel:public:ip:"+clientIP, settings.PublicIPRPM, panelRateLimitWindow)
		if err != nil {
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

// isPubliclyRoutableClientIP reports whether an address is eligible for public IP limiting.
// It requires global unicast and excludes loopback, private, link-local, and unspecified addresses.
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

func abortPanelRateLimited(c *gin.Context, retryAfter time.Duration) {
	if retryAfter <= 0 {
		retryAfter = panelRateLimitWindow
	}
	seconds := int64(retryAfter / time.Second)
	if retryAfter%time.Second > 0 {
		seconds++
	}
	c.Header("Retry-After", strconv.FormatInt(seconds, 10))
	httpx.AbortWithError(c, http.StatusTooManyRequests, "RATE_LIMITED", "Too many requests, please slow down and try again later")
}
