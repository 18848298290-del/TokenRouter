package grokforward

import (
	"context"
	"net/http"
	"strings"
	"time"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// contracts.go defines the inputs, observations, and callbacks used by Grok
// Responses forwarding.
// Grok Responses forwarding keeps caller, billing, and upstream model
// identities separate while request-local adapters supply transport and health
// effects. Its inputs also carry output mode and timing so the returned OpenAI
// result can describe the selected attempt to completion processing.
//
// Failure construction combines response retry guidance with the health decision
// that has already run for the attempt. Either source can permit reuse of the
// provider, while request-scoped capacity handling requires response-derived
// retryability and HTTP 429. Headers become an independent snapshot for the
// provider loop; captured error bytes remain borrowed from the attempt, and
// previously computed retry deadlines pass through without another clock read.

// ComposerVisionModel selects the auxiliary image-description model used by Composer before the main text request is sent.
const ComposerVisionModel = "grok-build-0.1"

// Input is the selected provider and one Responses request attempt.
type Input struct {
	ProviderID int64 // selected provider identifier
	// ProviderName is the diagnostic name; ProviderType is the credential kind;
	// Platform is the diagnostic platform.
	ProviderName, ProviderType, Platform string
	// OAuth enables OAuth cache policy; HTTPPresent enables HTTP rejection output;
	// Compact requests summary-turn adaptation.
	OAuth, HTTPPresent, Compact bool
	Body                        []byte    // supplied request bytes
	OriginalModel               string    // client requested model
	Stream                      bool      // client streaming choice
	StartedAt                   time.Time // request arrival time
}

// Options holds the codec, line-size limit, and request-lifecycle registration function.
type Options struct {
	// ResolveAttributes supplies model policy when configured; an error stops the
	// attempt before the codec patches the request body.
	ResolveAttributes func(context.Context, string) (modelcatalog.Attributes, error)
	Codec             grok.BodyCodec // platform body codec
	MaxLineSize       int            // SSE line limit
	// Enter registers execution with application activity tracking when present;
	// ResponsesExecutor defers the returned release through response reading.
	Enter func() (func(), error)
}

type (
	// Decision reports Grok health policy choices for handling one upstream failure.
	// Generic requests shared error output; Failover requests provider switching;
	// RetrySameProvider permits another attempt with this provider.
	Decision struct {
		// Generic requests shared error output; Failover requests provider switching;
		// RetrySameProvider permits another attempt with this provider.
		Generic, Failover, RetrySameProvider bool
	}
	// Retry carries response-derived limits for an outer same-provider retry.
	Retry struct {
		Retryable bool          // response allows retry
		Delay     time.Duration // minimum retry delay
		Deadline  time.Time     // latest retry time
		Max       int           // maximum retry attempts
	}
)

// Failure carries response evidence and retry guidance to the provider loop.
type Failure struct {
	StatusCode      int         // upstream HTTP status
	ResponseBody    []byte      // retained error bytes
	ResponseHeaders http.Header // upstream response headers
	// RetryableOnSameProvider permits provider reuse; RequestScopedTransient
	// identifies a retryable 429 for request-local retry handling.
	RetryableOnSameProvider, RequestScopedTransient bool
	SameProviderRetryDelay                          time.Duration // minimum retry delay
	SameProviderRetryDeadline                       time.Time     // latest retry time
	SameProviderRetryMax                            int           // maximum retry attempts
}

// Notice describes one upstream HTTP failure for request diagnostics.
type Notice struct {
	ProviderID int64 // selected provider identifier
	// Platform is the diagnostic platform; ProviderName is the configured name.
	Platform, ProviderName string
	UpstreamStatusCode     int // upstream HTTP status
	// UpstreamRequestID is the vendor request identifier; Kind is the handling
	// category; Message is the sanitized error explanation.
	UpstreamRequestID, Kind, Message string
}

// Ports supplies request preparation, provider health, HTTP exchange, and output operations.
type Ports interface {
	// BillingModel applies the provider mapping used for request billing.
	BillingModel(string) string
	// UpstreamModel selects the vendor model for a mapped billing model.
	UpstreamModel(string) string
	// InvalidRequest writes an HTTP request rejection with its offending
	// parameter.
	InvalidRequest(string, string)
	// SetError records upstream status, message, and payload for request
	// diagnostics.
	SetError(int, string, string)
	// ClientTools publishes this attempt's mapping for response restoration.
	// The installed adapter retains its internal maps, so callers leave those
	// maps unchanged while response readers use them.
	ClientTools(bridge.ResponsesClientToolMapping)
	// CacheIdentity derives the upstream prompt-cache identity from body and
	// model.
	CacheIdentity([]byte, string) string
	// FreeCacheRoute applies the Free OAuth function-tool routing rule to prepared
	// bytes. The intent bytes represent the request before cache mutation, and
	// failures stop forwarding before credential resolution.
	FreeCacheRoute([]byte, []byte, string) ([]byte, error)
	// Credential resolves and stores this request attempt's provider credential
	// before Build. Resolution uses the request credential budget, and an error
	// stops the attempt before transport starts.
	Credential(context.Context) error
	// Detach returns an upstream context and its release callback for the attempt.
	// The shared adapter removes client cancellation and deadlines while retaining
	// request values and server-abort state.
	Detach(context.Context) (context.Context, func())
	// ResolveProxy records the selected provider's proxy for later HTTP exchange.
	ResolveProxy()
	// Build creates an authenticated Responses request from the current adapter
	// state. The cache identity is attached as supplied, and the boolean controls
	// use of runtime route defaults.
	Build(context.Context, []byte, string, bool) (*http.Request, error)
	// Do sends a prepared request using the provider-isolated HTTP transport.
	// ResponsesExecutor closes the response's current Body after processing;
	// replacing Body redirects that cleanup to the replacement reader.
	// Transport failures are returned for classification.
	Do(*http.Request) (*http.Response, error)
	// ReadError returns bounded error-body bytes, retaining partial data when
	// reading fails. The installed reader leaves the consumed body open, so
	// callers retain its cleanup responsibility when replacing Body for another
	// handler.
	ReadError(*http.Response) []byte
	// Latency records upstream exchange duration in milliseconds for request
	// diagnostics.
	Latency(int64)
	// TransportError presents exchange failures using the selected provider and
	// request context.
	TransportError(context.Context, error) error
	// ReplayNotice logs a decoding replay and whether cache identity is present.
	ReplayNotice(bool)
	// ErrorMessage extracts and sanitizes the vendor explanation from error bytes.
	ErrorMessage([]byte) string
	// Health observes an upstream failure and returns generic, failover, and retry
	// decisions. The final boolean enables team-model context; Composer
	// description requests pass false for their auxiliary model.
	Health(context.Context, int, http.Header, []byte, string, bool) Decision
	// Observe appends the supplied upstream failure notice to request diagnostics.
	Observe(Notice)
	// HandleError delegates final Responses error output with the effective body
	// and model. It can return both result and error, and callers return those
	// values when policy handles the failure.
	HandleError(context.Context, *http.Response, []byte, string) (*forwardcore.OpenAIResult, error)
	// ShouldMarkTeam reports whether failure evidence requires a team-model quota
	// cooldown.
	ShouldMarkTeam(int, []byte) bool
	// MarkTeam applies the selected model's quota cooldown to the provider
	// overlay.
	MarkTeam(string)
	// RetryMetadata returns response-derived delay, deadline, and count guidance
	// for provider reuse.
	RetryMetadata(int, []byte) Retry
	// Failure constructs the failover error consumed by the outer provider loop.
	Failure(Failure) error
	// ObserveSuccess records successful response headers and status for provider
	// health.
	ObserveSuccess(context.Context, http.Header, int, string)
	// ReadStream reads Responses events and emits synchronized client output
	// through the HTTP adapter. It retains available observations beside reader
	// errors and uses the supplied start time for output timing.
	ReadStream(context.Context, *http.Response, time.Time, string, string) (upstream.ResponsesObservation, error)
	// ReadNonStream reads a buffered Responses result using the caller and
	// upstream models. It returns observed usage and output state beside any
	// error, leaving final response-body closure to execution.
	ReadNonStream(context.Context, *http.Response, string, string) (upstream.ResponsesObservation, error)
	// Sink returns the synchronous output destination for this request attempt.
	Sink() upstream.OutputSink
	// Effort resolves reasoning effort from the effective payload and caller
	// model.
	Effort([]byte, string) *string
	// ReadBody returns bounded response bytes for Composer description decoding.
	ReadBody(*http.Response) ([]byte, error)
	// HasTokens reports whether observed usage contains any counted token
	// quantity.
	HasTokens(*wire.ForwardUsage) bool
}

// firstNonEmpty returns the first trimmed, nonblank value in preference order.
// It returns an empty string when every candidate is blank, allowing callers to
// supply a status-specific diagnostic message.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// newFailure captures response evidence and combines the supplied retry decisions.
// Callers supply a nonnil response after computing RetryMetadata and applying
// health policy, keeping their existing observation order and deadline sampling.
//
// Either retry source permits provider reuse, while request-scoped transient
// handling requires a retryable HTTP 429 from response guidance. Headers are
// cloned for the outer attempt loop; body bytes remain shared with the caller.
// Delay, deadline, and attempt limits retain the supplied values without new
// clock reads or provider side effects.
func newFailure(response *http.Response, body []byte, retry Retry, retrySameProvider bool) Failure {
	return Failure{
		StatusCode:                response.StatusCode,
		ResponseBody:              body,
		ResponseHeaders:           response.Header.Clone(),
		RetryableOnSameProvider:   retry.Retryable || retrySameProvider,
		RequestScopedTransient:    retry.Retryable && response.StatusCode == http.StatusTooManyRequests,
		SameProviderRetryDelay:    retry.Delay,
		SameProviderRetryDeadline: retry.Deadline,
		SameProviderRetryMax:      retry.Max,
	}
}
