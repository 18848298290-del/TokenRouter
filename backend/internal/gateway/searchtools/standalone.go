package searchtools

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/search/contract"
)

// Standalone search chooses a provider, acquires its resources and retains
// them through the caller's response/completion handling. A replayable failure
// releases that attempt early before another candidate is selected.
//
// The request lease owns each attached attempt lease, including successful or
// terminal-error execution; callers release that parent on request exit. Four
// selection iterations bound this orchestration, including rejected
// acquisition, while initial selection/concurrency failures retain their
// stage. Later selection/acquisition failures can leave earlier execution
// evidence for final error handling. Search request fields are retained by
// value with shared slices/pointers, and forwarding policy decides whether an
// error permits switching.

// Selection contains a captured provider and its acquired/waiting resource
// evidence.
type Selection struct {
	ProviderID int64 // captured gateway-provider identifier
	Acquired   bool  // provider slot already acquired
	// Release belongs to captured acquisition evidence. Acquire interprets it and
	// returns the release attached to the request lease before forwarding
	// executes.
	Release func() // release already acquired provider slot
	// WaitPlan is used by the acquisition adapter when selection has not already
	// acquired a slot. Keep its captured fields stable until admission finishes.
	WaitPlan *scheduler.ProviderWaitPlan // optional provider admission wait plan
}

// StandalonePorts supplies selection, provider admission and synchronous
// search execution with caller-owned output handling.
type StandalonePorts interface {
	// Select captures a candidate respecting failed IDs and reports its
	// availability/error. The loop supplies model and context each iteration;
	// acquired or waiting resource evidence is interpreted by Acquire before
	// execution.
	Select(context.Context, string, map[int64]struct{}) (Selection, bool, error)
	// Acquire obtains the selected provider resource and returns release,
	// admission success and optional error. A successful result supplies the
	// release attached to the parent lease; rejected admission retires its own
	// unfinished resources.
	Acquire(context.Context, Selection) (func(), bool, error)
	// Execute runs search for the acquired provider using retained request fields
	// and resolved model/result limit. It returns response/vendor/error evidence
	// while the attached attempt lease remains owned by request cleanup.
	Execute(context.Context, int64, StandaloneRequest, string, int) (*contract.SearchResponse, string, error)
	// CanSwitch reports whether a failed standalone execution is replayable on
	// another provider. False retains its attempt under the request lease until
	// caller cleanup; true permits early release before reselection.
	CanSwitch(error) bool
}

// StandaloneResult retains provider identity and the latest executed search
// response/vendor observations.
type StandaloneResult struct {
	ProviderID int64 // most recent acquired gateway provider
	// Response retains Execute's pointer even when an error accompanies it.
	// Callers inspect the returned error before treating response data as
	// successful search output.
	Response *contract.SearchResponse // latest shared search response pointer
	Provider string                   // external search vendor label
}

// StandaloneFailure labels search failure stage while retaining its optional
// cause.
type StandaloneFailure struct {
	Stage string // selection, concurrency or execute stage
	Cause error  // optional retained failure cause
}

// noAvailableProvidersError supplies the HTTP message for missing
// standalone-search candidates.
type noAvailableProvidersError struct{}

// StandaloneRequest carries query and native Web/X search options to platform
// builders. Its slices and optional pointers remain caller-owned during
// synchronous execution.
type StandaloneRequest struct {
	Query string `json:"query"` // explicit search query spelling
	Input string `json:"input"` // alternate search input spelling
	// MaxResults, AllowedXHandles, ExcludedXHandles, EnableImageUnderstanding and
	// EnableVideoUnderstanding retain caller storage. Keep their slices and pointed
	// values stable while Execute or a platform builder reads this request; the
	// loop copies their references with the request value.
	//
	// MaxResults retains client presence for platform builders. RunStandalone
	// separately passes its resolved maxResults argument; this field is not
	// resolved by the loop.
	MaxResults               *int     `json:"max_results"`                // optional requested result limit
	AllowedXHandles          []string `json:"allowed_x_handles"`          // shared allowed X-handle names
	ExcludedXHandles         []string `json:"excluded_x_handles"`         // shared excluded X-handle names
	FromDate                 string   `json:"from_date"`                  // lower native search date spelling
	ToDate                   string   `json:"to_date"`                    // upper native search date spelling
	EnableImageUnderstanding *bool    `json:"enable_image_understanding"` // optional image understanding choice
	EnableVideoUnderstanding *bool    `json:"enable_video_understanding"` // optional video understanding choice
}

// Error returns a retained standalone cause's text, or the generic
// search-failed message when Cause is nil. Callers provide a nonnil receiver;
// stage remains separate metadata for HTTP formatting.
func (e *StandaloneFailure) Error() string {
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "web search failed"
}

// Unwrap exposes the optional standalone cause to error-chain inspection.
// Callers provide a nonnil receiver, and nil Cause remains nil rather than
// being replaced by the generic Error message.
func (e *StandaloneFailure) Unwrap() error { return e.Cause }

// Error returns the fixed no-candidate message used by standalone HTTP failure
// formatting.
func (noAvailableProvidersError) Error() string { return "No available providers" }

// RunStandalone performs at most four selection iterations and returns latest
// execution evidence or a stage-labeled error. Callers provide usable ports,
// stable request fields and a nonnil request lease that they release on every
// request-exit path.
//
// Initial selection or rejected acquisition with an error returns its specific
// stage. Later failures can end with retained execute evidence, including a
// nil-cause generic failure when no response was produced. Successful
// acquisition attaches an attempt lease to the parent before Execute.
// Replayable failure releases that attempt early and clears current provider
// identity before reselection; success and terminal error keep cleanup under
// the caller's parent lease.
func RunStandalone(ctx context.Context, request StandaloneRequest, model string, maxResults int, ports StandalonePorts, lease *scheduler.Lease) (StandaloneResult, error) {
	failed := make(map[int64]struct{})
	var result StandaloneResult
	hasProvider := false
	var lastErr error
	// Bound selection iterations, including candidates rejected during provider admission.
	for attempt := range 4 {
		selection, present, err := ports.Select(ctx, model, failed)
		if err != nil {
			if attempt == 0 {
				return result, &StandaloneFailure{Stage: "selection", Cause: err}
			}
			break
		}
		if !present {
			if attempt == 0 {
				return result, &StandaloneFailure{Stage: "selection", Cause: noAvailableProvidersError{}}
			}
			break
		}
		release, ok, err := ports.Acquire(ctx, selection)
		if !ok {
			if attempt == 0 && err != nil {
				return result, &StandaloneFailure{Stage: "concurrency", Cause: err}
			}
			failed[selection.ProviderID] = struct{}{}
			continue
		}
		// Attach cleanup before executing so successful and terminal-error paths
		// retain provider ownership until the caller has captured completion and finished output.
		attemptLease := scheduler.NewAttemptLease(lease, nil, release)
		result.ProviderID = selection.ProviderID
		hasProvider = true
		result.Response, result.Provider, lastErr = ports.Execute(ctx, selection.ProviderID, request, model, maxResults)
		if lastErr == nil {
			break
		}
		if !ports.CanSwitch(lastErr) {
			break
		}
		failed[selection.ProviderID] = struct{}{}
		// This error permits replay. Release the preceding attempt before another
		// selection can acquire resources for a different provider on the same request.
		attemptLease.Release()
		result.ProviderID = 0
		hasProvider = false
	}
	if lastErr != nil || result.Response == nil {
		return result, &StandaloneFailure{Stage: "execute", Cause: lastErr}
	}
	if !hasProvider {
		return result, &StandaloneFailure{Stage: "selection", Cause: noAvailableProvidersError{}}
	}
	return result, nil
}
