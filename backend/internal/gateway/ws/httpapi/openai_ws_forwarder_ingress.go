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

// openAIWSImageIntentForRoutingModel builds the intent-check payload using group-mapped model G.
// Broad intent controls image state and billing; explicit client intent controls permission checks.
func openAIWSImageIntentForRoutingModel(routingModel, upstreamModel string, body []byte, platform string) ([]byte, bool, bool) {
	imageIntentBody := body
	if routingModel != upstreamModel {
		imageIntentBody = openaiprotocol.ReplaceModelInBody(body, routingModel)
	}
	imageIntent := gatewayprovider.ImageIntentForPlatform(media.OpenAIResponsesEndpoint, routingModel, imageIntentBody, platform)
	explicitImageIntent := gatewayprovider.ImageIntent().IsExplicitImageGenerationIntent(media.OpenAIResponsesEndpoint, routingModel, imageIntentBody)
	return imageIntentBody, imageIntent, explicitImageIntent
}

// openAIWSIngressInterTurnIdleTimeout returns the allowed client idle time between completed turns.
func (s *OpenAIWebSocketExecutor) openAIWSIngressInterTurnIdleTimeout() time.Duration {
	if s != nil && s.Runtime != nil {
		return time.Duration(s.Runtime.Snapshot().IngressInterTurnIdleTimeoutSeconds) * time.Second
	}
	if s == nil || s.Options == nil || s.Options.IngressInterTurnIdleTimeoutSeconds <= 0 {
		return 0
	}
	return time.Duration(s.Options.IngressInterTurnIdleTimeoutSeconds) * time.Second
}

// newOpenAIWSDownstreamWriteContext uses the client lifecycle when available, so ingress lease loss
// lets the current frame finish before a retry close frame is sent. Otherwise it uses controlCtx.
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

// ProxyResponsesWebSocketFromClient adapts platform execution to the gateway/ws turn loop.
func (s *OpenAIWebSocketExecutor) ProxyResponsesWebSocketFromClient(ctx context.Context, c *gin.Context, clientConn *coderws.Conn, provider *gatewayprovider.ExecutionProvider, token string, firstClientMessage []byte, hooks *gatewayws.OpenAIIngressHooks) error {
	return s.executeWSIngressAdapter(ctx, c, clientConn, provider, token, firstClientMessage, hooks)
}
