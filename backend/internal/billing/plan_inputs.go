package billing

import (
	"bytes"
	"encoding/json"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// plan_inputs.go defines plan change requests and decodes nullable numeric
// patches.
// Plan updates distinguish an omitted nullable field from an explicit
// clear and a numeric value, including zero. Ordinary pointer fields
// retain existing values when nil, while OriginalPrice and the three
// quota ceilings use a presence bit with a nullable numeric value.
// That representation lets an administrator clear a ceiling without
// turning every omitted field into a clear operation.
//
// JSON decoding marks a received patch before interpreting null or a
// number. A fresh request keeps omitted patches absent; reusing a
// request object retains fields that the decoder never visits. Failed
// numeric decoding leaves a partial presence/clear state that callers
// discard with the decoding error. Business validation and persistent
// update selection are performed by the plan service and repository.

// NullableFloat64Patch records JSON field presence and an optional decoded plan number.
type NullableFloat64Patch struct {
	// Present and Value distinguish omission, explicit null, and a number.
	// A fresh patch has Present false; UnmarshalJSON sets it true and
	// clears Value before parsing. A present nil Value requests a clear,
	// including the partial state left by a returned decode error.
	Present bool     `json:"-"` // field was received
	Value   *float64 `json:"-"` // decoded number or clear
}

// CreatePlanRequest supplies new plan terms, group choices, and optional
// localized content.
type CreatePlanRequest struct {
	// Localization supplies the source copy when present. Plans.CreatePlan
	// uses its Name, Description, Features, and ProductName for the legacy
	// text fields before the repository prepares the versioned content.
	Localization *locale.Update[PlanCopy] `json:"localization"` // localized plan content update
	// GroupID and GroupIDs are normalized together on creation. A positive
	// legacy GroupID comes first, followed by distinct positive GroupIDs
	// in supplied order; an empty result permits unrestricted group
	// coverage for issued subscriptions.
	GroupID  int64   `json:"group_id"`  // legacy leading group
	GroupIDs []int64 `json:"group_ids"` // selected entitlement groups
	// GroupRateMultipliers retains overrides for selected positive group
	// IDs. Creation drops unselected entries and rejects selected values
	// at or below zero before writing normalized plan terms.
	GroupRateMultipliers map[int64]float64 `json:"group_rate_multipliers"` // selected group rate overrides
	Name                 string            `json:"name"`                   // source plan display name
	Description          string            `json:"description"`            // source plan display description
	Price                float64           `json:"price"`                  // configured plan base price
	OriginalPrice        *float64          `json:"original_price"`         // optional comparison display price
	Currency             string            `json:"currency"`               // optional plan price currency
	ValidityDays         int               `json:"validity_days"`          // configured duration count
	ValidityUnit         string            `json:"validity_unit"`          // configured duration unit
	// DailyLimitUSD, WeeklyLimitUSD, and MonthlyLimitUSD supply issued
	// window ceilings. Nil or zero leaves a window unlimited, while
	// the plan service rejects negative configured values before creation.
	DailyLimitUSD   *float64 `json:"daily_limit_usd"`   // USD daily quota ceiling
	WeeklyLimitUSD  *float64 `json:"weekly_limit_usd"`  // USD weekly quota ceiling
	MonthlyLimitUSD *float64 `json:"monthly_limit_usd"` // USD monthly quota ceiling
	Features        string   `json:"features"`          // source feature-list text
	ProductName     string   `json:"product_name"`      // source payment product name
	ForSale         bool     `json:"for_sale"`          // new-purchase availability flag
	SortOrder       int      `json:"sort_order"`        // plan display sort order
}

// UpdatePlanRequest supplies selected plan changes using nil ordinary pointers
// as unchanged fields. OriginalPrice and the three quota ceilings use
// NullableFloat64Patch to distinguish omission, clearing, and numeric
// replacement. OriginalPrice, DailyLimitUSD, WeeklyLimitUSD, and MonthlyLimitUSD
// retain storage when Present is false. A present nil Value clears storage,
// and a present number writes that number after the corresponding validation.
type UpdatePlanRequest struct {
	// Localization coordinates source text and translation revisions.
	// Plans.UpdatePlan rejects supplied legacy Name, Description, Features,
	// or ProductName pointers unless this content update is also present.
	Localization *locale.Update[PlanCopy] `json:"localization"` // localized plan content update
	// GroupID is a legacy leading group used by PlanStore.UpdatePlan
	// only when GroupIDs is supplied. Changing GroupID alone leaves the
	// stored group selection unchanged in that adapter.
	GroupID *int64 `json:"group_id"` // legacy leading group
	// GroupIDs requests a replacement group list when nonnil, including
	// an empty list to clear restrictions. PlanStore.UpdatePlan normalizes
	// it with the supplied legacy GroupID and rechecks the applicable
	// group-rate entries.
	GroupIDs *[]int64 `json:"group_ids"` // selected entitlement groups
	// GroupRateMultipliers requests rate replacement when nonnil. The
	// repository checks it against the supplied group list or the currently
	// stored selection when GroupIDs is absent, retaining only applicable
	// positive group IDs.
	GroupRateMultipliers *map[int64]float64   `json:"group_rate_multipliers"` // selected group rate overrides
	Name                 *string              `json:"name"`                   // source plan display name
	Description          *string              `json:"description"`            // source plan display description
	Price                *float64             `json:"price"`                  // configured plan base price
	OriginalPrice        NullableFloat64Patch `json:"original_price"`         // optional comparison display price
	Currency             *string              `json:"currency"`               // optional plan price currency
	ValidityDays         *int                 `json:"validity_days"`          // configured duration count
	ValidityUnit         *string              `json:"validity_unit"`          // configured duration unit
	DailyLimitUSD        NullableFloat64Patch `json:"daily_limit_usd"`        // USD daily quota ceiling
	WeeklyLimitUSD       NullableFloat64Patch `json:"weekly_limit_usd"`       // USD weekly quota ceiling
	MonthlyLimitUSD      NullableFloat64Patch `json:"monthly_limit_usd"`      // USD monthly quota ceiling
	Features             *string              `json:"features"`               // source feature-list text
	ProductName          *string              `json:"product_name"`           // source payment product name
	ForSale              *bool                `json:"for_sale"`               // new-purchase availability flag
	SortOrder            *int                 `json:"sort_order"`             // plan display sort order
}

// UnmarshalJSON records a received plan patch as explicit null or a
// float value, retaining numeric zero as a present number. The receiver
// starts marked Present with Value cleared, so a returned numeric
// decode error leaves partial state that callers discard.
//
// Omission never invokes this method: callers decode into a fresh
// request to get absent fields, while reused request storage can retain
// old patches. A null input succeeds with nil Value; other input is
// decoded as float64 and allocated on success. Business sign and quota
// validation occur after successful request decoding.
func (p *NullableFloat64Patch) UnmarshalJSON(data []byte) error {
	// Set both presence and the cleared value before interpreting input,
	// so explicit null replaces a reused numeric patch and remains
	// distinguishable from a field the decoder never received.
	p.Present = true
	p.Value = nil
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil
	}

	var value float64
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	p.Value = &value
	return nil
}
