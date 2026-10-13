package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	egressadapter "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/moderationflow"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	responseprotocol "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Native OpenAI streaming holds its first output while headers and failure
// events can still belong to a discarded attempt. Header preparation therefore
// returns a pending map for staged output and relays directly for other
// streams; the upstream reader selects when staged headers become eligible for
// commitment.
//
// Codex turn-state ownership follows that commitment, keeping another
// attempt's uncommitted state from being registered as delivered.
// Request-local model observations, heartbeat byte counts and Ops timestamps
// are bound separately from health and recovery callbacks. Grok selects its
// own idle timeout after the common configuration is read, while native OpenAI
// alone receives the first-semantic-output budget used by this reader.

// StreamOptions binds one Responses stream to its request and selected
// provider. Callers retain c and the configured services until reading ends;
// native OpenAI enables first-output staging and its effort-based timeout,
// while Grok uses a platform idle policy.
//
// Header preparation distinguishes pending and direct output. Pending filtered
// headers include the turn-state value but defer ownership recording to
// StagedHeadersCommitted. Direct headers are relayed immediately through
// Turns. This lets the upstream reader discard a staged attempt while keeping
// quota, model and timing observations associated with the request that
// eventually receives content.
func (p *OpenAIResponseOutput) StreamOptions(ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, reasoningEffort string) openai.StreamOptions {
	observer := UpstreamResponseModelObserverFromContext(c)
	if observer == nil {
		observer = BeginUpstreamResponseModelObservation(c)
	}
	options := openai.StreamOptions{
		NativeOpenAI: provider != nil && provider.Record.Platform == capability.PlatformOpenAI,

		StageFirstOutput: provider != nil && provider.Record.Platform == capability.PlatformOpenAI,

		CodexFailureTerminal: provider != nil && provider.View().IsOpenAIOAuthLike(),

		GrokIdlePolicy: provider != nil && provider.Record.Platform == capability.PlatformGrok,

		MaxLineSize: OpenAIResponseDefaultMaxLineSize,

		TTFTMode: func() string { return p.TTFTMode(ctx) },

		Observe: observer.ObserveOpenAI,

		Logf: func(format string, values ...any) {
			logging.LegacyPrintf("service.openai_gateway", format, values...)
		},

		IsCommitted: func() bool { return IsResponseCommitted(c) },

		MarkCommitted: func() { MarkResponseCommitted(c) },

		ClientOutputStarted: func(started bool) bool { return OpenAIStreamClientOutputStarted(c, started) },

		StagedHeadersCommitted: func(headers http.Header) { p.Turns.Commit(c, provider, headers) },

		ClearDisconnect: func() { gatewayprovider.ClearProxyStreamDisconnect(p.ProxyCircuit, provider) },

		RecordDisconnect: func(err error, requestID string) {
			gatewayprovider.RecordProxyStreamDisconnect(p.ProxyCircuit, provider, err, requestID)
		},

		TerminalSideEffects: func(body []byte, message string, headers http.Header, model string) {
			p.TerminalProviderEffects(c, provider, body, message, headers, model)
		},

		Failover: func(requestID string, body []byte, message string) error {
			return p.NewStreamFailure(c, provider, false, requestID, body, message)
		},

		FailoverWithModel: func(requestID string, body []byte, message, model string, headers http.Header) error {
			return p.NewStreamFailureWithModel(c, provider, false, requestID, body, message, model, headers)
		},

		MarkSafeFailover: func(err error) {
			// The reader has already established safe recovery after staged writes;
			// annotate its direct failover error so the outer loop can honor that decision.
			if failure, ok := err.(*forwardcore.UpstreamFailoverError); ok {
				failure.SafeToFailoverAfterWrite = true
			}
		},

		RecordError: func(requestID, kind string, body []byte, message string) {
			p.RecordStreamError(c, provider, false, requestID, kind, body, message)
		},

		CompactFallback: func(body []byte, message string) error { return NewOpenAICompactFailure(c, body, message) },

		ErrorRule: func(body []byte, message string) (int, string, string, bool) {
			return ApplyOpenAIStreamFailedErrorRule(c, provider.Record.Platform, body, message)
		},

		CapacitySuppressed: func(requestID, eventType string) {
			LogOpenAICapacityFailoverSuppressed(ctx, provider, "native_sse", requestID, eventType)
		},

		MarkCyber: func(value openai.CyberObservation) {
			MarkOpsCyberPolicy(c, moderationflow.Mark{
				Code:           value.Code,
				Message:        value.Message,
				Body:           value.Body,
				UpstreamStatus: value.UpstreamStatus,
				UpstreamInTok:  value.UpstreamInTok,
				UpstreamOutTok: value.UpstreamOutTok,
			})
		},

		ToolCorrector: p.Corrector,

		RestoreClientTools: func(body []byte) ([]byte, error) { return RestoreGrokResponsesClientToolPayload(c, body) },

		RestoreNamespace: func(body []byte) ([]byte, error) { return RestoreOpenAIResponsesNamespacePayload(c, body) },

		RestoreToolNames: func(body []byte, eventType string) []byte {
			return RestoreCodexToolNamesFromSSEContext(c, body, eventType)
		},

		EmptyCompleted: func(requestID string) error {
			return NewOpenAIResponsesEmptyCompletedFailoverError(c, ExecutionErrorProvider(provider), requestID)
		},

		CountSearch: grok.CountGrokNativeSearchCallsInSSEDataDedup,

		StreamTimeout: func(model string) {
			if p.Observer != nil {
				p.Observer.Core.HandleStreamTimeout(ctx, gatewayprovider.ExecutionRecord(provider), model)
			}
		},

		IdleCooldown: func() {
			// An idle Grok stream requests a two-minute provider cooldown through
			// its health adapter before the outer execution loop chooses further recovery.
			p.GrokHealth.TempUnschedule(ctx, provider.View(), 2*time.Minute, "grok stream idle timeout")
		},

		IdleFailover: func(interval time.Duration) error { return gatewayprovider.GrokStreamIdleFailure(provider, interval) },

		FirstOutputError: func(start time.Time, model, effort string, timeout time.Duration, phase string, headers http.Header) error {
			return p.FirstOutputFailure(ctx, c, provider, start, model, effort, timeout, phase, headers)
		},

		KeepaliveBytes: func(count int) { RecordOpenAIStreamKeepaliveBytes(c, count) },

		BuildOpenAIResponseFailedSSE: gatewayprovider.BuildOpenAIResponseFailedSSE,

		WrapOpenAIUpstreamWarningIfCyber: gatewayprovider.WrapOpenAIUpstreamWarningIfCyber,

		TruncateString: logredact.TruncateUTF8,

		OpenAIStreamDataStartsTTFT: gatewayprovider.OpenAIStreamDataStartsTTFT,

		OpenAIStreamEventIsTerminalWithType: responseprotocol.OpenAIStreamEventIsTerminalWithType,
	}
	if provider != nil {
		options.ProviderID = provider.Record.ID
	}
	if options.NativeOpenAI {
		options.FirstOutputTimeout = p.FirstOutputTimeout(reasoningEffort)
	}
	if p.Options.Configured {
		if p.Options.MaxLineSize > 0 {
			options.MaxLineSize = p.Options.MaxLineSize
		}
		if p.Options.StreamDataIntervalTimeout > 0 {
			// Reader timer configuration stores whole seconds.
			options.StreamInterval = time.Duration(p.Options.StreamDataIntervalTimeout) * time.Second
		}
		if p.Options.StreamKeepaliveInterval > 0 {
			// Reader timer configuration stores whole seconds.
			options.KeepaliveInterval = time.Duration(p.Options.StreamKeepaliveInterval) * time.Second
		}
	}
	if options.GrokIdlePolicy {
		seconds := 0
		if p.Options.Configured {
			seconds = p.Options.StreamDataIntervalTimeout
		}
		// Grok resolves its own idle defaults after common configuration, giving
		// that platform's stream reader the timeout required by its idle-recovery policy.
		options.StreamInterval = grok.ResolveStreamIdleTimeout(seconds)
	}
	options.PrepareHeaders = func(headers http.Header, staged bool, output http.Header) http.Header {
		var pending http.Header
		// First-output staging keeps these headers separate from the live writer;
		// a discarded provider attempt must not publish its selected response metadata.
		if staged {
			if p.Headers != nil {
				pending = egressadapter.FilterHeaders(headers, p.Headers)
			} else if requestID := strings.TrimSpace(headers.Get("x-request-id")); requestID != "" {
				pending = http.Header{"X-Request-Id": {requestID}}
			}
		} else if p.Headers != nil {
			egressadapter.WriteFilteredHeaders(output, headers, p.Headers)
		}
		// Turn state follows the same pending/direct distinction as response headers,
		// and ownership is recorded later only for the staged map chosen for output.
		if staged {
			StageCodexTurnState(&pending, headers)
		} else {
			p.Turns.Relay(c, provider, headers)
		}
		return pending
	}
	options.MarkTime = func(moment openai.StreamTime) {
		switch moment {
		case openai.StreamTimeFlush:
			MarkOpsTimestamp(c, telemetry.FirstDownstreamFlushAt)
		case openai.StreamTimeData:
			MarkOpsTimestamp(c, telemetry.FirstSSEDataAt)
		case openai.StreamTimeVisible:
			MarkOpsTimestamp(c, telemetry.FirstVisibleOutputAt)
		}
	}
	return options
}
