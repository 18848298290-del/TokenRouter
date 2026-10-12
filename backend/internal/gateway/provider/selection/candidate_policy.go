package selection

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

type (
	candidatePolicyKey    struct{}
	imageModelRequiredKey struct{}
	preferredProvidersKey struct{}
	candidatePolicy       struct {
		groupID  *int64
		stickyID int64
	}
)

type (
	selectionRequestKey struct{}
	selectionRequest    struct {
		groupID *int64
		model   string
	}
)

// withCandidatePolicy captures the group and sticky provider for window-cost, RPM, and eligibility rechecks.
func (s *Compatible) withCandidatePolicy(ctx context.Context, groupID *int64, session string) context.Context {
	state := candidatePolicy{groupID: groupID}
	if session != "" && s.cache != nil {
		state.stickyID, _ = s.getStickySessionProviderID(ctx, groupID, session)
	}
	return context.WithValue(ctx, candidatePolicyKey{}, state)
}

// candidateEligibilityReason applies platform limits after protocol and model eligibility checks.
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

// withSelectionRequest captures the group and model for eligibility checks after credential hydration.
func withSelectionRequest(ctx context.Context, groupID *int64, model string) context.Context {
	return context.WithValue(ctx, selectionRequestKey{}, selectionRequest{groupID: groupID, model: model})
}
