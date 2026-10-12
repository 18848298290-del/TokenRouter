package text

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/execution"
	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// fixedMessageRuntimeTest opens separate fixture cursors/counters around the
// shared outcome script. Captured request values and scripted outcome
// references are retained for serial assertions rather than deeply cloned.
type fixedMessageRuntimeTest struct {
	// inputs retains each request value passed to Open. Nested request references
	// stay shared with the caller, while tests inspect the captured scalar model
	// after serial execution.
	inputs   []execution.Request // retained requests passed to Open
	sessions []*messageFixture   // opened request-local fixture instances
	// outcomes is shared read-only across sessions opened by this test runtime.
	// Each fixture advances its own counters while retaining any mutable
	// references inside outcome values.
	outcomes []Outcome // shared read-only scripted outcome values
}

// Open appends the supplied request value and creates a separate messageFixture
// around the retained outcome slice. Tests call this runtime serially; fixture
// counters/cursors are separate while input references and scripted result
// contents remain shared.
func (r *fixedMessageRuntimeTest) Open(_ context.Context, in execution.Request, _ upstream.OutputSink) (MessagePorts, error) {
	r.inputs = append(r.inputs, in)
	p := &messageFixture{outcomes: r.outcomes}
	r.sessions = append(r.sessions, p)
	return p, nil
}

// TestFixedMessagesExecutorReturnsLastAttemptAndOwnsOnlyLoop executes two
// requests with a transient attempt and final usage result. Each execution must
// open a separate fixture cursor, report two invocations, retain final
// provider/result evidence, and complete once.
func TestFixedMessagesExecutorReturnsLastAttemptAndOwnsOnlyLoop(t *testing.T) {
	err := errors.New("temporary")
	r := &fixedMessageRuntimeTest{outcomes: []Outcome{{Err: err, Failure: &AttemptFailure{Cause: err, Policy: &failover.FailureInfo{StatusCode: 503, RetryNext: true}}}, {HasResult: true, Attempt: upstream.AttemptResult{Model: "final", HasUsage: true, Usage: upstream.TokenUsage{OutputTokens: 7}}}}}
	executor := NewMessagesExecutor(r, MessageOptions{MaxSwitches: 3, CompletePartialFailure: true}, MessageOptions{MaxSwitches: 1})
	for _, model := range []string{"first", "second"} {
		result, err := executor.Execute(context.Background(), execution.Request{Model: model}, nil)
		require.NoError(t, err)
		require.Equal(t, 2, result.Attempts)
		require.Equal(t, int64(2), result.Provider.ID)
		require.Equal(t, "final", result.Attempt.Model)
		require.Equal(t, 7, result.Attempt.Usage.OutputTokens)
	}
	require.Len(t, r.sessions, 2)
	require.NotSame(t, r.sessions[0], r.sessions[1])
	require.Equal(t, "first", r.inputs[0].Model)
	require.Equal(t, "second", r.inputs[1].Model)
	for _, s := range r.sessions {
		require.Equal(t, 2, s.prepares)
		require.Equal(t, 2, s.forwards)
		require.Equal(t, 1, s.completed)
		require.True(t, s.finished)
	}
}

// TestFixedMessagesExecutorKeepsGeminiPartialCompletionDifference returns the
// same partial-error usage under Messages and Gemini kinds. Both results must
// retain observed tokens, while the selected entry option changes whether
// completion is captured.
func TestFixedMessagesExecutorKeepsGeminiPartialCompletionDifference(t *testing.T) {
	err := errors.New("partial output")
	for _, kind := range []execution.TextKind{execution.TextMessages, execution.TextGeminiMessages} {
		r := &fixedMessageRuntimeTest{outcomes: []Outcome{{Err: err, HasResult: true, Attempt: upstream.AttemptResult{HasUsage: true, Usage: upstream.TokenUsage{OutputTokens: 2}}}}}
		executor := NewMessagesExecutor(r, MessageOptions{CompletePartialFailure: true}, MessageOptions{})
		result, got := executor.Execute(context.Background(), execution.Request{Text: execution.TextState{Kind: kind}}, nil)
		require.ErrorIs(t, got, err)
		require.Equal(t, 2, result.Attempt.Usage.OutputTokens)
		expected := 0
		if kind == execution.TextMessages {
			expected = 1
		}
		require.Equal(t, expected, r.sessions[0].completed)
	}
}
