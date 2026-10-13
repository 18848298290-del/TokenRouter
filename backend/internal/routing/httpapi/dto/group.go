package dto

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// User-facing group JSON exposes protocol admission and product limits while
// administrator responses add scheduling policy and provider counts. Image
// and Live compatibility flags stay internal, so JSON clients read the
// unified protocol fields for those entry permissions.

// Group contains the product settings and model choices exposed to users.
type Group struct {
	// LocalizationResolution describes the language used for display text when
	// localization is requested; mapping without a language leaves this value
	// absent.
	LocalizationResolution *locale.Resolution                   `json:"localization_resolution,omitempty"` // resolved display language metadata
	SearchTerms            []string                             `json:"search_terms,omitempty"`            // localized display search texts
	DisplayName            string                               `json:"display_name,omitempty"`            // resolved group display name
	ModelAttributes        map[string]modelcatalog.Presentation `json:"model_attributes,omitempty"`        // request-model display attribute map
	Models                 []string                             `json:"models"`                            // requestable model ID list
	ModelProtocols         map[string][]protocol.ProtocolID     `json:"model_protocols,omitempty"`         // request-model client protocol choices
	ID                     int64                                `json:"id"`                                // stored group identifier
	Name                   string                               `json:"name"`                              // persisted group business name
	Description            string                               `json:"description"`                       // group display description
	DisplayBrand           string                               `json:"display_brand"`                     // configured display brand
	RateMultiplier         float64                              `json:"rate_multiplier"`                   // group user price factor
	// Capacity is attached by capacity-aware group responses; absent
	// observations leave it nil and omit the JSON member.
	Capacity    *GroupCapacity `json:"capacity,omitempty"` // optional current group capacity
	IsExclusive bool           `json:"is_exclusive"`       // exclusive group membership flag
	Status      string         `json:"status"`             // stored group activation state
	// SessionIsolationEnabled rejects a designated session when its existing
	// assignment belongs to another group.
	SessionIsolationEnabled bool `json:"session_isolation_enabled"` // cross-group session isolation flag

	// AllowImageGeneration and AllowBatchImageGeneration mirror image admission
	// derived from protocol settings for internal compatibility consumers. Both
	// fields are excluded from serialized Group responses.
	AllowImageGeneration      bool `json:"-"` // derived image permission mirror
	AllowBatchImageGeneration bool `json:"-"` // derived image-batch permission mirror

	// ClaudeCodeOnly and FallbackGroupID describe the Claude Code restriction
	// and its alternate group for rejected clients. A nil FallbackGroupID leaves
	// that alternate group unconfigured.
	ClaudeCodeOnly  bool   `json:"claude_code_only"`  // Claude Code client restriction
	FallbackGroupID *int64 `json:"fallback_group_id"` // client restriction fallback group
	// FallbackGroupIDOnInvalidRequest supplies the alternate group for
	// qualifying invalid Anthropic requests; nil leaves that recovery selection
	// unconfigured.
	FallbackGroupIDOnInvalidRequest *int64 `json:"fallback_group_id_on_invalid_request"` // invalid Anthropic request fallback
	// UnavailableFallbackGroupID supplies the preferred API-key alternate when
	// this group is unavailable; nil leaves that preference unconfigured.
	UnavailableFallbackGroupID *int64 `json:"unavailable_fallback_group_id"` // unavailable group fallback preference

	// AllowedProtocols lists permitted client entries; an empty set disables new
	// calls through every entry controlled by the group.
	AllowedProtocols []protocol.ProtocolID `json:"allowed_protocols"` // permitted client protocol set
	// ProtocolFallbacks uses an absent source for automatic conversion, an empty
	// target slice for native-only routing, and an ordered nonempty slice for
	// restricted conversion choices.
	ProtocolFallbacks    map[protocol.ProtocolID][]protocol.ProtocolID `json:"protocol_fallbacks"`     // ordered per-entry conversion restrictions
	ResponsesImagePolicy string                                        `json:"responses_image_policy"` // Responses image tool policy
	// AllowMessagesDispatch reflects membership of Messages in AllowedProtocols
	// for internal compatibility consumers and is excluded from serialized Group
	// responses.
	AllowMessagesDispatch bool `json:"-"` // derived Messages permission mirror
	// AllowLive mirrors the normalized Live protocol permission for internal
	// consumers; serialized Group responses expose that admission through
	// AllowedProtocols instead.
	AllowLive bool `json:"-"` // derived Live permission mirror

	// RequireOAuthOnly admits runtime candidates with OAuth or Setup Token
	// credentials; other credential types fail the group qualification check.
	RequireOAuthOnly bool `json:"require_oauth_only"` // OAuth or Setup Token requirement
	// RequirePrivacySet checks saved privacy readiness for OpenAI and
	// Antigravity providers. Other platforms pass Record.IsPrivacySet without a
	// saved privacy requirement.
	RequirePrivacySet bool `json:"require_privacy_set"` // provider privacy readiness requirement

	// RPMLimit supplies each user's allowance in this group when positive.
	// A user-group override takes precedence; nonpositive values disable this
	// group gate, while scheduler.RPMAdmission still checks a positive user-wide
	// allowance.
	RPMLimit int `json:"rpm_limit"` // group requests per minute
	// MaxReasoningEffort caps effort from supported OpenAI or Anthropic request
	// fields; an empty value leaves requested effort uncapped by this group.
	MaxReasoningEffort string `json:"max_reasoning_effort"` // requested reasoning effort ceiling
	// MaxReasoningEffortOverLimit selects downgrade or deny when requested
	// effort exceeds the cap; downgrade is the default action.
	MaxReasoningEffortOverLimit string `json:"max_reasoning_effort_over_limit"` // effort ceiling excess action
	// ReasoningEffortMappings rewrites requested effort before the cap; rules
	// can apply generally or match exact, prefix, and suffix model names.
	ReasoningEffortMappings []routing.ReasoningEffortMapping `json:"reasoning_effort_mappings"` // model-scoped effort rewrite rules

	CreatedAt time.Time `json:"created_at"` // stored group creation time
	UpdatedAt time.Time `json:"updated_at"` // stored group update time
}

// GroupCapacity reports aggregated concurrency, session, and request-rate
// observations for a group.
type GroupCapacity struct {
	ConcurrencyUsed int `json:"concurrency_used"` // occupied provider concurrency slots
	ConcurrencyMax  int `json:"concurrency_max"`  // aggregate provider concurrency capacity
	SessionsUsed    int `json:"sessions_used"`    // occupied provider session slots
	SessionsMax     int `json:"sessions_max"`     // aggregate provider session capacity
	RPMUsed         int `json:"rpm_used"`         // observed requests per minute
	RPMMax          int `json:"rpm_max"`          // aggregate provider RPM capacity
}

// AdminGroup adds editable routing settings and provider details to
// user-visible fields. User-facing handlers return Group because
// administrator settings include internal provider selection and scheduling
// information.
type AdminGroup[A any] struct {
	Localization routing.GroupLocalization `json:"localization"` // editable localized group content
	Group                                  // user-visible shared group fields
	// ForceOpenAIFast mirrors the legacy forced-priority setting; the effective
	// OpenAIFastPolicy expresses the complete administrator acceleration
	// selection.
	ForceOpenAIFast  bool   `json:"force_openai_fast"`  // legacy forced-priority policy mirror
	OpenAIFastPolicy string `json:"openai_fast_policy"` // effective OpenAI acceleration policy

	SchedulerType string `json:"scheduler_type"` // provider scheduler selection
	// AdvancedSchedulerOverrides contains optional advanced scheduling values;
	// fields absent from the configuration inherit the corresponding gateway
	// settings.
	AdvancedSchedulerOverrides routing.GroupAdvancedSchedulerOverrides `json:"advanced_scheduler_overrides"` // per-group advanced scheduler settings
	RoutingPolicy              routing.GroupRoutingPolicy              `json:"routing_policy"`               // group model and feature rules

	// ModelRouting and ModelRoutingEnabled define optional preferred-provider
	// selection. Enabled rules map request model patterns to ordered provider
	// IDs; exact and trailing-wildcard rules are evaluated by group routing.
	ModelRouting        map[string][]int64 `json:"model_routing"`         // model-pattern preferred provider lists
	ModelRoutingEnabled bool               `json:"model_routing_enabled"` // preferred provider routing switch

	MCPXMLInject bool `json:"mcp_xml_inject"` // legacy Antigravity XML setting

	DefaultMappedModel      string                               `json:"default_mapped_model"`      // configured default mapped model
	ModelsListConfig        routing.GroupModelsListConfig        `json:"models_list_config"`        // custom request-model catalog settings
	AvailabilityProbeConfig routing.GroupAvailabilityProbeConfig `json:"availability_probe_config"` // active availability probe settings

	SupportedModelScopes     []string `json:"supported_model_scopes"`                // legacy Antigravity family metadata
	ProviderGroups           []A      `json:"provider_groups,omitempty"`             // optional linked provider details
	ProviderCount            int64    `json:"provider_count,omitempty"`              // total linked provider count
	ActiveProviderCount      int64    `json:"active_provider_count,omitempty"`       // active linked provider count
	RateLimitedProviderCount int64    `json:"rate_limited_provider_count,omitempty"` // rate-limited linked provider count

	SortOrder int `json:"sort_order"` // manual group display order
}
