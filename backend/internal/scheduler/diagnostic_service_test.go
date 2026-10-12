package scheduler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

func TestDiagnosticPolicySignalsOnlyReturnsContextualOrEnabledStrategies(t *testing.T) {
	group := &DiagnosticGroup{ID: 601, Advanced: true}
	effective := policy.EffectiveSettings{}

	// Baseline diagnostics mark capability checks as unevaluated when their request inputs are missing.
	baselineSignals := diagnosticPolicySignals(group, AdvancedSchedulerScoreDiagnosticRequest{}, effective, diagnosticPolicyOutcome{})
	require.Len(t, baselineSignals, 1)
	require.Equal(t, "request_capabilities", baselineSignals[0].Key)
	require.Equal(t, "not_evaluated", baselineSignals[0].State)

	effective.StickyWeightedEnabled = true
	effective.SubscriptionPriorityEnabled = true
	signals := diagnosticPolicySignals(group, AdvancedSchedulerScoreDiagnosticRequest{StickyProviderID: 99}, effective, diagnosticPolicyOutcome{
		sessionStickyState:     "weighted",
		subscriptionPoolActive: true,
	})
	require.Len(t, signals, 3)
	require.Equal(t, "session_sticky", signals[0].Key)
	require.Equal(t, "weighted", signals[0].State)
	require.Equal(t, "subscription_priority", signals[1].Key)
	require.Equal(t, "active_pool", signals[1].State)
}

func TestDiagnosticWeightedPreviousResponseIsIgnoredOutsideOpenAI(t *testing.T) {
	group := &DiagnosticGroup{ID: 602, Advanced: true}
	outcome := diagnosticHardStickyPolicyOutcome(
		[]*DiagnosticProvider{{ID: 99, Platform: capability.PlatformGemini}},
		group,
		AdvancedSchedulerScoreDiagnosticRequest{PreviousResponseProviderID: 99, StickyProviderID: 99},
		policy.EffectiveSettings{StickyWeightedEnabled: true},
		nil,
		policy.StickyEscapeConfig{},
	)

	require.Zero(t, outcome.forcedProviderID)
	require.Equal(t, "ignored", outcome.previousResponseState)
	require.Equal(t, "weighted", outcome.sessionStickyState)
}

// TestDiagnosticDisabledProviderReason checks the reason code used by the admin diagnostic translation.
func TestDiagnosticDisabledProviderReason(t *testing.T) {
	value := &DiagnosticProvider{ID: 7, Status: "active", Schedulable: false, GroupIDs: []int64{1}}
	require.Equal(t, "provider_disabled", diagnosticBaseHardFilterReason(value, &DiagnosticGroup{ID: 1}, time.Now()))
}
