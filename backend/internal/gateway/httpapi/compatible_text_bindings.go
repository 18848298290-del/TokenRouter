package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/gateway/execution"
	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Generic Responses and Chat entries reuse Messages admission operations while
// selecting their own image and moderation behavior. Group mapping supplies the
// model checked for image eligibility, and bound HTTP functions carry the
// prepared body and request observations into execution. Inherited Eligibility
// calls the Messages funding binding with empty platform and afterWait=false.
// FundingAdmission selects its initial-admission path, which can also check RPM
// after user-slot waiting when payer data and the RPM checker are available.

// compatibleTextHTTPBackend adds OpenAI-shaped preparation to the shared Messages bindings.
type compatibleTextHTTPBackend struct {
	messagesHTTPBackend // shared Messages admission operations
	// replace applies the group-selected model when image preparation requests
	// a rewritten body; GroupMappedImageIntent defines when that callback is used.
	replace requeststate.ModelBodyReplacer
}

// NewBoundCompatibleTextHandler binds HTTP services, group-model rewriting, and the executor.
// Dependencies are supplied during assembly and remain available while serving
// requests. The embedded Messages backend supplies admission callbacks, while
// replace serves the Responses image-preparation path.
func NewBoundCompatibleTextHandler(options MessagesHTTPOptions, bindings MessagesBindings, replace requeststate.ModelBodyReplacer, prompt MessagesPrompt, concurrency *ConcurrencyHelper, executor execution.Executor) *CompatibleTextHandler {
	return NewCompatibleTextHandler(options, compatibleTextHTTPBackend{messagesHTTPBackend{bindings}, replace}, prompt, concurrency, executor)
}

// ImageIntent prepares the group-mapped Responses body and reports image eligibility.
// It supplies a copied key to the compatibility-platform helper and delegates
// byte rewriting to GroupMappedImageIntent. Its body result may be the supplied
// slice when no rewrite is requested.
func (p compatibleTextHTTPBackend) ImageIntent(key *apikey.APIKey, model string, body []byte, mapping routing.GroupMappingResult) ([]byte, bool) {
	projected := apikey.CopyAPIKey(key)
	forwarded, _, image := GroupMappedImageIntent("/v1/responses", model, body, mapping, OpenAICompatibleRequestPlatform(projected), p.replace)
	return forwarded, image
}

// ImageContext adds the image-generation hint to the supplied execution context.
func (p compatibleTextHTTPBackend) ImageContext(ctx context.Context) context.Context {
	return requeststate.WithOpenAIImageGenerationIntent(ctx)
}

// ChatImageModel recognizes image-generation models after the group's mapping is applied.
func (p compatibleTextHTTPBackend) ChatImageModel(model string, mapping routing.GroupMappingResult) bool {
	return media.IsGPTImageGenerationModel(requeststate.GroupMappedModel(model, mapping))
}

// Moderate submits the supplied entry protocol and prepared body to gateway moderation.
// It passes a copied key view to the bound moderation operations. The handler
// reads any returned blocked decision before obtaining user capacity or executing upstream work.
func (p compatibleTextHTTPBackend) Moderate(c *gin.Context, log *zap.Logger, key *apikey.APIKey, subject authctx.AuthSubject, protocol, model string, body []byte) *moderation.Decision {
	return RunContentModeration(GatewayModerationEndpoints{}, c, log, p.bindings.Moderation, apikey.CopyAPIKey(key), subject, protocol, model, body)
}

// AuthLatency records elapsed preparation milliseconds under the Ops authentication latency key.
func (p compatibleTextHTTPBackend) AuthLatency(c *gin.Context, millis int64) {
	SetOpsLatencyMs(c, OpsAuthLatencyMsKey, millis)
}
