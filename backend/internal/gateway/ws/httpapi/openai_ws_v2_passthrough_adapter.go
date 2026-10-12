package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/moderationflow"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	upstreamopenai "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
	openaiwsv2 "github.com/TokenFlux/TokenRouter/internal/upstream/openai/ws/relay"
)

const openaiWSV2PassthroughModeFields = "ws_mode=passthrough ws_router=v2"

// gateway/ws owns first-output and active-read timeouts.

// openAIWSCoreFrames adapts upstream frame transport to the gateway interface.
type openAIWSCoreFrames struct{ openaiwsv2.FrameConn }

func (c openAIWSCoreFrames) ReadFrame(ctx context.Context) (int, []byte, error) {
	typ, body, err := c.FrameConn.ReadFrame(ctx)
	return int(typ), body, err
}

func (c openAIWSCoreFrames) WriteFrame(ctx context.Context, typ int, body []byte) error {
	return c.FrameConn.WriteFrame(ctx, coderws.MessageType(typ), body)
}

func (s *OpenAIWebSocketExecutor) proxyResponsesWebSocketV2Passthrough(
	ctx context.Context,
	c *gin.Context,
	clientConn *coderws.Conn,
	provider *gatewayprovider.ExecutionProvider,
	token string,
	firstClientMessage []byte,
	hooks *gatewayws.OpenAIIngressHooks,
	wsDecision egress.OpenAIWSProtocolDecision,
	tlsRouterMatch egress.TLSFingerprintRouterMatchResult,
) error {
	if s == nil {
		return errors.New("service is nil")
	}
	if clientConn == nil {
		return errors.New("client websocket is nil")
	}
	if provider == nil {
		return errors.New("provider is nil")
	}
	if err := validateOpenAIWSBearerToken(provider, token); err != nil {
		return err
	}
	port := &wsPassthroughAdapter{service: s, request: c, provider: provider, token: token, hooks: hooks, decision: wsDecision, router: tlsRouterMatch}
	var coreHooks *gatewayws.PassthroughHooks
	if hooks != nil {
		coreHooks = &gatewayws.PassthroughHooks{IngressHooks: wsIngressHooks(hooks), InitialRequestModel: hooks.InitialRequestModel, InitialTurnStartedAt: hooks.InitialTurnStartedAt, OnUpstreamError: hooks.OnUpstreamError}
	}
	runtime := gatewayws.PassthroughSession{Parameters: func() gatewayws.Parameters {
		if s.Runtime != nil {
			return s.Runtime.Snapshot()
		}
		if s.Options != nil {
			return *s.Options
		}
		return gatewayws.DefaultParameters()
	}, Port: port, Hooks: coreHooks, Options: gatewayws.PassthroughOptions{ProviderID: provider.Record.ID, OAuth: provider.View().IsOpenAIOAuth(), WriteTimeout: s.openAIWSWriteTimeout(), IdleTimeout: s.openAIWSPassthroughIdleTimeout(), InterTurnIdleTimeout: s.openAIWSIngressInterTurnIdleTimeout()}}
	return runtime.Run(ctx, gatewayhttp.WSClientFrames{Conn: clientConn}, firstClientMessage)
}

func openAIWSPassthroughRelayClientClose(exit openaiwsv2.RelayExit, completedTurns int) (coderws.StatusCode, string, bool) {
	if closeErr, ok := errors.AsType[*gatewayhttp.OpenAIWSClientCloseError](exit.Err); ok {
		return closeErr.StatusCode(), closeErr.Reason(), true
	}
	if _, ok := errors.AsType[*gatewayws.ActiveTurnTimeoutError](exit.Err); ok {
		return coderws.StatusGoingAway, "upstream websocket read timeout; please reconnect", true
	}
	if _, ok := errors.AsType[*gatewayws.FirstOutputTimeoutError](exit.Err); ok {
		if completedTurns > 0 || exit.WroteDownstream {
			return coderws.StatusGoingAway, "upstream produced no semantic output; please reconnect", true
		}
		return 0, "", false
	}
	if !exit.Graceful && exit.Stage == "read_upstream" {
		return coderws.StatusInternalError, "upstream websocket proxy failed", true
	}
	return 0, "", false
}

func markOpenAIWSV2PassthroughCyberPolicy(c *gin.Context, payload []byte) bool {
	hit, code, message := upstreamopenai.DetectOpenAICyberPolicy(payload)
	if !hit {
		return false
	}
	usage := openai.ForwardUsage{}
	openai.ParseWSResponseUsageFromCompletedEvent(payload, &usage)
	gatewayhttp.MarkOpsCyberPolicy(c, moderationflow.Mark{
		Code:           code,
		Message:        message,
		Body:           logredact.TruncateUTF8(string(payload), 4096),
		UpstreamStatus: http.StatusOK,
		UpstreamInTok:  usage.InputTokens,
		UpstreamOutTok: usage.OutputTokens,
	})
	return true
}

func (s *OpenAIWebSocketExecutor) mapOpenAIWSPassthroughDialError(
	err error,
	statusCode int,
	handshakeHeaders http.Header,
) error {
	if err == nil {
		return nil
	}
	wrappedErr := err
	if _, ok := errors.AsType[*upstreamopenai.WSDialError](err); !ok {
		var handshakeErr *upstreamopenai.WSHandshakeError
		var responseBody []byte
		if errors.As(err, &handshakeErr) && handshakeErr != nil {
			responseBody = append([]byte(nil), handshakeErr.Body...)
		}
		wrappedErr = &upstreamopenai.WSDialError{
			StatusCode:      statusCode,
			ResponseHeaders: upstream.CloneHeader(handshakeHeaders),
			ResponseBody:    responseBody,
			Err:             err,
		}
	}

	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return gatewayhttp.NewOpenAIWSClientCloseError(
			coderws.StatusTryAgainLater,
			"upstream websocket connect timeout",
			wrappedErr,
		)
	}
	if statusCode == http.StatusTooManyRequests {
		return gatewayhttp.NewOpenAIWSClientCloseError(
			coderws.StatusTryAgainLater,
			"upstream websocket is busy, please retry later",
			wrappedErr,
		)
	}
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		return gatewayhttp.NewOpenAIWSClientCloseError(
			coderws.StatusPolicyViolation,
			"upstream websocket authentication failed",
			wrappedErr,
		)
	}
	if statusCode >= http.StatusBadRequest && statusCode < http.StatusInternalServerError {
		return gatewayhttp.NewOpenAIWSClientCloseError(
			coderws.StatusPolicyViolation,
			"upstream websocket handshake rejected",
			wrappedErr,
		)
	}
	return fmt.Errorf("openai ws passthrough dial: %w", wrappedErr)
}

func logOpenAIWSV2Passthrough(format string, args ...any) {
	logging.LegacyPrintf(
		"service.openai_ws_v2",
		"[OpenAI WS v2 passthrough] %s "+format,
		append([]any{openaiWSV2PassthroughModeFields}, args...)...,
	)
}
