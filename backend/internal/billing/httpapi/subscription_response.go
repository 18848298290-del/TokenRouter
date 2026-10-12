package httpapi

import (
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingdto "github.com/TokenFlux/TokenRouter/internal/billing/httpapi/dto"
)

// User subscription responses can resolve plan display text in a selected
// language while retaining granted validity and quotas. The wrapper localizes
// a shallow plan view before DTO conversion, and administrator/batch wrappers
// retain the assignment data selected by their DTO mappers.

// UserSubscription aliases the JSON grant response with user-facing plan and
// quota data.
type UserSubscription = billingdto.UserSubscription

// AdminUserSubscription aliases the JSON grant response containing
// administrator assignment metadata.
type AdminUserSubscription = billingdto.AdminUserSubscription

// BulkAssignResult aliases batch grant counts, records and per-user JSON
// outcomes.
type BulkAssignResult = billingdto.BulkAssignResult

// DefaultSubscriptionSetting is the HTTP JSON representation of a default subscription plan.
type DefaultSubscriptionSetting struct {
	PlanID int64 `json:"plan_id"` // default signup plan identifier
}

// UserSubscriptionFromService builds a user response, returning nil for nil
// input. When a language is supplied, its first value localizes a shallow plan
// view on a subscription copy; the DTO mapper then applies its selective
// collection copies and shared-pointer rules.
func UserSubscriptionFromService(sub *billing.UserSubscription, language ...string) *UserSubscription {
	if sub != nil && len(language) > 0 {
		copy := *sub
		copy.Plan = billing.LocalizePlan(sub.Plan, language[0])
		return billingdto.UserSubscriptionFromService(&copy)
	}
	return billingdto.UserSubscriptionFromService(sub)
}

// UserSubscriptionFromServiceAdmin delegates administrator grant response
// mapping and returns nil for nil input. Assignment details and notes are
// included, while optional dates and quota pointers follow the DTO mapper's
// read-only sharing rules.
func UserSubscriptionFromServiceAdmin(sub *billing.UserSubscription) *AdminUserSubscription {
	return billingdto.UserSubscriptionFromServiceAdmin(sub)
}

// BulkAssignResultFromService delegates batch grant response mapping and
// returns nil for nil input. Administrator records and decimal status keys are
// rebuilt, while error storage and per-record optional pointers follow the DTO
// mapper's sharing rules.
func BulkAssignResultFromService(r *billing.BulkAssignResult) *BulkAssignResult {
	return billingdto.BulkAssignResultFromService(r)
}
