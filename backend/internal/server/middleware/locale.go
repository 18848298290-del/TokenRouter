package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// Locale puts the browser or API language preference into the request context.
func Locale(defaultLanguage func() string) gin.HandlerFunc {
	return func(c *gin.Context) {
		fallback := locale.Default()
		if defaultLanguage != nil {
			fallback = defaultLanguage()
		}
		requested := c.GetHeader("Accept-Language")
		// Browser navigation cannot set application headers, so HTML requests prefer the saved locale cookie.
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
