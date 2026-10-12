package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
)

// Context keys distinguish authenticated keys from keys loaded for failure diagnostics.
const (
	ContextKeyAPIKey            = "api_key"
	ContextKeyOpsFallbackAPIKey = "ops_fallback_api_key"
	ContextKeyForcePlatform     = "force_platform"
)

// APIKeyAuthMiddleware is Gin middleware for API key authentication.
type APIKeyAuthMiddleware gin.HandlerFunc

// GetAPIKeyFromContext reads the key stored by authentication middleware.
func GetAPIKeyFromContext(c *gin.Context) (*apikey.APIKey, bool) {
	value, exists := c.Get(string(ContextKeyAPIKey))
	if !exists {
		return nil, false
	}
	apiKey, ok := value.(*apikey.APIKey)
	return apiKey, ok
}

// SetOpsFallbackAPIKey stores a loaded key for Ops error logs when authentication exits early.
// This diagnostic value does not establish authorization for handlers or audit logs.
// Authenticated keys use ContextKeyAPIKey.
func SetOpsFallbackAPIKey(c *gin.Context, apiKey *apikey.APIKey) {
	if c == nil || apiKey == nil {
		return
	}
	c.Set(string(ContextKeyOpsFallbackAPIKey), apiKey)
}

// GetOpsFallbackAPIKey reads the diagnostic key reserved for Ops error logging.
func GetOpsFallbackAPIKey(c *gin.Context) (*apikey.APIKey, bool) {
	value, exists := c.Get(string(ContextKeyOpsFallbackAPIKey))
	if !exists {
		return nil, false
	}
	apiKey, ok := value.(*apikey.APIKey)
	return apiKey, ok
}

// ForcePlatform returns middleware that selects a required platform.
// It sets request context for services and Gin context for handlers.
func ForcePlatform(platform string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Services read the platform through apikey.ForcePlatformFromContext.
		ctx := apikey.WithForcePlatform(c.Request.Context(), platform)
		c.Request = c.Request.WithContext(ctx)
		// Handlers read the same platform through the Gin context helpers.
		c.Set(string(ContextKeyForcePlatform), platform)
		c.Next()
	}
}

// HasForcePlatform reports whether the Gin context contains a required platform.
func HasForcePlatform(c *gin.Context) bool {
	_, exists := c.Get(string(ContextKeyForcePlatform))
	return exists
}

// GetForcePlatformFromContext reads the required platform from Gin context.
func GetForcePlatformFromContext(c *gin.Context) (string, bool) {
	value, exists := c.Get(string(ContextKeyForcePlatform))
	if !exists {
		return "", false
	}
	platform, ok := value.(string)
	return platform, ok
}
