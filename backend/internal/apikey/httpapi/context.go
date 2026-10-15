package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
)

// context.go provides Gin context accessors for authenticated Keys, diagnostic
// Keys, and forced platform choices. Separate context names let Ops read a Key
// loaded during a failed authentication attempt while handlers use the
// authenticated Key name, and forced platform middleware publishes its entry
// choice to both Gin and standard request contexts.

// Gin context names keep admitted Key access, diagnostic lookup data and
// forced platform choice available to their distinct gateway and Ops readers.
const (
	ContextKeyAPIKey            = "api_key"
	ContextKeyOpsFallbackAPIKey = "ops_fallback_api_key"
	ContextKeyForcePlatform     = "force_platform"
)

// APIKeyAuthMiddleware is the Gin handler type used by Key authentication route
// setup.
type APIKeyAuthMiddleware gin.HandlerFunc

// GetAPIKeyFromContext reads the admitted Key pointer stored under the
// authentication name. Missing or differently typed values return false; a
// stored typed nil pointer still returns true, so callers check the returned
// pointer too.
func GetAPIKeyFromContext(c *gin.Context) (*apikey.APIKey, bool) {
	value, exists := c.Get(string(ContextKeyAPIKey))
	if !exists {
		return nil, false
	}
	apiKey, ok := value.(*apikey.APIKey)
	return apiKey, ok
}

// SetOpsFallbackAPIKey stores a loaded Key pointer for diagnostic Ops reads
// when authentication exits early. Nil contexts and Keys do nothing; this
// retained pointer establishes no handler admission or authenticated principal.
func SetOpsFallbackAPIKey(c *gin.Context, apiKey *apikey.APIKey) {
	if c == nil || apiKey == nil {
		return
	}
	c.Set(string(ContextKeyOpsFallbackAPIKey), apiKey)
}

// GetOpsFallbackAPIKey returns the retained diagnostic Key pointer when its
// stored type matches. Missing or differently typed values return false;
// callers use it for error observation and establish admission through the
// authenticated Key path. A stored typed nil pointer still reports true, so
// readers check the returned pointer before dereferencing it.
func GetOpsFallbackAPIKey(c *gin.Context) (*apikey.APIKey, bool) {
	value, exists := c.Get(string(ContextKeyOpsFallbackAPIKey))
	if !exists {
		return nil, false
	}
	apiKey, ok := value.(*apikey.APIKey)
	return apiKey, ok
}

// ForcePlatform returns middleware publishing the supplied platform in Gin and
// request metadata before invoking later handlers. Empty platform text remains
// a present entry value, and the returned middleware requires a usable request
// and Gin context.
func ForcePlatform(platform string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Services inspect standard request metadata while handlers inspect Gin.
		// Publish both values before Next so those readers observe the same entry choice.
		ctx := apikey.WithForcePlatform(c.Request.Context(), platform)
		c.Request = c.Request.WithContext(ctx)
		c.Set(string(ContextKeyForcePlatform), platform)
		c.Next()
	}
}

// HasForcePlatform reports whether Gin contains the forced-platform entry,
// including empty or differently typed values. Callers needing its string value
// use GetForcePlatformFromContext, which checks stored type separately from
// entry presence.
func HasForcePlatform(c *gin.Context) bool {
	_, exists := c.Get(string(ContextKeyForcePlatform))
	return exists
}

// GetForcePlatformFromContext reads the string platform entry and reports
// whether its type matches. Missing or differently typed values return false;
// empty stored text can return true, retaining presence separately from the
// platform value.
func GetForcePlatformFromContext(c *gin.Context) (string, bool) {
	value, exists := c.Get(string(ContextKeyForcePlatform))
	if !exists {
		return "", false
	}
	platform, ok := value.(string)
	return platform, ok
}
