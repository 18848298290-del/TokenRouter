package selection

import (
	"context"
	"log/slog"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// checkGroupModelRestriction checks the group's allowlist at the configured model stage.
// The selection precheck handles requested and group_mapped models.
// Upstream models require per-provider checks, so this precheck returns false for that stage.
func (s *Generic) checkGroupModelRestriction(ctx context.Context, groupID *int64, requestedModel string) bool {
	if groupID == nil || s.groupPolicies == nil || requestedModel == "" {
		return false
	}
	mapping := s.groupPolicies.ResolveGroupMapping(ctx, *groupID, requestedModel)
	billingModel := routing.ModelForRestriction(mapping.RestrictionModelSource, requestedModel, mapping.MappedModel)
	if billingModel == "" {
		return false
	}
	return s.groupPolicies.IsModelRestricted(ctx, *groupID, billingModel)
}

// isUpstreamModelRestrictedByGroup checks the final upstream model against the group allowlist.
// The selection loop calls it when restrictions are enabled with source upstream.
func (s *Generic) isUpstreamModelRestrictedByGroup(ctx context.Context, groupID int64, provider *gatewayprovider.ExecutionProvider, requestedModel string) bool {
	if s.groupPolicies == nil {
		return false
	}
	routingModel := requestedModel
	if mapping := s.groupPolicies.ResolveGroupMapping(ctx, groupID, requestedModel); mapping.Mapped {
		routingModel = mapping.MappedModel
	}
	upstreamModel := resolveProviderUpstreamModel(ctx, provider, routingModel)
	if upstreamModel == "" {
		return false
	}
	return s.groupPolicies.IsModelRestricted(ctx, groupID, upstreamModel)
}

// groupMappedModelForGroup returns the group-mapped model for provider selection.
func (s *Generic) groupMappedModelForGroup(ctx context.Context, groupID *int64, requestedModel string) string {
	if s == nil {
		return requestedModel
	}
	return s.groupPolicies.ResolveRoutingModel(ctx, groupID, requestedModel)
}

// needsUpstreamGroupRestrictionCheck reports whether the loop must check each provider's upstream model.
func (s *Generic) needsUpstreamGroupRestrictionCheck(ctx context.Context, groupID *int64) bool {
	if groupID == nil || s.groupPolicies == nil {
		return false
	}
	ch, err := s.groupPolicies.GetGroupPolicy(ctx, *groupID)
	if err != nil {
		slog.Warn("failed to check group upstream model restriction", "group_id", *groupID, "error", err)
		return false
	}
	if ch == nil || !ch.RestrictModels {
		return false
	}
	return ch.RestrictionSource() == routing.BillingModelSourceUpstream
}
