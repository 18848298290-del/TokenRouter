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

type fixedResponseRuntime struct {
	sessions []*plannedResponseSession
	plan     *routing.CandidatePlan
	outcome  ResponseOutcome
}
type plannedResponseSession struct {
	*responseFixture
	plan *routing.CandidatePlan
}

func (p *plannedResponseSession) Select(excluded map[int64]struct{}) (ResponseSelection, error) {
	s, e := p.responseFixture.Select(excluded)
	if p.plan != nil {
		s.Plan = *p.plan
		s.PlanProvided = true
	}
	return s, e
}

func (p *plannedResponseSession) Forward() ResponseOutcome {
	if p.plan != nil {
		p.plan.GroupID = 999
	}
	return p.responseFixture.Forward()
}

func (r *fixedResponseRuntime) Open(context.Context, execution.Request, upstream.OutputSink) (ResponsePorts, error) {
	p := &plannedResponseSession{responseFixture: &responseFixture{outcomes: []ResponseOutcome{r.outcome}}, plan: r.plan}
	r.sessions = append(r.sessions, p)
	return p, nil
}

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

// TestPricingRejectionStopsTextAttempts checks pricing rejection across all three shared text entry kinds.
func TestPricingRejectionStopsTextAttempts(t *testing.T) {
	prices := &admission.ModelPricing{Resolver: billing.NewPriceResolver(nil, billing.NewCalculator(nil, billing.CalculatorOptions{}), nil, nil)}
	rejected := prices.Check(context.Background(), nil, "codex-auto-review")
	require.ErrorIs(t, rejected, admission.ErrModelPricingRejected)
	require.ErrorIs(t, rejected, pricing.ErrModelPricingUnavailable)
	for _, tc := range []struct {
		name string
		kind execution.TextKind
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

// TestUsagePricingFailureKeepsCompletion checks that settlement-stage pricing errors still complete served output.
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
