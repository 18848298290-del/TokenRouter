package grok

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// responses_execute.go executes Grok Responses attempts and returns output and
// usage observations from the response reader.
// Grok Responses readers report usage and output commitment independently, and
// either observation can accompany a read error. The executor keeps those facts
// together in one attempt result while activity registration, provider-local
// replay, response hooks and stream wrappers determine how the response is
// consumed.

// DefaultStreamIdleTimeout gives Grok stream readers an idle budget when the
// gateway supplies no positive setting.
const DefaultStreamIdleTimeout = 180 * time.Second

// ResponsesTarget supplies the selected provider's Responses exchange and output
// reader. Callbacks run synchronously during Execute; the prepared exchange
// carries request cancellation, while the reader supplies completion evidence
// and any response-body replacement obligations.
type ResponsesTarget struct {
	// PassRawStream bypasses executor stream wrappers when a bridge reader needs
	// the unfiltered vendor events itself.
	PassRawStream bool   // bridge reader selection
	ProviderID    int64  // selected provider identifier
	Model         string // selected upstream model
	// Enter registers this attempt before exchange and returns a callable release
	// function that Execute defers until processing ends.
	Enter    func() (func(), error) // optional activity registration
	Exchange ResponsesExchange      // provider exchange callbacks
	// BeforeResponse receives the effective replay body; handled or error stops
	// normal reading while any replaced body needs callback cleanup.
	BeforeResponse func(*http.Response, []byte) (bool, error) // optional response policy
	// MaxLineSize bounds filtered stream lines; nonpositive values select the
	// wrapper's default SSE line limit.
	MaxLineSize int // SSE line bytes
	// ClientTools pairs adapted request tools with stream restoration; leave its
	// maps stable throughout this attempt's response reading.
	ClientTools bridge.ResponsesClientToolMapping // client tool mapping
	// ReadResponse supplies parsed observations with its error. Execute copies
	// HasUsage alongside values when Usage is nonnil, while commitment, disconnect
	// and timing observations are transferred separately.
	ReadResponse func(*http.Response, upstream.AttemptInput, upstream.OutputSink) (upstream.ResponsesObservation, error) // required response reader
}

// ResponsesExecutor runs one configured Grok Responses attempt through its
// target.
type ResponsesExecutor struct{}

// TargetID returns the provider identifier used to associate this attempt.
func (t *ResponsesTarget) TargetID() int64 { return t.ProviderID }

// String returns a fixed diagnostic label for Grok Responses targets.
func (*ResponsesTarget) String() string { return "grok responses target" }

// GoString returns the same fixed label for formatted target diagnostics.
func (t *ResponsesTarget) GoString() string { return t.String() }

// Execute exchanges a Responses request and returns its reader observations with
// the read error.
// The target requires Build, Do, ReadError and ReadResponse; an accepted
// exchange supplies a usable body. Activity and the current body close on
// return, while hooks own displaced-body cleanup.
//
// Reader flags and timing survive errors. HasUsage is copied only alongside a
// nonnil Usage pointer, so callers assess the resulting usage and commitment
// evidence before completion. Headers, image-size slices and timing pointers
// remain shared with the response or reader; copy them before independent
// mutation.
func (ResponsesExecutor) Execute(ctx context.Context, input upstream.AttemptInput, sink upstream.OutputSink) (result upstream.AttemptResult, err error) {
	defer func() { result.CapturePerformance(ctx, err) }()
	t, ok := input.Target.(*ResponsesTarget)
	if !ok || t == nil || t.ReadResponse == nil || t.Exchange.Build == nil || t.Exchange.Do == nil || t.Exchange.ReadError == nil {
		return result, errors.New("grok responses target is not configured")
	}
	if input.Protocol != protocol.ProtocolOpenAIResponses {
		return result, errors.New("unsupported grok responses protocol")
	}
	if t.Enter != nil {
		done, e := t.Enter()
		if e != nil {
			return result, e
		}
		// Release activity after response cleanup so application shutdown continues to
		// see this attempt while its current body is closing.
		defer done()
	}
	started := time.Now()
	resp, body, err := ExchangeResponses(input.Body, t.Exchange)
	if err != nil {
		return result, err
	}
	// Close the body present at return; stream wrappers may replace it and
	// propagate closure to their source.
	defer func() { _ = resp.Body.Close() }()
	result.RequestID = firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id"))
	result.UpstreamHeaders = resp.Header
	result.Model = input.ResponseModel
	result.UpstreamModel = t.Model
	result.Stream = input.Stream
	if t.BeforeResponse != nil {
		handled, e := t.BeforeResponse(resp, body)
		if handled || e != nil {
			return result, e
		}
	}
	// Normalize billing pings before restoring client tools; bridge readers take
	// the raw path because they interpret vendor events themselves.
	if input.Stream && !t.PassRawStream {
		resp.Body = NewGrokResponsesBillingPingFilterBody(resp.Body, t.MaxLineSize)
		if len(t.ClientTools.CustomTools) > 0 || t.ClientTools.ToolSearch || len(t.ClientTools.NamespaceTools) > 0 {
			resp.Body = upstream.NewResponsesClientToolStreamBody(resp.Body, t.ClientTools, t.MaxLineSize)
		}
	}
	// Reading can fail after usable observations arrive. Transfer that evidence
	// before returning the error so completion can assess partial work.
	observed, err := t.ReadResponse(resp, input, sink)
	// A usage pointer supplies token values and their HasUsage flag; commitment,
	// disconnect and timing evidence remain separate even when reading failed.
	if observed.Usage != nil {
		u := observed.Usage
		result.Usage = upstream.TokenUsage{
			InputTokens:              u.InputTokens,
			OutputTokens:             u.OutputTokens,
			CacheCreationInputTokens: u.CacheCreationInputTokens,
			CacheReadInputTokens:     u.CacheReadInputTokens,
			ImageOutputTokens:        u.ImageOutputTokens,
		}
		result.ImageInputTokens = u.ImageInputTokens
		result.HasUsage = observed.HasUsage
	}

	result.UpstreamResponseModel = observed.UpstreamResponseModel
	result.ResponseID = strings.TrimSpace(observed.ResponseID)
	result.SearchCount = observed.SearchCount
	result.ObservedImages = observed.ImageCount
	result.ImageOutputSizes = observed.ImageOutputSizes
	result.Served = observed.Served
	result.HTTPCommitted = observed.HTTPCommitted
	result.RetryCommitted = observed.RetryCommitted
	result.ClientDisconnect = observed.ClientDisconnected

	result.Duration = time.Since(started)
	return result, err
}

// ResolveStreamIdleTimeout converts a positive stream setting from seconds to a
// duration. Zero and negative settings select DefaultStreamIdleTimeout, which
// the gateway uses when deciding whether an idle stream can be retried.
func ResolveStreamIdleTimeout(cfgStreamIntervalSec int) time.Duration {
	if cfgStreamIntervalSec > 0 {
		// Settings use whole seconds; stream readers receive durations.
		return time.Duration(cfgStreamIntervalSec) * time.Second
	}
	return DefaultStreamIdleTimeout
}
