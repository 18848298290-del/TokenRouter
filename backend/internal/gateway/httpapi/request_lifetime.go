package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// RequestLifetime references the application's active-request barrier.
type RequestLifetime struct{ enter func() (func(), error) }

// BindRequestActivity must be called during assembly, before serving HTTP requests.
func (r *RequestLifetime) BindRequestActivity(enter func() (func(), error)) { r.enter = enter }

// BeginRequest enters the activity barrier and returns a release function; a closed barrier rejects the request.
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
