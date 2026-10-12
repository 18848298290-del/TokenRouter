package httpapi

import (
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingdto "github.com/TokenFlux/TokenRouter/internal/billing/httpapi/dto"
)

type UserSubscription = billingdto.UserSubscription

type AdminUserSubscription = billingdto.AdminUserSubscription

type BulkAssignResult = billingdto.BulkAssignResult

// DefaultSubscriptionSetting is the HTTP JSON representation of a default subscription plan.
type DefaultSubscriptionSetting struct {
	PlanID int64 `json:"plan_id"`
}

// UserSubscriptionFromService builds a response and localizes a copy of the plan when a language is supplied.
func UserSubscriptionFromService(sub *billing.UserSubscription, language ...string) *UserSubscription {
	if sub != nil && len(language) > 0 {
		copy := *sub
		copy.Plan = billing.LocalizePlan(sub.Plan, language[0])
		return billingdto.UserSubscriptionFromService(&copy)
	}
	return billingdto.UserSubscriptionFromService(sub)
}

// UserSubscriptionFromServiceAdmin builds the administrator subscription response.
func UserSubscriptionFromServiceAdmin(sub *billing.UserSubscription) *AdminUserSubscription {
	return billingdto.UserSubscriptionFromServiceAdmin(sub)
}

// BulkAssignResultFromService converts a batch grant result to its HTTP response.
func BulkAssignResultFromService(r *billing.BulkAssignResult) *BulkAssignResult {
	return billingdto.BulkAssignResultFromService(r)
}
