package text

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/execution"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// fixedResponseRuntime retains a scripted outcome and optional mutable
// candidate for serial executor tests.
type fixedResponseRuntime struct {
	sessions []*plannedResponseSession // opened request-local response fixture sessions
	// plan is retained for deliberate mutation during Forward. A nil pointer
	// supplies no candidate; a present pointer is copied into selection before
	// its group is changed.
	plan    *routing.CandidatePlan // shared candidate pointer for fixture mutation
	outcome ResponseOutcome        // retained scripted response outcome
}

// plannedResponseSession wraps the response fixture with an optionally supplied
// candidate plan.
type plannedResponseSession struct {
	*responseFixture // delegated serial response-loop observations
	// plan is retained for deliberate mutation during Forward. A nil pointer
	// supplies no candidate; a present pointer is copied into selection before
	// its group is changed.
	plan *routing.CandidatePlan // shared candidate pointer for fixture mutation
}

// Select delegates fixture selection and copies the supplied plan into the
// returned candidate when present. The copy captures scalar plan identity
// before Forward mutates the shared source pointer for the test.
func (p *plannedResponseSession) Select(excluded map[int64]struct{}) (ResponseSelection, error) {
	s, e := p.responseFixture.Select(excluded)
	if p.plan != nil {
		s.Plan = *p.plan
		s.PlanProvided = true
	}
	return s, e
}

// Forward mutates the shared candidate group ID before returning the scripted
// outcome. That deliberate change tests whether selection capture retains its
// earlier scalar value rather than rereading the source pointer.
func (p *plannedResponseSession) Forward() ResponseOutcome {
	if p.plan != nil {
		// Mutate the source after Select copied its scalar plan. The executor must
		// retain the captured group identity when this forwarding result is observed.
		p.plan.GroupID = 999
	}
	return p.responseFixture.Forward()
}

// Open creates a separate responseFixture cursor around one retained outcome
// and shared plan pointer. Calls are serial in these tests, and the opened
// sessions are retained for completion/counter assertions after Execute.
func (r *fixedResponseRuntime) Open(context.Context, execution.Request, upstream.OutputSink) (ResponsePorts, error) {
	p := &plannedResponseSession{responseFixture: &responseFixture{outcomes: []ResponseOutcome{r.outcome}}, plan: r.plan}
	r.sessions = append(r.sessions, p)
	return p, nil
}

// TestFixedResponsesCapturesOnlyProvidedCandidate supplies absent and present
// candidate plans and mutates the source during Forward. Execution must retain
// the selection-time scalar plan identity and its presence flag alongside one
// completion.
func TestFixedResponsesCapturesOnlyProvidedCandidate(t *testing.T) {
	for _, provided := range []bool{false, true} {
		runtime := &fixedResponseRuntime{outcome: ResponseOutcome{Outcome: Outcome{HasResult: true, Attempt: upstream.AttemptResult{Model: "observed"}}}}
		if provided {
			runtime.plan = &routing.CandidatePlan{ProviderID: 1, GroupID: 7}
		}
		executor := NewResponsesExecutor(runtime, ResponseOptions{}, ResponseOptions{})
		result, err := executor.Execute(context.Background(), execution.Request{}, nil)
		require.NoError(t, err)
		require.Equal(t, 1, result.Attempts)
		require.Equal(t, provided, result.PlanProvided)
		if provided {
			require.Equal(t, int64(7), result.Plan.GroupID)
		} else {
			require.Equal(t, routing.CandidatePlan{}, result.Plan)
		}
		require.Equal(t, 1, runtime.sessions[0].selected)
		require.Equal(t, 1, runtime.sessions[0].completed)
	}
}

// TestFixedResponsesRetainsPartialFailureAcrossIndependentRequests executes a
// shared partial-image script twice. Each request must open a separate fixture
// cursor, retain the error/image observation, and complete once without
// switching providers.
func TestFixedResponsesRetainsPartialFailureAcrossIndependentRequests(t *testing.T) {
	cause := errors.New("partial image")
	runtime := &fixedResponseRuntime{outcome: ResponseOutcome{Images: true, Outcome: Outcome{Err: cause, HasResult: true, Attempt: upstream.AttemptResult{ObservedImages: 1}}}}
	executor := NewResponsesExecutor(runtime, ResponseOptions{}, ResponseOptions{MaxSwitches: 3, FirstOutputBudget: true})
	for range 2 {
		result, err := executor.Execute(context.Background(), execution.Request{Text: execution.TextState{Kind: execution.TextOpenAIResponses}}, nil)
		require.ErrorIs(t, err, cause)
		require.Equal(t, 1, result.Attempt.ObservedImages)
		require.Equal(t, 1, result.Attempts)
	}
	require.NotSame(t, runtime.sessions[0], runtime.sessions[1])
	for _, p := range runtime.sessions {
		require.Equal(t, 1, p.partial)
		require.Equal(t, 1, p.completed)
		require.Zero(t, p.switched)
	}
}

// TestPricingRejectionStopsTextAttempts returns a wrapped local pricing
// rejection for Responses, Chat and Messages entry kinds. Each executor call
// must end after one Forward invocation and leave retry, success and completion
// callbacks unused.
func TestPricingRejectionStopsTextAttempts(t *testing.T) {
	prices := &admission.ModelPricing{Resolver: billing.NewPriceResolver(nil, billing.NewCalculator(nil, billing.CalculatorOptions{}), nil, nil)}
	rejected := prices.Check(context.Background(), nil, "codex-auto-review")
	require.ErrorIs(t, rejected, admission.ErrModelPricingRejected)
	require.ErrorIs(t, rejected, pricing.ErrModelPricingUnavailable)
	for _, tc := range []struct {
		name string             // case label for diagnostics
		kind execution.TextKind // selected shared text entry kind
	}{
		{name: "responses", kind: execution.TextOpenAIResponses},
		{name: "chat", kind: execution.TextOpenAIChat},
		{name: "messages", kind: execution.TextOpenAIMessages},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := fmt.Errorf("prepare request: %w", rejected)
			runtime := &fixedResponseRuntime{outcome: ResponseOutcome{Outcome: Outcome{Err: cause}}}
			executor := NewResponsesExecutor(runtime, ResponseOptions{MaxSwitches: 3}, ResponseOptions{MaxSwitches: 3})
			result, err := executor.Execute(context.Background(), execution.Request{Text: execution.TextState{Kind: tc.kind}}, nil)
			require.ErrorIs(t, err, admission.ErrModelPricingRejected)
			require.Equal(t, 1, result.Attempts)
			session := runtime.sessions[0]
			require.Equal(t, 1, session.selected)
			require.Equal(t, 1, session.forwarded)
			require.Zero(t, session.failures)
			require.Zero(t, session.failed)
			require.Zero(t, session.successes)
			require.Zero(t, session.completed)
			require.Zero(t, session.switched)
			require.Zero(t, session.waited)
		})
	}
}

// TestUsagePricingFailureKeepsCompletion supplies measured usage with a
// native-partial result and a pricing error. Execution must retain the
// error/tokens while invoking the partial completion and ordinary-failure
// observation callbacks.
func TestUsagePricingFailureKeepsCompletion(t *testing.T) {
	runtime := &fixedResponseRuntime{outcome: ResponseOutcome{NativePartial: true, Outcome: Outcome{
		Err: pricing.ErrModelPricingUnavailable, HasResult: true,
		Attempt: upstream.AttemptResult{HasUsage: true, Usage: upstream.TokenUsage{OutputTokens: 2}},
	}}}
	executor := NewResponsesExecutor(runtime, ResponseOptions{}, ResponseOptions{})
	result, err := executor.Execute(context.Background(), execution.Request{}, nil)
	require.ErrorIs(t, err, pricing.ErrModelPricingUnavailable)
	require.Equal(t, 2, result.Attempt.Usage.OutputTokens)
	require.Equal(t, 1, runtime.sessions[0].completed)
	require.Equal(t, 1, runtime.sessions[0].failures)
}
