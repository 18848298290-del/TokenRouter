package text

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// responseFixture counts attempts, same-provider waits, and completion calls.
type responseFixture struct {
	outcomes                                                             []ResponseOutcome
	skipFirst                                                            bool
	selected, forwarded, completed, switched, waited, partial, exhausted int
	providers                                                            []int64
	failures, failed, successes                                          int
}

func (*responseFixture) Context() context.Context { return context.Background() }
func (*responseFixture) CanAttempt() bool         { return true }
func (p *responseFixture) Select(excluded map[int64]struct{}) (ResponseSelection, error) {
	p.selected++
	id := int64(1)
	if _, ok := excluded[id]; ok {
		id = 2
	}
	selected := ResponseSelection{Selection: Selection{Provider: provider.ProviderSnapshot{ID: id}, RetryLimit: 2}, Available: true}
	if p.skipFirst && p.selected == 1 {
		selected.Skip = &AttemptFailure{Cause: errors.New("HTTP continuation unsupported")}
	}
	p.providers = append(p.providers, id)
	return selected, nil
}
func (*responseFixture) SelectionFailure(error, int, *AttemptFailure) {}
func (*responseFixture) Acquire() bool                                { return true }
func (p *responseFixture) Forward() ResponseOutcome {
	index := p.forwarded
	p.forwarded++
	if index >= len(p.outcomes) {
		return ResponseOutcome{Outcome: Outcome{Stop: true}}
	}
	return p.outcomes[index]
}
func (p *responseFixture) PartialImages(error)                                { p.partial++ }
func (*responseFixture) RetryReady(*AttemptFailure) bool                      { return true }
func (p *responseFixture) RetryWait(*AttemptFailure, int, int, time.Duration) { p.waited++ }
func (p *responseFixture) Exhausted(*AttemptFailure)                          { p.exhausted++ }
func (p *responseFixture) Switched()                                          { p.switched++ }
func (*responseFixture) Switching(*AttemptFailure, int, int)                  {}
func (p *responseFixture) OtherFailure(error)                                 { p.failures++ }
func (p *responseFixture) Failed()                                            { p.failed++ }
func (p *responseFixture) Complete()                                          { p.completed++ }
func (p *responseFixture) Success()                                           { p.successes++ }
func (*responseFixture) Completed(int)                                        {}

func TestResponsesUnsupportedContinuationSkipsWithoutSwitchBudget(t *testing.T) {
	p := &responseFixture{skipFirst: true, outcomes: []ResponseOutcome{{Outcome: Outcome{HasResult: true}}}}
	RunResponses(ResponseOptions{MaxSwitches: 0}, p)
	require.Equal(t, []int64{1, 2}, p.providers)
	require.Equal(t, 1, p.forwarded)
	require.Zero(t, p.switched)
	require.Equal(t, 1, p.completed)
}

func TestResponsesSameProviderBudgetThenSingleCompletion(t *testing.T) {
	cause := errors.New("temporary failure")
	failed := ResponseOutcome{Outcome: Outcome{Err: cause, Failure: &AttemptFailure{Cause: cause, Policy: &failover.FailureInfo{StatusCode: 503, RetryNext: true, RetryableOnSameProvider: true, SameProviderRetryMax: 1, SameProviderRetryDelay: time.Millisecond}}}}
	p := &responseFixture{outcomes: []ResponseOutcome{failed, failed, {Outcome: Outcome{HasResult: true}}}}
	RunResponses(ResponseOptions{MaxSwitches: 1}, p)
	require.Equal(t, []int64{1, 1, 2}, p.providers)
	require.Equal(t, 1, p.waited)
	require.Equal(t, 1, p.switched)
	require.Equal(t, 1, p.completed)
}

func TestResponsesPartialImagesDoNotRetry(t *testing.T) {
	cause := errors.New("stream interrupted")
	p := &responseFixture{outcomes: []ResponseOutcome{{Images: true, Outcome: Outcome{Err: cause, HasResult: true, Failure: &AttemptFailure{Cause: cause, Policy: &failover.FailureInfo{RetryNext: true}}}}}}
	RunResponses(ResponseOptions{MaxSwitches: 3}, p)
	require.Equal(t, 1, p.forwarded)
	require.Equal(t, 1, p.partial)
	require.Zero(t, p.switched)
	require.Equal(t, 1, p.completed)
}

func TestResponsesFirstOutputRecoveryHasSeparateBudget(t *testing.T) {
	cause := errors.New("no semantic output")
	failure := ResponseOutcome{FirstOutputRecovery: true, Outcome: Outcome{Err: cause, Failure: &AttemptFailure{Cause: cause, Policy: &failover.FailureInfo{RetryNext: true}}}}
	p := &responseFixture{outcomes: []ResponseOutcome{failure, failure}}
	RunResponses(ResponseOptions{MaxSwitches: 10, FirstOutputBudget: true}, p)
	require.Equal(t, 2, p.forwarded)
	require.Equal(t, 1, p.switched)
	require.Equal(t, 1, p.exhausted)
	require.Zero(t, p.completed)
}
