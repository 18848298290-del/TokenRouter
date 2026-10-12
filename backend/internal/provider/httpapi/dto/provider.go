package dto

import (
	"time"

	proxydto "github.com/TokenFlux/TokenRouter/internal/egress/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	routingdto "github.com/TokenFlux/TokenRouter/internal/routing/httpapi/dto"
)

type Provider struct {
	ID       int64   `json:"id"`
	Name     string  `json:"name"`
	Notes    *string `json:"notes"`
	Platform string  `json:"platform"`
	Type     string  `json:"type"`
	// Credentials contains non-sensitive keys after RedactCredentials runs. CredentialsStatus
	// exposes sensitive token, API key, and private-key presence through has_<key> flags.
	Credentials             map[string]any                  `json:"credentials"`
	CredentialsStatus       map[string]bool                 `json:"credentials_status,omitempty"`
	Extra                   map[string]any                  `json:"extra"`
	OllamaCloudUsage        *provider.OllamaCloudUsageState `json:"ollama_cloud_usage,omitempty"`
	ProxyID                 *int64                          `json:"proxy_id"`
	ProxyFallbackOriginID   *int64                          `json:"proxy_fallback_origin_id"`
	ProxyFallbackOriginName *string                         `json:"proxy_fallback_origin_name,omitempty"`
	Concurrency             int                             `json:"concurrency"`
	LoadFactor              *int                            `json:"load_factor,omitempty"`
	Priority                int                             `json:"priority"`
	RateMultiplier          float64                         `json:"rate_multiplier"`
	Status                  string                          `json:"status"`
	ErrorMessage            string                          `json:"error_message"`
	LastUsedAt              *time.Time                      `json:"last_used_at"`
	ExpiresAt               *int64                          `json:"expires_at"`
	AutoPauseOnExpired      bool                            `json:"auto_pause_on_expired"`
	CreatedAt               time.Time                       `json:"created_at"`
	UpdatedAt               time.Time                       `json:"updated_at"`

	Schedulable bool `json:"schedulable"`

	RateLimitedAt    *time.Time `json:"rate_limited_at"`
	RateLimitResetAt *time.Time `json:"rate_limit_reset_at"`
	OverloadUntil    *time.Time `json:"overload_until"`

	TempUnschedulableUntil  *time.Time `json:"temp_unschedulable_until"`
	TempUnschedulableReason string     `json:"temp_unschedulable_reason"`

	// QuotaAutoPaused reports whether an OpenAI provider is paused by a 5h or 7d quota threshold.
	QuotaAutoPaused bool `json:"quota_auto_paused"`

	SessionWindowStart  *time.Time `json:"session_window_start"`
	SessionWindowEnd    *time.Time `json:"session_window_end"`
	SessionWindowStatus string     `json:"session_window_status"`

	// Five-hour cost controls for Anthropic OAuth and Setup Token providers.
	// Extracted from Extra for display and editing.
	WindowCostLimit         *float64 `json:"window_cost_limit,omitempty"`
	WindowCostStickyReserve *float64 `json:"window_cost_sticky_reserve,omitempty"`

	// Session-count controls for Anthropic OAuth and Setup Token providers.
	// Extracted from Extra for display and editing.
	MaxSessions           *int `json:"max_sessions,omitempty"`
	SessionIdleTimeoutMin *int `json:"session_idle_timeout_minutes,omitempty"`

	// RPM limits for Anthropic OAuth and Setup Token providers.
	// Extracted from Extra for display and editing.
	BaseRPM          *int    `json:"base_rpm,omitempty"`
	RPMStrategy      *string `json:"rpm_strategy,omitempty"`
	RPMStickyBuffer  *int    `json:"rpm_sticky_buffer,omitempty"`
	UserMsgQueueMode *string `json:"user_msg_queue_mode,omitempty"`

	// TLS fingerprint settings for Anthropic OAuth/Setup Token and OpenAI OAuth providers.
	// Extracted from Extra for display and editing.
	EnableTLSFingerprint    *bool  `json:"enable_tls_fingerprint,omitempty"`
	TLSFingerprintProfileID *int64 `json:"tls_fingerprint_profile_id,omitempty"`
	TLSFingerprintRouterID  *int64 `json:"tls_fingerprint_router_id,omitempty"`

	// OpenAI OAuth client access policy.
	OpenAIOAuthClientPolicy *string `json:"openai_oauth_client_policy,omitempty"`

	// Session ID masking for Anthropic OAuth and Setup Token providers.
	// When enabled, metadata.user_id keeps the same session ID for 15 minutes.
	// Extracted from Extra for display and editing.
	EnableSessionIDMasking *bool `json:"session_id_masking_enabled,omitempty"`

	// Cache TTL overrides for Anthropic OAuth and Setup Token providers.
	// When enabled, all cache creation tokens are billed using the selected TTL category.
	CacheTTLOverrideEnabled *bool   `json:"cache_ttl_override_enabled,omitempty"`
	CacheTTLOverrideTarget  *string `json:"cache_ttl_override_target,omitempty"`

	// Custom Base URL relay settings for Anthropic OAuth and Setup Token providers.
	CustomBaseURLEnabled *bool   `json:"custom_base_url_enabled,omitempty"`
	CustomBaseURL        *string `json:"custom_base_url,omitempty"`

	// Quota limits for API key and Bedrock providers.
	QuotaLimit       *float64 `json:"quota_limit,omitempty"`
	QuotaUsed        *float64 `json:"quota_used,omitempty"`
	QuotaDailyLimit  *float64 `json:"quota_daily_limit,omitempty"`
	QuotaDailyUsed   *float64 `json:"quota_daily_used,omitempty"`
	QuotaWeeklyLimit *float64 `json:"quota_weekly_limit,omitempty"`
	QuotaWeeklyUsed  *float64 `json:"quota_weekly_used,omitempty"`

	// Fixed-time quota reset settings.
	QuotaDailyResetMode  *string `json:"quota_daily_reset_mode,omitempty"`
	QuotaDailyResetHour  *int    `json:"quota_daily_reset_hour,omitempty"`
	QuotaWeeklyResetMode *string `json:"quota_weekly_reset_mode,omitempty"`
	QuotaWeeklyResetDay  *int    `json:"quota_weekly_reset_day,omitempty"`
	QuotaWeeklyResetHour *int    `json:"quota_weekly_reset_hour,omitempty"`
	QuotaResetTimezone   *string `json:"quota_reset_timezone,omitempty"`
	QuotaDailyResetAt    *string `json:"quota_daily_reset_at,omitempty"`
	QuotaWeeklyResetAt   *string `json:"quota_weekly_reset_at,omitempty"`

	// Quota notification settings.
	QuotaNotifyDailyEnabled    *bool    `json:"quota_notify_daily_enabled,omitempty"`
	QuotaNotifyDailyThreshold  *float64 `json:"quota_notify_daily_threshold,omitempty"`
	QuotaNotifyWeeklyEnabled   *bool    `json:"quota_notify_weekly_enabled,omitempty"`
	QuotaNotifyWeeklyThreshold *float64 `json:"quota_notify_weekly_threshold,omitempty"`
	QuotaNotifyTotalEnabled    *bool    `json:"quota_notify_total_enabled,omitempty"`
	QuotaNotifyTotalThreshold  *float64 `json:"quota_notify_total_threshold,omitempty"`

	// Spark shadow relationship.
	ParentProviderID *int64 `json:"parent_provider_id,omitempty"`
	QuotaDimension   string `json:"quota_dimension,omitempty"`

	// Parent credentials and Extra fields populated for shadow providers.
	ParentEmail                 string `json:"parent_email,omitempty"`
	ParentPlanType              string `json:"parent_plan_type,omitempty"`
	ParentPrivacyMode           string `json:"parent_privacy_mode,omitempty"`
	ParentSubscriptionExpiresAt string `json:"parent_subscription_expires_at,omitempty"`
	ParentChatGPTAccountID      string `json:"parent_chatgpt_account_id,omitempty"`

	Proxy          *Proxy          `json:"proxy,omitempty"`
	ProviderGroups []ProviderGroup `json:"provider_groups,omitempty"`

	GroupIDs []int64  `json:"group_ids,omitempty"`
	Groups   []*Group `json:"groups,omitempty"`
}

type ProviderGroup struct {
	ProviderID int64     `json:"provider_id"`
	GroupID    int64     `json:"group_id"`
	CreatedAt  time.Time `json:"created_at"`

	Provider *Provider `json:"provider,omitempty"`
	Group    *Group    `json:"group,omitempty"`
}

type Group = routingdto.Group

type Proxy = proxydto.Proxy
