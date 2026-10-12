package provider

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/protocol/google"
)

const antigravitySubscriptionAbnormal = "abnormal"

// AntigravitySubscriptionResult contains the normalized plan and subscription error state.
type AntigravitySubscriptionResult struct {
	PlanType           string
	SubscriptionStatus string
	SubscriptionError  string
}

// NormalizeAntigravitySubscription extracts the plan and eligibility errors from LoadCodeAssist.
// It maps the tier ID returned by GetTier to a plan type.
func NormalizeAntigravitySubscription(resp *google.AntigravityLoadCodeAssistResponse) AntigravitySubscriptionResult {
	if resp == nil {
		return AntigravitySubscriptionResult{PlanType: "Free"}
	}
	tierID := resp.GetTier()
	planType := google.AntigravityTierIDToPlanType(tierID)
	if len(resp.IneligibleTiers) > 0 {
		if planType == "" || planType == "Free" {
			planType = "Abnormal"
		}
		result := AntigravitySubscriptionResult{
			PlanType:           planType,
			SubscriptionStatus: antigravitySubscriptionAbnormal,
		}
		if resp.IneligibleTiers[0] != nil {
			result.SubscriptionError = strings.TrimSpace(resp.IneligibleTiers[0].ReasonMessage)
		}
		return result
	}
	return AntigravitySubscriptionResult{
		PlanType: planType,
	}
}
