package httpapi

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// plan_response.go maps subscription plans to administrator response records.
// Administrator plan responses combine editable sale terms with versioned
// display content. PlanContent supplies stored localization or a source
// revision from legacy text, while the record response retains the plan's
// optional amounts and group collections for read-only serialization.

// PlanRecordResponse carries editable plan terms and localized source data for
// administrator endpoints.
type PlanRecordResponse struct {
	// Localization carries PlanContent's revisions and translations to the
	// editor. Existing content retains its translation map, so response consumers
	// keep that shared data unchanged.
	Localization billing.PlanLocalization `json:"localization"`          // versioned plan display content
	ID           int64                    `json:"id,omitempty"`          // local subscription plan identifier
	Name         string                   `json:"name,omitempty"`        // stored plan display name
	Description  string                   `json:"description,omitempty"` // stored plan description text
	Price        float64                  `json:"price,omitempty"`       // configured plan sale amount
	// OriginalPrice retains the plan's optional comparison amount pointer.
	// Serialization consumers keep that shared amount unchanged while reading
	// this administrator response.
	OriginalPrice *float64 `json:"original_price,omitempty"` // optional comparison display amount
	Currency      string   `json:"currency,omitempty"`       // configured sale currency code
	// ValidityDays retains the quantity interpreted together with ValidityUnit.
	// Grant computation converts configured weeks and months to day counts when
	// issuing subscriptions.
	ValidityDays int `json:"validity_days,omitempty"` // configured validity quantity
	// DailyLimitUSD, WeeklyLimitUSD and MonthlyLimitUSD retain the plan's
	// optional template pointers. Nil or nonpositive values represent unlimited
	// quotas, and serialization consumers leave the shared amounts unchanged.
	DailyLimitUSD   *float64 `json:"daily_limit_usd,omitempty"`   // USD
	WeeklyLimitUSD  *float64 `json:"weekly_limit_usd,omitempty"`  // USD
	MonthlyLimitUSD *float64 `json:"monthly_limit_usd,omitempty"` // USD
	ValidityUnit    string   `json:"validity_unit,omitempty"`     // configured validity quantity unit
	// GroupIDs and GroupRateMultipliers retain the plan's group collections for
	// this administrator record. The mapper shares their storage, so callers
	// leave them unchanged during serialization.
	GroupIDs             []int64           `json:"group_ids,omitempty"`              // applicable subscription group identifiers
	GroupRateMultipliers map[int64]float64 `json:"group_rate_multipliers,omitempty"` // plan group rate overrides
	Features             string            `json:"features,omitempty"`               // stored plan feature text
	ProductName          string            `json:"product_name,omitempty"`           // stored payment product name
	ForSale              bool              `json:"for_sale,omitempty"`               // new purchase availability flag
	SortOrder            int               `json:"sort_order,omitempty"`             // plan display sort order
	CreatedAt            time.Time         `json:"created_at,omitempty"`             // plan record creation time
	UpdatedAt            time.Time         `json:"updated_at,omitempty"`             // plan record update time
	Edges                struct{}          `json:"edges"`                            // empty response edges object
}

// planRecord builds the administrator record response or returns nil for nil
// input. Localization comes from PlanContent, including its legacy text
// revision; optional amount pointers and group collections remain shared with
// the plan for read-only serialization.
func planRecord(plan *billing.SubscriptionPlan) *PlanRecordResponse {
	if plan == nil {
		return nil
	}
	return &PlanRecordResponse{
		Localization:         billing.PlanLocalization(billing.PlanContent(plan)),
		ID:                   plan.ID,
		Name:                 plan.Name,
		Description:          plan.Description,
		Price:                plan.Price,
		OriginalPrice:        plan.OriginalPrice,
		Currency:             plan.Currency,
		ValidityDays:         plan.ValidityDays,
		DailyLimitUSD:        plan.DailyLimitUSD,
		WeeklyLimitUSD:       plan.WeeklyLimitUSD,
		MonthlyLimitUSD:      plan.MonthlyLimitUSD,
		ValidityUnit:         plan.ValidityUnit,
		GroupIDs:             plan.GroupIDs,
		GroupRateMultipliers: plan.GroupRateMultipliers,
		Features:             plan.Features,
		ProductName:          plan.ProductName,
		ForSale:              plan.ForSale,
		SortOrder:            plan.SortOrder,
		CreatedAt:            plan.CreatedAt,
		UpdatedAt:            plan.UpdatedAt,
	}
}
