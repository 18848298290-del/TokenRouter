package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/moderationflow"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	upstreamopenai "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
	openaiwsv2 "github.com/TokenFlux/TokenRouter/internal/upstream/openai/ws/relay"
)

// openai_ws_v2_passthrough_adapter.go starts dedicated Responses sessions and
// maps relay or handshake failures to client closure.
// Dedicated per-session Responses forwarding adapts client and provider frames
// to the gateway controller, which owns turn admission and read deadlines.
// Relay exits retain whether output reached the client, allowing first-output
// timeout to choose provider failover initially and reconnect after prior
// delivery.
//
// Typed close instructions keep their chosen status and cause, while handshake
// mapping retains upstream headers and body for diagnostics. A canceled dial
// returns its cancellation directly; timeouts, 429, authentication rejection
// and other client-status handshakes select different close reasons. Cyber
// event observation reads usage before marking policy evidence, and its
// bounded diagnostic body retains the response status separately from the
// event's HTTP transport success.

// openaiWSV2PassthroughModeFields supplies stable dedicated-relay labels on every
// passthrough diagnostic so transport selection is identifiable beside per-event fields.
const openaiWSV2PassthroughModeFields = "ws_mode=passthrough ws_router=v2"

// openAIWSCoreFrames adapts upstream frame transport to the gateway interface.
type openAIWSCoreFrames struct {
	openaiwsv2.FrameConn /* retained upstream frame transport */
}

// ReadFrame delegates one upstream frame read and converts its transport enum
// to the gateway integer. Payload and error remain the supplied transport
// result; the caller's context controls that underlying read.
func (c openAIWSCoreFrames) ReadFrame(ctx context.Context) (int, []byte, error) {
	typ, body, err := c.FrameConn.ReadFrame(ctx)
	return int(typ), body, err
}

// WriteFrame converts the gateway message type and delegates one upstream
// frame write. It forwards the caller's context and bytes directly, with
// transport cleanup and write timeout controlled by the session owner.
func (c openAIWSCoreFrames) WriteFrame(ctx context.Context, typ int, body []byte) error {
	return c.FrameConn.WriteFrame(ctx, coderws.MessageType(typ), body)
}

// proxyResponsesWebSocketV2Passthrough binds a selected provider and client
// connection to dedicated session execution. Missing executor, client or
// provider fails before relay; bearer validation accepts Agent Identity's
// dial-time authentication. Required shared services and request context must
// be initialized.
//
// The parameter callback reads Runtime for later turns, otherwise static
// Options or DefaultParameters. Optional HTTP hooks become gateway admission
// and completion callbacks while retaining client lifetime, initial model and
// first-turn timing. The gateway core owns normalization, dialing, relay
// cancellation and transport release; this adapter returns its final close or
// failover error to entry.
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
		coreHooks.BeginPerformance = func(turn int, started time.Time) *timing.Performance {
			turnCtx := ctx
			if hooks.ContextForTurn != nil {
				turnCtx = hooks.ContextForTurn(turn)
			}
			return timing.BeginPerformance(turnCtx, provider.Record.ID, started)
		}
		coreHooks.ClientDisconnected = func(turn int, at time.Time) {
			turnCtx := ctx
			if hooks.ContextForTurn != nil {
				turnCtx = hooks.ContextForTurn(turn)
			}
			timing.ObserveClientDisconnect(turnCtx, at)
		}
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

// openAIWSPassthroughRelayClientClose returns an optional close status and
// reason for a dedicated relay exit. Explicit typed closes take precedence,
// followed by active-turn timeout. A first-output timeout returns reconnect
// only after completed turns or downstream writes; an initial pre-output
// timeout remains available for failover.
//
// A non-graceful read_upstream exit produces an internal-error close. Other
// exits return false so the controller can classify graceful client
// termination or ordinary errors separately. The completed-turn count and
// WroteDownstream flag therefore prevent a later idle upstream failure from
// being presented as a new initial provider attempt.
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

// markOpenAIWSV2PassthroughCyberPolicy records detected Cyber error evidence
// with any completed-event usage. Unmatched payloads return false; matches
// store code, message and a UTF-8-limited body under HTTP 200 because the
// failure arrived inside an accepted WS event stream.
func markOpenAIWSV2PassthroughCyberPolicy(c *gin.Context, payload []byte) bool {
	hit, code, message := upstreamopenai.DetectOpenAICyberPolicy(payload)
	if !hit {
		return false
	}
	usage := openai.ForwardUsage{}
	openai.ParseWSResponseUsageFromCompletedEvent(payload, &usage)
	gatewayhttp.MarkOpsCyberPolicy(c, moderationflow.Mark{
		Code:    code,
		Message: message,
		// Limit the policy diagnostic excerpt at a UTF-8 boundary while retaining
		// parsed usage separately for Cyber observation of the failed WS turn.
		Body:           logredact.TruncateUTF8(string(payload), 4096),
		UpstreamStatus: http.StatusOK,
		UpstreamInTok:  usage.InputTokens,
		UpstreamOutTok: usage.OutputTokens,
	})
	return true
}

// mapOpenAIWSPassthroughDialError retains handshake diagnostics and selects
// the client instruction for a dedicated dial failure. Nil error returns nil.
// Existing WSDialError values stay wrapped as supplied; other errors gain
// cloned headers and copied handshake body when available.
//
// Cancellation returns the original error even after diagnostic preparation.
// Deadline and 429 use try-again-later, 401 or 403 use authentication policy
// closure, and other 4xx statuses use handshake-rejected policy closure.
// Remaining failures return a wrapped diagnostic error, leaving entry to
// choose its general failure presentation.
func (s *OpenAIWebSocketExecutor) mapOpenAIWSPassthroughDialError(
	err error,
	statusCode int,
	handshakeHeaders http.Header,
) error {
	if err == nil {
		return nil
	}
	wrappedErr := err
	// Attach handshake evidence before choosing the client close status so wrapped
	// non-WebSocket errors still expose upstream rejection details to entry diagnostics.
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

// logOpenAIWSV2Passthrough writes a legacy structured diagnostic with
// dedicated relay mode labels.
func logOpenAIWSV2Passthrough(format string, args ...any) {
	logging.LegacyPrintf(
		"service.openai_ws_v2",
		"[OpenAI WS v2 passthrough] %s "+format,
		append([]any{openaiWSV2PassthroughModeFields}, args...)...,
	)
}
