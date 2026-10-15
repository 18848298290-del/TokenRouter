package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/compact"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	forward "github.com/TokenFlux/TokenRouter/internal/gateway/provider/openaiforward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	openaicore "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// openai_forward_http_adapter.go binds Responses HTTP attempts to recovery,
// provider health, and output callbacks and prepares encrypted-content
// retries.
// Responses HTTP recovery reuses prepared model and policy state while a
// rejected request body may need decoding and encrypted-history removal. The
// supplied decoder must retain numeric precision, because full serialization
// of a repaired body would otherwise change unrelated large identifiers.
//
// The HTTP options bind identity, Compact recovery, provider health and final
// output to one live request. Stream/nonstream output settings are constructed
// when the runner has a response, keeping their request observations tied to
// the selected attempt. Captured error bytes support both recovery
// classification and later client presentation, and client-tool SSE wrapping
// follows the request's restoration mapping while the shared runner owns
// response cleanup.

// nativeForwardHTTPOptions binds the current Responses attempt to the shared
// HTTP runner. Callers retain c, provider, executor services and input bytes
// throughout RunHTTP. Stream/nonstream options are constructed after response
// arrival, and supplied encrypted-retry/lineage callbacks retain this prepared
// request's body state.
//
// Identity and Compact callbacks prepare local recovery, while health
// callbacks can return a classified failover or leave generic error output to
// ErrorResponse. Client-tool stream wrapping uses any positive configured line
// limit, independently of Configured. Response ownership and usage snapshots
// are delegated after the runner observes their corresponding output; body
// closure and retry ordering stay with RunHTTP.
func (s *OpenAIResponsesExecutor) nativeForwardHTTPOptions(ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, input forward.HTTPInput, exchange openai.HTTPExchangeOptions, retryEncrypted func([]byte) ([]byte, bool, error), markLineage func([]byte)) forward.HTTPOptions {
	return forward.HTTPOptions{
		Exchange: exchange, Sink: NewResponseSink(c),
		StreamOptions: func() openai.StreamOptions {
			return s.Output.StreamOptions(ctx, c, provider, input.ReasoningEffortValue)
		},
		NonStreamOptions: func() openai.NonStreamOptions { return s.Output.NonStreamOptions(ctx, c, provider) },
		ReadErrorBody:    s.Output.ReadErrorBody,
		IsAgentIdentity:  func(ctx context.Context) bool { return s.Requests.Identity.UsesAgentIdentity(ctx, provider) },
		InvalidAgentTask: openai.IsAgentTaskInvalidHTTPResponse,
		RecoverAgentTask: func(ctx context.Context) error {
			return s.Requests.Identity.Recover(ctx, provider, provider.View().GetCredential("task_id"))
		},
		RedactErrorBody: func(ctx context.Context, body []byte) []byte {
			return s.Requests.Identity.Redact(ctx, provider, body)
		},
		ErrorDetails: func(body []byte) (string, string) {
			return logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(body))), upstream.ExtractErrorCode(body)
		},
		RetryEncrypted: retryEncrypted, MarkInvalidLineage: markLineage,
		CompactRetry: func(body []byte, status int, message string, payload []byte, tried bool) ([]byte, string, bool) {
			return s.Text.Compact.Prepare(c, provider, input.RequestedModel, body, status, message, payload, tried)
		},
		CompactRetryObserved: func(resp *http.Response, payload []byte, message string) {
			s.Text.Compact.Observe(c, provider, resp, payload, message, false)
		},
		CompactSignal: func(err error) (forward.CompactFailure, bool) {
			signal, ok := compact.AsFailure(err)
			if !ok {
				return forward.CompactFailure{}, false
			}
			return forward.CompactFailure{Message: signal.Message, Payload: signal.Payload}, true
		},
		CompactErrorResponse: func(resp *http.Response, signal forward.CompactFailure) (*http.Response, []byte) {
			return CompactFallbackErrorResponse(resp, &compact.Failure{Message: signal.Message, Payload: signal.Payload})
		},
		ShouldFailover: gatewayprovider.ShouldFailoverOpenAIResponse,
		HTTPFailover: func(resp *http.Response, payload []byte, message, model string) error {
			detail := ""
			if s.Output.Options.LogUpstreamErrorBody {
				limit := s.Output.Options.LogUpstreamErrorBodyMaxBytes
				if limit <= 0 {
					limit = 2048
				}
				// Bound optional diagnostic detail while health receives the captured
				// payload; UTF-8 truncation keeps the recorded excerpt character-complete.
				detail = logredact.TruncateUTF8(string(payload), limit)
			}
			AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{Platform: provider.Record.Platform, ProviderID: provider.Record.ID, ProviderName: provider.Record.Name, UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: resp.Header.Get("x-request-id"), Kind: "failover", Message: message, Detail: detail})
			decision := s.Output.ApplyHTTPFailure(ctx, resp, provider, payload, model)
			if decision.ShouldReturnGenericError() {
				return nil
			}
			return gatewayprovider.NewOpenAIUpstreamFailure(resp.StatusCode, resp.Header, payload, message, decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode))
		},
		CompactFailover: func(resp *http.Response, payload []byte, message, model string) error {
			AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{Platform: provider.Record.Platform, ProviderID: provider.Record.ID, ProviderName: provider.Record.Name, UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: resp.Header.Get("x-request-id"), Kind: "failover", Message: message})
			disabled := s.Output.ApplyHTTPFailure(ctx, resp, provider, payload, model).StopScheduling
			return (gatewayprovider.OpenAIFailoverPolicy{Health: s.Output.Health}).NewProviderFailure(provider, resp.StatusCode, resp.Header, payload, message, disabled, !disabled && provider.View().IsPoolMode() && (provider.View().IsPoolModeRetryableStatus(resp.StatusCode) || openai.IsOpenAITransientProcessingError(resp.StatusCode, message, payload)))
		},
		ErrorResponse: func(resp *http.Response, body []byte, model string) error {
			_, err := s.Output.ResponseError(ctx, resp, c, provider, body, model)
			return err
		},
		ErrorSchedulingModel: gatewayprovider.ErrorSchedulingModel,
		WrapResponseBody: func(resp *http.Response) {
			if mapping, ok := OpenAIResponsesClientToolMapping(c); ok && openai.IsEventStreamResponse(resp.Header) {
				limit := OpenAIResponseDefaultMaxLineSize
				if s.Output.Options.MaxLineSize > 0 {
					limit = s.Output.Options.MaxLineSize
				}
				// A request mapping identifies lowered client tools. Restore its SSE
				// nodes while preserving response-body cleanup through the wrapper's Close.
				resp.Body = upstream.NewResponsesClientToolStreamBody(resp.Body, mapping, limit)
			}
		},
		BindResponseOwner: func(ctx context.Context, id string) { s.Output.BindResponseProvider(ctx, c, provider, id) },
		UpdateUsageSnapshot: func(ctx context.Context, headers http.Header) {
			if snapshot := openai.ParseCodexRateLimitHeaders(headers); snapshot != nil {
				s.Text.CodexUsage.Observe(ctx, provider.Record.ID, snapshot)
			}
		},
		ObserveUpstreamModel: func(model string) { SetOpsUpstreamModel(c, model) },
		ObservedServiceTier:  func() string { return ObservedUpstreamResponseServiceTier(c) },
		ResolvedServiceTier:  func(tier *string) *string { return ResolvedOpenAIUpstreamServiceTier(c, tier) },
		ExtractServiceTier:   requeststate.ExtractOpenAIServiceTierFromBody,
		Log: func(format string, args ...any) {
			logging.LegacyPrintf("service.openai_gateway", format, args...)
		},
	}
}

// prepareOpenAIHTTPEncryptedRetry removes encrypted reasoning from a decoded
// request and serializes an adopted repair. Callers supply a writable,
// cache-aware decoder that retains JSON number precision. Decoding failures
// return before trimming, and no removable content returns the supplied body
// with false.
//
// Successful trimming mutates the decoder's map before serialization, so an
// encoding error can leave that mutable view changed even though no
// replacement body is returned. The caller therefore treats the byte result
// and change flag as adoption evidence. Number-aware serialization keeps
// supported unrelated numeric literals in the repaired body while the shared
// HTTP runner owns whether an explicit upstream rejection permits retry.
func prepareOpenAIHTTPEncryptedRetry(body []byte, decode func([]byte) (map[string]any, error)) ([]byte, bool, error) {
	decoded, err := decode(body)
	if err != nil {
		return nil, false, err
	}
	// An encrypted-content rejection permits removing reasoning before reencoding.
	// The decoder retains number literals so unrelated identifiers survive whole-body serialization.
	if !openaicore.TrimEncryptedReasoningItems(decoded) {
		return body, false, nil
	}
	result, err := wirejson.Marshal(decoded)
	if err != nil {
		return nil, false, fmt.Errorf("serialize invalid_encrypted_content retry body: %w", err)
	}
	return result, true, nil
}
