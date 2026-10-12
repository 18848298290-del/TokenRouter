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

// responseFixture supplies serial Responses outcomes and records retries,
// partial handling and completion callbacks.
type responseFixture struct {
	// outcomes is retained and read by the Forward cursor. Mutable result
	// references inside its entries remain shared, and exhausted scripts yield
	// Stop for terminal fixture handling.
	outcomes []ResponseOutcome // retained ordered response outcome script
	// skipFirst sets Skip on the first captured selection. That fixture path
	// tests candidate exclusion before forwarding while the ordinary switch
	// counter stays unchanged.
	skipFirst                                                            bool    // skip first unsupported continuation candidate
	selected, forwarded, completed, switched, waited, partial, exhausted int     // selection callback invocations, forwarding callback invocations, completion capture calls, switch observation calls, retry delay observations, partial-image handling calls and exhaustion handling calls
	providers                                                            []int64 // selected provider identifiers in order
	failures, failed, successes                                          int     // OtherFailure callback invocations, Failed callback invocations and Success callback invocations
}

// Context returns an active background context for Responses fixture waiting.
func (*responseFixture) Context() context.Context { return context.Background() }

// CanAttempt permits each scripted Responses fixture execution to select again.
func (*responseFixture) CanAttempt() bool { return true }

// Select returns provider one until excluded, then provider two, and records
// selected identifiers. skipFirst marks its first captured candidate
// unsupported, while repeated excluded-provider enforcement beyond these two
// choices remains outside this fixture.
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

// SelectionFailure accepts fixture selection diagnostics without recording
// extra callback counters.
func (*responseFixture) SelectionFailure(error, int, *AttemptFailure) {}

// Acquire permits fixture forwarding while no external provider resource is
// owned.
func (*responseFixture) Acquire() bool { return true }

// Forward advances the supplied outcome cursor and returns its retained value.
// Running past that script yields Stop, so unexpected continuation ends safely
// without indexing beyond fixture input.
func (p *responseFixture) Forward() ResponseOutcome {
	index := p.forwarded
	p.forwarded++
	if index >= len(p.outcomes) {
		return ResponseOutcome{Outcome: Outcome{Stop: true}}
	}
	return p.outcomes[index]
}

// PartialImages counts the loop's retained partial-image error handling
// callback.
func (p *responseFixture) PartialImages(error) { p.partial++ }

// RetryReady permits every classified fixture failure to enter recovery
// decisions.
func (*responseFixture) RetryReady(*AttemptFailure) bool { return true }

// RetryWait counts an observed same-provider delay before the loop waits.
func (p *responseFixture) RetryWait(*AttemptFailure, int, int, time.Duration) { p.waited++ }

// Exhausted counts terminal classified recovery failures in this Responses
// fixture.
func (p *responseFixture) Exhausted(*AttemptFailure) { p.exhausted++ }

// Switched counts ordinary provider-switch observations before final budget
// evaluation.
func (p *responseFixture) Switched() { p.switched++ }

// Switching accepts allowed-transition details without modifying this fixture's
// counters.
func (*responseFixture) Switching(*AttemptFailure, int, int) {}

// OtherFailure counts ordinary or native-partial error handling before
// completion.
func (p *responseFixture) OtherFailure(error) { p.failures++ }

// Failed counts final failure handling after a fixture result is captured.
func (p *responseFixture) Failed() { p.failed++ }

// Complete counts completion capture requested by the Responses loop.
func (p *responseFixture) Complete() { p.completed++ }

// Success counts successful or retained-image provider feedback requested by
// execution.
func (p *responseFixture) Success() { p.successes++ }

// Completed accepts final switch-count reporting without changing fixture
// observations.
func (*responseFixture) Completed(int) {}

// TestResponsesUnsupportedContinuationSkipsWithoutSwitchBudget marks the first
// candidate unsupported and supplies a successful next target. The loop must
// exclude that candidate without spending a zero switch allowance or forwarding
// it.
func TestResponsesUnsupportedContinuationSkipsWithoutSwitchBudget(t *testing.T) {
	p := &responseFixture{skipFirst: true, outcomes: []ResponseOutcome{{Outcome: Outcome{HasResult: true}}}}
	RunResponses(ResponseOptions{MaxSwitches: 0}, p)
	require.Equal(t, []int64{1, 2}, p.providers)
	require.Equal(t, 1, p.forwarded)
	require.Zero(t, p.switched)
	require.Equal(t, 1, p.completed)
}

// TestResponsesSameProviderBudgetThenSingleCompletion supplies two classified
// failures and a success with one same-provider retry allowed. Execution must
// wait once, switch once, and complete only the final attempt.
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

// TestResponsesPartialImagesDoNotRetry supplies an error with retained
// image-result evidence and a retry policy. The image path must handle the
// partial error and complete once while keeping switch count zero.
func TestResponsesPartialImagesDoNotRetry(t *testing.T) {
	cause := errors.New("stream interrupted")
	p := &responseFixture{outcomes: []ResponseOutcome{{Images: true, Outcome: Outcome{Err: cause, HasResult: true, Failure: &AttemptFailure{Cause: cause, Policy: &failover.FailureInfo{RetryNext: true}}}}}}
	RunResponses(ResponseOptions{MaxSwitches: 3}, p)
	require.Equal(t, 1, p.forwarded)
	require.Equal(t, 1, p.partial)
	require.Zero(t, p.switched)
	require.Equal(t, 1, p.completed)
}

// TestResponsesFirstOutputRecoveryHasSeparateBudget supplies repeated
// first-output recovery failures under a large ordinary switch allowance. The
// separate allowance must end after one switch, with exhaustion and zero
// completion callbacks.
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
