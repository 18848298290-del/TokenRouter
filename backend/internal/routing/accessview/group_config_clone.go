package accessview

import (
	"maps"
	"slices"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// Authentication and management snapshots copy the group's declared slices,
// maps, and optional scalar pointers into separate storage. Nested feature
// values follow GroupRoutingPolicy.Clone's supported map and slice shapes, so
// other dynamic values can remain shared with the source.

// CloneGroupConfig copies the group's declared collections and optional scalar
// pointers into separate storage. Nil input returns nil, and nil versus empty
// collection shapes remain unchanged. Nested features use
// GroupRoutingPolicy.Clone's supported shapes; other dynamic feature values can
// retain shared references.
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

// cloneGroupPointer allocates a copy of a supplied value and preserves nil. The
// value copy is shallow when T contains references; current callers use scalar
// group settings.
func cloneGroupPointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

// CloneGroupAdvancedSchedulerOverrides delegates copying of every supplied
// override pointer to the scheduler policy package. Nil fields retain
// inheritance, and callers can modify populated result values independently.
func CloneGroupAdvancedSchedulerOverrides(overrides GroupAdvancedSchedulerOverrides) GroupAdvancedSchedulerOverrides {
	return policy.CloneGroupAdvancedSchedulerOverrides(overrides)
}
