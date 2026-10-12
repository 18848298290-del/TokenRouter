package accessview

import (
	"maps"
	"slices"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// CloneGroupConfig deep-copies group configuration and preserves nil versus empty collections.
func CloneGroupConfig(g *GroupConfig) *GroupConfig {
	if g == nil {
		return nil
	}
	out := *g
	out.Localization.Translations = maps.Clone(g.Localization.Translations)
	out.Localization.SourceLocale = cloneGroupPointer(g.Localization.SourceLocale)
	out.Models = slices.Clone(g.Models)
	out.ModelProtocols = maps.Clone(g.ModelProtocols)
	for model, protocols := range out.ModelProtocols {
		out.ModelProtocols[model] = slices.Clone(protocols)
	}
	out.RoutingPolicy = g.RoutingPolicy.Clone()

	out.FallbackGroupID = cloneGroupPointer(g.FallbackGroupID)
	out.FallbackGroupIDOnInvalidRequest = cloneGroupPointer(g.FallbackGroupIDOnInvalidRequest)
	out.UnavailableFallbackGroupID = cloneGroupPointer(g.UnavailableFallbackGroupID)
	out.ModelRouting = maps.Clone(g.ModelRouting)
	for key, value := range out.ModelRouting {
		out.ModelRouting[key] = slices.Clone(value)
	}
	out.SupportedModelScopes = slices.Clone(g.SupportedModelScopes)
	out.AllowedProtocols = slices.Clone(g.AllowedProtocols)
	out.ProtocolFallbacks = protocol.CloneFallbacks(g.ProtocolFallbacks)
	out.ReasoningEffortMappings = slices.Clone(g.ReasoningEffortMappings)
	out.AdvancedSchedulerOverrides = CloneGroupAdvancedSchedulerOverrides(g.AdvancedSchedulerOverrides)
	out.ModelsListConfig.Models = slices.Clone(g.ModelsListConfig.Models)
	out.AvailabilityProbeConfig.MaxRetries = cloneGroupPointer(g.AvailabilityProbeConfig.MaxRetries)
	return &out
}

func cloneGroupPointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

// CloneGroupAdvancedSchedulerOverrides returns an independent copy of advanced scheduling overrides.
func CloneGroupAdvancedSchedulerOverrides(overrides GroupAdvancedSchedulerOverrides) GroupAdvancedSchedulerOverrides {
	return policy.CloneGroupAdvancedSchedulerOverrides(overrides)
}
