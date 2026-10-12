package httpapi

import (
	"context"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	openaiprotocol "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// Responses image checks use the group-resolved routing model even when the
// wire request names a different provider model. Broad image intent supplies
// accounting state, while explicit client intent governs group permission
// checks; Grok's broad check ignores passive tool declarations. Inspection
// bytes remain separate from the actual provider request selected for
// forwarding.
//
// Downstream writes select the original client lifetime independently of
// ingress lease control. Losing capacity ownership can therefore leave frame
// delivery available before the session's reconnect close, while upstream
// operations still use control cancellation. Client idle waits read live
// timeout settings at each boundary between completed turns, and the public
// forwarding method binds those operations to the gateway turn loop.

// openAIWSImageIntentForRoutingModel returns a routing-model inspection body
// with broad and explicit image decisions. Differing routing and upstream
// models replace only the inspection model; the caller retains its actual
// provider body for execution. Broad intent supplies image accounting, while
// explicit intent controls local permission rejection.
//
// The platform-aware broad predicate ignores passive Grok tool declarations,
// and the explicit predicate checks direct generation intent independently.
// Keeping both results lets a provider alias target an image-named model
// without changing the group model whose permissions and pricing were chosen
// for this request.
func openAIWSImageIntentForRoutingModel(routingModel, upstreamModel string, body []byte, platform string) ([]byte, bool, bool) {
	imageIntentBody := body
	if routingModel != upstreamModel {
		// Permission and image billing inspect the group model, before provider
		// aliases can make the same accepted request appear to target another model.
		imageIntentBody = openaiprotocol.ReplaceModelInBody(body, routingModel)
	}
	imageIntent := gatewayprovider.ImageIntentForPlatform(media.OpenAIResponsesEndpoint, routingModel, imageIntentBody, platform)
	explicitImageIntent := gatewayprovider.ImageIntent().IsExplicitImageGenerationIntent(media.OpenAIResponsesEndpoint, routingModel, imageIntentBody)
	return imageIntentBody, imageIntent, explicitImageIntent
}

// openAIWSIngressInterTurnIdleTimeout returns the budget for waiting between
// completed client turns. Runtime is read at each wait; otherwise positive
// static seconds apply, and nil or nonpositive static settings disable this
// timer.
func (s *OpenAIWebSocketExecutor) openAIWSIngressInterTurnIdleTimeout() time.Duration {
	if s != nil && s.Runtime != nil {
		// Convert the latest seconds snapshot for the next client-idle timer.
		return time.Duration(s.Runtime.Snapshot().IngressInterTurnIdleTimeoutSeconds) * time.Second
	}
	if s == nil || s.Options == nil || s.Options.IngressInterTurnIdleTimeoutSeconds <= 0 {
		return 0
	}
	// Convert static seconds when the live snapshot reader is absent.
	return time.Duration(s.Options.IngressInterTurnIdleTimeoutSeconds) * time.Second
}

// newOpenAIWSDownstreamWriteContext returns a timeout context for one
// downstream frame and its cancel function. It prefers ClientLifecycleContext
// over controlCtx so capacity lease cancellation leaves client delivery
// possible; a missing parent uses Background. Callers cancel after the write,
// and the supplied timeout is passed directly to WithTimeout.
func newOpenAIWSDownstreamWriteContext(controlCtx context.Context, hooks *gatewayws.OpenAIIngressHooks, timeout time.Duration) (context.Context, context.CancelFunc) {
	writeParent := controlCtx
	if hooks != nil && hooks.ClientLifecycleContext != nil {
		writeParent = hooks.ClientLifecycleContext
	}
	if writeParent == nil {
		writeParent = context.Background()
	}
	return context.WithTimeout(writeParent, timeout)
}

// ProxyResponsesWebSocketFromClient executes the selected provider against the
// upgraded client session. It delegates transport selection and turn lifecycle
// to executeWSIngressAdapter; that path validates entry arguments, handles
// preemption, and returns typed close or failover errors for the entry loop.
// Required injected services are supplied by construction.
func (s *OpenAIWebSocketExecutor) ProxyResponsesWebSocketFromClient(ctx context.Context, c *gin.Context, clientConn *coderws.Conn, provider *gatewayprovider.ExecutionProvider, token string, firstClientMessage []byte, hooks *gatewayws.OpenAIIngressHooks) error {
	return s.executeWSIngressAdapter(ctx, c, clientConn, provider, token, firstClientMessage, hooks)
}
