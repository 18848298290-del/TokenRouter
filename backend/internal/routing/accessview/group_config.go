package accessview

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// GroupConfig contains group settings read by provider and other modules.
type GroupConfig struct {
	Localization GroupLocalization
	// DisplayName is resolved for the request; Name remains the persisted business name.
	DisplayName   string
	RoutingPolicy GroupRoutingPolicy
	ID            int64
	Name          string
	Description   string
	// Models is the requestable catalog populated for console queries.
	Models         []string
	ModelProtocols map[string][]protocol.ProtocolID

	// SchedulerType selects basic or advanced provider scheduling for the group.
	SchedulerType GroupSchedulerType
	// AdvancedSchedulerOverrides applies to advanced groups; unset fields inherit gateway settings.
	AdvancedSchedulerOverrides GroupAdvancedSchedulerOverrides
	DisplayBrand               string
	RateMultiplier             float64
	IsExclusive                bool
	Status                     string
	Hydrated                   bool // Loaded from trusted storage.
	// DuplicateOperationID is persisted internally to recover a committed group duplication.
	DuplicateOperationID string

	// SessionIsolationEnabled rejects designated sessions already assigned to another group.
	SessionIsolationEnabled bool

	// Image-generation permissions are independent of the shared pricing configuration.
	AllowImageGeneration      bool
	AllowBatchImageGeneration bool

	// Claude Code client restriction and its fallback group.
	ClaudeCodeOnly  bool
	FallbackGroupID *int64
	// Fallback group for invalid Anthropic requests.
	FallbackGroupIDOnInvalidRequest *int64
	// UnavailableFallbackGroupID is the preferred API key fallback when this group is disabled.
	UnavailableFallbackGroupID *int64

	// ModelRouting assigns preferred providers to request model patterns.
	// Keys are exact IDs or trailing-wildcard patterns such as "claude-opus-*".
	// Values are ordered provider ID lists.
	ModelRouting        map[string][]int64
	ModelRoutingEnabled bool

	// Legacy Antigravity MCP XML setting retained for compatibility.
	MCPXMLInject bool

	// Legacy Antigravity model-family selection; current routing does not filter by this field.
	// Allowed values: claude, gemini_text, gemini_image.
	SupportedModelScopes []string

	// Display order of the group.
	SortOrder int

	// AllowedProtocols lists permitted client protocols and operations. An empty set disables all entries.
	AllowedProtocols     []protocol.ProtocolID
	ProtocolFallbacks    map[protocol.ProtocolID][]protocol.ProtocolID
	ResponsesImagePolicy string
	// AllowMessagesDispatch is a deprecated persisted compatibility value derived from AllowedProtocols.
	AllowMessagesDispatch bool
	AllowLive             bool
	// ForceOpenAIFast requests service_tier=priority for OpenAI execution.
	ForceOpenAIFast bool
	// OpenAIFastPolicy stores the selected mutually exclusive acceleration policy.
	OpenAIFastPolicy string

	RequireOAuthOnly   bool // Restricts provider association to permitted non-API-key authentication types.
	RequirePrivacySet  bool // Requires providers to have privacy settings successfully applied before scheduling.
	DefaultMappedModel string
	ModelsListConfig   GroupModelsListConfig
	// AvailabilityProbeConfig controls active availability probes for the group.
	AvailabilityProbeConfig GroupAvailabilityProbeConfig

	// RPMLimit is the group request limit per minute; zero imposes no group limit.
	// A positive limit replaces the user RPM limit, and a user-group rpm_override can override it.
	RPMLimit int

	// MaxReasoningEffort caps effective OpenAI and Anthropic reasoning effort.
	// An empty value imposes no cap. Anthropic does not support minimal.
	MaxReasoningEffort string
	// MaxReasoningEffortOverLimit selects downgrade or rejection when the requested effort exceeds the cap.
	MaxReasoningEffortOverLimit string
	// ReasoningEffortMappings rewrites requested effort before applying the cap.
	ReasoningEffortMappings []ReasoningEffortMapping

	CreatedAt                time.Time
	UpdatedAt                time.Time
	ProviderCount            int64
	ActiveProviderCount      int64
	RateLimitedProviderCount int64
}

// GroupAdvancedSchedulerOverrides uses the policy package's group scheduling settings.
type GroupAdvancedSchedulerOverrides = policy.GroupAdvancedSchedulerOverrides

// GroupAvailabilityProbeConfig controls active probes for a group.
// It is stored on the group so model, prompt, and frequency can be configured per group.
type GroupAvailabilityProbeConfig struct {
	Enabled         bool   `json:"enabled"`
	IntervalMinutes int    `json:"interval_minutes,omitempty"`
	ModelID         string `json:"model_id,omitempty"`
	Prompt          string `json:"prompt,omitempty"`
	TimeoutSeconds  int    `json:"timeout_seconds,omitempty"`
	// MaxRetries counts retries after the first failed attempt; nil selects the server default.
	MaxRetries *int   `json:"max_retries,omitempty"`
	UserAgent  string `json:"user_agent,omitempty"`
}

// GroupCopy contains the user-facing display name and description.
type GroupCopy struct {
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

// GroupLocalization is persisted separately from the group's business name.
type GroupLocalization locale.Content[GroupCopy]

// GroupModelsListConfig controls the optional custom /v1/models response list.
type GroupModelsListConfig struct {
	Enabled bool     `json:"enabled"`
	Models  []string `json:"models,omitempty"`
}

// ReasoningEffortMapping rewrites effort using a model pattern and match type.
type ReasoningEffortMapping struct {
	From      string `json:"from"`
	To        string `json:"to"`
	MatchType string `json:"match_type,omitempty"`
	Model     string `json:"model,omitempty"`
}

// GroupSchedulerType identifies the provider selection strategy used by a group.
type GroupSchedulerType string
