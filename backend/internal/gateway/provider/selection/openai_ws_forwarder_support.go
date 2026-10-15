package selection

import (
	"context"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	schedulercore "github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// openai_ws_forwarder_support.go resolves previous-response provider affinity
// and returns an acquired slot or wait plan.
// A previous OpenAI response binds continuation to the provider that created
// its state. Response lookup is group-scoped and uses the already resolved
// routing model; provider mapping, endpoint capability, privacy, parent health,
// and optional database rechecks decide whether that binding can serve this
// request.
//
// Permanent target failures and selected runtime failures remove the stale
// response binding best-effort. A request-specific model, capability, privacy,
// or quota rejection can leave the binding available for a different
// continuation request. After eligibility, an acquired slot refreshes the
// response affinity, while a busy provider yields a bounded wait plan so the
// caller can retain the chain's selected provider.

// selectProviderByPreviousResponseIDForCapability validates a response-bound
// provider for an already mapped routing model. Missing or unusable bindings
// return no selection; a usable binding then attempts provider capacity
// acquisition.
//
// Successful acquisition refreshes the response binding and returns its release
// handle for the caller's attempt. Binding-write failure is logged and leaves
// current acquisition usable. A busy or unsuccessful acquisition with a
// concurrency service returns a wait plan for that same provider, keeping
// continuation state associated with the creating target while the caller
// awaits capacity.
func (s *Compatible) selectProviderByPreviousResponseIDForCapability(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	routingModel string,
	excludedIDs map[int64]struct{},
	requiredCapability providercore.OpenAIEndpointCapability,
	requireCompact bool,
) (*gatewayprovider.SelectionResult, error) {
	if s == nil {
		return nil, nil
	}
	providerID, provider, responseID, store := s.resolveProviderByPreviousResponseIDForCapability(
		ctx,
		groupID,
		previousResponseID,
		routingModel,
		excludedIDs,
		requiredCapability,
		requireCompact,
	)
	if providerID <= 0 || provider == nil || store == nil {
		return nil, nil
	}

	// Continuation needs this provider's stored response state. When immediate
	// acquisition is unavailable, retain this target in the wait plan below.
	result, acquireErr := s.tryAcquireProviderSlot(ctx, providerID, provider.Record.Concurrency)
	if acquireErr == nil && result.Acquired {
		// Affinity refresh follows slot acquisition. A write failure is logged while
		// the selected response-chain target remains usable for the current attempt.
		gatewayprovider.LogOpenAIWSBindResponseProviderWarn(
			derefGroupID(groupID),
			providerID,
			responseID,
			store.BindResponseProvider(ctx, derefGroupID(groupID), responseID, providerID, s.OpenAIHTTPResponseStickyTTL()),
		)
		return &gatewayprovider.SelectionResult{
			Provider:    provider,
			Acquired:    true,
			ReleaseFunc: result.ReleaseFunc,
		}, nil
	}

	cfg := s.schedulingConfig()
	if s.concurrencyService != nil {
		return &gatewayprovider.SelectionResult{
			Provider: provider,
			WaitPlan: &schedulercore.ProviderWaitPlan{
				ProviderID:     providerID,
				MaxConcurrency: provider.Record.Concurrency,
				Timeout:        cfg.StickySessionWaitTimeout,
				MaxWaiting:     cfg.StickySessionMaxWaiting,
			},
		}, nil
	}
	return nil, nil
}

// ResolveProviderIDByPreviousResponseIDForScheduler returns the eligible
// response-chain provider ID for an already mapped model. It captures the
// group's privacy rule before lookup; missing or rejected bindings return zero
// for ordinary scheduler handling.
func (s *Compatible) ResolveProviderIDByPreviousResponseIDForScheduler(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	routingModel string,
	excludedIDs map[int64]struct{},
	requiredCapability providercore.OpenAIEndpointCapability,
	requireCompact bool,
) int64 {
	ctx = s.withOpenAIGroupPrivacyRequirement(ctx, groupID)
	providerID, _, _, _ := s.resolveProviderByPreviousResponseIDForCapability(
		ctx,
		groupID,
		previousResponseID,
		routingModel,
		excludedIDs,
		requiredCapability,
		requireCompact,
	)
	return providerID
}

// resolveProviderByPreviousResponseIDForCapability returns the group-scoped
// provider binding, checked target, trimmed response ID, and store. Missing
// inputs, reads, or eligibility return an empty result so callers can try their
// other selection paths.
//
// The target must support continuation transport, protocol, model, capability,
// quota, shadow health, privacy, and applicable upstream group restrictions.
// Paired snapshot and repository readers repeat relevant checks on the latest
// database record and use that target afterward. Missing records, unschedulable
// targets, unhealthy parents, database runtime blocks, and disabled Compact
// trigger best-effort deletion; request-specific compatibility failures can
// leave the binding intact.
func (s *Compatible) resolveProviderByPreviousResponseIDForCapability(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	routingModel string,
	excludedIDs map[int64]struct{},
	requiredCapability providercore.OpenAIEndpointCapability,
	requireCompact bool,
) (int64, *gatewayprovider.ExecutionProvider, string, session.OpenAIWSStateStore) {
	if s == nil {
		return 0, nil, "", nil
	}
	responseID := strings.TrimSpace(previousResponseID)
	if responseID == "" {
		return 0, nil, "", nil
	}
	routingModel = strings.TrimSpace(routingModel)
	store := s.ResponseStateStore()
	if store == nil {
		return 0, nil, "", nil
	}

	// Response affinity is optional selection information. An unreadable binding
	// yields no continuation target so the caller can evaluate its other selection paths.
	providerID, err := store.GetResponseProvider(ctx, derefGroupID(groupID), responseID)
	if err != nil || providerID <= 0 {
		return 0, nil, "", nil
	}
	if excludedIDs != nil {
		if _, excluded := excludedIDs[providerID]; excluded {
			return 0, nil, "", nil
		}
	}

	provider, err := s.getSchedulableProvider(ctx, providerID)
	if err != nil || provider == nil {
		// An unavailable full-provider result cannot validate continuation credentials.
		// Remove the recorded affinity best-effort before returning no usable target.
		_ = store.DeleteResponseProvider(ctx, derefGroupID(groupID), responseID)
		return 0, nil, "", nil
	}

	if s.ResolveTransport(provider).Transport != egress.OpenAIUpstreamTransportResponsesWebsocketV2 && !provider.View().IsOpenAIApiKey() {
		return 0, nil, "", nil
	}
	if shouldClearStickySession(provider, routingModel) || !provider.View().IsOpenAI() || !provider.View().IsSchedulable() {
		// Provider status, platform, or model cooldown invalidates this continuation.
		// Remove its affinity so later requests cannot keep retrying that unusable target.
		_ = store.DeleteResponseProvider(ctx, derefGroupID(groupID), responseID)
		return 0, nil, "", nil
	}
	if (hasOpenAIProviderGroupMetadata(provider) && !s.openAIProviderMatchesSchedulingGroup(provider, groupID)) || !s.openAIProviderPassesPrivacyRequirement(ctx, groupID, provider) {
		return 0, nil, "", nil
	}
	if !s.shadowProtocolsAllowed(ctx, provider) || !providercore.ParentHealthyForShadow(gatewayprovider.ExecutionRecord(provider), func(id int64) *providercore.Record {
		return gatewayprovider.ExecutionRecord(s.parentProviderLookup(ctx)(id))
	}) {
		// The shadow cannot use this parent's credentials or protocol policy for
		// continuation. Clear affinity best-effort until selection can establish a usable parent.
		_ = store.DeleteResponseProvider(ctx, derefGroupID(groupID), responseID)
		return 0, nil, "", nil
	}
	if !gatewayprovider.ExecutionModelPolicy(provider).SupportsCompatibleRouting(ctx, routingModel) {
		return 0, nil, "", nil
	}
	if !provideradapter.SupportsOpenAIEndpoint(gatewayprovider.ExecutionProtocolRecord(provider), requiredCapability) {
		return 0, nil, "", nil
	}

	if paused, _ := gatewayprovider.OpenAIQuotaPause(ctx, provider); paused {
		return 0, nil, "", nil
	}
	if s.schedulerSnapshot != nil && s.providerRepo != nil {
		latest, latestErr := s.providerRepo.GetByID(ctx, provider.Record.ID)
		if latestErr != nil || latest == nil {
			// A stale snapshot cannot replace the unavailable database target. Clear
			// its recorded response affinity before the caller evaluates another selection path.
			_ = store.DeleteResponseProvider(ctx, derefGroupID(groupID), responseID)
			return 0, nil, "", nil
		}
		if shouldClearStickySession(latest, routingModel) || !latest.View().IsOpenAI() || !latest.View().IsSchedulable() {
			// The database record revokes status, platform, or model eligibility that
			// the snapshot supplied. Remove affinity so that stale observation cannot drive continuation.
			_ = store.DeleteResponseProvider(ctx, derefGroupID(groupID), responseID)
			return 0, nil, "", nil
		}
		if (hasOpenAIProviderGroupMetadata(latest) && !s.openAIProviderMatchesSchedulingGroup(latest, groupID)) || !s.openAIProviderPassesPrivacyRequirement(ctx, groupID, latest) {
			return 0, nil, "", nil
		}
		if !s.shadowProtocolsAllowed(ctx, latest) || !providercore.ParentHealthyForShadow(gatewayprovider.ExecutionRecord(latest), func(id int64) *providercore.Record {
			return gatewayprovider.ExecutionRecord(s.parentProviderLookup(ctx)(id))
		}) {
			// Fresh target checks still require usable parent credentials and protocol
			// policy. Remove this shadow's response affinity when that relationship fails validation.
			_ = store.DeleteResponseProvider(ctx, derefGroupID(groupID), responseID)
			return 0, nil, "", nil
		}
		if !gatewayprovider.ExecutionModelPolicy(latest).SupportsCompatibleRouting(ctx, routingModel) {
			return 0, nil, "", nil
		}
		if !provideradapter.SupportsOpenAIEndpoint(gatewayprovider.ExecutionProtocolRecord(latest), requiredCapability) {
			return 0, nil, "", nil
		}
		if paused, _ := gatewayprovider.OpenAIQuotaPause(ctx, latest); paused {
			return 0, nil, "", nil
		}
		if s.isOpenAIProviderRequestRuntimeBlocked(latest, routingModel) {
			// Database continuation rechecks observe current credential and model blocks.
			// Invalidate affinity when either prevents this latest target from accepting the request.
			_ = store.DeleteResponseProvider(ctx, derefGroupID(groupID), responseID)
			return 0, nil, "", nil
		}
		provider = latest
	}
	if requireCompact && !gatewayprovider.AllowsCompatibleCompact(provider) {
		// Disabled Compact prevents this continuation target from serving the requested
		// endpoint. Delete the affinity best-effort before ordinary candidate selection resumes.
		_ = store.DeleteResponseProvider(ctx, derefGroupID(groupID), responseID)
		return 0, nil, "", nil
	}
	if groupID != nil && s.NeedsUpstreamGroupRestriction(ctx, groupID) &&
		s.UpstreamRoutingModelRestricted(ctx, *groupID, provider, routingModel, requireCompact) {
		return 0, nil, "", nil
	}
	return providerID, provider, responseID, store
}

// ResolveTransport delegates egress transport resolution using the current
// execution target's protocol view. It supplies the target's concurrency when a
// view exists, and nil targets are handled by the shared transport rules.
func (s *Compatible) ResolveTransport(value *gatewayprovider.ExecutionProvider) egress.OpenAIWSProtocolDecision {
	view := gatewayprovider.ExecutionProtocolRecord(value)
	if view !=
		nil {
		view.Concurrency = value.Record.Concurrency
	}
	return gatewayprovider.ResolveOpenAIWSTransport(view)
}
