package media

import (
	"context"
	"time"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// EmbeddingResult holds billing observations from the selected provider's execution.
type EmbeddingResult struct {
	RequestID, Model, BillingModel, UpstreamModel string
	Headers                                       map[string][]string
	Usage                                         protocolopenai.ForwardUsage
	Duration                                      time.Duration
}

// EmbeddingOutcome separates committed output from errors eligible for provider failover.
type EmbeddingOutcome struct {
	Result        *EmbeddingResult
	Err           error
	Failover      bool
	OutputChanged bool
	StatusCode    int
}

// EmbeddingEvent reports execution stages; the entry adapter handles logs and HTTP output.
type EmbeddingEvent struct {
	Kind                            string
	Provider                        provider.ProviderSnapshot
	Outcome                         EmbeddingOutcome
	Excluded, Switches, MaxSwitches int
	Elapsed                         time.Duration
}

// EmbeddingsPorts supplies request operations; acquisition waits on the selected provider.
type EmbeddingsPorts interface {
	SelectEmbedding(context.Context, map[int64]struct{}) (provider.ProviderSnapshot, bool, error)
	AcquireEmbedding(context.Context, provider.ProviderSnapshot) (func(), bool)
	ForwardEmbedding(context.Context, provider.ProviderSnapshot, []byte) EmbeddingOutcome
	ReportEmbedding(context.Context, provider.ProviderSnapshot, *EmbeddingResult, bool, error)
	CompleteEmbedding(context.Context, provider.ProviderSnapshot, *EmbeddingResult)
	SwitchEmbedding(provider.ProviderSnapshot)
	ObserveEmbedding(EmbeddingEvent)
	ClientGone() bool
}

// EmbeddingFailure identifies the failed stage for the HTTP adapter's protocol error response.
type EmbeddingFailure struct {
	Stage    string
	Err      error
	Outcome  EmbeddingOutcome
	Excluded int
}

// RunEmbeddings attempts providers and stops when slot acquisition fails.
// Callers complete admission first. Each attempt releases its slot before feedback and completion.
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
		outcome := func() EmbeddingOutcome {
			if release != nil {
				defer release()
			}
			return ports.ForwardEmbedding(ctx, selected, body)
		}()
		ports.ObserveEmbedding(EmbeddingEvent{Kind: "response", Elapsed: time.Since(started)})
		if outcome.Err != nil {
			if outcome.Failover {
				if outcome.OutputChanged {
					return &EmbeddingFailure{Stage: "exhausted", Outcome: outcome}
				}
				ports.ReportEmbedding(ctx, selected, outcome.Result, false, outcome.Err)
				if ports.ClientGone() {
					ports.ObserveEmbedding(EmbeddingEvent{Kind: "forward_canceled", Provider: selected, Outcome: outcome})
					return nil
				}
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
