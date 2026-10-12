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

type compatibleTextHTTPBackend struct {
	messagesHTTPBackend
	replace requeststate.ModelBodyReplacer
}

// NewBoundCompatibleTextHandler binds HTTP services, group-model rewriting, and the executor.
func NewBoundCompatibleTextHandler(options MessagesHTTPOptions, bindings MessagesBindings, replace requeststate.ModelBodyReplacer, prompt MessagesPrompt, concurrency *ConcurrencyHelper, executor execution.Executor) *CompatibleTextHandler {
	return NewCompatibleTextHandler(options, compatibleTextHTTPBackend{messagesHTTPBackend{bindings}, replace}, prompt, concurrency, executor)
}

func (p compatibleTextHTTPBackend) ImageIntent(key *apikey.APIKey, model string, body []byte, mapping routing.GroupMappingResult) ([]byte, bool) {
	projected := apikey.CopyAPIKey(key)
	forwarded, _, image := GroupMappedImageIntent("/v1/responses", model, body, mapping, OpenAICompatibleRequestPlatform(projected), p.replace)
	return forwarded, image
}

func (p compatibleTextHTTPBackend) ImageContext(ctx context.Context) context.Context {
	return requeststate.WithOpenAIImageGenerationIntent(ctx)
}

func (p compatibleTextHTTPBackend) ChatImageModel(model string, mapping routing.GroupMappingResult) bool {
	return media.IsGPTImageGenerationModel(requeststate.GroupMappedModel(model, mapping))
}

func (p compatibleTextHTTPBackend) Moderate(c *gin.Context, log *zap.Logger, key *apikey.APIKey, subject authctx.AuthSubject, protocol, model string, body []byte) *moderation.Decision {
	return RunContentModeration(GatewayModerationEndpoints{}, c, log, p.bindings.Moderation, apikey.CopyAPIKey(key), subject, protocol, model, body)
}

func (p compatibleTextHTTPBackend) AuthLatency(c *gin.Context, millis int64) {
	SetOpsLatencyMs(c, OpsAuthLatencyMsKey, millis)
}
