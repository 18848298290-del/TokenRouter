package dto

import (
	"time"

	proxydto "github.com/TokenFlux/TokenRouter/internal/egress/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	routingdto "github.com/TokenFlux/TokenRouter/internal/routing/httpapi/dto"
)

// Provider administration displays configuration together with stored health
// and scheduling windows. Sensitive credential values are represented by
// presence flags, and Ollama-managed Extra becomes a separate eligible usage
// view so the browser can inspect configuration without receiving those managed
// storage values. The mapper determines optional fields from platform and
// effective getter rules.
//
// Nil pointers and omitted JSON fields carry different meanings from explicit
// false or zero. Cost, session, RPM, TLS, cache and quota controls appear under
// their own eligibility predicates; some effective defaults are displayed even
// when a neighboring limit is disabled. Loaded relationships are added by the
// full mapper, while parent account fields are enriched separately for
// credential shadows.

// Provider is the redacted administrator view of provider configuration, stored
// health and relationships.
type Provider struct {
	ID       int64   `json:"id"`       // TokenRouter provider identifier
	Name     string  `json:"name"`     // provider display name
	Notes    *string `json:"notes"`    // administrator description text
	Platform string  `json:"platform"` // upstream platform identifier
	Type     string  `json:"type"`     // provider authentication kind
	// Credentials and CredentialsStatus separate retained configuration from known
	// sensitive values. The mapper removes classifier-selected keys and reports
	// configured secret presence through has_<key> flags; unknown keys remain
	// visible.
	Credentials       map[string]any  `json:"credentials"`                  // redacted credential configuration values
	CredentialsStatus map[string]bool `json:"credentials_status,omitempty"` // configured sensitive-key presence flags
	// Extra omits Ollama-managed session, auto-refresh and snapshot keys. Upstream
	// usage configuration is reduced to its permitted display fields before this
	// object reaches JSON encoding.
	Extra map[string]any `json:"extra"` // redacted provider configuration values
	// OllamaCloudUsage is populated only when the cloned provider passes usage
	// eligibility. Shared-account observations come from usage resolution before
	// mapping; RuntimePresenter subsequently adds the service's encryption-key
	// configuration flag to this response state.
	// eligible Ollama usage display state
	OllamaCloudUsage *provider.OllamaCloudUsageState `json:"ollama_cloud_usage,omitempty"`
	// selected egress proxy identifier
	ProxyID *int64 `json:"proxy_id"`
	// pre-fallback proxy identifier
	ProxyFallbackOriginID *int64 `json:"proxy_fallback_origin_id"`
	// pre-fallback proxy display name
	ProxyFallbackOriginName *string `json:"proxy_fallback_origin_name,omitempty"`
	// configured provider request capacity
	Concurrency int `json:"concurrency"`
	// scheduler capacity override value
	LoadFactor *int `json:"load_factor,omitempty"`
	// scheduler priority value
	Priority int `json:"priority"`
	// effective provider billing multiplier
	RateMultiplier float64 `json:"rate_multiplier"`
	// stored provider health state
	Status string `json:"status"`
	// stored provider error text
	ErrorMessage string `json:"error_message"`
	// last provider activity timestamp
	LastUsedAt *time.Time `json:"last_used_at"`
	ExpiresAt  *int64     `json:"expires_at"` // Unix seconds
	// pause on expiry
	AutoPauseOnExpired bool `json:"auto_pause_on_expired"`
	// provider creation timestamp
	CreatedAt time.Time `json:"created_at"`
	// provider last-update timestamp
	UpdatedAt time.Time `json:"updated_at"`

	Schedulable bool `json:"schedulable"` // administrator scheduling eligibility switch

	RateLimitedAt    *time.Time `json:"rate_limited_at"`     // provider limit observation timestamp
	RateLimitResetAt *time.Time `json:"rate_limit_reset_at"` // provider limit reset timestamp
	OverloadUntil    *time.Time `json:"overload_until"`      // upstream overload avoidance deadline

	TempUnschedulableUntil  *time.Time `json:"temp_unschedulable_until"`  // temporary scheduling avoidance deadline
	TempUnschedulableReason string     `json:"temp_unschedulable_reason"` // temporary scheduling avoidance reason

	// QuotaAutoPaused reports whether an OpenAI provider is paused by a 5h or 7d quota threshold.
	QuotaAutoPaused bool `json:"quota_auto_paused"` // observed OpenAI quota-pause display

	SessionWindowStart  *time.Time `json:"session_window_start"`  // recorded usage window start
	SessionWindowEnd    *time.Time `json:"session_window_end"`    // recorded usage window end
	SessionWindowStatus string     `json:"session_window_status"` // recorded usage window state

	// WindowCostLimit and WindowCostStickyReserve are Anthropic OAuth or Setup
	// Token display controls. The mapper includes each positive getter result
	// independently, so a reserve default can appear with no limit.
	WindowCostLimit         *float64 `json:"window_cost_limit,omitempty"`          // USD
	WindowCostStickyReserve *float64 `json:"window_cost_sticky_reserve,omitempty"` // USD

	// MaxSessions and SessionIdleTimeoutMin are Anthropic OAuth or Setup Token
	// controls populated independently from positive getter results. The default
	// idle timeout can appear even when the session limit is disabled.
	MaxSessions           *int `json:"max_sessions,omitempty"`                 // configured concurrent session limit
	SessionIdleTimeoutMin *int `json:"session_idle_timeout_minutes,omitempty"` // minutes

	// BaseRPM, RPMStrategy and RPMStickyBuffer appear together for eligible
	// Anthropic providers with a positive base RPM. The strategy and sticky buffer
	// come from RuntimeConfig's effective getters.
	BaseRPM         *int    `json:"base_rpm,omitempty"`          // base requests per minute
	RPMStrategy     *string `json:"rpm_strategy,omitempty"`      // request-rate admission strategy label
	RPMStickyBuffer *int    `json:"rpm_sticky_buffer,omitempty"` // additional sticky request capacity
	// UserMsgQueueMode appears for eligible Anthropic providers when RuntimeConfig
	// selects a nonempty mode. Unsupported settings resolve to the global policy
	// and leave this response field absent.
	UserMsgQueueMode *string `json:"user_msg_queue_mode,omitempty"` // effective user-message queue selector

	// EnableTLSFingerprint, TLSFingerprintProfileID and TLSFingerprintRouterID are
	// populated for supported provider types. Enablement requires true, while
	// nonzero profile and router selectors are exposed independently of that flag.
	EnableTLSFingerprint    *bool  `json:"enable_tls_fingerprint,omitempty"`     // configured TLS fingerprint enablement
	TLSFingerprintProfileID *int64 `json:"tls_fingerprint_profile_id,omitempty"` // configured TLS profile selector
	TLSFingerprintRouterID  *int64 `json:"tls_fingerprint_router_id,omitempty"`  // configured TLS router selector

	// OpenAIOAuthClientPolicy is populated for OpenAI OAuth providers from the
	// normalized effective getter. Other provider types leave this optional
	// display field absent.
	// normalized OpenAI client-policy selector
	OpenAIOAuthClientPolicy *string `json:"openai_oauth_client_policy,omitempty"`

	// EnableSessionIDMasking appears when an eligible Anthropic provider enables
	// session-ID masking. The record's masking predicate supplies eligibility
	// before this display flag is set.
	EnableSessionIDMasking *bool `json:"session_id_masking_enabled,omitempty"` // configured session identifier masking

	// CacheTTLOverrideEnabled and CacheTTLOverrideTarget appear together when the
	// eligible Anthropic provider enables its cache billing override. The target
	// comes from the effective record getter, including its default.
	CacheTTLOverrideEnabled *bool   `json:"cache_ttl_override_enabled,omitempty"` // configured cache billing override
	CacheTTLOverrideTarget  *string `json:"cache_ttl_override_target,omitempty"`  // selected cache billing TTL category

	// CustomBaseURLEnabled appears when an eligible Anthropic provider enables
	// relay configuration. CustomBaseURL is additionally populated only when the
	// effective URL getter returns nonempty text.
	CustomBaseURLEnabled *bool   `json:"custom_base_url_enabled,omitempty"` // configured relay URL enablement
	CustomBaseURL        *string `json:"custom_base_url,omitempty"`         // configured upstream relay URL

	// QuotaLimit, QuotaUsed and the daily and weekly quota fields are populated
	// for API Key or Bedrock providers. Each positive limit admits its
	// corresponding usage value into the response.
	QuotaLimit      *float64 `json:"quota_limit,omitempty"`       // total provider cost limit
	QuotaUsed       *float64 `json:"quota_used,omitempty"`        // stored total provider consumption
	QuotaDailyLimit *float64 `json:"quota_daily_limit,omitempty"` // daily provider cost limit
	// QuotaDailyUsed displays zero when the record's daily period is expired. This
	// mapper performs no quota storage reset and leaves persisted usage
	// maintenance to its owning use case.
	QuotaDailyUsed   *float64 `json:"quota_daily_used,omitempty"`   // displayed daily provider consumption
	QuotaWeeklyLimit *float64 `json:"quota_weekly_limit,omitempty"` // weekly provider cost limit
	// QuotaWeeklyUsed displays zero when the record's weekly period is expired.
	// This mapper performs no quota storage reset and leaves persisted usage
	// maintenance to its owning use case.
	QuotaWeeklyUsed *float64 `json:"quota_weekly_used,omitempty"` // displayed weekly provider consumption

	// QuotaDailyResetMode and QuotaDailyResetHour appear for fixed daily resets;
	// QuotaWeeklyResetMode, QuotaWeeklyResetDay and QuotaWeeklyResetHour appear
	// for fixed weekly resets. Either fixed mode admits QuotaResetTimezone.
	QuotaDailyResetMode  *string `json:"quota_daily_reset_mode,omitempty"`  // fixed daily reset selector
	QuotaDailyResetHour  *int    `json:"quota_daily_reset_hour,omitempty"`  // daily reset clock hour
	QuotaWeeklyResetMode *string `json:"quota_weekly_reset_mode,omitempty"` // fixed weekly reset selector
	QuotaWeeklyResetDay  *int    `json:"quota_weekly_reset_day,omitempty"`  // weekly reset weekday selector
	QuotaWeeklyResetHour *int    `json:"quota_weekly_reset_hour,omitempty"` // weekly reset clock hour
	QuotaResetTimezone   *string `json:"quota_reset_timezone,omitempty"`    // quota reset timezone name
	QuotaDailyResetAt    *string `json:"quota_daily_reset_at,omitempty"`    // stored daily reset timestamp text
	QuotaWeeklyResetAt   *string `json:"quota_weekly_reset_at,omitempty"`   // stored weekly reset timestamp text

	// QuotaNotifyDailyEnabled, QuotaNotifyWeeklyEnabled and
	// QuotaNotifyTotalEnabled appear only when their getters report true. Their
	// three threshold fields are independently included for positive values,
	// including disabled notification configurations.
	// daily notification enablement flag
	QuotaNotifyDailyEnabled *bool `json:"quota_notify_daily_enabled,omitempty"`
	// daily notification threshold value
	QuotaNotifyDailyThreshold *float64 `json:"quota_notify_daily_threshold,omitempty"`
	// weekly notification enablement flag
	QuotaNotifyWeeklyEnabled *bool `json:"quota_notify_weekly_enabled,omitempty"`
	// weekly notification threshold value
	QuotaNotifyWeeklyThreshold *float64 `json:"quota_notify_weekly_threshold,omitempty"`
	// total notification enablement flag
	QuotaNotifyTotalEnabled *bool `json:"quota_notify_total_enabled,omitempty"`
	// total notification threshold value
	QuotaNotifyTotalThreshold *float64 `json:"quota_notify_total_threshold,omitempty"`

	// ParentProviderID identifies a credential shadow's parent. RuntimePresenter
	// later resolves matching parents to fill the parent account and privacy
	// display fields; this mapper initially leaves those fields empty.
	ParentProviderID *int64 `json:"parent_provider_id,omitempty"` // credential parent provider identifier
	QuotaDimension   string `json:"quota_dimension,omitempty"`    // shadow quota dimension selector

	// ParentEmail, ParentPlanType, ParentPrivacyMode, ParentSubscriptionExpiresAt
	// and ParentChatGPTAccountID are filled together by RuntimePresenter from a
	// successfully loaded parent. Missing parents and failed parent queries leave
	// these display fields unchanged.
	ParentEmail                 string `json:"parent_email,omitempty"`                   // parent account email hint
	ParentPlanType              string `json:"parent_plan_type,omitempty"`               // parent subscription plan label
	ParentPrivacyMode           string `json:"parent_privacy_mode,omitempty"`            // parent privacy observation mode
	ParentSubscriptionExpiresAt string `json:"parent_subscription_expires_at,omitempty"` // parent subscription expiry text
	// parent upstream account identifier
	ParentChatGPTAccountID string `json:"parent_chatgpt_account_id,omitempty"`

	Proxy *Proxy `json:"proxy,omitempty"` // selected proxy display details
	// ProviderGroups and Groups are populated by ProviderFromRecord from loaded
	// associations. ProviderFromRecordShallow supplies GroupIDs but leaves these
	// nested relationship objects absent.
	ProviderGroups []ProviderGroup `json:"provider_groups,omitempty"` // provider group association views

	GroupIDs []int64  `json:"group_ids,omitempty"` // associated routing group identifiers
	Groups   []*Group `json:"groups,omitempty"`    // associated routing group views
}

// ProviderGroup is a loaded provider-to-routing-group association for
// administrator JSON presentation.
type ProviderGroup struct {
	ProviderID int64     `json:"provider_id"` // associated TokenRouter provider identifier
	GroupID    int64     `json:"group_id"`    // associated routing group identifier
	CreatedAt  time.Time `json:"created_at"`  // association creation timestamp

	// Provider is mapped shallowly when a membership includes a provider. This
	// limits association recursion while retaining the provider's redacted
	// configuration display.
	Provider *Provider `json:"provider,omitempty"` // optional shallow provider view
	Group    *Group    `json:"group,omitempty"`    // optional routing group view
}

// Group reuses the routing administrator DTO for loaded provider associations.
type Group = routingdto.Group

// Proxy reuses the egress administrator DTO for a provider's selected proxy.
type Proxy = proxydto.Proxy
