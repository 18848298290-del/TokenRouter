package media

import (
	"context"
	"time"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Embedding execution switches providers only while a classified failure
// has left output unchanged. The request adapter owns selection and writes,
// while the loop carries the last switched outcome into a later selection
// error and keeps a separate count of permitted transitions.
//
// Each provider slot ends when ForwardEmbedding returns, before scheduling
// feedback or successful usage completion. A committed-output failover
// error stops before failure reporting; ordinary forwarding failures are
// reported and returned directly. Client departure or a failed slot wait
// returns nil for adapter-owned handling, and nonpositive switch allowance
// selects three permitted transitions.

// EmbeddingResult carries embedding usage and model observations into
// gateway completion.
type EmbeddingResult struct {
	// RequestID identifies upstream execution; Model, BillingModel and
	// UpstreamModel describe response, settlement and provider names for that
	// attempt. A downstream completion adapter can choose among these
	// identities.
	RequestID, Model, BillingModel, UpstreamModel string // upstream request identifier, client response model, selected billing model and provider execution model
	// Headers retains the map and slices supplied by the forwarding adapter.
	// RunEmbeddings passes the same result to reporting and completion, so
	// consumers coordinate mutations or copy it.
	Headers  map[string][]string         // shared upstream response header evidence
	Usage    protocolopenai.ForwardUsage // observed embedding token usage
	Duration time.Duration               // observed embedding attempt duration
}

// EmbeddingOutcome separates attempt failure policy from observed response
// commitment.
type EmbeddingOutcome struct {
	// Result may be absent or accompany a forwarding error. Reporting and
	// completion receive its shared pointer directly, so consumers retain or
	// copy observations under adapter ownership.
	Result *EmbeddingResult // optional embedding result evidence
	Err    error            // forwarding attempt error value
	// Failover matters when Err is nonnil and requests recovery through
	// another provider. OutputChanged takes precedence on that path, ending
	// execution before scheduling failure feedback.
	Failover bool // classified provider-switch permission
	// OutputChanged forbids provider switching when a forwarding error permits
	// failover. The flag records adapter-observed writer changes, rather than
	// confirming client receipt of bytes.
	OutputChanged bool // observed downstream output change
	StatusCode    int  // observed upstream HTTP status
}

// EmbeddingEvent presents embedding execution stages to the adapter
// observer.
type EmbeddingEvent struct {
	Kind     string                    // embedding loop stage label
	Provider provider.ProviderSnapshot // stage-selected provider snapshot
	Outcome  EmbeddingOutcome          // shared forwarding result evidence
	// Excluded counts rejected provider IDs, while Switches and MaxSwitches
	// track transitions permitted by the loop. Each event populates the
	// counters relevant to its Kind.
	Excluded, Switches, MaxSwitches int           // excluded provider count, used provider transitions and allowed provider transitions
	Elapsed                         time.Duration // routing or response elapsed time
}

// EmbeddingsPorts supplies provider admission, forwarding, feedback and
// embedding completion callbacks.
type EmbeddingsPorts interface {
	// SelectEmbedding chooses a provider using ctx and the excluded IDs. An
	// error can return the last switched outcome through selection failure,
	// while absent selection has its own empty-selection stage.
	SelectEmbedding(context.Context, map[int64]struct{}) (provider.ProviderSnapshot, bool, error)
	// AcquireEmbedding waits for the selected provider and reports whether
	// forwarding may start. False leaves final handling with the adapter; any
	// nonnil release runs before scheduling feedback and completion.
	AcquireEmbedding(context.Context, provider.ProviderSnapshot) (func(), bool)
	// ForwardEmbedding sends one request using shared body bytes and returns
	// result plus recovery evidence. The adapter owns output emission and sets
	// OutputChanged when its writer observation forbids failover.
	ForwardEmbedding(context.Context, provider.ProviderSnapshot, []byte) EmbeddingOutcome
	// ReportEmbedding records success or failure after forwarding has released
	// its provider slot. A failover error with changed output returns before
	// this callback; other errors supply their optional result evidence.
	ReportEmbedding(context.Context, provider.ProviderSnapshot, *EmbeddingResult, bool, error)
	// CompleteEmbedding submits observations after successful forwarding and
	// scheduling feedback. Result can be nil because the loop does not guard
	// it; the implementation owns that case and any usage-recording failure.
	CompleteEmbedding(context.Context, provider.ProviderSnapshot, *EmbeddingResult)
	// SwitchEmbedding updates adapter state before the current provider ID
	// enters the exclusion map. It runs before checking the transition
	// allowance, including the final attempt that exhausts recovery.
	SwitchEmbedding(provider.ProviderSnapshot)
	// ObserveEmbedding handles the stage event at the point emitted by the
	// loop. Result pointers and header storage remain shared, so an observer
	// copies any data retained beyond synchronous handling.
	ObserveEmbedding(EmbeddingEvent)
	// ClientGone reports whether the adapter considers the request client
	// unavailable. It suppresses final handling for selection errors and stops
	// classified recovery after failure feedback has already been reported.
	ClientGone() bool
}

// EmbeddingFailure carries the terminal embedding stage into protocol error
// handling.
type EmbeddingFailure struct {
	Stage    string           // selection, empty_selection, forward or exhausted
	Err      error            // direct selection or forwarding error
	Outcome  EmbeddingOutcome // terminal or prior attempt evidence
	Excluded int              // excluded IDs on selection error
}

// RunEmbeddings forwards an admitted embedding request and switches
// providers within maxSwitches, defaulting nonpositive values to three. The
// caller keeps body bytes stable and supplies adapter handling for
// unavailable clients or failed slot waits. Slots are released before
// feedback or completion, and successful completion receives the result
// even when nil.
//
// For failover errors, observed output change stops execution before
// failure reporting or exclusion. Otherwise reporting precedes the
// client-departure check, then switch bookkeeping excludes the current
// provider before exhaustion is tested. A selection error retains the last
// switched outcome, whereas absent selection returns its own
// empty-selection stage.
func RunEmbeddings(ctx context.Context, body []byte, maxSwitches int, ports EmbeddingsPorts) *EmbeddingFailure {
	if maxSwitches <= 0 {
		maxSwitches = 3
	}
	excluded := make(map[int64]struct{})
	var last EmbeddingOutcome
	switches := 0
	routingStart := time.Now()
	for {
		selected, present, err := ports.SelectEmbedding(ctx, excluded)
		// Selection errors reach final handling only while the client remains
		// available; departure turns this path into an adapter-handled return
		// instead of another response.
		if err != nil {
			if ports.ClientGone() {
				ports.ObserveEmbedding(EmbeddingEvent{Kind: "select_canceled", Outcome: EmbeddingOutcome{Err: err}})
				return nil
			}
			ports.ObserveEmbedding(EmbeddingEvent{Kind: "select_failed", Outcome: EmbeddingOutcome{Err: err}, Excluded: len(excluded)})
			return &EmbeddingFailure{Stage: "selection", Err: err, Outcome: last, Excluded: len(excluded)}
		}
		if !present {
			return &EmbeddingFailure{Stage: "empty_selection"}
		}
		ports.ObserveEmbedding(EmbeddingEvent{Kind: "selected", Provider: selected})
		release, acquired := ports.AcquireEmbedding(ctx, selected)
		if !acquired {
			return nil
		}
		ports.ObserveEmbedding(EmbeddingEvent{Kind: "routing", Elapsed: time.Since(routingStart)})
		started := time.Now()
		// Release the provider slot as forwarding returns, allowing later report
		// and completion callbacks to perform their work without retaining attempt
		// admission.
		outcome := func() EmbeddingOutcome {
			if release != nil {
				defer release()
			}
			return ports.ForwardEmbedding(ctx, selected, body)
		}()
		ports.ObserveEmbedding(EmbeddingEvent{Kind: "response", Elapsed: time.Since(started)})
		if outcome.Err != nil {
			if outcome.Failover {
				// A failover-classified error can switch only before observed output
				// changes; return here before reporting or excluding the provider for
				// this response.
				if outcome.OutputChanged {
					return &EmbeddingFailure{Stage: "exhausted", Outcome: outcome}
				}
				ports.ReportEmbedding(ctx, selected, outcome.Result, false, outcome.Err)
				// Failure feedback is already recorded when client departure ends
				// recovery, preventing another provider attempt or final protocol
				// response for the departed client.
				if ports.ClientGone() {
					ports.ObserveEmbedding(EmbeddingEvent{Kind: "forward_canceled", Provider: selected, Outcome: outcome})
					return nil
				}
				// Apply adapter switching state and exclude this provider before
				// testing exhaustion, so recovery state includes the failed attempt
				// even on its terminal path.
				ports.SwitchEmbedding(selected)
				excluded[selected.ID] = struct{}{}
				last = outcome
				if switches >= maxSwitches {
					return &EmbeddingFailure{Stage: "exhausted", Outcome: outcome}
				}
				switches++
				ports.ObserveEmbedding(EmbeddingEvent{Kind: "switch", Provider: selected, Outcome: outcome, Switches: switches, MaxSwitches: maxSwitches})
				continue
			}
			ports.ReportEmbedding(ctx, selected, outcome.Result, false, outcome.Err)
			return &EmbeddingFailure{Stage: "forward", Outcome: outcome, Err: outcome.Err}
		}
		ports.ReportEmbedding(ctx, selected, outcome.Result, true, nil)
		ports.CompleteEmbedding(ctx, selected, outcome.Result)
		ports.ObserveEmbedding(EmbeddingEvent{Kind: "completed", Provider: selected, Switches: switches})
		return nil
	}
}
