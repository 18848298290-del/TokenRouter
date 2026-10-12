package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// openAIImageIntentHintContextKey caches HTTP request image intent. Absence means unknown; both boolean
// values are cached decisions.
const openAIImageIntentHintContextKey = "openai_image_intent_hint"

type ImageIntentClassifier func(endpoint string, requestedModel string, body []byte) bool

// GroupMappedImageIntent rewrites the model and body with the group mapping before classifying image intent.
func GroupMappedImageIntent(endpoint, model string, body []byte, mapping routing.GroupMappingResult, platform string, replace requeststate.ModelBodyReplacer) ([]byte, string, bool) {
	target := requeststate.GroupMappedModel(model, mapping)
	rewritten := requeststate.ModelMappedBody(body, mapping.Mapped, target, replace)
	return rewritten, target, gatewayadapter.ImageIntentForPlatform(endpoint, target, rewritten, platform)
}

// SeedOpenAIForwardImageIntentHint seeds unmapped requests; mapped requests require classification after
// rewriting.
func SeedOpenAIForwardImageIntentHint(c *gin.Context, mapped, image bool) {
	if mapped {
		return
	}
	SetOpenAIImageIntentHint(c, image)
}

// SetOpenAIImageIntentHint stores the canonical image-intent decision for an HTTP request.
func SetOpenAIImageIntentHint(c *gin.Context, imageIntent bool) {
	if c == nil || GetOpenAIClientTransport(c) != OpenAIClientTransportHTTP {
		return
	}
	c.Set(openAIImageIntentHintContextKey, imageIntent)
}

func GetOpenAIImageIntentHint(c *gin.Context) (imageIntent bool, known bool) {
	if c == nil || GetOpenAIClientTransport(c) != OpenAIClientTransportHTTP {
		return false, false
	}
	value, ok := c.Get(openAIImageIntentHintContextKey)
	if !ok {
		return false, false
	}
	imageIntent, ok = value.(bool)
	return imageIntent, ok
}

func ResolveOpenAIImageIntentHint(
	c *gin.Context,
	requestedModel string,
	canonicalBody []byte,
	classify ImageIntentClassifier,
) bool {
	if imageIntent, known := GetOpenAIImageIntentHint(c); known {
		return imageIntent
	}
	imageIntent := classify(media.OpenAIResponsesEndpoint, requestedModel, canonicalBody)
	SetOpenAIImageIntentHint(c, imageIntent)
	return imageIntent
}

func ResolveOpenAIPassthroughImageIntent(
	c *gin.Context,
	canonicalRequestedModel string,
	canonicalBody []byte,
	attemptRequestedModel string,
	attemptBody []byte,
	attemptInvalidated bool,
	classify ImageIntentClassifier,
) bool {
	imageIntent := ResolveOpenAIImageIntentHint(c, canonicalRequestedModel, canonicalBody, classify)
	if attemptInvalidated {
		// Reclassify this attempt after stripping or Compact rewriting; keep the request-level hint unchanged.
		imageIntent = classify(media.OpenAIResponsesEndpoint, attemptRequestedModel, attemptBody)
	}
	return imageIntent
}
