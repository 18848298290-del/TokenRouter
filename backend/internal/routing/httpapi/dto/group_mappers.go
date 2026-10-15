package dto

import (
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// group_mappers.go converts routing groups into administrative and localized
// user response values.
// Group responses carry requestable model choices and effective protocol
// admission alongside stored product settings. Administrative mapping adds
// editable policy and provider counts, while optional language resolution
// replaces user-facing text and records which language supplied it for the
// caller.

// GroupFromRoutingBase maps a nonnil routing group into user-visible fields.
// It allocates Models and effective AllowedProtocols slices, while
// model/protocol maps, reasoning mappings, and fallback pointers retain
// source storage. Treat those retained values as read-only during response
// use.
func GroupFromRoutingBase(g *routing.Group) Group {
	return Group{
		Models: append([]string{}, g.Models...), ModelProtocols: g.ModelProtocols,
		ID:                              g.ID,
		Name:                            g.Name,
		DisplayName:                     g.DisplayName,
		Description:                     g.Description,
		DisplayBrand:                    g.DisplayBrand,
		RateMultiplier:                  g.RateMultiplier,
		IsExclusive:                     g.IsExclusive,
		Status:                          g.Status,
		SessionIsolationEnabled:         g.SessionIsolationEnabled,
		AllowImageGeneration:            g.AllowImageGeneration,
		AllowBatchImageGeneration:       g.AllowBatchImageGeneration,
		ClaudeCodeOnly:                  g.ClaudeCodeOnly,
		FallbackGroupID:                 g.FallbackGroupID,
		FallbackGroupIDOnInvalidRequest: g.FallbackGroupIDOnInvalidRequest,
		UnavailableFallbackGroupID:      g.UnavailableFallbackGroupID,
		AllowedProtocols:                g.EffectiveAllowedProtocols(),
		ProtocolFallbacks:               g.ProtocolFallbacks,
		ResponsesImagePolicy:            g.ResponsesImagePolicy,
		AllowMessagesDispatch:           g.AllowsClientProtocol(protocol.ProtocolAnthropicMessages),
		AllowLive:                       g.AllowLive,
		RequireOAuthOnly:                g.RequireOAuthOnly,
		RequirePrivacySet:               g.RequirePrivacySet,
		RPMLimit:                        g.RPMLimit,
		MaxReasoningEffort:              g.MaxReasoningEffort,
		MaxReasoningEffortOverLimit:     g.MaxReasoningEffortOverLimit,
		ReasoningEffortMappings:         g.ReasoningEffortMappings,
		CreatedAt:                       g.CreatedAt,
		UpdatedAt:                       g.UpdatedAt,
	}
}

// AdminGroupFromRouting returns administrator settings and provider counts,
// or nil for a nil source. It copies scheduler override pointers and the
// routing policy's model map, allowlist, and recognized feature collections.
// Localization translations, model routing, custom-list and legacy-scope
// slices, probe pointers, unsupported dynamic feature values, and storage
// retained by GroupFromRoutingBase can remain shared. Callers treat those
// retained values as read-only during response use.
func AdminGroupFromRouting[A any](g *routing.Group) *AdminGroup[A] {
	if g == nil {
		return nil
	}
	out := &AdminGroup[A]{
		Localization:               routing.GroupLocalization(routing.GroupContent(g)),
		Group:                      GroupFromRoutingBase(g),
		ForceOpenAIFast:            g.ForceOpenAIFast,
		OpenAIFastPolicy:           g.EffectiveOpenAIFastPolicy(),
		SchedulerType:              string(g.SchedulerType),
		AdvancedSchedulerOverrides: policy.CloneGroupAdvancedSchedulerOverrides(g.AdvancedSchedulerOverrides),
		RoutingPolicy:              g.RoutingPolicy.Clone(),
		ModelRouting:               g.ModelRouting,
		ModelRoutingEnabled:        g.ModelRoutingEnabled,
		MCPXMLInject:               g.MCPXMLInject,
		DefaultMappedModel:         g.DefaultMappedModel,
		ModelsListConfig:           g.ModelsListConfig,
		AvailabilityProbeConfig:    g.AvailabilityProbeConfig,
		SupportedModelScopes:       g.SupportedModelScopes,
		ProviderCount:              g.ProviderCount,
		ActiveProviderCount:        g.ActiveProviderCount,
		RateLimitedProviderCount:   g.RateLimitedProviderCount,
		SortOrder:                  g.SortOrder,
	}

	return out
}

// GroupFromRouting returns the user-visible DTO, or nil when the source group
// is nil. When language is supplied, its first value resolves display name
// and description and adds resolution metadata and saved search texts;
// otherwise input display text is used. Collection ownership follows
// GroupFromRoutingBase.
func GroupFromRouting(g *routing.Group, language ...string) *Group {
	if g == nil {
		return nil
	}
	out := GroupFromRoutingBase(g)
	if len(language) > 0 {
		copy, actual := routing.GroupDisplay(g, language[0])
		out.LocalizationResolution = &actual
		out.SearchTerms = routing.GroupSearchTexts(g)
		out.DisplayName = copy.DisplayName
		out.Description = copy.Description
	}
	return &out
}

// GroupCapacityFromSummary returns a new capacity DTO from aggregate
// observations. A nil summary remains nil so callers can omit unavailable
// capacity, while a present summary retains all six values including zero.
func GroupCapacityFromSummary(v *routing.GroupCapacitySummary) *GroupCapacity {
	if v == nil {
		return nil
	}
	return &GroupCapacity{ConcurrencyUsed: v.ConcurrencyUsed, ConcurrencyMax: v.ConcurrencyMax, SessionsUsed: v.SessionsUsed, SessionsMax: v.SessionsMax, RPMUsed: v.RPMUsed, RPMMax: v.RPMMax}
}
