package text

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// messageFixture supplies serial Messages outcomes and records loop progress,
// completion and final service evidence.
type messageFixture struct {
	// outcomes is retained as a read-only script whose entries Forward selects by
	// its invocation counter. Mutable references inside each supplied outcome
	// stay shared with the test.
	outcomes                                          []Outcome // retained ordered forwarding outcome script
	prepares, selected, forwards, completed, switches int       // attempt preparation calls, selection callback invocations, forwarding callback invocations, completion callback invocations and switch callback invocations
	// finished and served capture Finish invocation and its served argument.
	// exhausted and forcedStream capture terminal Exhausted handling and its
	// stream argument; the fixture records those separate callback pairs
	// serially.
	finished, served, exhausted, forcedStream bool // Finish callback ran, request marked served, terminal exhaustion observed and captured exhaustion stream flag
}

// Context returns an active background context for Messages fixture retries.
func (*messageFixture) Context() context.Context { return context.Background() }

// Begin accepts fixture lifecycle start without allocating any request
// resources.
func (*messageFixture) Begin() {}

// Finish records lifecycle completion and the supplied served decision.
func (p *messageFixture) Finish(served bool) { p.finished = true; p.served = served }

// PrepareAttempt counts fresh preparation and permits another fixture
// selection.
func (p *messageFixture) PrepareAttempt() bool { p.prepares++; return true }

// Select returns a newly numbered Anthropic candidate for each fixture
// invocation.
func (p *messageFixture) Select(map[int64]struct{}) (Selection, error) {
	p.selected++
	return Selection{Provider: provider.ProviderSnapshot{ID: int64(p.selected), Platform: "anthropic"}}, nil
}

// FirstSelectionFailure accepts initial fixture failure without producing
// client output.
func (*messageFixture) FirstSelectionFailure(error, bool) {}

// SingleProviderRetry accepts fixture retry observation without changing
// supplied outcomes.
func (*messageFixture) SingleProviderRetry() {}

// Canceled accepts cancellation observation without altering this fixture's
// recorded counters.
func (*messageFixture) Canceled() {}

// Exhausted records terminal classified failure and its changed-output bool.
func (p *messageFixture) Exhausted(_ *AttemptFailure, _ string, stream bool) {
	p.exhausted = true
	p.forcedStream = stream
}

// Intercept leaves this fixture request available for acquisition and
// forwarding.
func (*messageFixture) Intercept() bool { return false }

// Acquire permits forwarding while the fixture owns no external resources.
func (*messageFixture) Acquire() bool { return true }

// Forward advances the fixture outcome cursor and returns the retained value.
// Reading past supplied outcomes yields Stop, preventing an unexpected loop
// continuation from indexing beyond the scripted fixture.
func (p *messageFixture) Forward(AttemptState) Outcome {
	i := p.forwards
	p.forwards++
	if i >= len(p.outcomes) {
		return Outcome{Stop: true}
	}
	return p.outcomes[i]
}

// PolicyFailure accepts fixture refusal handling without producing request
// output.
func (*messageFixture) PolicyFailure(error) {}

// Fallback declines group recovery for every supplied Messages fixture error.
func (*messageFixture) Fallback(error, bool) bool { return false }

// OtherFailure accepts fixture error observation without changing completion
// counters.
func (*messageFixture) OtherFailure(error) {}

// Complete counts completion capture requested by the Messages loop.
func (p *messageFixture) Complete(AttemptState) { p.completed++ }

// Success accepts provider-success observation without performing funds
// persistence work.
func (*messageFixture) Success() {}

// Switched counts allowed failover continuation observed by this Messages
// fixture.
func (p *messageFixture) Switched() { p.switches++ }

// Abandon accepts fixture target retirement without owning external provider
// resources.
func (*messageFixture) Abandon(int64) {}

// TempUnscheduleRetryableError accepts fixture scheduling feedback without
// mutating provider state.
func (*messageFixture) TempUnscheduleRetryableError(context.Context, int64, *AttemptFailure) {}

// TestMessagesKeepPartialCompletionPerEntry runs ordinary partial failure with
// completion disabled and enabled. The selected option must control callback
// count and Finish's served flag while forwarding occurs once.
func TestMessagesKeepPartialCompletionPerEntry(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		p := &messageFixture{outcomes: []Outcome{{Err: errors.New("stream ended"), HasResult: true}}}
		RunMessages(MessageOptions{MaxSwitches: 3, CompletePartialFailure: enabled}, p)
		require.Equal(t, 1, p.forwards)
		require.True(t, p.finished)
		require.Equal(t, enabled, p.served)
		want := 0
		if enabled {
			want = 1
		}
		require.Equal(t, want, p.completed)
	}
}

// TestMessagesRetryRebuildsAndCompletesOnlyFinalAttempt supplies a classified
// transient failure followed by success. The loop must prepare and forward
// twice, observe one continuation, and capture completion only for the final
// result.
func TestMessagesRetryRebuildsAndCompletesOnlyFinalAttempt(t *testing.T) {
	err := errors.New("temporary failure")
	p := &messageFixture{outcomes: []Outcome{{Err: err, Failure: &AttemptFailure{Cause: err, Policy: &failover.FailureInfo{StatusCode: 503, RetryNext: true}}}, {HasResult: true}}}
	RunMessages(MessageOptions{MaxSwitches: 3, CompletePartialFailure: true}, p)
	require.Equal(t, 2, p.prepares)
	require.Equal(t, 2, p.forwards)
	require.Equal(t, 1, p.completed)
	require.Equal(t, 1, p.switches)
	require.True(t, p.served)
}

// TestMessagesWrittenFailureCannotSwitchOrComplete marks changed output on a
// classified forwarding failure. The loop must stop after one selection,
// request stream-safe exhaustion, and leave retry and completion counters at
// zero.
func TestMessagesWrittenFailureCannotSwitchOrComplete(t *testing.T) {
	err := errors.New("failure after output")
	p := &messageFixture{outcomes: []Outcome{{Err: err, HasResult: true, OutputChanged: true, Failure: &AttemptFailure{Cause: err, Policy: &failover.FailureInfo{StatusCode: 503, RetryNext: true}}}}}
	RunMessages(MessageOptions{MaxSwitches: 3, CompletePartialFailure: true}, p)
	require.Equal(t, 1, p.selected)
	require.Zero(t, p.switches)
	require.Zero(t, p.completed)
	require.True(t, p.exhausted)
	require.True(t, p.forcedStream)
	require.True(t, p.finished)
}
