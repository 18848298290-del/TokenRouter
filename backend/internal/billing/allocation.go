package billing

// An allocation records the funding source and the USD amount charged or
// reserved against it. Checkout snapshots separately retain purchased plan
// terms so fulfillment can grant the recorded duration and quotas after
// the current plan configuration has changed.

// Funding-source labels distinguish subscription quota from wallet balance
// when settlement persists allocations or task capture reads recorded holds.
const (
	BillingAllocationTypeSubscription BillingAllocationType = "subscription" // subscription quota funding
	BillingAllocationTypeBalance      BillingAllocationType = "balance"      // user wallet funding
)

// SubscriptionPlanSnapshot retains checkout plan display, price, duration, and quota terms.
type SubscriptionPlanSnapshot struct {
	Locale       string  `json:"locale,omitempty"`       // checkout display language
	ProductName  string  `json:"product_name,omitempty"` // checkout product display name
	Name         string  `json:"name"`                   // checkout plan display name
	Price        float64 `json:"price"`                  // checkout plan base price
	Currency     string  `json:"currency,omitempty"`     // checkout plan price currency
	ValidityDays int     `json:"validity_days"`          // issued validity in days
	// DailyLimitUSD, WeeklyLimitUSD, and MonthlyLimitUSD retain the quota
	// ceilings used when subscription fulfillment issues an entitlement.
	// Consumers treat absent or nonpositive ceilings as unlimited for the
	// corresponding window.
	DailyLimitUSD   *float64 `json:"daily_limit_usd,omitempty"`   // USD daily quota
	WeeklyLimitUSD  *float64 `json:"weekly_limit_usd,omitempty"`  // USD weekly quota
	MonthlyLimitUSD *float64 `json:"monthly_limit_usd,omitempty"` // USD monthly quota
}

// BillingAllocationType names the source consumed by one recorded funding allocation.
type BillingAllocationType string

// BillingAllocation retains a source charge or hold and its optional subscription identity.
type BillingAllocation struct {
	Type      BillingAllocationType `json:"type"`       // recorded funding source
	AmountUSD float64               `json:"amount_usd"` // USD source charge
	// BaseAmountUSD and RateMultiplier retain source-specific pricing
	// details for task snapshots. AmountUSD is the charged or reserved
	// quantity. Capture uses recorded rates when the command has snapshot
	// version two or later and positive BaseAmountUSD; other commands
	// distribute their actual amount across the recorded AmountUSD holds.
	BaseAmountUSD  float64 `json:"base_amount_usd,omitempty"` // USD source pricing base
	RateMultiplier float64 `json:"rate_multiplier,omitempty"` // source pricing multiplier
	// SubscriptionID and PlanID identify the entitlement behind subscription
	// funding when the producing flow has those identities. Wallet allocations
	// written by settlement omit both identifiers and retain their balance
	// source and amount.
	SubscriptionID *int64 `json:"subscription_id,omitempty"` // funded user subscription
	PlanID         *int64 `json:"plan_id,omitempty"`         // funded subscription plan
}
