package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Responses admission distinguishes a missing handler from missing services
// used by that handler. Dependency checks return ordered diagnostic names and
// write a 503 only while the HTTP response is still available for an error
// body.

// OpenAIDependencies records which Responses entry dependencies were bound at
// startup. Handler determines whether the remaining service flags are
// inspected.
type OpenAIDependencies struct {
	// Handler, Gateway, Funding, Keys, and Concurrency describe one entry's
	// installed dependencies. Missing checks the service flags after
	// Handler confirms that the handler itself exists.
	// Handler: entry instance presence. Gateway: provider selection service
	// presence. Funding: billing eligibility reader presence. Keys: API-key
	// service presence. Concurrency: user-slot helper presence.
	Handler, Gateway, Funding, Keys, Concurrency bool
}

// Missing returns diagnostic names for dependencies absent from the Responses
// entry. An absent Handler ends the check immediately; otherwise Gateway,
// Funding, Keys, and Concurrency are reported in that order.
func (d OpenAIDependencies) Missing() []string {
	missing := make([]string, 0, 5)
	if !d.Handler {
		return append(missing, "handler")
	}
	if !d.Gateway {
		missing = append(missing, "gatewayService")
	}
	if !d.Funding {
		missing = append(missing, "billingCacheService")
	}
	if !d.Keys {
		missing = append(missing, "apiKeyService")
	}
	if !d.Concurrency {
		missing = append(missing, "concurrencyHelper")
	}
	return missing
}

// Ensure reports whether the Responses dependencies are ready and logs missing
// names. A nil logger selects the request logger. Missing dependencies produce
// a 503 only when c has an unwritten response writer; callers stop processing
// on false.
func (d OpenAIDependencies) Ensure(c *gin.Context, log *zap.Logger) bool {
	missing := d.Missing()
	if len(missing) == 0 {
		return true
	}
	if log == nil {
		log = RequestLogger(c, "handler.openai_gateway.responses")
	}
	log.Error("openai.handler_dependencies_missing", zap.Strings("missing_dependencies", missing))
	if c != nil && c.Writer != nil && !c.Writer.Written() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "api_error", "message": "Service temporarily unavailable"}})
	}
	return false
}
