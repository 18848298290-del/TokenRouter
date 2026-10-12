package selection

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Compatible candidates need both their protocol route and the request's
// admitted group to remain eligible. The candidate context records a sticky
// provider so Anthropic window-cost and RPM checks can apply their sticky
// allowances consistently during fresh and database rechecks. Image requests
// inspect the provider-mapped upstream name, including aliases admitted by
// wildcard scopes.
//
// Group model-routing preferences temporarily narrow the candidate pool within
// the same group. Generic quota, scheduling thresholds, and Gemini local quota
// checks then contribute platform-specific rejection reasons. A separate
// captured selection request lets hydration validate the group and model
// against the later full record, keeping lightweight candidate state from
// deciding execution eligibility by itself.

type (
	// candidatePolicyKey separates compatible eligibility state from other request
	// context values.
	candidatePolicyKey struct{}
	// imageModelRequiredKey marks selection requests requiring a mapped
	// image-generation model identity.
	imageModelRequiredKey struct{}
	// preferredProvidersKey stores the group model-routing provider subset for one
	// selection pass.
	preferredProvidersKey struct{}
	// candidatePolicy holds the admitted group and existing session provider for
	// eligibility rechecks.
	candidatePolicy struct {
		// groupID and stickyID describe the same request: the group limits membership,
		// while the sticky provider alone can receive reserved window-cost and RPM capacity.
		// groupID borrows the supplied admitted identifier. Callers keep its value
		// stable while the captured policy is reused for candidate eligibility checks.
		groupID  *int64 // admitted selection group
		stickyID int64  // bound provider identifier
	}
)

type (
	// selectionRequestKey identifies hydration inputs captured by slot-free and
	// load-aware entry points.
	selectionRequestKey struct{}
	// selectionRequest retains group and model inputs for validating a hydrated
	// provider record.
	selectionRequest struct {
		// groupID and model retain the selection inputs until full provider hydration.
		// The later record is checked against their group membership and model eligibility together.
		// groupID borrows the caller's identifier pointer, which stays stable for
		// every hydration check using this captured selection request.
		groupID *int64 // captured request group
		model   string // captured request model
	}
)

// withCandidatePolicy captures group membership and the existing sticky
// provider for eligibility rechecks. The sticky reader's returned ID supplies
// window-cost and RPM reserve eligibility even when the read also reports an
// error; zero selects the ordinary candidate allowance.
func (s *Compatible) withCandidatePolicy(ctx context.Context, groupID *int64, session string) context.Context {
	state := candidatePolicy{groupID: groupID}
	if session != "" && s.cache != nil {
		// Affinity is optional input for candidate eligibility. Retain the sticky
		// reader's identifier and discard its error so reserve checks use the
		// binding value that the reader returned.
		state.stickyID, _ = s.getStickySessionProviderID(ctx, groupID, session)
	}
	return context.WithValue(ctx, candidatePolicyKey{}, state)
}

// candidateEligibilityReason returns the first compatible candidate rejection,
// or an empty string when its checks pass. Protocol and model eligibility run
// first, followed by image identity, preferred-provider membership, admitted
// group membership, and OAuth requirements.
//
// The captured sticky provider controls Anthropic window-cost and RPM reserve
// eligibility. Generic quota and scheduling thresholds apply before Gemini's
// local precheck when those selector dependencies exist. Each reason names the
// gate that rejected this candidate so fresh and database checks can reuse the
// same decision inputs.
func (s *Compatible) candidateEligibilityReason(ctx context.Context, value *gatewayadapter.ExecutionProvider, platform, model string, compact bool, required provider.OpenAIEndpointCapability) string {
	if reason := gatewayadapter.CompatibleEligibilityReason(ctx, value, platform, model, compact, required); reason != "" {
		return reason
	}
	if required, _ := ctx.Value(imageModelRequiredKey{}).(bool); required && !media.IsImageGenerationModel(gatewayadapter.ExecutionModelPolicy(value).UpstreamModel(ctx, model)) {
		return "image_model_required"
	}
	if preferred, configured := ctx.Value(preferredProvidersKey{}).(map[int64]struct{}); configured {
		if _, found := preferred[value.Record.ID]; !found {
			return "model_routing_deferred"
		}
	}
	state, _ := ctx.Value(candidatePolicyKey{}).(candidatePolicy)
	if state.groupID != nil && !openAIStickyProviderMatchesGroup(value, state.groupID) {
		return "group_mismatch"
	}
	group, _ := requeststate.GroupFromContext(ctx)
	if group != nil && group.RequireOAuthOnly && !value.View().IsOAuth() {
		return "oauth_required"
	}
	if s.generic != nil {
		if !s.generic.isProviderSchedulableForQuota(value) {
			return "quota_exceeded"
		}
		if !s.generic.isProviderSchedulableForWindowCost(ctx, value, value.Record.ID == state.stickyID) {
			return "window_cost_exceeded"
		}
		if !s.generic.isProviderSchedulableForRPM(ctx, value, value.Record.ID == state.stickyID) {
			return "rpm_exceeded"
		}
		if s.generic.isProviderBlockedBySchedulingThreshold(ctx, value) {
			return "scheduling_threshold"
		}
	}
	if s.gemini != nil && value.Record.Platform == capability.PlatformGemini && !s.gemini.passesRateLimitPreCheckWithCache(ctx, value, model, nil) {
		return "gemini_quota_exhausted"
	}
	return ""
}

// withSelectionRequest captures group and model inputs for later credential
// hydration checks. The group pointer stays attached to this request value;
// callers provide a nonnil context and retain stable input identities.
func withSelectionRequest(ctx context.Context, groupID *int64, model string) context.Context {
	return context.WithValue(ctx, selectionRequestKey{}, selectionRequest{groupID: groupID, model: model})
}
