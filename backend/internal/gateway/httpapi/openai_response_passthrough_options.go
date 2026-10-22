package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/gateway/moderationflow"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaytelemetry "github.com/TokenFlux/TokenRouter/internal/gateway/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	upstreamopenai "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// openai_response_passthrough_options.go configures passthrough readers with
// HTTP headers, keepalives and terminal-error callbacks.
// Passthrough readers retain Responses framing while sharing HTTP health and
// terminal-error decisions with ordinary output. Their stream bindings carry
// the current provider and request observations, and their nonstream binding
// changes the terminal failover observation to identify the passthrough path.
//
// Keepalive startup copies the reader's prepared header values onto the live
// HTTP writer before a heartbeat can commit them. The returned stop callback
// belongs to the reader's lifetime. Passthrough header relay includes quota
// and turn-state policy, while model observation uses the request observer
// established during attempt preparation. These choices keep timeout and error
// handling attached to the response whose bytes the client receives.

// PassthroughOptions binds passthrough stream and buffered readers to the
// current HTTP attempt. Callers establish model observation during attempt
// preparation and retain c, provider and dependencies until reading completes.
// The reader owns its returned heartbeat-stop callback, while the execution
// caller closes the response body.
//
// Startup copies prepared header slices before starting a configured
// heartbeat, so the writer commits the reader's current metadata. Nonstream
// terminal classification uses passthrough observations, and stream error
// callbacks preserve that path marker for Ops. Model, missing-usage and
// missing-terminal observations remain separate from the entry's completion
// decision.
func (p *OpenAIResponseOutput) PassthroughOptions(ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider) upstreamopenai.PassthroughOptions {
	stream := upstreamopenai.StreamOptions{
		NativeOpenAI: provider != nil && provider.Record.Platform == capability.PlatformOpenAI,
		MaxLineSize:  OpenAIResponseDefaultMaxLineSize,

		Logf: func(format string, args ...any) {
			logging.LegacyPrintf("service.openai_gateway", format, args...)
		},
		MarkCommitted:       func() { MarkResponseCommitted(c) },
		ClientOutputStarted: func(started bool) bool { return OpenAIStreamClientOutputStarted(c, started) },
		ClearDisconnect:     func() { gatewayprovider.ClearProxyStreamDisconnect(p.ProxyCircuit, provider) },
		RecordDisconnect: func(err error, id string) {
			gatewayprovider.RecordProxyStreamDisconnect(p.ProxyCircuit, provider, err, id)
		},
		TerminalSideEffects: func(body []byte, message string, headers http.Header, model string) {
			p.TerminalProviderEffects(c, provider, body, message, headers, model)
		},
		Failover: func(id string, body []byte, message string) error {
			return p.NewStreamFailure(c, provider, true, id, body, message)
		},
		RecordError: func(id, kind string, body []byte, message string) {
			p.RecordStreamError(c, provider, true, id, kind, body, message)
		},
		CompactFallback: func(body []byte, message string) error { return NewOpenAICompactFailure(c, body, message) },
		ErrorRule: func(body []byte, message string) (int, string, string, bool) {
			return ApplyOpenAIStreamFailedErrorRule(c, provider.Record.Platform, body, message)
		},
		CapacitySuppressed: func(id, event string) {
			LogOpenAICapacityFailoverSuppressed(ctx, provider, "passthrough_sse", id, event)
		},
		MarkCyber: func(value upstreamopenai.CyberObservation) {
			MarkOpsCyberPolicy(c, moderationflow.Mark{Code: value.Code, Message: value.Message, Body: value.Body, UpstreamStatus: value.UpstreamStatus, UpstreamInTok: value.UpstreamInTok, UpstreamOutTok: value.UpstreamOutTok})
		},
		RestoreNamespace: func(body []byte) ([]byte, error) { return RestoreOpenAIResponsesNamespacePayload(c, body) },
		RestoreToolNames: func(body []byte, event string) []byte {
			return RestoreCodexToolNamesFromSSEContext(c, body, event)
		},
		EmptyCompleted: func(id string) error {
			return NewOpenAIResponsesEmptyCompletedFailoverError(c, ExecutionErrorProvider(provider), id)
		},
		BuildOpenAIResponseFailedSSE:     gatewayprovider.BuildOpenAIResponseFailedSSE,
		WrapOpenAIUpstreamWarningIfCyber: gatewayprovider.WrapOpenAIUpstreamWarningIfCyber,
		TruncateString:                   logredact.TruncateUTF8,

		OpenAIStreamEventIsTerminalWithType: openai.OpenAIStreamEventIsTerminalWithType,
	}
	if provider != nil {
		stream.ProviderID = provider.Record.ID
	}
	if p.Options.Configured && p.Options.MaxLineSize > 0 {
		stream.MaxLineSize = p.Options.MaxLineSize
	}
	nonstream := p.NonStreamOptions(ctx, c, provider)
	nonstream.TerminalFailover = func(resp *http.Response, event string, body []byte, message, model string) error {
		// A nil concrete failure means no failover proposal. Return nil as an
		// interface so the buffered reader can choose its remaining protocol-error path.
		//nolint:revive // Returning the concrete pointer directly would wrap a nil pointer in a non-nil error.
		if failure := p.nonStreamingTerminalFailure(c, resp, provider, true, event, body, message, model); failure != nil {
			return failure
		}
		return nil
	}
	return upstreamopenai.PassthroughOptions{
		ObserveModel: func(body []byte, event string) {
			UpstreamResponseModelObserverFromContext(c).ObserveOpenAIModel(body, event)
		},
		StreamOptions: stream,
		NonStream:     nonstream,
		Headers: func(dst, src http.Header) {
			WriteOpenAIPassthroughResponseHeaders(dst, src, p.Headers)
		},
		StartKeepalive: func(headers http.Header) func() {
			// Prepared headers must reach the live writer before a heartbeat can
			// commit its status; copy value slices to keep later header-map edits separate.
			for key, values := range headers {
				c.Writer.Header()[key] = append([]string(nil), values...)
			}
			if p.Options.Configured && p.Options.StreamKeepaliveInterval > 0 {
				// Heartbeat settings store seconds; the reader receives its stop callback
				// and ends that writer before its own response lifetime finishes.
				return StartOpenAISSEKeepalive(c, time.Duration(p.Options.StreamKeepaliveInterval)*time.Second)
			}
			return func() {}
		},
		MissingUsage: func(resp *http.Response, usage *openai.ForwardUsage, event string, disconnected bool) {
			if resp == nil {
				return
			}
			var id int64
			if provider != nil {
				id = provider.Record.ID
			}
			gatewaytelemetry.SuccessMissingUsage(ctx, id, resp.StatusCode, usage, event, disconnected)
		},
		MissingTerminal: func(id string) {
			logging.FromContext(ctx).With(zap.String("component", "service.openai_gateway"), zap.Int64("provider_id", provider.Record.ID), zap.String("upstream_request_id", id)).Info("OpenAI passthrough upstream stream ended without [DONE], possible interrupted stream")
		},
		PassthroughFailoverWithModel: func(id string, body []byte, message, model string) error {
			return p.NewStreamFailureWithModel(c, provider, true, id, body, message, model)
		},
	}
}
