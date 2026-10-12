package middleware

import (
	"bytes"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"

	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// ProviderTerminology rejects legacy account fields in selected admin query parameters and top-level JSON fields.
func ProviderTerminology() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := strings.TrimPrefix(c.Request.URL.Path, "/api/v1/admin/")
		section, _, _ := strings.Cut(path, "/")
		switch section {
		case "providers", "groups", "pricing-configs", "proxies", "settings", "ops", "usage", "dashboard":
		default:
			c.Next()
			return
		}
		legacy := func(key string) bool {
			for part := range strings.SplitSeq(key, "_") {
				if part == "account" || part == "accounts" {
					return true
				}
			}
			return false
		}
		reject := func(key string) {
			response.BadRequest(c, "field "+key+" has been renamed to "+strings.ReplaceAll(key, "account", "provider"))
			c.Abort()
		}
		for key := range c.Request.URL.Query() {
			if legacy(key) {
				reject(key)
				return
			}
		}
		if c.ContentType() == "application/json" && c.Request.Body != nil && c.Request.ContentLength != 0 {
			var fields map[string]any
			if err := c.ShouldBindBodyWith(&fields, binding.JSON); err != nil {
				response.BadRequest(c, "Invalid request: "+err.Error())
				c.Abort()
				return
			}
			for key := range fields {
				if legacy(key) {
					reject(key)
					return
				}
			}
			// Restore the body for ordinary JSON binding; Gin's cache is used by ShouldBindBodyWith.
			if raw, ok := c.Get(gin.BodyBytesKey); ok {
				if body, ok := raw.([]byte); ok {
					c.Request.Body = io.NopCloser(bytes.NewReader(body))
				}
			}
		}
		c.Next()
	}
}
