package media

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// voice.go runs voice provider attempts with bounded candidate selection and
// successful audio completion.
// Voice recovery selects at most four candidates and excludes a provider
// after its adapter permits another attempt. Forwarding owns protocol
// errors and output, while candidate-exhaustion results carry the last
// retryable forwarding error when one exists.
//
// An acquired provider requires a usable release function, invoked as
// forwarding returns before successful completion. Selection errors and
// missing candidates use the prior forwarding error to choose NoProviders,
// so that flag describes this loop's history. A nonretryable error or
// failed admission ends with adapter-owned handling and a nil loop result.

// VoiceRequest carries admitted voice payload and endpoint labels through
// provider execution.
type VoiceRequest struct {
	// Endpoint and ContentType identify the voice operation and input encoding
	// used by forwarding and completion. RunVoice passes these strings
	// unchanged with the same body reference to each attempt.
	Endpoint, ContentType string // voice endpoint name and MIME
	// Body retains the caller's request bytes across attempts and completion.
	// Callers keep the storage stable until RunVoice returns or copy it before
	// independent mutation.
	Body []byte // shared voice request byte payload
}

// VoiceResult carries response timing and audio usage into completion
// callbacks.
type VoiceResult struct {
	// RequestID identifies upstream execution, Model is its response model and
	// UpstreamModel records the provider model. Completion receives all three
	// identities from the forwarding adapter.
	RequestID, Model, UpstreamModel string // upstream request identifier, client response model and provider execution model
	// Headers retains the map and slices supplied with the result. The loop
	// passes that storage to completion directly, leaving copying or mutation
	// coordination with consumers.
	Headers  map[string][]string // shared upstream response header evidence
	Duration time.Duration       // observed voice attempt elapsed time
	// AudioUsage retains the forwarding adapter's pointer and may be nil.
	// CompleteVoice receives this result directly, so callers arrange
	// ownership for any retained or mutable usage evidence.
	AudioUsage *protocol.AudioUsage // optional observed audio billing units
}

// VoiceOutcome combines voice forwarding observations with permission to
// choose another provider.
type VoiceOutcome struct {
	// Result can accompany successful or failed forwarding and retains the
	// adapter's pointer. Nil-error forwarding passes that pointer to
	// CompleteVoice, including when no observations were returned.
	Result *VoiceResult // optional voice response observations
	Err    error        // voice forwarding attempt error
	// RetryNext is consulted when Err is nonnil. True excludes the current
	// provider and saves its error; false leaves final protocol handling with
	// the forwarding adapter.
	RetryNext bool // adapter permits another provider attempt
}

// VoicePorts supplies voice provider admission, forwarding and usage
// completion callbacks.
type VoicePorts interface {
	// SelectVoice selects a candidate under ctx using the excluded provider
	// IDs. Error or absence ends the loop; RunVoice returns its prior
	// forwarding error rather than the selection error.
	SelectVoice(context.Context, map[int64]struct{}) (provider.ProviderSnapshot, bool, error)
	// AcquireVoice reports whether the selected provider slot was acquired.
	// True requires a callable release function, invoked after forwarding and
	// before completion; false leaves request output with the adapter.
	AcquireVoice(context.Context, provider.ProviderSnapshot) (func(), bool)
	// ForwardVoice performs one admitted voice attempt with the shared request
	// payload. Its outcome owns retry eligibility and optional result
	// observations; nonretryable failures are already handled by the request
	// adapter.
	ForwardVoice(context.Context, provider.ProviderSnapshot, VoiceRequest) VoiceOutcome
	// CompleteVoice handles a nil-error attempt after its provider slot is
	// released. The loop can supply a nil result, so implementations handle
	// missing observations and own usage-recording errors.
	CompleteVoice(context.Context, provider.ProviderSnapshot, VoiceRequest, *VoiceResult)
}

// VoiceFailure describes candidate exhaustion for the request adapter's
// final response.
type VoiceFailure struct {
	// NoProviders is true when selection ends before any saved forwarding
	// error. It can follow a selection error even after a candidate was
	// previously considered by the adapter.
	NoProviders bool  // no prior retryable forwarding error
	Last        error // last retryable forwarding error
}

// RunVoice performs at most four provider selections for an admitted voice
// request. Callers keep body storage stable and return a callable release
// on successful acquisition; each slot ends before CompleteVoice.
//
// RetryNext excludes the current provider and saves its forwarding error
// for exhaustion reporting. Selection error or absence returns that saved
// error, with NoProviders true when none exists. Nonretryable forwarding
// failure and failed admission return nil because adapter handling owns the
// client response; nil-error forwarding invokes completion even with a nil
// result.
func RunVoice(ctx context.Context, request VoiceRequest, ports VoicePorts) *VoiceFailure {
	excluded := make(map[int64]struct{})
	var last error
	// Four selections bound voice recovery across provider candidates.
	for range 4 {
		selected, present, err := ports.SelectVoice(ctx, excluded)
		// Selection error or absence reports the saved forwarding history,
		// leaving the selection error itself unreported because VoiceFailure
		// models candidate exhaustion for its HTTP consumer.
		if err != nil || !present {
			return &VoiceFailure{NoProviders: last == nil, Last: last}
		}
		release, acquired := ports.AcquireVoice(ctx, selected)
		if !acquired {
			return nil
		}
		// Keep slot ownership inside forwarding so completion runs after release,
		// even when the selected adapter returns an error requiring another
		// candidate.
		outcome := func() VoiceOutcome { defer release(); return ports.ForwardVoice(ctx, selected, request) }()
		if outcome.Err == nil {
			ports.CompleteVoice(ctx, selected, request, outcome.Result)
			return nil
		}
		// Only adapter-permitted recovery records the failed provider and error,
		// allowing later selection exhaustion to report forwarding evidence from
		// the last eligible attempt.
		if outcome.RetryNext {
			excluded[selected.ID] = struct{}{}
			last = outcome.Err
			continue
		}
		return nil
	}
	if last != nil {
		return &VoiceFailure{Last: last}
	}
	return nil
}
