package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// locale.go selects request language and marks routes eligible for user
// presentation.
// HTML navigation can carry a saved locale cookie even when the browser's
// Accept-Language expresses another preference. Requests below /api/ negotiate
// language from the header, and administrator or setup prefixes disable user
// presentation while retaining a selected language in context.
//
// The saved cookie takes precedence only for a non-API request accepting HTML
// and only when its value normalizes to a supported language. Other requests
// negotiate from Accept-Language and the configured default. The selected
// language is written to context and Content-Language, while the separate
// presentation flag keeps administrator and setup responses outside
// user-facing localization.

// Locale negotiates a supported language and attaches language and
// user-presentation flags before downstream handling. A nil defaultLanguage
// uses the catalog default; otherwise the callback is read on each request and
// negotiation handles unsupported results. HTML requests outside /api/ prefer a
// valid saved locale cookie.
//
// The response receives Content-Language and Vary: Accept-Language.
// Presentation is disabled for /api/v1/admin/, /admin/, and /setup prefixes,
// independently of the negotiated language. Supply a callback that can be read
// by concurrent requests.
func Locale(defaultLanguage func() string) gin.HandlerFunc {
	return func(c *gin.Context) {
		fallback := locale.Default()
		if defaultLanguage != nil {
			fallback = defaultLanguage()
		}
		requested := c.GetHeader("Accept-Language")
		// HTML navigation relies on browser-managed headers. Prefer a supported saved
		// cookie outside /api/ so the user's selected page language survives
		// navigation.
		if !strings.HasPrefix(c.Request.URL.Path, "/api/") && strings.Contains(c.GetHeader("Accept"), "text/html") {
			if cookie, err := c.Cookie("tokenrouter_locale"); err == nil && locale.Normalize(cookie) != "" {
				requested = cookie
			}
		}
		code := locale.Negotiate(requested, fallback)
		ctx := locale.WithLanguage(c.Request.Context(), code)
		userFacing := !strings.HasPrefix(c.Request.URL.Path, "/api/v1/admin/") && !strings.HasPrefix(c.Request.URL.Path, "/admin/") && !strings.HasPrefix(c.Request.URL.Path, "/setup")
		c.Request = c.Request.WithContext(locale.WithUserPresentation(ctx, userFacing))
		c.Header("Content-Language", code)
		c.Writer.Header().Add("Vary", "Accept-Language")
		c.Next()
	}
}
