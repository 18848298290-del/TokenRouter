package routing

import (
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

type CreateGroupInput struct {
	Localization  *locale.Update[GroupCopy]
	RoutingPolicy GroupRoutingPolicy
	Name          string
	Description   string
	// SchedulerType defaults to basic when empty.
	SchedulerType string
	// AdvancedSchedulerOverrides inherits gateway settings for fields that are absent.
	AdvancedSchedulerOverrides GroupAdvancedSchedulerOverrides
	DisplayBrand               string
	SortOrder                  *int
	RateMultiplier             float64
	IsExclusive                bool
	// SessionIsolationEnabled rejects designated sessions already assigned to another group.
	SessionIsolationEnabled bool

	// Image-generation permissions are independent of shared pricing configuration.
	AllowImageGeneration      bool
	AllowBatchImageGeneration bool

	ClaudeCodeOnly  bool   // Restricts access to Claude Code clients.
	FallbackGroupID *int64 // Fallback for clients rejected by the Claude Code restriction.
	// Fallback for invalid Anthropic requests.
	FallbackGroupIDOnInvalidRequest *int64
	// UnavailableFallbackGroupID is the preferred API key fallback when this group is unavailable.
	UnavailableFallbackGroupID *int64
	// Preferred provider IDs by model pattern.
	ModelRouting        map[string][]int64
	ModelRoutingEnabled bool // Enables preferred-provider model routing.
	MCPXMLInject        *bool
	// Legacy Antigravity model-family configuration.
	SupportedModelScopes []string
	// AllowedProtocols defaults to the shared text entries when nil; an explicit empty set is valid.
	LegacyProtocolInput  bool
	AllowedProtocols     []protocol.ProtocolID
	ProtocolFallbacks    map[protocol.ProtocolID][]protocol.ProtocolID
	ResponsesImagePolicy string
	// AllowMessagesDispatch is compatibility input when the complete protocol set is omitted.
	AllowMessagesDispatch bool
	AllowLive             bool
	// ForceOpenAIFast is the legacy input for forcing priority on applicable OpenAI requests.
	ForceOpenAIFast bool
	// A named policy takes precedence over the legacy boolean.
	OpenAIFastPolicy *string

	DefaultMappedModel string
	RequireOAuthOnly   bool
	RequirePrivacySet  bool
	ModelsListConfig   GroupModelsListConfig
	// AvailabilityProbeConfig controls active group probes.
	AvailabilityProbeConfig GroupAvailabilityProbeConfig
	// RPMLimit is the group limit per minute; zero imposes no group limit.
	RPMLimit int
	// MaxReasoningEffort caps OpenAI and Anthropic effort; empty means no cap.
	MaxReasoningEffort string
	// MaxReasoningEffortOverLimit selects downgrade (the default) or deny when the cap is exceeded.
	MaxReasoningEffortOverLimit string
	// ReasoningEffortMappings rewrites requested effort using configured model matching.
	ReasoningEffortMappings []ReasoningEffortMapping
	// Copies provider associations from these groups in the creation transaction.
	CopyProvidersFromGroupIDs []int64
}

type UpdateGroupInput struct {
	Localization  *locale.Update[GroupCopy]
	RoutingPolicy *GroupRoutingPolicy
	Name          string
	Description   *string
	// SchedulerType retains the current value when nil.
	SchedulerType *string
	// AdvancedSchedulerOverrides retains the current value when nil; an empty object restores inheritance.
	AdvancedSchedulerOverrides *GroupAdvancedSchedulerOverrides
	DisplayBrand               *string
	SortOrder                  *int
	RateMultiplier             *float64 // A pointer distinguishes an omitted value from zero.
	IsExclusive                *bool
	// SessionIsolationEnabled controls isolation for the target group.
	SessionIsolationEnabled *bool
	Status                  string

	// Image-generation permissions are independent of shared pricing configuration.
	AllowImageGeneration      *bool
	AllowBatchImageGeneration *bool

	ClaudeCodeOnly  *bool  // Restricts access to Claude Code clients.
	FallbackGroupID *int64 // Fallback for clients rejected by the Claude Code restriction.
	// Fallback for invalid Anthropic requests.
	FallbackGroupIDOnInvalidRequest *int64
	// UnavailableFallbackGroupID is the preferred API key fallback when this group is unavailable.
	UnavailableFallbackGroupID *int64
	// Preferred provider IDs by model pattern.
	ModelRouting        map[string][]int64
	ModelRoutingEnabled *bool // Enables preferred-provider model routing.
	MCPXMLInject        *bool
	// Legacy Antigravity model-family configuration.
	SupportedModelScopes *[]string
	// AllowedProtocols retains the current value when nil; a supplied value replaces the entire set.
	LegacyProtocolInput  bool
	AllowedProtocols     *[]protocol.ProtocolID
	ProtocolFallbacks    map[protocol.ProtocolID][]protocol.ProtocolID
	ResponsesImagePolicy string
	// AllowMessagesDispatch is compatibility input when the complete protocol set is omitted.
	AllowMessagesDispatch *bool
	AllowLive             *bool
	// ForceOpenAIFast retains the current value when nil; applicable OpenAI providers enforce it.
	ForceOpenAIFast *bool
	// A named policy takes precedence over the legacy boolean.
	OpenAIFastPolicy *string

	DefaultMappedModel *string
	RequireOAuthOnly   *bool
	RequirePrivacySet  *bool
	ModelsListConfig   *GroupModelsListConfig
	// AvailabilityProbeConfig retains the current probe settings when nil.
	AvailabilityProbeConfig *GroupAvailabilityProbeConfig
	// RPMLimit uses zero for no group limit and nil to retain the current value.
	RPMLimit *int
	// MaxReasoningEffort uses an empty string to clear the cap and nil to retain it.
	MaxReasoningEffort *string
	// MaxReasoningEffortOverLimit uses an empty string for downgrade and nil to retain the current value.
	MaxReasoningEffortOverLimit *string
	// ReasoningEffortMappings uses nil to retain mappings, an empty list to clear them, and other lists to
	// replace them.
	ReasoningEffortMappings *[]ReasoningEffortMapping
	// Replaces current provider associations with the source groups' providers in the update transaction.
	CopyProvidersFromGroupIDs []int64
}
