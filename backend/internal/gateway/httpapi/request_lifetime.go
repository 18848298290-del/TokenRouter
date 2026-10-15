package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// request_lifetime.go admits HTTP handlers through the application activity
// barrier and presents shutdown rejections in the inbound protocol.
// Requests can enqueue completion work until their handlers finish. The
// application supplies one activity barrier so shutdown can wait for that work
// before closing completion and storage dependencies; a rejected entry is
// reported in the inbound protocol.

// RequestLifetime holds the application callback used to count active requests.
type RequestLifetime struct {
	// enter is bound during assembly and read by handlers after serving begins.
	// Its owner decides shutdown admission and supplies the accepted activity release.
	enter func() (func(), error)
}

// BindRequestActivity installs the application-owned request entry callback.
// Assembly calls it before serving requests, because handlers read the binding
// directly and do not coordinate concurrent replacement.
func (r *RequestLifetime) BindRequestActivity(enter func() (func(), error)) { r.enter = enter }

// BeginRequest enters the shared activity barrier and returns its accepted
// release callback. Callers release accepted activity after request cleanup and
// completion capture. An absent binding accepts with an empty callback; an
// entry error writes a protocol-specific 503 and returns false before the
// handler reads its body.
func (r *RequestLifetime) BeginRequest(c *gin.Context, format string) (func(), bool) {
	if r.enter == nil {
		return func() {}, true
	}
	release, err := r.enter()
	if err == nil {
		return release, true
	}
	const message = "Service is shutting down"
	switch format {
	case "systemone":
		WriteSystemOneError(c, http.StatusServiceUnavailable, "api_error", "", "", message)
	case "google":
		WriteGoogleError(c, http.StatusServiceUnavailable, message)
	case "anthropic":
		WriteAnthropicError(c, http.StatusServiceUnavailable, "api_error", "", message)
	default:
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "api_error", "message": message}})
	}
	return nil, false
}
