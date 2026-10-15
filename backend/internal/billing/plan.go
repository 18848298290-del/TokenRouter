package billing

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// plan.go defines subscription sale templates and their group display values.
// Plans are editable sale templates, while a granted subscription
// retains its own issued duration and quota limits. Plan group
// configuration still supplies applicable group coverage and rates,
// and public responses resolve the display text independently of
// those funding and entitlement values.

// SubscriptionPlan retains configured sale terms, group applicability, and
// localized display data.
// Name, Description, Features, and ProductName carry the display copy selected
// by LocalizePlan. Administrator updates pass these fields through a localization
// update so their persisted source revisions and translations are handled together.
type SubscriptionPlan struct {
	// Localization supplies versioned source and translated display text.
	// LocalizePlan uses it when its revision is positive; otherwise the
	// legacy text fields remain the displayed content.
	Localization  PlanLocalization  // stored localized plan content
	Resolution    locale.Resolution // resolved display language metadata
	ID            int64             // local plan identifier
	Name          string            // resolved plan display name
	Description   string            // resolved plan display description
	Price         float64           // configured plan base price
	OriginalPrice *float64          // optional comparison display price
	Currency      string            // stored plan price currency
	// ValidityDays and ValidityUnit describe the issued duration template.
	// ComputeValidityDays multiplies week counts by seven and month counts
	// by thirty; it treats other unit text as the supplied day count.
	ValidityDays int    // configured duration count
	ValidityUnit string // configured duration unit
	// GroupIDs and GroupRateMultipliers define group coverage and selected
	// group rates. Empty GroupIDs permits any group in subscription coverage
	// checks; nonempty IDs restrict it, and stored group-rate overrides
	// are read by settlement for eligible subscriptions.
	GroupIDs             []int64           // applicable group identifiers
	GroupRateMultipliers map[int64]float64 // selected group rate overrides
	// GroupsRestricted and ApplicableGroups are response metadata populated
	// by SubscriptionService.EnrichSubscriptionPlanGroups. The flag reflects
	// nonempty GroupIDs, while group labels are loaded when a restricted
	// plan has not already supplied applicable labels.
	GroupsRestricted bool                    // derived group restriction flag
	ApplicableGroups []SubscriptionPlanGroup // hydrated applicable group labels
	// DailyLimitUSD, WeeklyLimitUSD, and MonthlyLimitUSD are copied to a
	// new entitlement at issuance. Nil or zero ceilings impose no finite
	// window limit; later plan quota edits do not replace those issued
	// subscription fields.
	DailyLimitUSD   *float64 // USD issued daily ceiling
	WeeklyLimitUSD  *float64 // USD issued weekly ceiling
	MonthlyLimitUSD *float64 // USD issued monthly ceiling
	Features        string   // resolved feature-list text
	ProductName     string   // resolved payment product name
	// ForSale controls new subscription purchases at Checkout.ValidateSubOrder.
	// A retained plan can still describe existing grants and historical
	// orders when it is not currently offered for sale.
	ForSale   bool      // purchase availability flag
	SortOrder int       // plan display sort order
	CreatedAt time.Time // plan creation timestamp
	UpdatedAt time.Time // latest plan update
}

// SubscriptionPlanGroup identifies one covered group and its user-facing display name.
type SubscriptionPlanGroup struct {
	ID   int64  // covered group identifier
	Name string // group display name
}
