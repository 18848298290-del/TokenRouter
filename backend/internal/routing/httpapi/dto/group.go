package dto

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

type Group struct {
	LocalizationResolution *locale.Resolution                   `json:"localization_resolution,omitempty"`
	SearchTerms            []string                             `json:"search_terms,omitempty"`
	DisplayName            string                               `json:"display_name,omitempty"`
	ModelAttributes        map[string]modelcatalog.Presentation `json:"model_attributes,omitempty"`
	Models                 []string                             `json:"models"`
	ModelProtocols         map[string][]protocol.ProtocolID     `json:"model_protocols,omitempty"`
	ID                     int64                                `json:"id"`
	Name                   string                               `json:"name"`
	Description            string                               `json:"description"`
	DisplayBrand           string                               `json:"display_brand"`
	RateMultiplier         float64                              `json:"rate_multiplier"`
	Capacity               *GroupCapacity                       `json:"capacity,omitempty"`
	IsExclusive            bool                                 `json:"is_exclusive"`
	Status                 string                               `json:"status"`
	// SessionIsolationEnabled rejects designated sessions already assigned to another group.
	SessionIsolationEnabled bool `json:"session_isolation_enabled"`

	// Internal compatibility values for image permissions; omitted from JSON.
	AllowImageGeneration      bool `json:"-"`
	AllowBatchImageGeneration bool `json:"-"`

	// Claude Code client restriction and fallback.
	ClaudeCodeOnly  bool   `json:"claude_code_only"`
	FallbackGroupID *int64 `json:"fallback_group_id"`
	// Fallback group for invalid Anthropic requests.
	FallbackGroupIDOnInvalidRequest *int64 `json:"fallback_group_id_on_invalid_request"`
	// Preferred API key fallback when this group is unavailable.
	UnavailableFallbackGroupID *int64 `json:"unavailable_fallback_group_id"`

	// AllowedProtocols is the complete set of permitted client protocols and operations.
	AllowedProtocols     []protocol.ProtocolID                         `json:"allowed_protocols"`
	ProtocolFallbacks    map[protocol.ProtocolID][]protocol.ProtocolID `json:"protocol_fallbacks"`
	ResponsesImagePolicy string                                        `json:"responses_image_policy"`
	// AllowMessagesDispatch is a deprecated value derived from the protocol set.
	AllowMessagesDispatch bool `json:"-"`
	// Internal compatibility value for Live admission; omitted from JSON.
	AllowLive bool `json:"-"`

	// Provider authentication and privacy requirements.
	RequireOAuthOnly  bool `json:"require_oauth_only"`
	RequirePrivacySet bool `json:"require_privacy_set"`

	// RPMLimit replaces the user RPM limit when positive; zero imposes no group limit.
	RPMLimit int `json:"rpm_limit"`
	// MaxReasoningEffort caps requested reasoning effort; empty means no cap.
	MaxReasoningEffort string `json:"max_reasoning_effort"`
	// MaxReasoningEffortOverLimit selects downgrade (the default) or deny above the cap.
	MaxReasoningEffortOverLimit string `json:"max_reasoning_effort_over_limit"`
	// ReasoningEffortMappings rewrites effort with optional exact, prefix, or suffix model matching.
	ReasoningEffortMappings []routing.ReasoningEffortMapping `json:"reasoning_effort_mappings"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type GroupCapacity struct {
	ConcurrencyUsed int `json:"concurrency_used"`
	ConcurrencyMax  int `json:"concurrency_max"`
	SessionsUsed    int `json:"sessions_used"`
	SessionsMax     int `json:"sessions_max"`
	RPMUsed         int `json:"rpm_used"`
	RPMMax          int `json:"rpm_max"`
}

// AdminGroup contains administrator-only settings and internal provider information.
// User-facing endpoints must use Group to avoid exposing routing and provider internals.
type AdminGroup[A any] struct {
	Localization routing.GroupLocalization `json:"localization"`
	Group
	// ForceOpenAIFast is the administrator-visible legacy policy for applicable OpenAI execution.
	ForceOpenAIFast bool `json:"force_openai_fast"`
	// OpenAIFastPolicy stores the selected mutually exclusive acceleration policy.
	OpenAIFastPolicy string `json:"openai_fast_policy"`

	// SchedulerType selects the group scheduler in administrative responses.
	SchedulerType string `json:"scheduler_type"`
	// AdvancedSchedulerOverrides exposes administrative overrides; missing fields inherit gateway settings.
	AdvancedSchedulerOverrides routing.GroupAdvancedSchedulerOverrides `json:"advanced_scheduler_overrides"`
	RoutingPolicy              routing.GroupRoutingPolicy              `json:"routing_policy"`

	// Preferred provider IDs by request model pattern.
	ModelRouting        map[string][]int64 `json:"model_routing"`
	ModelRoutingEnabled bool               `json:"model_routing_enabled"`

	// Legacy Antigravity MCP XML setting retained for compatibility.
	MCPXMLInject bool `json:"mcp_xml_inject"`

	// Model mapping and optional custom catalog settings.
	DefaultMappedModel string                        `json:"default_mapped_model"`
	ModelsListConfig   routing.GroupModelsListConfig `json:"models_list_config"`
	// AvailabilityProbeConfig exposes active probe configuration to administrators.
	AvailabilityProbeConfig routing.GroupAvailabilityProbeConfig `json:"availability_probe_config"`

	// Legacy Antigravity model-family configuration.
	SupportedModelScopes     []string `json:"supported_model_scopes"`
	ProviderGroups           []A      `json:"provider_groups,omitempty"`
	ProviderCount            int64    `json:"provider_count,omitempty"`
	ActiveProviderCount      int64    `json:"active_provider_count,omitempty"`
	RateLimitedProviderCount int64    `json:"rate_limited_provider_count,omitempty"`

	// Group display order.
	SortOrder int `json:"sort_order"`
}
