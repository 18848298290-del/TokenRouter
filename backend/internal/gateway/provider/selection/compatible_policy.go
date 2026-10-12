package selection

import (
	"context"
	"log/slog"
	"time"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

const openaiStickySessionTTL = time.Hour

func (s *Compatible) CheckGroupModelRestriction(ctx context.Context, groupID *int64, requestedModel string) bool {
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

// resolveGroupRoutingModel returns the group-mapped model used by compatible selection.
func (s *Compatible) resolveGroupRoutingModel(ctx context.Context, groupID *int64, requestedModel string) string {
	if s == nil {
		return requestedModel
	}
	return s.groupPolicies.ResolveRoutingModel(ctx, groupID,
		requestedModel,
	)
}

// UpstreamRoutingModelRestricted checks the final upstream model after group and protocol mapping.
func (s *Compatible) UpstreamRoutingModelRestricted(ctx context.Context, groupID int64, provider *gatewayprovider.ExecutionProvider, routingModel string, requireCompact bool) bool {
	if s.groupPolicies == nil {
		return false
	}
	upstreamModel := gatewayprovider.ExecutionModelPolicy(provider).UpstreamModel(ctx, routingModel)
	if requireCompact {
		upstreamModel = gatewayprovider.ExecutionModelPolicy(provider).OpenAIUpstream(routingModel, true)
	}
	if upstreamModel == "" {
		return false
	}
	return s.groupPolicies.IsModelRestricted(ctx, groupID, upstreamModel)
}

func (s *Compatible) NeedsUpstreamGroupRestriction(ctx context.Context, groupID *int64) bool {
	if groupID == nil || s.groupPolicies == nil {
		return false
	}
	ch, err := s.groupPolicies.GetGroupPolicy(ctx, *groupID)
	if err != nil {
		slog.Warn("failed to check openai group upstream model restriction", "group_id", *groupID, "error", err)
		return false
	}
	if ch == nil || !ch.RestrictModels {
		return false
	}
	return ch.RestrictionSource() == routing.BillingModelSourceUpstream
}

// shadowProtocolsAllowed checks the shadow against its parent's protocol configuration.
func (s *Compatible) shadowProtocolsAllowed(ctx context.Context, provider *gatewayprovider.ExecutionProvider) bool {
	if provider == nil || !provider.View().IsShadow() {
		return true
	}
	if source, _ := requeststate.ClientProtocolFromContext(ctx); source == "" {
		return true
	}
	parent := s.parentProviderLookup(ctx)(*provider.Record.ParentProviderID)
	return parent != nil && gatewayprovider.ExecutionModelPolicy(parent).AllowsProtocol(ctx)
}
