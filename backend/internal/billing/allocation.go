package billing

const (
	BillingAllocationTypeSubscription BillingAllocationType = "subscription"
	BillingAllocationTypeBalance      BillingAllocationType = "balance"
)

type SubscriptionPlanSnapshot struct {
	Locale          string   `json:"locale,omitempty"`
	ProductName     string   `json:"product_name,omitempty"`
	Name            string   `json:"name"`
	Price           float64  `json:"price"`
	Currency        string   `json:"currency,omitempty"` // Plan price currency at checkout.
	ValidityDays    int      `json:"validity_days"`
	DailyLimitUSD   *float64 `json:"daily_limit_usd,omitempty"`
	WeeklyLimitUSD  *float64 `json:"weekly_limit_usd,omitempty"`
	MonthlyLimitUSD *float64 `json:"monthly_limit_usd,omitempty"`
}

type BillingAllocationType string

type BillingAllocation struct {
	Type      BillingAllocationType `json:"type"`
	AmountUSD float64               `json:"amount_usd"`
	// BaseAmountUSD and RateMultiplier describe source-specific charges. Older records with neither field use AmountUSD.
	BaseAmountUSD  float64 `json:"base_amount_usd,omitempty"`
	RateMultiplier float64 `json:"rate_multiplier,omitempty"`
	SubscriptionID *int64  `json:"subscription_id,omitempty"`
	PlanID         *int64  `json:"plan_id,omitempty"`
}
