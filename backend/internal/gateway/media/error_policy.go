package media

import (
	"fmt"
	"time"
)

// error_policy.go orders media failure classification, provider policy
// effects, recovery and final response presentation.
// Upstream status handling decides recovery before final client-message
// rewriting, with operation-specific exceptions for content rejection and
// invalid input. Callback order matters because policy can update provider
// state, output can commit a response, and a returned failover error
// directs another attempt.
//
// Grok content rejection and embedding invalid input bypass ordinary
// provider policy; Alpha unsupported endpoints can switch while selected
// statuses suppress health effects. Image authorization recovery precedes
// ordinary classification, and image-response cyber warnings are written
// before warning wrapping. Display rewrite runs only after switching has
// been declined. These functions invoke callbacks lazily on reached paths,
// leaving body capture, actual health writes and client serialization with
// their request adapters.

// ErrorResponse supplies client status and protocol error fields for
// adapter serialization.
type ErrorResponse struct {
	Status int // client response HTTP status
	// Type labels the protocol error object while Message supplies its
	// client-visible text. Response writers serialize these selected values
	// after classification or display rewriting has chosen the final response.
	Type, Message string // protocol error type and client response message
}

// GrokRetry carries provider and policy retry evidence into failover
// construction.
type GrokRetry struct {
	// Retryable records provider-specific recovery evidence; PolicyRetryable
	// records configured policy allowance. The Grok HTTP NewFailover callback
	// combines them for same-provider retry, while request-transient status
	// uses Retryable with status 429.
	Retryable, PolicyRetryable bool          // provider retry evidence and configured policy retry allowance
	Delay                      time.Duration // suggested same-provider retry delay
	Deadline                   time.Time     // absolute same-provider retry deadline
	Maximum                    int           // same-provider retry count override
}

// GrokFailurePorts supplies lazy content, recovery and final-display
// decisions for Grok.
type GrokFailurePorts struct {
	// ContentRejection runs first and supplies client text when matched. Its
	// true result bypasses Generic, Failover and Rewrite before writing a
	// fixed 403 response.
	ContentRejection func() (bool, string) // content-policy classification callback
	// Generic runs after content rejection is declined. True writes a fixed
	// 500 response and returns before any failover or final display rewrite.
	Generic func() bool // generic response policy predicate
	// Failover runs after content and generic handling decline. True selects
	// retry construction before final response rewriting can change status or
	// text.
	Failover func() bool // provider recovery policy predicate
	// Retry is required only after Failover returns true. Its values reach
	// NewFailover, with the additional boolean derived from Retryable and
	// upstream status 429.
	Retry func() GrokRetry // Grok same-provider retry metadata
	// Observe runs on each selected failure path before response writing or
	// failover construction. The callback owns retaining any captured
	// request/provider evidence.
	Observe func(string, string) // failure kind and message observer
	// Rewrite runs after failover is declined and may replace final client
	// fields. Its match affects display output, leaving the earlier recovery
	// decision unchanged.
	Rewrite func() (ErrorResponse, bool) // final display response matcher
	// Write serializes the chosen client response on nonfailover paths. This
	// callback returns no write error, so ResolveGrokFailure returns its
	// separately constructed forwarding error.
	Write func(ErrorResponse) // selected error response emitter
	// NewFailover returns the selected recovery error from retry metadata and
	// transient boolean. It is required on the failover path, and the resolver
	// returns its result directly.
	NewFailover func(GrokRetry, bool) error // classified Grok failure constructor
}

// EmbeddingFailurePorts supplies embedding invalid-input, provider-state
// and failover handling.
type EmbeddingFailurePorts struct {
	// InvalidRequest runs first; true forwards the captured upstream response
	// and bypasses provider policy. Other callbacks are required only when
	// their later paths are reached.
	InvalidRequest func() bool // invalid client request predicate
	// ApplyPolicy runs after invalid input is declined and before Generic or
	// Failover. It can publish adapter-owned provider effects that those later
	// predicates inspect.
	ApplyPolicy func() // embedding provider-state policy callback
	// Generic runs after ApplyPolicy and precedes failover. True writes a
	// fixed 500 response, then returns the resolver's status-specific error.
	Generic func() bool // generic embedding response predicate
	// Failover is evaluated after provider policy and generic response
	// handling. True selects RecordFailover and NewFailover rather than
	// forwarding the captured response.
	Failover func() bool // embedding provider recovery predicate
	// RecordFailover runs before NewFailover only when switching is selected.
	// The callback owns its observation effects; these two calls do not form a
	// transaction.
	RecordFailover func() // recovery observation side-effect callback
	// NewFailover supplies the returned recovery error after RecordFailover.
	// Its result is returned directly, so the caller provides the
	// classification consumed by the embedding loop.
	NewFailover func() error // embedding classified failure constructor
	// Forward emits the captured upstream response for invalid input or final
	// nonfailover handling. ResolveEmbeddingFailure separately returns its
	// status error after the callback has run.
	Forward func() // captured upstream error response emitter
	// Write is required for the generic-response path after provider policy.
	// It serializes the selected ErrorResponse, while the resolver constructs
	// and returns a separate forwarding error.
	Write func(ErrorResponse) // generic client response emitter
}

// AlphaFailurePorts binds search endpoint recovery to optional preparation
// and health effects.
type AlphaFailurePorts struct {
	// Prepare optionally runs after Failover or EndpointUnsupported permits
	// recovery. It executes before allowed provider side effects and
	// classified failure construction.
	Prepare func() // optional selected-failure preparation callback
	// Failover is consulted first and short-circuits EndpointUnsupported when
	// true. When both predicates decline, ResolveAlphaFailure returns nil and
	// performs no preparation or health callback.
	Failover func() bool // Alpha provider recovery predicate
	// EndpointUnsupported runs when Failover declines and can independently
	// select recovery. API-key 404/405 handling supplies this compatibility
	// decision through the request adapter.
	EndpointUnsupported func() bool // search endpoint incompatibility predicate
	// ApplySideEffects is required on selected recovery paths except statuses
	// 401, 404 and 405. Its returned disable flag reaches NewFailover after
	// optional preparation.
	ApplySideEffects func() bool // selected provider health-effect callback
	// NewFailover receives the provider-disable observation, false when health
	// effects are suppressed. Its returned value becomes the resolver result,
	// leaving error construction with the request adapter.
	NewFailover func(bool) error // Alpha classified failure constructor
}

// ImageFailurePorts supplies authorization recovery before image HTTP
// failure handling.
type ImageFailurePorts struct {
	// Recover optionally runs before ordinary failure policy. Matched success
	// requests retry; matched error is wrapped, while an error accompanying
	// unmatched false is discarded.
	Recover func() (bool, error) // optional authorization recovery callback
	// Failover runs only after recovery is absent or unmatched. True invokes
	// Observe and ApplyPolicy before choosing final handling or a classified
	// recovery error.
	Failover func() bool // ordinary image recovery predicate
	// Observe records adapter evidence when Failover selects recovery. It runs
	// before ApplyPolicy, which can then choose final Handle rather than
	// NewFailover.
	Observe func() // selected image failover observer
	// ApplyPolicy runs on selected failover paths after observation. True
	// chooses Handle for final response policy; false permits returning the
	// NewFailover callback result.
	ApplyPolicy func() bool // image provider-policy final-response decision
	// NewFailover is required when Failover succeeds and ApplyPolicy declines
	// final handling. Its result is returned with retry false, distinct from
	// successful authorization recovery.
	NewFailover func() error // image classified recovery-error constructor
	// Handle owns final image output when switching is declined or provider
	// policy chooses final handling. Its returned error is paired with retry
	// false by the resolver.
	Handle func() error // ordinary final image failure handler
}

// ImageResponseFailurePorts supplies cyber-warning, provider policy and
// final image-response decisions.
type ImageResponseFailurePorts struct {
	// CyberMessage runs first and returns client text when matched. That path
	// writes the warning response and bypasses ordinary provider policy,
	// generic handling and switching.
	CyberMessage func() (string, bool) // cyber-warning client message classifier
	// Observe records the selected message before warning output, failover
	// construction or final rewriting. Its captured upstream details remain
	// owned by the request adapter.
	Observe func(string, string) // image response error kind observer
	// Write supplies the return value for generic or rewritten responses. The
	// cyber-warning path discards this callback's error and instead returns
	// the WrapCyber result.
	Write func(ErrorResponse) error // selected image error response writer
	// WrapCyber receives the resolver's warning error after client output is
	// attempted. Its returned value becomes the cyber path result regardless
	// of Write's error.
	WrapCyber func(error) error // cyber-warning error wrapper callback
	// ApplyPolicy runs only after cyber classification declines, before
	// Generic and Failover. The request adapter owns provider effects and the
	// state read by those later predicates.
	ApplyPolicy func() // image response provider-state policy
	// Generic is evaluated after provider policy and before failover. True
	// observes the upstream message and returns Write for a fixed 500 client
	// response.
	Generic func() bool // generic image response predicate
	// Failover runs after cyber and generic handling decline. True observes
	// failover and returns NewFailover before display rewrite or default
	// response handling.
	Failover func() bool // image response recovery predicate
	// NewFailover is required on the selected recovery path after observation.
	// Its result is returned directly, leaving retry metadata and error
	// construction with the adapter.
	NewFailover func() error // image-response classified failure constructor
	// Rewrite is consulted after provider switching is declined. A matching
	// response is written and its write error returned; unmatched results
	// select DefaultResponse.
	Rewrite func() (ErrorResponse, bool) // matched final image display response
	// DefaultResponse owns final response handling when cyber, generic,
	// failover and rewrite paths decline. Its returned error becomes the
	// resolver's result after ordinary error observation.
	DefaultResponse func() error // unmatched final image response handler
}

// ResolveGrokFailure chooses Grok recovery or final response fields using
// lazily invoked callbacks. Content rejection wins with fixed 403 output;
// generic policy wins next with fixed 500 output, before failover is
// considered.
//
// Selected failover observes the error, obtains retry metadata and passes
// request-transient true only for Retryable status 429. Final rewriting
// runs after switching is declined, and an unmatched rewrite uses
// status-specific error type with the supplied message. Writers return no
// error, so final-output paths return a separately constructed upstream
// failure; the failover path returns its callback result.
func ResolveGrokFailure(status int, message string, p GrokFailurePorts) error {
	// Content rejection has its own fixed client response; decide it before
	// generic policy or switching so later status display rules cannot replace
	// that classification.
	if rejected, client := p.ContentRejection(); rejected {
		p.Observe("http_error", client)
		p.Write(ErrorResponse{Status: 403, Type: "invalid_request_error", Message: client})
		return fmt.Errorf("grok content policy rejection: %s", client)
	}
	if p.Generic() {
		p.Observe("http_error", message)
		p.Write(ErrorResponse{Status: 500, Type: "upstream_error", Message: "Upstream gateway error"})
		return fmt.Errorf("upstream error: %d (not in custom error codes) message=%s", status, message)
	}
	if p.Failover() {
		p.Observe("failover", message)
		retry := p.Retry()
		return p.NewFailover(retry, retry.Retryable && status == 429)
	}
	p.Observe("http_error", message)
	if response, matched := p.Rewrite(); matched {
		p.Write(response)
		return fmt.Errorf("upstream error: %d (passthrough rule matched) message=%s", status, message)
	}
	typ := "upstream_error"
	switch status {
	case 400:
		typ = "invalid_request_error"
	case 404:
		typ = "not_found_error"
	case 429:
		typ = "rate_limit_error"
	}
	p.Write(ErrorResponse{Status: status, Type: typ, Message: message})
	return fmt.Errorf("upstream error: %d %s", status, message)
}

// ResolveEmbeddingFailure returns embedding failure handling in
// invalid-input, provider-policy, generic and failover order. Callers bind
// each reached callback; invalid input forwards immediately before any
// provider effect or later classification.
//
// Ordinary policy runs before generic handling, then selected failover
// records observation before constructing its returned error. A declined
// switch forwards the captured response, and both forwarding paths return
// separately constructed status errors. Those callbacks retain
// request/response state externally; the ordered effects and output are not
// rolled back when a later callback returns its result.
func ResolveEmbeddingFailure(status int, p EmbeddingFailurePorts) error {
	// Forward invalid input immediately before any provider-policy side
	// effects, keeping a client request classification from updating provider
	// state through the ordinary error path.
	if p.InvalidRequest() {
		p.Forward()
		return fmt.Errorf("upstream invalid request: %d", status)
	}
	p.ApplyPolicy()
	if p.Generic() {
		p.Write(ErrorResponse{Status: 500, Type: "upstream_error", Message: "Upstream gateway error"})
		return fmt.Errorf("upstream error: %d (not in custom error codes)", status)
	}
	if p.Failover() {
		p.RecordFailover()
		return p.NewFailover()
	}
	p.Forward()
	return fmt.Errorf("upstream returned status %d", status)
}

// ResolveAlphaFailure returns selected search recovery or nil when both
// recovery predicates decline. Failover is evaluated first, with
// EndpointUnsupported considered only when it returns false.
//
// Selected recovery invokes optional Prepare before provider side effects.
// Statuses 401, 404 and 405 suppress ApplySideEffects while still reaching
// NewFailover with disabled false; other statuses use its returned disable
// flag. Preparation and health callbacks therefore occur only for chosen
// recovery paths, and the caller owns the constructed error and any
// captured response replay.
func ResolveAlphaFailure(status int, p AlphaFailurePorts) error {
	// Choose recovery before preparation or health work, allowing unsupported
	// search endpoints to switch independently of ordinary provider failover
	// classification.
	if !p.Failover() && !p.EndpointUnsupported() {
		return nil
	}
	if p.Prepare != nil {
		p.Prepare()
	}
	disabled := false
	// Selected authentication or endpoint-compatibility statuses skip
	// provider-health mutation while still producing recovery evidence, so
	// NewFailover receives false for their disable observation.
	if AlphaProviderErrorSideEffects(status) {
		disabled = p.ApplySideEffects()
	}
	return p.NewFailover(disabled)
}

// ResolveImageFailure returns whether authorization recovery requests
// another attempt, with any selected failure error. Optional Recover runs
// before ordinary policy: matched success returns true, matched error is
// wrapped, and an unmatched error is discarded.
//
// Ordinary Failover invokes observation before ApplyPolicy, whose true
// result selects final Handle; otherwise NewFailover supplies the recovery
// error. A declined Failover also uses Handle. These ordinary paths return
// retry false, distinguishing successful identity recovery from classified
// provider failure; callbacks own captured response output and side effects
// in the selected order.
func ResolveImageFailure(p ImageFailurePorts) (bool, error) {
	// Give identity recovery first access to the captured failure before
	// observation and provider policy; unmatched recovery falls through even
	// when it returned an error.
	if p.Recover != nil {
		matched, err := p.Recover()
		if matched {
			if err != nil {
				return false, fmt.Errorf("agent identity task recovery failed: %w", err)
			}
			return true, nil
		}
	}
	if p.Failover() {
		p.Observe()
		if p.ApplyPolicy() {
			return false, p.Handle()
		}
		return false, p.NewFailover()
	}
	return false, p.Handle()
}

// ResolveImageResponseFailure chooses warning, recovery or final image
// output using callbacks bound to one response. CyberMessage wins before
// provider policy, attempts warning output and returns a wrapped warning
// error while discarding Write's error.
//
// Other paths apply policy before generic handling and failover; a selected
// switch precedes display rewriting. Generic or matched rewritten output
// returns Write's value directly, which can be nil, while unmatched final
// output returns DefaultResponse. The resolver therefore separates warning
// identity, recovery classification and output error according to the path
// instead of assuming every written failure returns the same kind of error.
func ResolveImageResponseFailure(status int, message string, p ImageResponseFailurePorts) error {
	// Write the warning response before wrapping its failure identity; ignore
	// the writer's returned error here so WrapCyber supplies the selected
	// warning result.
	if client, matched := p.CyberMessage(); matched {
		p.Observe("http_error", client)
		_ = p.Write(ErrorResponse{Status: status, Type: "invalid_request_error", Message: client})
		return p.WrapCyber(fmt.Errorf("upstream error: %d message=%s", status, client))
	}
	p.ApplyPolicy()
	if p.Generic() {
		p.Observe("http_error", message)
		return p.Write(ErrorResponse{Status: 500, Type: "upstream_error", Message: "Upstream gateway error"})
	}
	if p.Failover() {
		p.Observe("failover", message)
		return p.NewFailover()
	}
	p.Observe("http_error", message)
	if response, matched := p.Rewrite(); matched {
		return p.Write(response)
	}
	return p.DefaultResponse()
}
