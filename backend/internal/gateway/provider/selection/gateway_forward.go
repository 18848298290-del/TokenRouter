package selection

import (
	"context"
	"log/slog"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Group restrictions select their model stage separately from provider routing.
// Requested and group-mapped names can be checked before selection, while
// upstream restrictions need one group mapping followed by the chosen
// provider's final model resolution against the same group policy.

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

// isUpstreamModelRestrictedByGroup checks the final upstream model against the
// group's allowlist. Pass the client model so this helper can apply one group
// mapping before provider rules; missing policy or final identity returns
// false.
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

// groupMappedModelForGroup delegates one group mapping for provider selection.
// A nil selector, missing policy inputs, or an empty mapping retains
// requestedModel through PricingConfigService, so the provider stage still
// receives the available identity.
func (s *Generic) groupMappedModelForGroup(ctx context.Context, groupID *int64, requestedModel string) string {
	if s == nil {
		return requestedModel
	}
	return s.groupPolicies.ResolveRoutingModel(ctx, groupID, requestedModel)
}

// needsUpstreamGroupRestrictionCheck reports whether an enabled group policy
// uses upstream model restriction. Missing group or policy service returns
// false, and read errors are logged before returning false for that call.
func (s *Generic) needsUpstreamGroupRestrictionCheck(ctx context.Context, groupID *int64) bool {
	if groupID == nil || s.groupPolicies == nil {
		return false
	}
	ch, err := s.groupPolicies.GetGroupPolicy(ctx, *groupID)
	if err != nil {
		// A failed policy read cannot identify the upstream restriction stage. Leave
		// this per-provider gate disabled for the call and retain the diagnostic warning.
		slog.Warn("failed to check group upstream model restriction", "group_id", *groupID, "error", err)
		return false
	}
	if ch == nil || !ch.RestrictModels {
		return false
	}
	return ch.RestrictionSource() == routing.BillingModelSourceUpstream
}
