//go:build !embed

package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

// embed_off.go provides frontend server placeholders and unavailable responses
// for builds without embedded assets.
// An API-only build can use the same application wiring without shipping the
// frontend bundle. PublicSettingsProvider and FrontendServer remain defined for
// that build, but construction reports the missing frontend and the available
// fallback handlers send an aborted HTTP 404 response.

// PublicSettingsProvider keeps the public-settings injection dependency
// available to application wiring in either build.
type PublicSettingsProvider interface {
	// GetPublicSettingsForInjection returns JSON-compatible public settings for
	// the context language. Keep returned data stable while the consumer encodes
	// it, and observe ctx cancellation for the HTML request's query budget; an
	// error selects the base page.
	GetPublicSettingsForInjection(ctx context.Context) (any, error)
}

// FrontendServer is the API-only build's placeholder for the embedded frontend
// server type.
type FrontendServer struct{}

// NewFrontendServer returns a nil server and the frontend-not-embedded error
// for this build. The supplied provider is unused, so application callers
// consult HasEmbeddedFrontend before attempting frontend construction.
func NewFrontendServer(settingsProvider PublicSettingsProvider) (*FrontendServer, error) {
	return nil, errors.New("frontend not embedded")
}

// InvalidateCache accepts invalidation notifications and performs no work in
// this API-only build.
func (s *FrontendServer) InvalidateCache() {}

// Middleware returns a handler that sends the build's missing-frontend message
// with HTTP 404. It aborts further Gin handlers, and constructing it requires
// no state from the receiver.
func (s *FrontendServer) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.String(http.StatusNotFound, "Frontend not embedded. Build with -tags embed to include frontend.")
		c.Abort()
	}
}

// ServeEmbeddedFrontend returns the API-only build's missing-frontend HTTP 404
// handler. The handler writes the explanatory build message and aborts Gin
// processing, so later endpoint handlers do not run.
func ServeEmbeddedFrontend() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.String(http.StatusNotFound, "Frontend not embedded. Build with -tags embed to include frontend.")
		c.Abort()
	}
}

// HasEmbeddedFrontend reports false because this build omits the embedded
// frontend bundle.
func HasEmbeddedFrontend() bool {
	return false
}
