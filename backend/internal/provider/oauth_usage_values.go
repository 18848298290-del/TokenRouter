package provider

import (
	"time"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/provider/usageview"
)

// WindowStats contains local usage totals for a time window.
//
// Cost sums COALESCE(provider_stats_cost, total_cost) * COALESCE(provider_rate_multiplier, 1).
// StandardCost is total_cost before multipliers.
// UserCost is actual_cost charged to the user or API key, including the group multiplier.
type WindowStats struct {
	Requests     int64   `json:"requests"`
	Tokens       int64   `json:"tokens"`
	Cost         float64 `json:"cost"`
	StandardCost float64 `json:"standard_cost"`
	UserCost     float64 `json:"user_cost"`
}

// UsageProgress describes utilization and reset timing for a quota window.
type UsageProgress struct {
	Utilization      float64      `json:"utilization"`            // Percent used; 100 means fully used and values may exceed 100.
	ResetsAt         *time.Time   `json:"resets_at"`              // Quota reset time.
	RemainingSeconds int          `json:"remaining_seconds"`      // Seconds until reset.
	WindowStats      *WindowStats `json:"window_stats,omitempty"` // Local usage since the window start.
	UsedRequests     int64        `json:"used_requests,omitempty"`
	LimitRequests    int64        `json:"limit_requests,omitempty"`
}

// AntigravityModelQuota contains quota information for one Antigravity model.
type AntigravityModelQuota struct {
	Utilization int    `json:"utilization"` // Percent used, from 0 to 100.
	ResetTime   string `json:"reset_time"`  // ISO 8601 reset time.
}

// AntigravityModelDetail contains the capabilities reported for one Antigravity model.
type AntigravityModelDetail struct {
	DisplayName        string          `json:"display_name,omitempty"`
	SupportsImages     *bool           `json:"supports_images,omitempty"`
	SupportsThinking   *bool           `json:"supports_thinking,omitempty"`
	ThinkingBudget     *int            `json:"thinking_budget,omitempty"`
	Recommended        *bool           `json:"recommended,omitempty"`
	MaxTokens          *int            `json:"max_tokens,omitempty"`
	MaxOutputTokens    *int            `json:"max_output_tokens,omitempty"`
	SupportedMimeTypes map[string]bool `json:"supported_mime_types,omitempty"`
}

// AICredit contains an Antigravity AI Credits balance.
type AICredit struct {
	CreditType     string  `json:"credit_type,omitempty"`
	Amount         float64 `json:"amount,omitempty"`
	MinimumBalance float64 `json:"minimum_balance,omitempty"`
}

type QoderQuotaProgress struct {
	Total          float64 `json:"total"`
	Used           float64 `json:"used"`
	Remaining      float64 `json:"remaining"`
	Percentage     float64 `json:"percentage"`
	Unit           string  `json:"unit,omitempty"`
	DetailURL      string  `json:"detail_url,omitempty"`
	Cap            float64 `json:"cap,omitempty"`
	Available      bool    `json:"available,omitempty"`
	OrganizationID string  `json:"organization_id,omitempty"`
}

type QoderQuotaInfo struct {
	UserID               string              `json:"user_id,omitempty"`
	UserType             string              `json:"user_type,omitempty"`
	UsageType            string              `json:"usage_type,omitempty"`
	TotalUsagePercentage float64             `json:"total_usage_percentage"`
	IsQuotaExceeded      bool                `json:"is_quota_exceeded"`
	ExpiresAt            *time.Time          `json:"expires_at,omitempty"`
	UpgradeURL           string              `json:"upgrade_url,omitempty"`
	AddCreditsURL        string              `json:"add_credits_url,omitempty"`
	UserQuota            *QoderQuotaProgress `json:"user_quota,omitempty"`
	AddOnQuota           *QoderQuotaProgress `json:"add_on_quota,omitempty"`
	OrgResourcePackage   *QoderQuotaProgress `json:"org_resource_package,omitempty"`
	IsPlanQuotaProrated  bool                `json:"is_plan_quota_prorated,omitempty"`
	LastUpdatedAt        *time.Time          `json:"last_updated_at,omitempty"`
	SnapshotFromProvider bool                `json:"snapshot_from_provider,omitempty"`
}

// UsageInfo contains provider usage and quota display data.
type UsageInfo struct {
	Source             string         `json:"source,omitempty"`               // "passive" or "active"
	UpdatedAt          *time.Time     `json:"updated_at,omitempty"`           // Observation time.
	FiveHour           *UsageProgress `json:"five_hour"`                      // Five-hour window.
	SevenDay           *UsageProgress `json:"seven_day,omitempty"`            // Seven-day window.
	SevenDaySonnet     *UsageProgress `json:"seven_day_sonnet,omitempty"`     // Seven-day Sonnet window.
	SevenDayFable      *UsageProgress `json:"seven_day_fable,omitempty"`      // Seven-day Fable window from the 7d_oi header.
	GeminiSharedDaily  *UsageProgress `json:"gemini_shared_daily,omitempty"`  // Gemini shared pool RPD (Google One / Code Assist)
	GeminiProDaily     *UsageProgress `json:"gemini_pro_daily,omitempty"`     // Gemini Pro daily quota.
	GeminiFlashDaily   *UsageProgress `json:"gemini_flash_daily,omitempty"`   // Gemini Flash daily quota.
	GeminiSharedMinute *UsageProgress `json:"gemini_shared_minute,omitempty"` // Gemini shared pool RPM (Google One / Code Assist)
	GeminiProMinute    *UsageProgress `json:"gemini_pro_minute,omitempty"`    // Gemini Pro RPM
	GeminiFlashMinute  *UsageProgress `json:"gemini_flash_minute,omitempty"`  // Gemini Flash RPM

	// QuotaAutoPaused reports whether an OpenAI quota threshold has paused scheduling.
	QuotaAutoPaused bool `json:"quota_auto_paused"`

	// Antigravity quotas keyed by model.
	AntigravityQuota map[string]*AntigravityModelQuota `json:"antigravity_quota,omitempty"`

	// Grok / xAI passive quota observations.
	GrokRequestQuota       *usageview.QuotaWindow `json:"grok_request_quota,omitempty"`
	GrokTokenQuota         *usageview.QuotaWindow `json:"grok_token_quota,omitempty"`
	GrokRetryAfterSeconds  *int                   `json:"grok_retry_after_seconds,omitempty"`
	GrokEntitlementStatus  string                 `json:"grok_entitlement_status,omitempty"`
	GrokQuotaSnapshotState string                 `json:"grok_quota_snapshot_state,omitempty"`
	GrokLastQuotaProbeAt   string                 `json:"grok_last_quota_probe_at,omitempty"`
	GrokLastHeadersSeenAt  string                 `json:"grok_last_headers_seen_at,omitempty"`
	GrokLastStatusCode     int                    `json:"grok_last_status_code,omitempty"`
	GrokFreeTokenLimit     int64                  `json:"grok_free_token_limit,omitempty"`
	GrokLocalUsage         *WindowStats           `json:"grok_local_usage,omitempty"`
	GrokLocalUsage24h      *WindowStats           `json:"grok_local_usage_24h,omitempty"`
	GrokLocalUsage7d       *WindowStats           `json:"grok_local_usage_7d,omitempty"`
	GrokLocalUsageMonthly  *WindowStats           `json:"grok_local_usage_monthly,omitempty"`
	// ThirtyDay is the upstream monthly billing window, with utilization derived from used/monthlyLimit.
	ThirtyDay   *UsageProgress            `json:"thirty_day,omitempty"`
	GrokBilling *usageview.BillingSummary `json:"grok_billing,omitempty"`

	// Antigravity subscription metadata.
	SubscriptionTier    string `json:"subscription_tier,omitempty"`     // Normalized tier: FREE, PRO, ULTRA, or UNKNOWN.
	SubscriptionTierRaw string `json:"subscription_tier_raw,omitempty"` // Upstream subscription tier name.

	// Model capabilities keyed identically to AntigravityQuota.
	AntigravityQuotaDetails map[string]*AntigravityModelDetail `json:"antigravity_quota_details,omitempty"`

	// Antigravity AI Credits balances.
	AICredits []AICredit `json:"ai_credits,omitempty"`

	// Monthly credits on the upstream Qoder account.
	QoderQuota *QoderQuotaInfo `json:"qoder_quota,omitempty"`

	// Antigravity deprecated-model redirects: old_model_id -> new_model_id.
	ModelForwardingRules map[string]string `json:"model_forwarding_rules,omitempty"`

	// IsForbidden reports an upstream Antigravity HTTP 403.
	IsForbidden     bool   `json:"is_forbidden,omitempty"`
	ForbiddenReason string `json:"forbidden_reason,omitempty"`
	ForbiddenType   string `json:"forbidden_type,omitempty"` // "validation" / "violation" / "forbidden"
	ValidationURL   string `json:"validation_url,omitempty"` // Verification or appeal URL.

	// Status flags derived from ForbiddenType or the HTTP error code.
	NeedsVerify bool `json:"needs_verify,omitempty"` // Manual verification required: forbidden_type=validation.
	IsBanned    bool `json:"is_banned,omitempty"`    // Account banned: forbidden_type=violation.
	NeedsReauth bool `json:"needs_reauth,omitempty"` // Authorization required after a 401 response.

	// Machine-readable failure code, such as forbidden, unauthenticated, rate_limited, or network_error.
	ErrorCode string `json:"error_code,omitempty"`

	// Display error from a failed usage query.
	Error string `json:"error,omitempty"`
}

type (
	ClaudeUsageWindow   = wire.ClaudeUsageWindow
	ClaudeUsageResponse = wire.ClaudeUsageResponse
)
