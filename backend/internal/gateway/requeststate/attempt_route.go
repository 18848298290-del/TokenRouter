package requeststate

import (
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// AttemptRoute holds the current candidate's protocol and model plan.
// The selected plan is copied by value; callers supply model mappings when resolving models.
type AttemptRoute struct {
	protocol  protocol.ProtocolID
	candidate routing.CandidatePlan
	planned   bool
}

func (a AttemptRoute) Protocol() protocol.ProtocolID { return a.protocol }

// Candidate returns the captured plan and whether one was provided.
func (a AttemptRoute) Candidate() (routing.CandidatePlan, bool) {
	return a.candidate, a.planned
}

// ResolveAttempt rechecks provider capabilities and discards a saved plan belonging to a different group.
func (s RoutingState) ResolveAttempt(snapshot provider.ProviderSnapshot, previous AttemptRoute) (AttemptRoute, bool, error) {
	plan, planned := s.plan, s.planSet
	if planned && s.group != nil && plan.GroupID() != s.group.ID {
		planned = false
	}
	if previous.protocol != "" && !planned {
		return previous, false, nil
	}
	if s.clientProtocol == "" {
		return previous, false, nil
	}
	if !planned {
		plan = routing.Plan(routing.PlanInput{Group: s.group, ClientProtocol: s.clientProtocol})
	} else {
		plan = plan.WithClientProtocol(s.clientProtocol)
	}
	candidate, ok := plan.ResolveCandidate(snapshot)
	if !ok {
		return AttemptRoute{}, false, fmt.Errorf("provider %d has no enabled route for %s", snapshot.ID, s.clientProtocol)
	}
	result := AttemptRoute{protocol: candidate.UpstreamProtocol, planned: planned}
	if planned {
		result.candidate = candidate
	}
	return result, true, nil
}

// ResolveModel applies one-hop mapping using configuration supplied at the time of resolution.
func (a AttemptRoute) ResolveModel(id int64, platform string, mapping map[string]string, requested string) (string, bool) {
	if a.planned {
		snapshot := provider.ProviderSnapshot{ID: id, Platform: platform, ModelPolicy: provider.NewModelRoutingSnapshot(mapping)}
		candidate, matched := a.candidate.ResolveModel(snapshot, requested)
		return candidate.Models.ProviderMappedModel, matched
	}
	return provider.ResolveMappedModel(mapping, requested)
}
