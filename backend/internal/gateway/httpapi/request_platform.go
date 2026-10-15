package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
)

// request_platform.go resolves route-forced platform restrictions for
// OpenAI-compatible provider selection. OpenAI-compatible entry points can
// select providers from several platforms according to their protocol
// capabilities. Dedicated route middleware can constrain that selection with a
// forced platform; ordinary API Key group data leaves the platform
// unspecified.

// OpenAICompatibleRequestPlatform returns an unspecified platform for
// capability-based provider selection.
func OpenAICompatibleRequestPlatform(_ *apikey.APIKey) string { return "" }

// EffectiveAPIKeyPlatform returns a nonblank route-forced platform after
// trimming it. A missing context or blank forced value leaves the platform
// unspecified, allowing compatible selection to inspect candidate capabilities
// across platforms.
func EffectiveAPIKeyPlatform(c *gin.Context, apiKey *apikey.APIKey) string {
	if c != nil {
		if forced, ok := keyhttp.GetForcePlatformFromContext(c); ok && strings.TrimSpace(forced) != "" {
			return strings.TrimSpace(forced)
		}
	}
	return OpenAICompatibleRequestPlatform(apiKey)
}
