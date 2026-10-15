package requeststate

import (
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// attempt_route.go resolves provider protocol attempts from request routing
// state and applies the current model mapping.
// Provider capabilities can change between selections even when the request
// keeps its group plan. Candidate resolution therefore rechecks the current
// provider snapshot against the client protocol before a later attempt uses a
// route.
//
// A saved plan is usable only while its group matches the captured group.
// Without a saved plan, an existing attempt protocol can be retained for
// compatibility, otherwise the state builds a capability route from the current
// group. Only an admitted saved plan publishes a CandidatePlan to callers;
// model mapping remains late-bound to the configuration supplied to
// ResolveModel, so a refreshed mapping is read when each attempt resolves its
// model.

// AttemptRoute records one candidate's protocol and optional admitted model
// plan.
type AttemptRoute struct {
	// candidate is meaningful when planned is true; an unplanned compatibility
	// attempt can retain protocol alone. Captured candidate values are returned
	// by value when an admitted route supplied them.
	protocol  protocol.ProtocolID   // resolved upstream protocol identifier
	candidate routing.CandidatePlan // captured admitted candidate plan
	planned   bool                  // candidate plan presence flag
}

// Protocol returns the upstream protocol selected for this attempt.
func (a AttemptRoute) Protocol() protocol.ProtocolID { return a.protocol }

// Candidate returns the captured candidate by value and reports whether an
// admitted plan supplied it. An unplanned compatibility route can still have a
// protocol while this result remains absent.
func (a AttemptRoute) Candidate() (routing.CandidatePlan, bool) {
	return a.candidate, a.planned
}

// ResolveAttempt rechecks the current provider's enabled protocol route and
// returns the resolved attempt with true when resolution occurs. A saved plan
// whose group differs from the captured group is discarded for this decision.
//
// Without a usable saved plan, an already selected protocol is retained with
// false, as is any attempt lacking a client protocol. Otherwise current group
// policy supplies a fresh capability plan. Resolution failure returns an error
// and an empty attempt; an unplanned success publishes its protocol alone,
// while a planned success also captures its candidate for later model
// resolution.
func (s RoutingState) ResolveAttempt(snapshot provider.ProviderSnapshot, previous AttemptRoute) (AttemptRoute, bool, error) {
	plan, planned := s.plan, s.planSet
	// A derived group fallback can retain its predecessor's plan. Discard that
	// plan here so the new group supplies capability rules for this selection.
	if planned && s.group != nil && plan.GroupID() != s.group.ID {
		planned = false
	}
	// Compatibility callers may already have selected a protocol before an
	// admitted model plan exists; keep that established route for their attempt.
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

// ResolveModel applies one provider mapping using the supplied current
// configuration and reports whether it matched. Planned attempts resolve
// through their captured candidate; compatibility attempts use
// provider.ResolveMappedModel directly, so the saved attempt does not freeze
// the mapping.
func (a AttemptRoute) ResolveModel(id int64, platform string, mapping map[string]string, requested string) (string, bool) {
	if a.planned {
		snapshot := provider.ProviderSnapshot{ID: id, Platform: platform, ModelPolicy: provider.NewModelRoutingSnapshot(mapping)}
		candidate, matched := a.candidate.ResolveModel(snapshot, requested)
		return candidate.Models.ProviderMappedModel, matched
	}
	return provider.ResolveMappedModel(mapping, requested)
}
