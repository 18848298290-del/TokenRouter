package usageview

import (
	"time"
)

const (
	UpstreamUsageAdapterSub2API         = "sub2api"
	UpstreamUsageAdapterNewAPI          = "new_api"
	UpstreamUsageAdapterZivv            = "zivv"
	UpstreamUsageAdapterKimiCoding      = "kimi_coding"
	UpstreamUsageAdapterZhipuCoding     = "zhipu_coding"
	UpstreamUsageAdapterKimiBalance     = "kimi_balance"
	UpstreamUsageAdapterDeepseekBalance = "deepseek_balance"
)

// UpstreamUsageAmount holds optional used, total, and remaining amounts.
type UpstreamUsageAmount struct {
	Used      *float64 `json:"used,omitempty"`
	Total     *float64 `json:"total,omitempty"`
	Remaining *float64 `json:"remaining,omitempty"`
}

// UpstreamUsageBalanceEntry is one currency balance.
type UpstreamUsageBalanceEntry struct {
	Currency  string  `json:"currency"`
	Remaining float64 `json:"remaining"`
}

// UpstreamUsageLimit holds a named upstream quota window independently of OAuth window names.
type UpstreamUsageLimit struct {
	Name      string     `json:"name"`
	Used      *float64   `json:"used,omitempty"`
	Limit     *float64   `json:"limit,omitempty"`
	Remaining *float64   `json:"remaining,omitempty"`
	ResetAt   *time.Time `json:"reset_at,omitempty"`
}

// UpstreamUsageSubscription holds a subscription balance and periodic limits.
type UpstreamUsageSubscription struct {
	PlanName  string               `json:"plan_name"`
	Unlimited bool                 `json:"unlimited,omitempty"`
	Remaining *float64             `json:"remaining,omitempty"`
	ExpiresAt *time.Time           `json:"expires_at,omitempty"`
	Limits    []UpstreamUsageLimit `json:"limits,omitempty"`
}

// UpstreamUsageInfo is the normalized usage returned by an adapter.
type UpstreamUsageInfo struct {
	Provider string `json:"provider"`
	Mode     string `json:"mode"`
	Unit     string `json:"unit,omitempty"`
	// New API and Zivv use Balance for the wallet and Limits/Subscription for API key quota.
	Balance      *UpstreamUsageAmount        `json:"balance,omitempty"`
	Balances     []UpstreamUsageBalanceEntry `json:"balances,omitempty"`
	Available    *bool                       `json:"available,omitempty"`
	Limits       []UpstreamUsageLimit        `json:"limits,omitempty"`
	Subscription *UpstreamUsageSubscription  `json:"subscription,omitempty"`
	ExpiresAt    *time.Time                  `json:"expires_at,omitempty"`
}

// CNQuotaTier is a rolling Coding Plan usage window.
type CNQuotaTier struct {
	Window      string  `json:"window"`
	UsedPercent float64 `json:"used_percent"`
	ResetAt     string  `json:"reset_at,omitempty"`
}
