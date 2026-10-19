package selection

import (
	"context"
	"sync"

	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// candidate_policy.go evaluates compatible provider candidates against request
// policy and platform quota constraints.
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

// imageSelectionState retains capability failures across candidate attempts.
// Its mutex protects diagnostics when the scheduler evaluates candidates
// concurrently; any supported route keeps transient selection errors authoritative.
type imageSelectionState struct {
	// mu protects supported and err across concurrent candidate checks. A
	// successful declaration makes scheduler availability authoritative; otherwise
	// err retains the most informative local capability failure for presentation.
	mu        sync.Mutex // candidate observation lock
	supported bool       // observed supported image route
	err       error      // retained local capability failure
}

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

// observe records one candidate's capability outcome for the current selection.
// A successful declaration records that the pool contains an image-capable
// route, allowing the scheduler to report capacity or later eligibility failure.
// Other candidates can continue recording errors without clearing that evidence.
//
// Configuration-read failures outrank unknown declarations, which outrank known
// rejection. The mutex preserves that priority when candidate checks complete
// concurrently, and the state belongs to one selection including its native-to-
// basic protocol retry.
func (s *imageSelectionState) observe(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		s.supported = true
	} else if s.err == nil || apperror.Reason(err) == "MODEL_ATTRIBUTES_UNAVAILABLE" || apperror.Reason(s.err) == "MODEL_CAPABILITY_UNSUPPORTED" {
		s.err = err
	}
}

// failure returns a local error only when no inspected route supports images.
// A supported route leaves capacity, authentication, and protocol diagnostics
// to the scheduler even if other candidates lacked model attributes.
func (s *imageSelectionState) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.supported {
		return nil
	}
	return s.err
}

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
// first, followed by preferred-provider membership, admitted group membership,
// OAuth requirements, and final-route output capabilities.
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
	imageState, imageRequired := ctx.Value(imageModelRequiredKey{}).(*imageSelectionState)
	if imageRequired {
		model := gatewayadapter.ExecutionModelPolicy(value).UpstreamModel(ctx, model)
		resolution, err := gatewayadapter.ResolveModelAttributes(ctx, value, model, nil)
		if err == nil {
			err = routing.RequireImageOutput(resolution.Attributes)
		}
		imageState.observe(err)
		if err != nil {
			return apperror.Reason(err)
		}
	}
	if source, present := requeststate.ClientProtocolFromContext(ctx); !imageRequired && present && (source == capability.ProtocolOpenAIChatCompletions || source == capability.ProtocolAnthropicMessages) {
		model := gatewayadapter.ExecutionModelPolicy(value).UpstreamModel(ctx, model)
		resolution, err := gatewayadapter.ResolveModelAttributes(ctx, value, model, nil)
		if err != nil {
			return apperror.Reason(err)
		}
		if resolution.Attributes.OutputOnly("image") {
			return "MODEL_CAPABILITY_UNSUPPORTED"
		}
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
