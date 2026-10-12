package provider

import (
	textflow "github.com/TokenFlux/TokenRouter/internal/gateway/text"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// SelectionResult holds the selected provider, slot release function, and optional wait plan.
// AdvancedSchedulerFeedback captures the feedback parameters used for this selection.
type SelectionResult struct {
	Provider                  *ExecutionProvider
	Acquired                  bool
	ReleaseFunc               func()
	WaitPlan                  *scheduler.ProviderWaitPlan
	AdvancedScheduler         bool
	AdvancedSchedulerFeedback *policy.FeedbackConfig
}

// CaptureTextSelection captures the candidate plan, retry limit, and provider snapshot.
func CaptureTextSelection(provider *ExecutionProvider) textflow.Selection {
	plan, provided := ExecutionCandidatePlan(provider)
	return textflow.Selection{Provider: ExecutionSnapshot(provider), RetryLimit: provider.View().GetPoolModeRetryCount(), Plan: plan, PlanProvided: provided}
}
