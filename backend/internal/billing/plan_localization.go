package billing

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// plan_localization.go validates plan display content and produces localized
// plan views.
// Plan display text is resolved from versioned content when available,
// while legacy plans keep their existing text fields as the source.
// Localization creates a shallow plan view for display, so group
// collections, quota pointers, and stored content remain shared with
// the source plan.

// PlanCopy groups the plan name, description, feature text, and payment product name.
type PlanCopy struct {
	Name        string `json:"name"`         // plan display name
	Description string `json:"description"`  // plan display description
	Features    string `json:"features"`     // newline-separated feature text
	ProductName string `json:"product_name"` // payment product display name
}

// PlanLocalization retains versioned source and translations for the plan display copy.
type PlanLocalization locale.Content[PlanCopy]

// PlanContent returns recorded localized content when its revision is
// positive, otherwise revision-one legacy content from the text fields.
// Pass a nonnil plan; recorded content is returned by value with its
// existing translation map and other references retained.
func PlanContent(plan *SubscriptionPlan) locale.Content[PlanCopy] {
	if plan.Localization.Revision > 0 {
		return locale.Content[PlanCopy](plan.Localization)
	}
	content := locale.Original(PlanCopy{Name: plan.Name, Description: plan.Description, Features: plan.Features, ProductName: plan.ProductName})
	// Legacy text needs an initial content revision for administrator
	// editing. Mark both revisions one so subsequent locale.Prepare calls
	// can compare the editor's revision with that synthesized source.
	content.Revision, content.SourceRevision = 1, 1
	return content
}

// ValidatePlanCopy checks one source or translation copy for a nonblank
// name and bounded text. Name and ProductName use one hundred code
// points; Description and Features use sixty-five thousand five hundred
// thirty-six bytes. The validator retains supplied whitespace and
// returns the first structured length or name error.
func ValidatePlanCopy(copy PlanCopy) error {
	// The display-name limit counts Unicode code points, allowing translated
	// names to use the same length ceiling without measuring UTF-8 bytes.
	if strings.TrimSpace(copy.Name) == "" || len([]rune(copy.Name)) > 100 {
		return apperror.BadRequest("PLAN_NAME_REQUIRED", "Plan name is required and must not exceed 100 characters.")
	}
	// Long description and feature fields use byte limits, while the
	// payment product name shares the display name's code-point ceiling
	// for every copy validated by locale.Prepare.
	if len(copy.Description) > 65536 || len(copy.Features) > 65536 || len([]rune(copy.ProductName)) > 100 {
		return apperror.BadRequest("PLAN_TEXT_TOO_LONG", "Plan text exceeds the allowed length.")
	}
	return nil
}

// LocalizePlan returns nil for nil input or a shallow display copy
// of the plan. Positive content revision resolves Name, Description,
// Features, ProductName, and Resolution for language; other fields and
// nested references remain shared, so callers leave those references
// unchanged when treating the result as a display view.
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
