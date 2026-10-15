package usageview

// grok_grok.go defines Grok billing summaries and quota-window values for usage
// displays.
// Grok billing data can expose weekly progress, monthly included-credit
// spending, and prepaid or on-demand dollar amounts at the same time. Display
// values retain those sources separately so a monthly billing date cannot
// become the reset date for an exhausted weekly window.

// BillingProductSummary records one upstream product and its optional reported
// usage percentage.
type BillingProductSummary struct {
	Product string `json:"product"` // upstream product display name
	// UsagePercent records reported progress for this product. Nil means that
	// source supplied no retained percentage, while a pointed zero is observed.
	UsagePercent *float64 `json:"usage_percent,omitempty"` // percent
}

// BillingSummary retains separately sourced Grok billing windows and probe
// observations for consumers.
type BillingSummary struct {
	// PeriodType, UsagePercent, PeriodStart, and PeriodEnd describe the selected
	// primary window. grok.BuildBillingSummary uses weekly evidence when present and
	// monthly dates otherwise; monthly included-credit progress uses UsedPercent
	// separately.
	PeriodType   string   `json:"period_type,omitempty"`   // selected primary window kind
	UsagePercent *float64 `json:"usage_percent,omitempty"` // percent
	PeriodStart  string   `json:"period_start,omitempty"`  // primary period start text
	PeriodEnd    string   `json:"period_end,omitempty"`    // primary period end text
	// ProductUsage retains weekly product observations and their optional pointers.
	// CloneBillingSummary copies both slice storage and percentages for
	// independent mutation.
	ProductUsage []BillingProductSummary `json:"product_usage,omitempty"` // weekly product usage observations
	// MonthlyLimitCents, UsedCents, and IncludedUsedCents use monthly credit cents.
	// grok.BuildBillingSummary caps IncludedUsedCents at a positive MonthlyLimitCents;
	// nil pointers retain absent source values instead of asserting a zero amount.
	MonthlyLimitCents *float64 `json:"monthly_limit_cents,omitempty"` // cents
	UsedCents         *float64 `json:"used_cents,omitempty"`          // cents
	IncludedUsedCents *float64 `json:"included_used_cents,omitempty"` // cents
	// BillingPeriodStart and BillingPeriodEnd retain monthly billing dates
	// separately from the primary window dates so consumers can choose the
	// reset corresponding to the usage dimension they display or schedule.
	BillingPeriodStart string `json:"billing_period_start,omitempty"` // monthly period start text
	BillingPeriodEnd   string `json:"billing_period_end,omitempty"`   // monthly period end text
	// UsedPercent is derived by grok.BuildBillingSummary when monthly limit is positive
	// and included usage exists. UsagePercent independently carries weekly progress.
	UsedPercent *float64 `json:"used_percent,omitempty"` // percent
	Plan        string   `json:"plan,omitempty"`         // inferred subscription display label
	// StatusCode summarizes the probe result, while WeeklyStatusCode and
	// MonthlyStatusCode retain each endpoint status. Grok media eligibility checks
	// all three for a forbidden response; zero means no retained HTTP status.
	StatusCode        int    `json:"status_code,omitempty"`         // selected probe HTTP status
	WeeklyStatusCode  int    `json:"weekly_status_code,omitempty"`  // weekly probe HTTP status
	MonthlyStatusCode int    `json:"monthly_status_code,omitempty"` // monthly probe HTTP status
	Source            string `json:"source,omitempty"`              // billing observation source label
	// FetchedAt and UpdatedAt are set by grok.StampBillingSummary for the latest probe.
	// WeeklyUpdatedAt and MonthlyUpdatedAt advance for their successful window
	// merges, retaining older times when that window failed; stamps use UTC RFC3339.
	FetchedAt        string `json:"fetched_at,omitempty"`         // latest probe fetch timestamp
	UpdatedAt        string `json:"updated_at,omitempty"`         // latest summary update timestamp
	WeeklyUpdatedAt  string `json:"weekly_updated_at,omitempty"`  // weekly successful update timestamp
	MonthlyUpdatedAt string `json:"monthly_updated_at,omitempty"` // monthly successful update timestamp
	// Partial and FailedWindows describe the latest weekly/monthly probe outcome.
	// A failed window can retain previous amounts and dates, so retained data
	// alone does not establish that both windows were refreshed successfully.
	Partial       bool     `json:"partial,omitempty"`        // incomplete latest window probe
	FailedWindows []string `json:"failed_windows,omitempty"` // failed billing window names
	// PrepaidBalance, OnDemandCap, and OnDemandUsed retain reported dollar values.
	// MonthlyLimit and MonthlyUsed are the monthly cent fields converted to dollars;
	// nil in any of these fields means that amount was not retained.
	PrepaidBalance       *float64 `json:"prepaid_balance,omitempty"`         // USD
	OnDemandCap          *float64 `json:"on_demand_cap,omitempty"`           // USD
	OnDemandUsed         *float64 `json:"on_demand_used,omitempty"`          // USD
	MonthlyLimit         *float64 `json:"monthly_limit,omitempty"`           // USD
	MonthlyUsed          *float64 `json:"monthly_used,omitempty"`            // USD
	TopUpMethod          string   `json:"top_up_method,omitempty"`           // reported account top-up method
	IsUnifiedBillingUser bool     `json:"is_unified_billing_user,omitempty"` // reported unified billing membership
}

// QuotaWindow retains one Grok header-based request or token quota dimension.
type QuotaWindow struct {
	// Limit and Remaining use the containing request or token dimension. Each
	// pointer can be absent independently; a retained zero is an observed value
	// used by quota consumers rather than a missing header.
	Limit     *int64 `json:"limit,omitempty"`     // reported dimension quota limit
	Remaining *int64 `json:"remaining,omitempty"` // reported dimension quota remaining
	// ResetUnix and ResetAt represent the same parsed reset in Grok header
	// normalization. ResetUnix stores epoch seconds; ResetAt stores its UTC RFC3339
	// spelling, while absent or unparsed reset input leaves both unavailable.
	ResetUnix *int64 `json:"reset_unix,omitempty"` // Unix seconds
	ResetAt   string `json:"reset_at,omitempty"`   // UTC RFC3339
}
