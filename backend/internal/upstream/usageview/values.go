package usageview

import (
	"time"
)

// values.go defines the normalized balances, quotas, and subscription values
// returned by usage adapters.
// API-key usage adapters report wallets, key quotas, subscriptions, or
// percentage windows in one display representation. Optional pointers
// distinguish an absent observation from a reported zero, and separate wallet
// and key-quota fields keep balances available without interpreting an
// unlimited key as money.

// These adapter identifiers select fixed upstream query formats in provider
// execution and identify their normalized results.
const (
	UpstreamUsageAdapterSub2API         = "sub2api"          // Sub2API usage query adapter
	UpstreamUsageAdapterNewAPI          = "new_api"          // New API usage query adapter
	UpstreamUsageAdapterZivv            = "zivv"             // Zivv wallet query adapter
	UpstreamUsageAdapterKimiCoding      = "kimi_coding"      // Kimi coding percentage adapter
	UpstreamUsageAdapterZhipuCoding     = "zhipu_coding"     // Zhipu coding percentage adapter
	UpstreamUsageAdapterKimiBalance     = "kimi_balance"     // Kimi wallet balance adapter
	UpstreamUsageAdapterDeepseekBalance = "deepseek_balance" // DeepSeek multicurrency balance adapter
)

// UpstreamUsageAmount holds independently optional amounts reported in the
// containing result unit.
type UpstreamUsageAmount struct {
	// Used, Total, and Remaining use the containing result's unit and can be
	// absent independently; nil means unobserved. ValidateUsageAmount requires
	// finite nonnegative Used and Total while treating a pointed zero as observed.
	Used  *float64 `json:"used,omitempty"`  // reported used amount
	Total *float64 `json:"total,omitempty"` // reported total amount
	// Remaining can report wallet debt. ValidateUsageAmount accepts finite
	// negative values here; adapter-specific token quota rules are checked separately.
	Remaining *float64 `json:"remaining,omitempty"` // reported remaining amount
}

// UpstreamUsageBalanceEntry retains one reported wallet amount with its own
// currency label.
type UpstreamUsageBalanceEntry struct {
	Currency  string  `json:"currency"`  // reported balance currency label
	Remaining float64 `json:"remaining"` // amount in Currency
}

// UpstreamUsageLimit retains one named quota window and its optional
// normalized amounts.
type UpstreamUsageLimit struct {
	// Name identifies the upstream window. ValidateUsageLimits requires nonblank
	// names and compares their trimmed spellings case-sensitively for duplicate windows.
	Name string `json:"name"` // upstream quota window name
	// Used, Limit, and Remaining use the result's shared unit and can be absent
	// independently. ValidateUsageLimits requires at least one observed value
	// and finite nonnegative Used and Limit when those values are supplied.
	Used  *float64 `json:"used,omitempty"`  // reported window used amount
	Limit *float64 `json:"limit,omitempty"` // reported window quota limit
	// Remaining accepts finite negative values in ValidateUsageLimits. An adapter
	// needing an exhausted window floored at zero performs that normalization separately.
	Remaining *float64 `json:"remaining,omitempty"` // reported window remaining amount
	// ResetAt is optional reset evidence. A supplied zero time fails normalized
	// validation; adapters retain a parsed reset or omit unrecognized reset input.
	ResetAt *time.Time `json:"reset_at,omitempty"` // optional window reset instant
}

// UpstreamUsageSubscription retains a named subscription and its optional
// remaining amount or windows. Unlimited, Remaining, and Limits select its
// quota representation: normalized validation rejects remaining/window data
// for Unlimited and requires at least one remaining amount or window when
// Unlimited is false.
type UpstreamUsageSubscription struct {
	// PlanName identifies the reported plan. ValidateNormalizedUsage requires a
	// nonblank trimmed name whenever the optional subscription object is supplied.
	PlanName  string   `json:"plan_name"`           // reported subscription plan name
	Unlimited bool     `json:"unlimited,omitempty"` // subscription has unlimited quota
	Remaining *float64 `json:"remaining,omitempty"` // reported subscription remaining amount
	// ExpiresAt supplies optional expiry evidence. ValidateNormalizedUsage rejects
	// a zero time when present and leaves chronological interpretation to consumers.
	ExpiresAt *time.Time           `json:"expires_at,omitempty"` // optional subscription expiry instant
	Limits    []UpstreamUsageLimit `json:"limits,omitempty"`     // reported subscription quota windows
}

// UpstreamUsageInfo holds the normalized adapter result validated before
// administrator usage display. Balance, Limits, and Subscription retain
// different funding observations: New API and Zivv put wallet amounts in
// Balance and key-quota observations in Limits or Subscription, so key limits
// do not become a wallet amount.
type UpstreamUsageInfo struct {
	// Provider identifies the normalization source. ValidateNormalizedUsage requires
	// a nonblank trimmed label while leaving the supplied spelling unchanged.
	Provider string `json:"provider"` // upstream normalization source label
	// Mode selects the required normalized shape. Validation accepts exact balance,
	// quota, limits, or subscription labels and checks the corresponding fields.
	Mode string `json:"mode"` // normalized usage presentation mode
	// Unit labels shared amounts. ValidateNormalizedUsage accepts empty, USD, CNY,
	// TOKENS, or PERCENT, while separate Balances entries carry their own currencies.
	Unit    string               `json:"unit,omitempty"`    // normalized shared amount unit
	Balance *UpstreamUsageAmount `json:"balance,omitempty"` // reported primary balance amounts
	// Balances retains DeepSeek's separate currency observations. Its Balance and
	// Unit use the first retained entry while monitoring can inspect every currency.
	Balances []UpstreamUsageBalanceEntry `json:"balances,omitempty"` // reported separate currency balances
	// Available retains DeepSeek's availability observation. That adapter defaults
	// it to true when is_available is absent; nil represents adapters without this field.
	Available    *bool                      `json:"available,omitempty"`    // reported account availability observation
	Limits       []UpstreamUsageLimit       `json:"limits,omitempty"`       // reported usage quota windows
	Subscription *UpstreamUsageSubscription `json:"subscription,omitempty"` // reported subscription usage details
	// ExpiresAt supplies optional expiry evidence outside a subscription. Normalized
	// validation rejects a supplied zero time and leaves expiry policy to consumers.
	ExpiresAt *time.Time `json:"expires_at,omitempty"` // optional upstream expiry instant
}

// CNQuotaTier carries one reported coding-plan percentage window before
// normalized limit construction.
type CNQuotaTier struct {
	Window string `json:"window"` // coding quota window name
	// UsedPercent is the reported percentage. usageclient.CnUsageLimits accepts
	// finite nonnegative values, retains overuse, and floors its derived
	// remainder at zero.
	UsedPercent float64 `json:"used_percent"` // percent
	// ResetAt supplies reset text to usageclient.CnUsageLimits. That function
	// omits invalid or zero RFC3339 resets while retaining the percentage window.
	ResetAt string `json:"reset_at,omitempty"` // supplied reset timestamp text
}
