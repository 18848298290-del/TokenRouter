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

// selectProviderByPreviousResponseIDForCapability validates the response-chain provider using an already mapped routing model.
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

	result, acquireErr := s.tryAcquireProviderSlot(ctx, providerID, provider.Record.Concurrency)
	if acquireErr == nil && result.Acquired {
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

// ResolveProviderIDByPreviousResponseIDForScheduler resolves a provider eligible to continue the response chain for the routing model.
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

// resolveProviderByPreviousResponseIDForCapability checks the bound provider's model, capabilities, and group restrictions.
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
		_ = store.DeleteResponseProvider(ctx, derefGroupID(groupID), responseID)
		return 0, nil, "", nil
	}

	if s.ResolveTransport(provider).Transport != egress.OpenAIUpstreamTransportResponsesWebsocketV2 && !provider.View().IsOpenAIApiKey() {
		return 0, nil, "", nil
	}
	if shouldClearStickySession(provider, routingModel) || !provider.View().IsOpenAI() || !provider.View().IsSchedulable() {
		_ = store.DeleteResponseProvider(ctx, derefGroupID(groupID), responseID)
		return 0, nil, "", nil
	}
	if (hasOpenAIProviderGroupMetadata(provider) && !s.openAIProviderMatchesSchedulingGroup(provider, groupID)) || !s.openAIProviderPassesPrivacyRequirement(ctx, groupID, provider) {
		return 0, nil, "", nil
	}
	if !s.shadowProtocolsAllowed(ctx, provider) || !providercore.ParentHealthyForShadow(gatewayprovider.ExecutionRecord(provider), func(id int64) *providercore.Record {
		return gatewayprovider.ExecutionRecord(s.parentProviderLookup(ctx)(id))
	}) {
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
			_ = store.DeleteResponseProvider(ctx, derefGroupID(groupID), responseID)
			return 0, nil, "", nil
		}
		if shouldClearStickySession(latest, routingModel) || !latest.View().IsOpenAI() || !latest.View().IsSchedulable() {
			_ = store.DeleteResponseProvider(ctx, derefGroupID(groupID), responseID)
			return 0, nil, "", nil
		}
		if (hasOpenAIProviderGroupMetadata(latest) && !s.openAIProviderMatchesSchedulingGroup(latest, groupID)) || !s.openAIProviderPassesPrivacyRequirement(ctx, groupID, latest) {
			return 0, nil, "", nil
		}
		if !s.shadowProtocolsAllowed(ctx, latest) || !providercore.ParentHealthyForShadow(gatewayprovider.ExecutionRecord(latest), func(id int64) *providercore.Record {
			return gatewayprovider.ExecutionRecord(s.parentProviderLookup(ctx)(id))
		}) {
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
			_ = store.DeleteResponseProvider(ctx, derefGroupID(groupID), responseID)
			return 0, nil, "", nil
		}
		provider = latest
	}
	if requireCompact && !gatewayprovider.AllowsCompatibleCompact(provider) {
		_ = store.DeleteResponseProvider(ctx, derefGroupID(groupID), responseID)
		return 0, nil, "", nil
	}
	if groupID != nil && s.NeedsUpstreamGroupRestriction(ctx, groupID) &&
		s.UpstreamRoutingModelRestricted(ctx, *groupID, provider, routingModel, requireCompact) {
		return 0, nil, "", nil
	}
	return providerID, provider, responseID, store
}

// ResolveTransport applies egress transport rules to the current provider configuration.
func (s *Compatible) ResolveTransport(value *gatewayprovider.ExecutionProvider) egress.OpenAIWSProtocolDecision {
	view := gatewayprovider.ExecutionProtocolRecord(value)
	if view !=
		nil {
		view.Concurrency = value.Record.Concurrency
	}
	return gatewayprovider.ResolveOpenAIWSTransport(view)
}
