package forward

import (
	"context"
	"encoding/json"
)

// conversion_contract.go defines adapter operations for sending OpenAI client
// requests through Anthropic. Conversion always asks Anthropic for a stream,
// then chooses either event output or buffered JSON using the client's original
// stream setting.

// ConversionInput selects provider preparation rules before the converted request is exchanged.
type ConversionInput struct {
	OAuth bool // OAuth mimic eligibility
}

// ErrorDecision holds provider policy choices consumed before client error display.
type ErrorDecision struct {
	// Generic selects the generic envelope before recovery in conversion/count
	// handling; Failover permits recovery, and RetrySameProvider accompanies its
	// signal. Status-qualified Messages HTTP paths use their own failover predicate.
	Generic, Failover, RetrySameProvider bool // generic error selection; provider failover permission; same-provider retry permission
}

// ConversionPorts supplies request preparation, network exchange, health checks, and output for protocol conversion.
type ConversionPorts interface {
	// NormalizeResponses returns optional legacy-ingress normalization and a
	// changed flag. AsResponses selects returned bytes only when changed is true;
	// an error ends conversion before client-tool adaptation or credential
	// acquisition.
	NormalizeResponses([]byte) ([]byte, bool, error)
	// ResolveModel selects provider mapping and platform normalization for the
	// client model. Conversion retains the original input for client output and
	// uses that same input when this callback returns an empty selection.
	ResolveModel(context.Context, string) string
	// Effort returns recorded effort for the first usable model candidate and
	// selected client format. Conversion supplies mapped before original model;
	// a converted Anthropic OutputConfig can override this observation before the
	// thinking default is considered.
	Effort([]byte, bool, ...string) *string
	// ThinkingFallback supplies recorded effort when model-specific thinking
	// is enabled without a usable effort observation. Existing effort wins;
	// conversion uses the returned observation for result metadata while request
	// preparation remains separately defined by the bridge.
	ThinkingFallback(*string, []byte, string) *string
	// ModelNotice records the logical and selected upstream models with client output mode.
	ModelNotice(string, string, string, bool)
	// Mimic applies OAuth system, metadata, cache, and tool preparation to the
	// converted Anthropic body. Both conversion entrypoints invoke it for OAuth
	// input, leaving byte allocation and response-name state with the request
	// adapter.
	Mimic(context.Context, []byte, json.RawMessage, string) []byte
	// CacheLimit applies the Anthropic total cache-control marker limit after
	// optional OAuth preparation. Its selected body is used for credential/build
	// execution, and storage ownership remains with the callback's implementation.
	CacheLimit([]byte) []byte
	// Credential resolves and retains selected-provider credentials for the
	// converted request. On error conversion stops before Build and Send; successful
	// resolution makes the retained token available to those later adapter steps.
	Credential(context.Context) error
	// Build prepares and retains the upstream request, returning its selected
	// wire body for later speed metadata. Conversion requests streaming upstream
	// for either downstream mode, and an error prevents Send from starting.
	Build(context.Context, []byte, string, bool, bool) ([]byte, error)
	// Send executes the request retained by Build and returns usable response
	// handles. Success supplies nonnil Lines and Close for conversion; the caller
	// defers Close after Send and the adapter handles failed transport cleanup.
	Send(context.Context) (Response, error)
	// ReadErrorBody captures the retained upstream HTTP error body and reports
	// read failure. Conversion retains available partial bytes even on error for
	// summary, health, and failover; the concrete adapter installs a replacement
	// reader.
	ReadErrorBody() ([]byte, error)
	// ErrorMessage extracts and sanitizes the summary used for client output
	// and failover observation. It masks recognized credential query values, while
	// raw captured body storage follows the separate health and recovery handling.
	ErrorMessage([]byte) string
	// Health applies selected-provider policy using source status, captured
	// body, and selected upstream model. Conversion evaluates Generic before
	// Failover and carries RetrySameProvider into a recovery signal before mapping
	// any client-facing status.
	Health(context.Context, int, []byte, string) ErrorDecision
	// FailoverNotice submits selected source status and summary to the request observer.
	FailoverNotice(int, string)
	// FailoverError constructs a gateway recovery signal retaining source
	// status, body bytes, and same-provider retry permission. The surrounding request
	// loop still owns whether output state and remaining attempts allow another
	// provider exchange.
	FailoverError(int, []byte, bool) error
	// Output returns the current request's client-protocol writer and tool
	// restoration state. Conversion uses it for HTTP errors or successful response
	// adapters; the concrete writer controls commitment, headers, and framing.
	Output() Output
}

// MapStatus returns 502 for source statuses at least 500 and otherwise retains
// the supplied status. Conversion uses this for final client error output after
// provider health and recovery have used the original upstream status.
func MapStatus(status int) int {
	// Present server failures as gateway errors after health policy has used
	// the source status, keeping client display separate from that recovery decision.
	if status >= 500 {
		return 502
	}
	return status
}
