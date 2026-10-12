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
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	upstreamopenai "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

func (p *OpenAIResponseOutput) PassthroughOptions(ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider) upstreamopenai.PassthroughOptions {
	stream := upstreamopenai.StreamOptions{
		NativeOpenAI: provider != nil && provider.Record.Platform == capability.PlatformOpenAI,
		MaxLineSize:  OpenAIResponseDefaultMaxLineSize,
		TTFTMode:     func() string { return p.TTFTMode(ctx) },
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
		BuildOpenAIResponseFailedSSE:        gatewayprovider.BuildOpenAIResponseFailedSSE,
		WrapOpenAIUpstreamWarningIfCyber:    gatewayprovider.WrapOpenAIUpstreamWarningIfCyber,
		TruncateString:                      logredact.TruncateUTF8,
		OpenAIStreamDataStartsTTFT:          gatewayprovider.OpenAIStreamDataStartsTTFT,
		OpenAIStreamEventIsTerminalWithType: openai.OpenAIStreamEventIsTerminalWithType,
	}
	if provider != nil {
		stream.ProviderID = provider.Record.ID
	}
	if p.Options.Configured && p.Options.MaxLineSize > 0 {
		stream.MaxLineSize = p.Options.MaxLineSize
	}
	stream.MarkTime = func(moment upstreamopenai.StreamTime) {
		switch moment {
		case upstreamopenai.StreamTimeFlush:
			MarkOpsTimestamp(c, telemetry.FirstDownstreamFlushAt)
		case upstreamopenai.StreamTimeData:
			MarkOpsTimestamp(c, telemetry.FirstSSEDataAt)
		case upstreamopenai.StreamTimeVisible:
			MarkOpsTimestamp(c, telemetry.FirstVisibleOutputAt)
		}
	}
	nonstream := p.NonStreamOptions(ctx, c, provider)
	nonstream.TerminalFailover = func(resp *http.Response, event string, body []byte, message, model string) error {
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
			// Headers were set before heartbeats started; synchronize metadata from the current output adapter here.
			for key, values := range headers {
				c.Writer.Header()[key] = append([]string(nil), values...)
			}
			if p.Options.Configured && p.Options.StreamKeepaliveInterval > 0 {
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
			logging.FromContext(ctx).With(zap.String("component", "service.openai_gateway"), zap.Int64("provider_id", provider.Record.ID), zap.String("upstream_request_id", id)).Info("OpenAI passthrough 上游流在未收到 [DONE] 时结束，疑似断流")
		},
		PassthroughFailoverWithModel: func(id string, body []byte, message, model string) error {
			return p.NewStreamFailureWithModel(c, provider, true, id, body, message, model)
		},
	}
}
