package middleware

import (
	"bytes"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"

	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// provider_terminology.go validates administrator provider field names before
// downstream request binding.
// Selected administrator resources use provider field names for local
// identifiers while nested upstream credentials can still contain account
// fields. Query keys and top-level JSON keys are checked before handlers bind
// input, and accepted JSON bytes are replayed from Gin's cache for the next
// binder.
//
// The check recognizes exact account or accounts components separated by
// underscores. It chooses resource sections from the path after removing the
// administrator prefix, so route assembly determines where this middleware is
// installed. Invalid JSON and detected legacy keys abort with 400; ordinary
// binding after validation reads the same cached body bytes.

// ProviderTerminology rejects legacy query keys and top-level JSON fields for
// selected administrator resource sections. Mount it on the intended
// administrator group: section detection removes the expected prefix but does
// not independently authenticate or require that prefix. Matching uses exact
// account or accounts underscore components.
//
// Nested JSON keys remain available to upstream credential payloads. Nonempty
// JSON bodies are bound into a map first, with malformed input or legacy keys
// aborting at 400; accepted body bytes are restored from Gin's cache before
// downstream ordinary binding.
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
		// Inspect top-level JSON fields before the handler binds its request type.
		// Nested credential objects remain outside this local identifier terminology
		// check.
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
			// ShouldBindBodyWith has consumed the original reader while retaining its
			// bytes in Gin's cache. Replay those bytes so a later ordinary JSON binder
			// receives the same payload.
			if raw, ok := c.Get(gin.BodyBytesKey); ok {
				if body, ok := raw.([]byte); ok {
					c.Request.Body = io.NopCloser(bytes.NewReader(body))
				}
			}
		}
		c.Next()
	}
}
