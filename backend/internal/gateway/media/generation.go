package media

import (
	"context"
	"maps"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// GenerationResult holds response observations used to prepare completion.
type GenerationResult struct {
	RequestID, ResponseID, Model, BillingModel, UpstreamModel                    string
	Headers, ResponseHeaders                                                     map[string][]string
	Usage                                                                        protocolopenai.ForwardUsage
	Stream                                                                       bool
	Duration                                                                     time.Duration
	FirstTokenMs                                                                 *int
	ImageCount, VideoCount, VideoDurationSeconds                                 int
	ImageSize, ImageInputSize, ImageOutputSize, ImageSizeSource, VideoResolution string
	ImageOutputSizes                                                             []string
	ImageSizeBreakdown                                                           map[string]int
	// VideoState is the normalized task state for the completed request summary.
	VideoState string
}
type GenerationSelection struct {
	Provider   provider.ProviderSnapshot
	RetryLimit int
}
type GenerationOutcome struct {
	Result                         *GenerationResult
	Err                            error
	Failure                        *failover.FailureInfo
	OutputChanged, ReportFailure   bool
	ImageError                     bool
	ImageErrorRetryable            bool
	ImageErrorStatus               int
	ImageErrorType, ImageErrorCode string
}
type GenerationRequest struct {
	Body                    []byte
	Stream                  bool
	MaxSwitches             int
	RoutingStarted          time.Time
	Generation, VideoLookup bool
	BoundProviderID         int64
}
type GenerationEvent struct {
	Kind                                                    string
	Selection                                               GenerationSelection
	Outcome                                                 GenerationOutcome
	Excluded, Switches, MaxSwitches, RetryCount, RetryLimit int
	Elapsed, Delay                                          time.Duration
	Reason                                                  string
	ProbeFailed                                             bool
}
type GenerationFailure struct {
	Stage               string
	Err                 error
	Outcome             GenerationOutcome
	Excluded            int
	EligibilityRejected bool
	SelectedID          int64
}

// GenerationPorts supplies provider selection, execution, and output operations for a generation request.
type GenerationPorts interface {
	SelectGeneration(context.Context, map[int64]struct{}) (GenerationSelection, bool, error)
	ActivateGeneration(GenerationSelection)
	GenerationEligible(context.Context, GenerationSelection) (bool, string, error)
	AcquireGeneration(context.Context, GenerationSelection) (func(), bool)
	StartGenerationKeepalive() func()
	ForwardGeneration(context.Context, GenerationSelection, []byte) GenerationOutcome
	ReportGeneration(context.Context, GenerationSelection, *GenerationResult, bool, error)
	CompleteGeneration(context.Context, GenerationSelection, *GenerationResult)
	SwitchGeneration(GenerationSelection)
	StopGeneration429(GenerationSelection, int, int) bool
	GenerationClientGone() bool
	ObserveGeneration(GenerationEvent)
	EndGeneration(GenerationFailure)
}

// RunImages manages retries and JSON keepalives, submitting completion when partial images exist.
func RunImages(ctx context.Context, request GenerationRequest, p GenerationPorts) {
	excluded := map[int64]struct{}{}
	retries := map[int64]int{}
	switches := 0
	var last GenerationOutcome
	var stopKeepalive func()
	defer func() {
		if stopKeepalive != nil {
			stopKeepalive()
		}
	}()
	for {
		p.ObserveGeneration(GenerationEvent{Kind: "selecting", Excluded: len(excluded)})
		selected, present, err := p.SelectGeneration(ctx, excluded)
		if err != nil {
			if p.GenerationClientGone() {
				p.ObserveGeneration(GenerationEvent{Kind: "select_canceled", Outcome: GenerationOutcome{Err: err}})
				return
			}
			p.ObserveGeneration(GenerationEvent{Kind: "select_failed", Outcome: GenerationOutcome{Err: err}, Excluded: len(excluded)})
			p.EndGeneration(GenerationFailure{Stage: "selection", Err: err, Outcome: last, Excluded: len(excluded)})
			return
		}
		if !present {
			p.EndGeneration(GenerationFailure{Stage: "empty_selection"})
			return
		}
		p.ActivateGeneration(selected)
		release, acquired := p.AcquireGeneration(ctx, selected)
		if !acquired {
			return
		}
		p.ObserveGeneration(GenerationEvent{Kind: "routing", Elapsed: time.Since(request.RoutingStarted)})
		if !request.Stream && stopKeepalive == nil {
			stopKeepalive = p.StartGenerationKeepalive()
		}
		started := time.Now()
		result := func() GenerationOutcome {
			if release != nil {
				defer release()
			}
			return p.ForwardGeneration(ctx, selected, request.Body)
		}()
		p.ObserveGeneration(GenerationEvent{Kind: "response", Selection: selected, Outcome: result, Elapsed: time.Since(started)})
		if result.Err != nil && (result.Result == nil || result.Result.ImageCount <= 0) {
			if result.ImageError {
				if result.ImageErrorRetryable {
					p.ReportGeneration(ctx, selected, result.Result, false, result.Err)
				} else {
					p.ReportGeneration(ctx, selected, result.Result, true, nil)
				}
				p.ObserveGeneration(GenerationEvent{Kind: "image_error", Selection: selected, Outcome: result})
				return
			}
			if result.Failure == nil {
				p.EndGeneration(GenerationFailure{Stage: "unexpected", Err: result.Err, Outcome: result})
				return
			}
			p.ReportGeneration(ctx, selected, result.Result, false, result.Err)
			if result.OutputChanged {
				p.ObserveGeneration(GenerationEvent{Kind: "after_output", Selection: selected, Outcome: result})
				p.EndGeneration(GenerationFailure{Stage: "exhausted", Outcome: result})
				return
			}
			if p.GenerationClientGone() {
				p.ObserveGeneration(GenerationEvent{Kind: "forward_canceled", Selection: selected, Outcome: result})
				return
			}
			retryLimit := failover.EffectiveSameProviderRetryLimit(result.Failure, selected.RetryLimit)
			if result.Failure.RetryableOnSameProvider && failover.SameProviderRetryAllowed(result.Failure, retries[selected.Provider.ID], retryLimit) {
				retries[selected.Provider.ID]++
				delay := failover.SameProviderRetryDelayFor(result.Failure, retries[selected.Provider.ID])
				p.ObserveGeneration(GenerationEvent{Kind: "retry", Selection: selected, Outcome: result, RetryCount: retries[selected.Provider.ID], RetryLimit: retryLimit, Delay: delay})
				select {
				case <-ctx.Done():
					return
				case <-time.After(delay):
				}
				continue
			}
			p.SwitchGeneration(selected)
			excluded[selected.Provider.ID] = struct{}{}
			last = result
			if switches >= request.MaxSwitches {
				p.EndGeneration(GenerationFailure{Stage: "exhausted", Outcome: result})
				return
			}
			switches++
			if p.StopGeneration429(selected, result.Failure.StatusCode, switches) {
				p.EndGeneration(GenerationFailure{Stage: "exhausted", Outcome: result})
				return
			}
			p.ObserveGeneration(GenerationEvent{Kind: "switch", Selection: selected, Outcome: result, Switches: switches, MaxSwitches: request.MaxSwitches})
			continue
		}
		if result.Err != nil {
			p.ObserveGeneration(GenerationEvent{Kind: "partial", Selection: selected, Outcome: result})
		}
		p.CompleteGeneration(ctx, selected, result.Result)
		p.ObserveGeneration(GenerationEvent{Kind: "completed", Selection: selected, Switches: switches})
		return
	}
}

// RunGrokMedia runs generation attempts and keeps video lookups on the task's bound provider.
func RunGrokMedia(ctx context.Context, request GenerationRequest, p GenerationPorts) {
	if request.MaxSwitches <= 0 {
		request.MaxSwitches = 3
	}
	excluded := map[int64]struct{}{}
	retries := map[int64]int{}
	switches := 0
	rejected := false
	var last GenerationOutcome
	for {
		if p.GenerationClientGone() {
			return
		}
		selected, present, err := p.SelectGeneration(ctx, excluded)
		if err != nil {
			if p.GenerationClientGone() {
				p.ObserveGeneration(GenerationEvent{Kind: "select_canceled", Outcome: GenerationOutcome{Err: err}})
				return
			}
			p.ObserveGeneration(GenerationEvent{Kind: "select_failed", Outcome: GenerationOutcome{Err: err}, Excluded: len(excluded)})
			p.EndGeneration(GenerationFailure{Stage: "selection", Err: err, Outcome: last, Excluded: len(excluded), EligibilityRejected: rejected})
			return
		}
		if !present {
			p.EndGeneration(GenerationFailure{Stage: "empty_selection"})
			return
		}
		if request.BoundProviderID > 0 && selected.Provider.ID != request.BoundProviderID {
			p.EndGeneration(GenerationFailure{Stage: "bound_unavailable", SelectedID: selected.Provider.ID})
			return
		}
		p.ObserveGeneration(GenerationEvent{Kind: "schedule", Selection: selected})
		if request.Generation {
			eligible, reason, err := p.GenerationEligible(ctx, selected)
			if !eligible {
				rejected = true
				excluded[selected.Provider.ID] = struct{}{}
				p.ObserveGeneration(GenerationEvent{Kind: "ineligible", Selection: selected, Reason: reason, ProbeFailed: err != nil})
				if switches >= request.MaxSwitches {
					p.EndGeneration(GenerationFailure{Stage: "ineligible"})
					return
				}
				switches++
				continue
			}
		}
		p.ActivateGeneration(selected)
		release, acquired := p.AcquireGeneration(ctx, selected)
		if !acquired {
			return
		}
		p.ObserveGeneration(GenerationEvent{Kind: "routing", Elapsed: time.Since(request.RoutingStarted)})
		started := time.Now()
		result := func() GenerationOutcome {
			if release != nil {
				defer release()
			}
			return p.ForwardGeneration(ctx, selected, request.Body)
		}()
		p.ObserveGeneration(GenerationEvent{Kind: "response", Selection: selected, Outcome: result, Elapsed: time.Since(started)})
		if result.Err != nil {
			if result.Failure == nil {
				p.ReportGeneration(ctx, selected, nil, false, nil)
				p.EndGeneration(GenerationFailure{Stage: "unexpected", Err: result.Err, Outcome: result})
				return
			}
			if p.GenerationClientGone() {
				p.ObserveGeneration(GenerationEvent{Kind: "forward_canceled", Selection: selected, Outcome: result})
				return
			}
			if result.ReportFailure {
				p.ReportGeneration(ctx, selected, nil, false, nil)
			}
			if result.OutputChanged || !result.Failure.RetryNext || request.VideoLookup {
				p.EndGeneration(GenerationFailure{Stage: "exhausted", Outcome: result})
				return
			}
			retryLimit := failover.EffectiveSameProviderRetryLimit(result.Failure, selected.RetryLimit)
			if result.Failure.RetryableOnSameProvider && failover.SameProviderRetryAllowed(result.Failure, retries[selected.Provider.ID], retryLimit) {
				retries[selected.Provider.ID]++
				delay := failover.SameProviderRetryDelayFor(result.Failure, retries[selected.Provider.ID])
				p.ObserveGeneration(GenerationEvent{Kind: "retry", Selection: selected, Outcome: result, RetryCount: retries[selected.Provider.ID], RetryLimit: retryLimit, Delay: delay})
				select {
				case <-ctx.Done():
					return
				case <-time.After(delay):
				}
				continue
			}
			p.SwitchGeneration(selected)
			excluded[selected.Provider.ID] = struct{}{}
			last = result
			if switches >= request.MaxSwitches {
				p.EndGeneration(GenerationFailure{Stage: "exhausted", Outcome: result})
				return
			}
			switches++
			if p.StopGeneration429(selected, result.Failure.StatusCode, switches) {
				p.EndGeneration(GenerationFailure{Stage: "exhausted", Outcome: result})
				return
			}
			p.ObserveGeneration(GenerationEvent{Kind: "switch", Selection: selected, Outcome: result, Switches: switches, MaxSwitches: request.MaxSwitches})
			continue
		}
		p.ReportGeneration(ctx, selected, result.Result, true, nil)
		p.CompleteGeneration(ctx, selected, result.Result)
		p.ObserveGeneration(GenerationEvent{Kind: "completed", Selection: selected, Switches: switches})
		return
	}
}

// CloneGenerationResult copies mutable fields for independent output, execution, and completion snapshots.
func CloneGenerationResult(input *GenerationResult) *GenerationResult {
	if input == nil {
		return nil
	}
	output := *input
	if input.FirstTokenMs != nil {
		value := *input.FirstTokenMs
		output.FirstTokenMs = &value
	}
	output.ImageOutputSizes = append([]string(nil), input.ImageOutputSizes...)
	if input.ImageOutputSizes != nil && output.ImageOutputSizes == nil {
		output.ImageOutputSizes = []string{}
	}
	if input.ImageSizeBreakdown != nil {
		output.ImageSizeBreakdown = make(map[string]int, len(input.ImageSizeBreakdown))
		maps.Copy(output.ImageSizeBreakdown, input.ImageSizeBreakdown)
	}
	output.Headers = cloneGenerationHeaders(input.Headers)
	output.ResponseHeaders = cloneGenerationHeaders(input.ResponseHeaders)
	return &output
}

func cloneGenerationHeaders(input map[string][]string) map[string][]string {
	if input == nil {
		return nil
	}
	output := make(map[string][]string, len(input))
	for key, value := range input {
		if value == nil {
			output[key] = nil
		} else {
			output[key] = append([]string{}, value...)
		}
	}
	return output
}
