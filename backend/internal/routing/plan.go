package routing

import (
	"slices"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// ModelChain records client, key-redirected, group-mapped, and provider-mapped models, with one mapping per stage.
// Execution normalizes vendor names and restores response models; usage records consume this chain.
type ModelChain struct {
	ClientModel, RequestedModel, GroupMappedModel, ProviderMappedModel string
	APIKeyRedirected, GroupMapped                                      bool
	RestrictionModelSource                                             string
	RestrictModels                                                     bool
	PricingConfigID                                                    int64
	BillingModelSource                                                 string
}

// PlanInput contains the admitted group, requested model, and client protocol.
type PlanInput struct {
	GroupID        *int64
	RequestedModel string
	GroupMapping   GroupMappingResult
	Group          *Group
	ClientProtocol capability.ProtocolID
}

// RoutePlan snapshots group and protocol policy; candidate resolution supplies provider-specific results.
// The protocol fallback map is copied when the plan is built.
type RoutePlan struct {
	models         ModelChain
	groupID        int64
	schedulerType  GroupSchedulerType
	clientProtocol capability.ProtocolID
	allowed        []capability.ProtocolID
	fallbacks      map[capability.ProtocolID][]capability.ProtocolID
}

// CandidatePlan contains the route resolved for one provider attempt.
type CandidatePlan struct {
	Models           ModelChain
	ProviderID       int64
	GroupID          int64
	ClientProtocol   capability.ProtocolID
	UpstreamProtocol capability.ProtocolID
}

// Mapping returns group mapping, whitelist-stage, and billing metadata from the plan.
func (p RoutePlan) Mapping() GroupMappingResult {
	return GroupMappingResult{
		MappedModel:            p.models.GroupMappedModel,
		PricingConfigID:        p.models.PricingConfigID,
		Mapped:                 p.models.GroupMapped,
		BillingModelSource:     p.models.BillingModelSource,
		ClientModel:            p.models.ClientModel,
		APIKeyRedirected:       p.models.APIKeyRedirected,
		RestrictModels:         p.models.RestrictModels,
		RestrictionModelSource: p.models.RestrictionModelSource,
	}
}

func (p RoutePlan) Models() ModelChain { return p.models }

// ResolveModel returns a copy with the provider-mapped model resolved from the supplied snapshot.
func (p CandidatePlan) ResolveModel(snapshot provider.ProviderSnapshot, requested string) (CandidatePlan, bool) {
	mapped, matched := snapshot.ModelPolicy.Resolve(requested)
	p.Models.ProviderMappedModel = mapped
	return p, matched
}

// Plan snapshots group policy after the caller has admitted the request.
func Plan(input PlanInput) RoutePlan {
	plan := RoutePlan{
		clientProtocol: input.ClientProtocol,
		models: ModelChain{
			RequestedModel:         input.RequestedModel,
			ClientModel:            input.GroupMapping.ClientModel,
			APIKeyRedirected:       input.GroupMapping.APIKeyRedirected,
			GroupMappedModel:       input.GroupMapping.MappedModel,
			GroupMapped:            input.GroupMapping.Mapped,
			RestrictModels:         input.GroupMapping.RestrictModels,
			RestrictionModelSource: input.GroupMapping.RestrictionModelSource,
			PricingConfigID:        input.GroupMapping.PricingConfigID,
			BillingModelSource:     input.GroupMapping.BillingModelSource,
		},
	}
	if input.Group != nil {
		plan.groupID = input.Group.ID
		plan.schedulerType = input.Group.SchedulerType
		plan.allowed = slices.Clone(input.Group.AllowedProtocols)
		plan.fallbacks = protocol.CloneFallbacks(input.Group.ProtocolFallbacks)
	}
	if input.GroupID != nil {
		plan.groupID = *input.GroupID
	}
	return plan
}

// ResolveCandidate prefers native support, then a single conversion. Call it again after refreshing or rechecking
// a provider.
func (p RoutePlan) ResolveCandidate(candidate provider.ProviderSnapshot) (CandidatePlan, bool) {
	target, ok := capability.ResolveRoute(candidate.Protocols(), p.clientProtocol, p.fallbacks)
	if !ok {
		return CandidatePlan{}, false
	}
	return CandidatePlan{Models: p.models, ProviderID: candidate.ID, GroupID: p.groupID, ClientProtocol: p.clientProtocol, UpstreamProtocol: target}, true
}

// GroupID returns the plan's final group ID.
func (p RoutePlan) GroupID() int64 { return p.groupID }

func (p RoutePlan) SchedulerType() GroupSchedulerType { return p.schedulerType }

func (p RoutePlan) AllowedProtocols() []capability.ProtocolID { return slices.Clone(p.allowed) }

// WithClientProtocol returns a copy using the execution layer's selected client protocol.
func (p RoutePlan) WithClientProtocol(source capability.ProtocolID) RoutePlan {
	p.clientProtocol = source
	return p
}
