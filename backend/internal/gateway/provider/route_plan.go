package provider

import (
	"context"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/gateway/modeltrace"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// RoutePlanner combines the admitted group, group model mapping, and billing metadata.
type RoutePlanner struct{ groupPolicies *routing.PricingConfigService }

func NewRoutePlanner(groupPolicies *routing.PricingConfigService) *RoutePlanner {
	return &RoutePlanner{groupPolicies: groupPolicies}
}

func (p *RoutePlanner) PlanKey(ctx context.Context, key *apikey.APIKey, requested string) routing.RoutePlan {
	var group *routing.Group
	var id *int64
	if key != nil {
		group, id = key.Group, key.GroupID
	}
	return p.PlanRoute(ctx, group, id, requested)
}

func (p *RoutePlanner) PlanRoute(ctx context.Context, group *routing.Group, id *int64, requested string) routing.RoutePlan {
	mapping := routing.GroupMappingResult{MappedModel: requested}
	if p.groupPolicies != nil && id != nil {
		mapping = p.groupPolicies.ResolveGroupMapping(ctx, *id, requested)
	}
	mapping = modeltrace.WithGroupRedirect(mapping, ctx, requested)
	return RoutePlanForMapping(ctx, group, id, requested, mapping)
}

// RoutePlanForMapping reads the request group, checks its ID, and builds a protocol route.
func RoutePlanForMapping(ctx context.Context, group *routing.Group, groupID *int64, requested string, mapping routing.GroupMappingResult) routing.RoutePlan {
	if group == nil {
		group, _ = requeststate.GroupFromContext(ctx)
	}
	if group != nil && groupID != nil && group.ID != *groupID {
		group = nil
	}
	var view *routing.Group
	if group != nil {
		view = &routing.Group{ID: group.ID, SchedulerType: group.SchedulerType, AllowedProtocols: group.AllowedProtocols, ProtocolFallbacks: group.ProtocolFallbacks}
	}
	protocol, _ := requeststate.ClientProtocolFromContext(ctx)
	return routing.Plan(routing.PlanInput{Group: view, GroupID: groupID, ClientProtocol: protocol, RequestedModel: requested, GroupMapping: mapping})
}

// ProviderForProtocolAttempt revalidates the candidate against the current plan and reads model rules during matching.
func ProviderForProtocolAttempt(ctx context.Context, value *ExecutionProvider) (*ExecutionProvider, error) {
	if value == nil {
		return nil, fmt.Errorf("provider is nil")
	}
	attempt, resolved, err := requeststate.RoutingStateFromContext(ctx).ResolveAttempt(ExecutionSnapshot(value), value.Route)
	if err != nil {
		return nil, err
	}
	if !resolved {
		return value, nil
	}
	copied := *value
	copied.Route = attempt
	return &copied, nil
}
