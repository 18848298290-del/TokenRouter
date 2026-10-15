package postgres

import (
	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// group_mapper.go converts stored Ent group records into hydrated routing
// groups.
// Stored groups carry nullable text and JSON routing rules into administrative
// and request reads. Conversion marks the group hydrated and decodes policy
// separately from the directly assigned collections, so callers must
// distinguish copied scheduler overrides from shared entity data.

// GroupFromEnt converts a stored group into hydrated routing data, returning
// nil for nil input. DecodeGroupRoutingPolicy turns malformed nonempty JSON
// into an enabled restriction with no allowed models.
//
// Scheduler override pointers are cloned. Other reference-bearing values copied
// directly from the entity remain shared, including fallback IDs,
// ModelsListConfig.Models, and AvailabilityProbeConfig.MaxRetries; callers
// copy them before mutation.
func GroupFromEnt(g *dbent.Group) *routing.Group {
	if g == nil {
		return nil
	}
	routingPolicy := routing.DecodeGroupRoutingPolicy(g.RoutingPolicy)
	return &routing.Group{
		Localization:                    g.Localization,
		ID:                              g.ID,
		Name:                            g.Name,
		Description:                     derefString(g.Description),
		SchedulerType:                   routing.GroupSchedulerType(g.SchedulerType),
		AdvancedSchedulerOverrides:      policy.CloneGroupAdvancedSchedulerOverrides(g.AdvancedSchedulerOverrides),
		DisplayBrand:                    g.DisplayBrand,
		RateMultiplier:                  g.RateMultiplier,
		IsExclusive:                     g.IsExclusive,
		Status:                          g.Status,
		Hydrated:                        true,
		DuplicateOperationID:            derefString(g.DuplicateOperationID),
		SessionIsolationEnabled:         g.SessionIsolationEnabled,
		AllowImageGeneration:            g.AllowImageGeneration,
		AllowBatchImageGeneration:       g.AllowBatchImageGeneration,
		RoutingPolicy:                   routingPolicy,
		ClaudeCodeOnly:                  g.ClaudeCodeOnly,
		FallbackGroupID:                 g.FallbackGroupID,
		FallbackGroupIDOnInvalidRequest: g.FallbackGroupIDOnInvalidRequest,
		UnavailableFallbackGroupID:      g.UnavailableFallbackGroupID,
		ModelRouting:                    g.ModelRouting,
		ModelRoutingEnabled:             g.ModelRoutingEnabled,
		MCPXMLInject:                    g.McpXMLInject,
		SupportedModelScopes:            g.SupportedModelScopes,
		SortOrder:                       g.SortOrder,
		AllowedProtocols:                g.AllowedProtocols,
		ProtocolFallbacks:               g.ProtocolFallbacks,
		ResponsesImagePolicy:            g.ResponsesImagePolicy,
		AllowMessagesDispatch:           g.AllowMessagesDispatch,
		AllowLive:                       g.AllowLive,
		ForceOpenAIFast:                 g.ForceOpenaiFast,
		OpenAIFastPolicy:                g.OpenaiFastPolicy,
		RequireOAuthOnly:                g.RequireOauthOnly,
		RequirePrivacySet:               g.RequirePrivacySet,
		DefaultMappedModel:              g.DefaultMappedModel,
		ModelsListConfig:                g.ModelsListConfig,
		AvailabilityProbeConfig:         g.AvailabilityProbeConfig,
		RPMLimit:                        g.RpmLimit,
		MaxReasoningEffort:              g.MaxReasoningEffort,
		MaxReasoningEffortOverLimit:     g.MaxReasoningEffortOverLimit,
		ReasoningEffortMappings:         g.ReasoningEffortMappings,
		CreatedAt:                       g.CreatedAt,
		UpdatedAt:                       g.UpdatedAt,
	}
}

// derefString returns stored text or an empty string for SQL null. Group
// descriptions and duplicate-operation identifiers use that empty value when
// the entity supplies no pointer.
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
