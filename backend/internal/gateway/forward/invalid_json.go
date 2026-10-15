package forward

import (
	"context"
	"fmt"
)

// invalid_json.go classifies undecodable successful upstream responses for
// health policy and failover. A successful upstream HTTP status can still carry
// an undecodable response body. Forwarding classifies that protocol failure as
// 502 for health policy, retains the original status in diagnostics, and lets
// the adapter choose a generic parse error or a provider failover signal.

// InvalidJSONInput carries a failed decode and captured metadata from a 2xx exchange.
type InvalidJSONInput struct {
	ProviderID              int64  // failed exchange provider ID
	ProviderName, RequestID string // provider display name; upstream request identifier
	UpstreamStatus          int    // original upstream HTTP status
	// Headers and Body retain the failed exchange's evidence for policy and
	// output. This input does not clone their map, slices, or bytes; consumers
	// needing independent mutable evidence copy it themselves.
	Headers map[string][]string // captured upstream response headers
	Body    []byte              // captured undecodable response body
	// ParseError retains the decode cause used by diagnostics and wrapped by
	// the generic error branch, keeping that failure available to error-chain callers.
	ParseError      error
	RequestedModels []string // model candidates for health policy
}

// InvalidJSONPorts supplies observation, health policy, and failure construction for decode errors.
type InvalidJSONPorts interface {
	// Log records the formatted decode diagnostic before health classification.
	// The message includes the original successful HTTP status, provider, request
	// ID, and parse cause, keeping transport status separate from the classified 502.
	Log(string)
	// Health evaluates an undecodable response as the supplied classified status
	// using its body and requested-model candidates. The returned decision chooses
	// generic handling or carries same-provider retry permission into failure construction.
	Health(context.Context, int, []byte, []string) ErrorDecision
	// Failover constructs the provider-switch signal from classified status and
	// captured response evidence. It receives Health's same-provider retry choice;
	// the request executor still decides whether its output and budget allow replay.
	Failover(int, map[string][]string, []byte, bool) error
}

// InvalidJSON logs failed JSON decoding, evaluates health policy at status 502,
// and returns either a generic error wrapping ParseError or the adapter's
// failover signal. The input retains its original HTTP status for diagnostics;
// response closure and actual replay eligibility remain the caller's responsibility.
func InvalidJSON(ctx context.Context, p InvalidJSONPorts, in InvalidJSONInput) error {
	// Successful HTTP transport does not make undecodable protocol output usable.
	// Health policy therefore receives a gateway failure while diagnostics keep
	// the original upstream status available.
	const status = 502
	p.Log(fmt.Sprintf("Provider %d(%s): upstream returned non-JSON 2xx response, attempting failover: status=%d request_id=%s error=%v", in.ProviderID, in.ProviderName, in.UpstreamStatus, in.RequestID, in.ParseError))
	decision := p.Health(ctx, status, in.Body, in.RequestedModels)
	if decision.Generic {
		return fmt.Errorf("upstream returned invalid JSON (not in custom error codes): %w", in.ParseError)
	}
	return p.Failover(status, in.Headers, in.Body, decision.RetrySameProvider)
}
