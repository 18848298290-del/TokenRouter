package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// An image-intent decision can change when group mapping or Compact rewriting
// changes a model, or when recovery removes an image-generation tool. HTTP
// requests cache the canonical body's classification, including false results,
// so repeated provider attempts can reuse a known decision. A missing or
// incorrectly typed context value remains unknown and triggers classification.
//
// Attempt-specific changes use their own model and body after resolving the
// canonical hint. Their classification is returned for that execution without
// updating the cache, allowing a later unchanged attempt to use the original
// canonical decision. WebSocket and unknown transports never store this HTTP
// hint, so each resolution on those transports invokes the classifier.

// openAIImageIntentHintContextKey stores the HTTP canonical classification.
// Both boolean values represent known decisions; missing or nonboolean values
// require classification when a request next resolves image intent.
const openAIImageIntentHintContextKey = "openai_image_intent_hint"

// ImageIntentClassifier evaluates image-generation intent for one endpoint,
// model, and payload.
type ImageIntentClassifier func(endpoint string, requestedModel string, body []byte) bool

// GroupMappedImageIntent classifies using the effective group model and
// corresponding body. The supplied replacement callback handles mapped body
// edits; returned bytes can reuse input storage. Callers retain the original
// model separately for request attribution.
func GroupMappedImageIntent(endpoint, model string, body []byte, mapping routing.GroupMappingResult, platform string, replace requeststate.ModelBodyReplacer) ([]byte, string, bool) {
	target := requeststate.GroupMappedModel(model, mapping)
	rewritten := requeststate.ModelMappedBody(body, mapping.Mapped, target, replace)
	return rewritten, target, gatewayadapter.ImageIntentForPlatform(endpoint, target, rewritten, platform)
}

// SeedOpenAIForwardImageIntentHint stores a canonical decision only for
// unmapped requests. Mapped requests remain unresolved because classification
// must follow their model rewrite. SetOpenAIImageIntentHint additionally limits
// storage to HTTP transport.
func SeedOpenAIForwardImageIntentHint(c *gin.Context, mapped, image bool) {
	if mapped {
		return
	}
	SetOpenAIImageIntentHint(c, image)
}

// SetOpenAIImageIntentHint stores an HTTP request's canonical image-generation
// decision. Nil contexts and other transports are ignored. Both true and false
// are stored, so a later read can distinguish a negative result from an unknown
// decision.
func SetOpenAIImageIntentHint(c *gin.Context, imageIntent bool) {
	if c == nil || GetOpenAIClientTransport(c) != OpenAIClientTransportHTTP {
		return
	}
	c.Set(openAIImageIntentHintContextKey, imageIntent)
}

// GetOpenAIImageIntentHint returns the canonical HTTP decision and whether it
// is known. Nil contexts, other transports, absent values, and nonboolean
// values return unknown. A stored false remains a known decision for callers
// deciding whether to rescan.
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

// ResolveOpenAIImageIntentHint reuses a known canonical decision or classifies
// the request. Callers provide classify when the hint is unknown. Only HTTP
// transport stores the result, so WebSocket and unknown transports classify
// again on subsequent calls.
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

// ResolveOpenAIPassthroughImageIntent derives image eligibility for the current
// passthrough attempt. The canonical model and body determine the request-level
// hint, including cached false results. Callers mark attemptInvalidated when
// stripping or Compact mapping changes inputs that can affect image intent.
//
// Invalidated attempts classify their own model and body after resolving the
// canonical hint. That result is returned without replacing the request-level
// cache, so a subsequent provider using unchanged canonical input receives the
// canonical decision rather than the prior attempt's transformed
// classification.
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
		// Stripping and Compact mapping can change this attempt's
		// eligibility. Reclassify its inputs locally so later providers
		// still read the canonical request decision.
		imageIntent = classify(media.OpenAIResponsesEndpoint, attemptRequestedModel, attemptBody)
	}
	return imageIntent
}
