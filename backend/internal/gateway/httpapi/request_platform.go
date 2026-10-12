package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
)

// OpenAICompatibleRequestPlatform returns an empty platform so selection can use provider capabilities.
func OpenAICompatibleRequestPlatform(_ *apikey.APIKey) string { return "" }

// EffectiveAPIKeyPlatform returns the platform forced by route middleware.
// An empty result leaves the platform to provider selection.
func EffectiveAPIKeyPlatform(c *gin.Context, apiKey *apikey.APIKey) string {
	if c != nil {
		if forced, ok := keyhttp.GetForcePlatformFromContext(c); ok && strings.TrimSpace(forced) != "" {
			return strings.TrimSpace(forced)
		}
	}
	return OpenAICompatibleRequestPlatform(apiKey)
}
