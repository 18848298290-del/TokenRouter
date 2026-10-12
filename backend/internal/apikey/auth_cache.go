package apikey

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
)

// APIKeyAuthSnapshot caches the key data needed for authentication and request admission.
type APIKeyAuthSnapshot struct {
	Version  int    `json:"version"`
	APIKeyID int64  `json:"api_key_id"`
	UserID   int64  `json:"user_id"`
	TeamID   *int64 `json:"team_id,omitempty"`
	// TeamOwnerDisabled stores the team owner's independent key lock.
	TeamOwnerDisabled bool                               `json:"team_owner_disabled"`
	CreatedAt         time.Time                          `json:"created_at"`
	GroupID           *int64                             `json:"group_id,omitempty"`
	IsComposite       bool                               `json:"is_composite"`
	CompositeGroups   []APIKeyAuthCompositeGroupSnapshot `json:"composite_groups,omitempty"`
	Name              string                             `json:"name"`
	Status            string                             `json:"status"`
	// FastModePolicy supplies the key policy to gateway requests.
	FastModePolicy string `json:"fast_mode_policy"`
	// BillingMode and PreferredSubscriptionID supply the request's funding selection.
	BillingMode             string `json:"billing_mode"`
	PreferredSubscriptionID *int64 `json:"preferred_subscription_id,omitempty"`
	// ModelMapping supplies model redirects to request processing.
	ModelMapping   map[string]string        `json:"model_mapping,omitempty"`
	IPWhitelist    []string                 `json:"ip_whitelist,omitempty"`
	IPBlacklist    []string                 `json:"ip_blacklist,omitempty"`
	User           APIKeyAuthUserSnapshot   `json:"user"`
	ActorUser      *APIKeyAuthActorSnapshot `json:"actor_user,omitempty"`
	Team           *APIKeyAuthTeamSnapshot  `json:"team,omitempty"`
	TeamMembership *TeamMembership          `json:"team_membership,omitempty"`
	Group          *APIKeyAuthGroupSnapshot `json:"group,omitempty"`

	// Key quota in USD.
	Quota     float64 `json:"quota"`      // Quota limit in USD (0 = unlimited)
	QuotaUsed float64 `json:"quota_used"` // Used quota amount

	// Key expiration.
	ExpiresAt *time.Time `json:"expires_at,omitempty"` // Expiration time (nil = never expires)

	// Spending limits are cached here; current window usage is read separately during admission.
	// ConcurrencyLimit and RPMLimit use zero for unlimited requests.
	ConcurrencyLimit int     `json:"concurrency_limit"`
	RPMLimit         int     `json:"rpm_limit"`
	RateLimit5h      float64 `json:"rate_limit_5h"`
	RateLimit1d      float64 `json:"rate_limit_1d"`
	RateLimit7d      float64 `json:"rate_limit_7d"`
	// FallbackWhenGroupUnavailable permits request fallback from an unavailable group.
	FallbackWhenGroupUnavailable bool `json:"fallback_when_group_unavailable"`
}

// APIKeyAuthCompositeGroupSnapshot caches a composite prefix and its authentication group data.
type APIKeyAuthCompositeGroupSnapshot struct {
	ID                   int64                    `json:"id"`
	GroupID              int64                    `json:"group_id"`
	Prefix               string                   `json:"prefix"`
	NormalizedPrefix     string                   `json:"normalized_prefix"`
	SortOrder            int                      `json:"sort_order"`
	UserGroupRPMOverride *int                     `json:"user_group_rpm_override,omitempty"`
	Group                *APIKeyAuthGroupSnapshot `json:"group,omitempty"`
}

// APIKeyAuthActorSnapshot caches the member's identity and account status.
type APIKeyAuthActorSnapshot struct {
	ID       int64  `json:"id"`
	Status   string `json:"status"`
	Email    string `json:"email"`
	Username string `json:"username"`
}

// APIKeyAuthTeamSnapshot caches team identity and status for authentication.
type APIKeyAuthTeamSnapshot struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// APIKeyAuthUserSnapshot caches the payer's identity, permissions, and admission data.
type APIKeyAuthUserSnapshot struct {
	ID            int64   `json:"id"`
	Status        string  `json:"status"`
	Role          string  `json:"role"`
	Balance       float64 `json:"balance"`
	Concurrency   int     `json:"concurrency"`
	AllowedGroups []int64 `json:"allowed_groups,omitempty"`

	// Balance notification fields (required for CheckBalanceAfterDeduction)
	Email                      string             `json:"email"`
	Username                   string             `json:"username"`
	BalanceNotifyEnabled       bool               `json:"balance_notify_enabled"`
	BalanceNotifyThresholdType string             `json:"balance_notify_threshold_type"`
	BalanceNotifyThreshold     *float64           `json:"balance_notify_threshold,omitempty"`
	BalanceNotifyExtraEmails   []NotifyEmailEntry `json:"balance_notify_extra_emails,omitempty"`
	TotalRecharged             float64            `json:"total_recharged"`

	// RPMLimit is the user request limit per minute; zero means unlimited. More specific limits take precedence.
	RPMLimit int `json:"rpm_limit"`

	// UserGroupRPMOverride is the request limit for this key's user and group.
	// Nil defers to group or user limits; zero means unlimited; positive values set the limit.
	UserGroupRPMOverride *int `json:"user_group_rpm_override,omitempty"`

	// DisabledPublicGroups identifies public groups denied to this user, including on existing keys.
	DisabledPublicGroups []int64 `json:"disabled_public_groups,omitempty"`
}

// APIKeyAuthGroupSnapshot caches group policies used during request processing.
type APIKeyAuthGroupSnapshot struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`

	SchedulerType GroupSchedulerType `json:"scheduler_type"`
	// AdvancedSchedulerOverrides supplies group configuration to request scheduling.
	AdvancedSchedulerOverrides GroupAdvancedSchedulerOverrides `json:"advanced_scheduler_overrides"`
	IsExclusive                bool                            `json:"is_exclusive"`
	Status                     string                          `json:"status"`
	RateMultiplier             float64                         `json:"rate_multiplier"`
	SessionIsolationEnabled    bool                            `json:"session_isolation_enabled"`
	AllowImageGeneration       bool                            `json:"allow_image_generation"`
	AllowBatchImageGeneration  bool                            `json:"allow_batch_image_generation"`

	RoutingPolicy routing.GroupRoutingPolicy `json:"routing_policy"`

	ClaudeCodeOnly                  bool   `json:"claude_code_only"`
	FallbackGroupID                 *int64 `json:"fallback_group_id,omitempty"`
	FallbackGroupIDOnInvalidRequest *int64 `json:"fallback_group_id_on_invalid_request,omitempty"`
	UnavailableFallbackGroupID      *int64 `json:"unavailable_fallback_group_id,omitempty"`

	// ModelRouting supplies model-specific provider candidates to gateway selection.
	// An empty map leaves provider selection to the group's other routing rules.
	ModelRouting        map[string][]int64 `json:"model_routing,omitempty"`
	ModelRoutingEnabled bool               `json:"model_routing_enabled"`
	MCPXMLInject        bool               `json:"mcp_xml_inject"`

	// Supported model families for Antigravity providers.
	SupportedModelScopes []string `json:"supported_model_scopes,omitempty"`

	// AllowedProtocols is serialized even when empty so the snapshot retains protocol restrictions.
	AllowedProtocols     []protocol.ProtocolID                         `json:"allowed_protocols"`
	ProtocolFallbacks    map[protocol.ProtocolID][]protocol.ProtocolID `json:"protocol_fallbacks"`
	ResponsesImagePolicy string                                        `json:"responses_image_policy"`
	AllowLive            bool                                          `json:"allow_live"`
	// ForceOpenAIFast caches the legacy group Fast flag for request processing.
	ForceOpenAIFast bool `json:"force_openai_fast"`
	// OpenAIFastPolicy stores the administrator's selected acceleration policy.
	OpenAIFastPolicy string `json:"openai_fast_policy"`

	DefaultMappedModel string                `json:"default_mapped_model,omitempty"`
	ModelsListConfig   GroupModelsListConfig `json:"models_list_config,omitempty"`

	// RPMLimit is the group request limit per minute; zero means unlimited.
	RPMLimit int `json:"rpm_limit"`

	// MaxReasoningEffort caps OpenAI and Anthropic reasoning effort; empty means unlimited.
	MaxReasoningEffort string `json:"max_reasoning_effort,omitempty"`
	// MaxReasoningEffortOverLimit selects downgrade (the default) or deny when the cap is exceeded.
	MaxReasoningEffortOverLimit string `json:"max_reasoning_effort_over_limit,omitempty"`
	// ReasoningEffortMappings rewrites requested effort before applying the cap.
	ReasoningEffortMappings []ReasoningEffortMapping `json:"reasoning_effort_mappings"`
}

// APIKeyAuthCacheEntry stores an authentication snapshot or a negative lookup result.
type APIKeyAuthCacheEntry struct {
	NotFound bool                `json:"not_found"`
	Snapshot *APIKeyAuthSnapshot `json:"snapshot,omitempty"`
}

// GroupSchedulerType and the configuration aliases use routing's group types.
type (
	GroupSchedulerType              = accessview.GroupSchedulerType
	GroupAdvancedSchedulerOverrides = accessview.GroupAdvancedSchedulerOverrides
	GroupModelsListConfig           = accessview.GroupModelsListConfig

	ReasoningEffortMapping = routing.ReasoningEffortMapping
)
