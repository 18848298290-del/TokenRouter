package billing

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// PlanCopy contains user-facing plan text, with one feature per line in Features.
type PlanCopy struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Features    string `json:"features"`
	ProductName string `json:"product_name"`
}

// PlanLocalization holds a plan's source text and translations.
type PlanLocalization locale.Content[PlanCopy]

// PlanContent returns stored localization, or initializes revision one from the plan's text fields.
func PlanContent(plan *SubscriptionPlan) locale.Content[PlanCopy] {
	if plan.Localization.Revision > 0 {
		return locale.Content[PlanCopy](plan.Localization)
	}
	content := locale.Original(PlanCopy{Name: plan.Name, Description: plan.Description, Features: plan.Features, ProductName: plan.ProductName})
	content.Revision, content.SourceRevision = 1, 1
	return content
}

// ValidatePlanCopy checks names in Unicode code points and description/features in bytes, for every language.
func ValidatePlanCopy(copy PlanCopy) error {
	if strings.TrimSpace(copy.Name) == "" || len([]rune(copy.Name)) > 100 {
		return apperror.BadRequest("PLAN_NAME_REQUIRED", "Plan name is required and must not exceed 100 characters.")
	}
	if len(copy.Description) > 65536 || len(copy.Features) > 65536 || len([]rune(copy.ProductName)) > 100 {
		return apperror.BadRequest("PLAN_TEXT_TOO_LONG", "Plan text exceeds the allowed length.")
	}
	return nil
}

// LocalizePlan returns a shallow copy with localized display text, leaving the source plan's fields unchanged.
func LocalizePlan(plan *SubscriptionPlan, language string) *SubscriptionPlan {
	if plan == nil {
		return nil
	}
	result := *plan
	if plan.Localization.Revision > 0 {
		copy, resolution := locale.Content[PlanCopy](plan.Localization).Resolve(language)
		result.Name, result.Description = copy.Name, copy.Description
		result.Features, result.ProductName = copy.Features, copy.ProductName
		result.Resolution = resolution
	}
	return &result
}
