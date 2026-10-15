package provider

import (
	textflow "github.com/TokenFlux/TokenRouter/internal/gateway/text"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// selection.go represents selected provider resources and captures route
// information for the text provider loop.
// Provider selection can return an occupied slot or a plan for waiting until
// capacity becomes available. The result also carries the advanced scheduler
// parameters chosen for this request, while text capture records the selected
// route and retry limit for the provider loop.

// SelectionResult describes the chosen execution target, capacity outcome, and
// request feedback configuration.
type SelectionResult struct {
	Provider *ExecutionProvider // chosen request execution target
	// Acquired and ReleaseFunc describe the selection's held provider slot.
	// The execution owner releases an acquired slot after its upstream work ends;
	// WaitPlan supplies a later admission path when no slot was acquired.
	//
	// provider slot acquisition result
	Acquired bool
	// acquired provider slot cleanup
	ReleaseFunc func()
	// optional provider wait admission
	WaitPlan *scheduler.ProviderWaitPlan
	// advanced selection was used
	AdvancedScheduler bool
	// AdvancedSchedulerFeedback holds the parameters resolved for this selection;
	// completion uses them so intervening settings changes cannot alter this request's feedback.
	AdvancedSchedulerFeedback *policy.FeedbackConfig
}

// CaptureTextSelection captures candidate routing, pool retry count, and
// provider eligibility for the text loop. The target must be nonnil; an absent
// candidate is represented by PlanProvided=false while the snapshot still
// supplies provider state.
func CaptureTextSelection(provider *ExecutionProvider) textflow.Selection {
	plan, provided := ExecutionCandidatePlan(provider)
	return textflow.Selection{Provider: ExecutionSnapshot(provider), RetryLimit: provider.View().GetPoolModeRetryCount(), Plan: plan, PlanProvided: provided}
}
