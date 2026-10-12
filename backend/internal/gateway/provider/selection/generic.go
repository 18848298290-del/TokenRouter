package selection

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	schedulercore "github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

type selectionFailureStats struct {
	Total              int
	Eligible           int
	Excluded           int
	Unschedulable      int
	PlatformFiltered   int
	ModelUnsupported   int
	ModelRateLimited   int
	SamplePlatformIDs  []int64
	SampleMappingIDs   []int64
	SampleRateLimitIDs []string
}

type selectionFailureDiagnosis struct {
	Category string
	Detail   string
}

// SelectProviderForModel selects a model-compatible provider using session stickiness and priority.
func (s *Generic) SelectProviderForModel(ctx context.Context, groupID *int64, sessionHash string, requestedModel string) (*gatewayprovider.ExecutionProvider, error) {
	return s.SelectProviderForModelWithExclusions(ctx, groupID, sessionHash, requestedModel, nil)
}

// SelectProviderForModelWithExclusions selects a compatible provider outside the exclusion set.
func (s *Generic) SelectProviderForModelWithExclusions(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}) (*gatewayprovider.ExecutionProvider, error) {
	ctx = withSelectionRequest(ctx, groupID, requestedModel)
	core, scope := s.genericSelector()
	selected, err := core.SelectOnly(ctx, schedulercore.SelectionInput{GroupID: groupID, SessionHash: sessionHash, RequestedModel: requestedModel, ExcludedIDs: excludedIDs})
	return scope.oldProvider(selected), err
}

// SelectProviderWithLoadAwareness selects by load and may return a wait plan.
// metadataUserID and tokenRouterUserID are accepted for caller compatibility;
// this selection path does not use them.
// @project-doc docs/architecture/gateway_request_lifecycle.md#account_selection_and_failover
func (s *Generic) SelectProviderWithLoadAwareness(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, metadataUserID string, tokenRouterUserID int64) (*gatewayprovider.SelectionResult, error) {
	ctx = withSelectionRequest(ctx, groupID, requestedModel)
	core, scope := s.genericSelector()
	plan, _ := requeststate.RoutePlanFromContext(ctx)
	result, err := core.Select(ctx, schedulercore.SelectionInput{RoutePlan: plan, GroupID: groupID, SessionHash: sessionHash, RequestedModel: requestedModel, ExcludedIDs: excludedIDs})
	return scope.restore(result), err
}

// ReportAdvancedProviderScheduleResult records feedback for advanced selections.
// It uses the selection's captured feedback settings when present.
func (s *Generic) ReportAdvancedProviderScheduleResult(selection *gatewayprovider.SelectionResult, providerID int64, success bool, result *forwardcore.MessagesResult) {
	if s == nil || selection == nil || !selection.AdvancedScheduler || providerID <= 0 {
		return
	}
	var firstTokenMs *int
	if result != nil {
		firstTokenMs = result.FirstTokenMs
	}
	if selection.AdvancedSchedulerFeedback != nil {
		s.advancedSchedulerStats().Report(providerID, success, firstTokenMs, *selection.AdvancedSchedulerFeedback)
		return
	}
	s.advancedSchedulerStats().Report(providerID, success, firstTokenMs)
}

// RecordAdvancedProviderSwitch records a switch for an advanced selection.
func (s *Generic) RecordAdvancedProviderSwitch(selection *gatewayprovider.SelectionResult) {
	if s == nil || selection == nil || !selection.AdvancedScheduler {
		return
	}
	s.advancedSchedulerStats().ReportSwitch()
}

// advancedSchedulerStats returns shared runtime feedback, initializing it when absent.
func (s *Generic) advancedSchedulerStats() *schedulercore.RuntimeStats {
	if s == nil {
		return nil
	}
	if s.advancedProviderStats == nil {
		s.advancedProviderStats = schedulercore.NewRuntimeStats(time.Now)
	}
	return s.advancedProviderStats
}

// advancedSchedulerEffectiveSettingsForRequest applies final-group overrides to global scheduler settings.
func (s *Generic) advancedSchedulerEffectiveSettingsForRequest(ctx context.Context, id *int64) policy.EffectiveSettings {
	if ctx == nil {
		ctx = context.Background()
	}
	group := schedulerRequestGroup(ctx, id, s.schedulerSnapshot != nil, s.readSchedulingGroup)
	return s.schedulerParameters.Effective(ctx, schedulerGroupOverrides(group))
}

func (s *Generic) schedulingConfig() schedulercore.FlowOptions {
	return s.options.Scheduling
}

func (s *Generic) withGroupContext(ctx context.Context, group *routing.Group) context.Context {
	if !routing.IsGroupContextValid(group) {
		return ctx
	}
	if existing, ok := requeststate.GroupFromContext(ctx); ok && existing != nil && existing.ID == group.ID && routing.IsGroupContextValid(existing) {
		return ctx
	}
	return requeststate.WithGroup(ctx, group)
}

func (s *Generic) groupFromContext(ctx context.Context, groupID int64) *routing.Group {
	if group, ok := requeststate.GroupFromContext(ctx); ok && routing.IsGroupContextValid(group) && group.ID == groupID {
		return group
	}
	return nil
}

func (s *Generic) resolveGroupByID(ctx context.Context, groupID int64) (*routing.Group, error) {
	if group := s.groupFromContext(ctx, groupID); group != nil {
		return group, nil
	}
	group, err := s.groupRepo.GetByIDLite(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("get group failed: %w", err)
	}
	return group, nil
}

func (s *Generic) routingProviderIDsForRequest(ctx context.Context, groupID *int64, requestedModel string, platform string) []int64 {
	if groupID == nil || requestedModel == "" {
		return nil
	}
	group, err := s.resolveGroupByID(ctx, *groupID)
	if err != nil || group == nil {
		if s.debugModelRoutingEnabled() {
			logging.LegacyPrintf("service.gateway", "[ModelRoutingDebug] resolve group failed: group_id=%v model=%s platform=%s err=%v", derefGroupID(groupID), requestedModel, platform, err)
		}
		return nil
	}

	routingModel := s.groupMappedModelForGroup(ctx, groupID, requestedModel)
	ids := group.GetRoutingProviderIDs(routingModel)
	if s.debugModelRoutingEnabled() {
		logging.LegacyPrintf("service.gateway", "[ModelRoutingDebug] routing lookup: group_id=%d model=%s enabled=%v rules=%d matched_ids=%v",
			group.ID, requestedModel, group.ModelRoutingEnabled, len(group.ModelRouting), ids)
	}
	return ids
}

func (s *Generic) resolveGatewayGroup(ctx context.Context, groupID *int64) (*routing.Group, *int64, error) {
	group, err := currentSelectionGroup(ctx, groupID, s.resolveGroupByID)
	if err != nil {
		return nil, nil, err
	}
	return group, groupID, nil
}

// checkClaudeCodeRestriction validates client restrictions on the admitted group.
func (s *Generic) checkClaudeCodeRestriction(ctx context.Context, groupID *int64) (*routing.Group, *int64, error) {
	if groupID == nil {
		return nil, groupID, nil
	}

	group, resolvedID, err := s.resolveGatewayGroup(ctx, groupID)
	if err != nil {
		return nil, nil, err
	}

	return group, resolvedID, nil
}

func (s *Generic) resolvePlatform(ctx context.Context, groupID *int64, group *routing.Group) (string, bool, error) {
	if platform, forced := apikey.ForcePlatformFromContext(ctx); forced && platform != "" {
		return platform, true, nil
	}
	return "", false, nil
}

func (s *Generic) listSchedulableProviders(ctx context.Context, groupID *int64, platform string, hasForcePlatform bool) ([]gatewayprovider.ExecutionProvider, bool, error) {
	if groupID == nil || *groupID <= 0 {
		return nil, false, nil
	}
	var providers []gatewayprovider.ExecutionProvider
	var err error
	if s.schedulerSnapshot != nil {
		providers, _, err = readSnapshotProviders(ctx, s.schedulerSnapshot, groupID, platform, hasForcePlatform)
	} else if platform == "" {
		providers, err = s.providerRepo.ListSchedulableByGroupIDAndPlatforms(ctx, *groupID, capability.ProviderPlatforms())
	} else {
		providers, err = s.providerRepo.ListSchedulableByGroupIDAndPlatform(ctx, *groupID, platform)
	}
	if err != nil {
		return nil, false, err
	}
	providers = s.filterProvidersBySchedulingThreshold(ctx, providers)
	providers = s.filterGrokFreeQuotaProvidersForGateway(ctx, providers)
	return providers, false, nil
}

// IsSingleAntigravityProviderGroup reports whether exactly one schedulable Antigravity provider belongs to the group.
// Handlers use this to set SingleProviderRetry before the first attempt,
// so a 503 does not leave the sole provider model-limited for later requests.
func (s *Generic) IsSingleAntigravityProviderGroup(ctx context.Context, groupID *int64) bool {
	providers, _, err := s.listSchedulableProviders(ctx, groupID, capability.PlatformAntigravity, true)
	if err != nil {
		return false
	}
	return len(providers) == 1
}

func (s *Generic) isProviderAllowedForPlatform(provider *gatewayprovider.ExecutionProvider, platform string, useMixed bool) bool {
	return provider != nil && (platform == "" || provider.Record.Platform == platform)
}

func (s *Generic) isProviderSchedulableForSelection(provider *gatewayprovider.ExecutionProvider) bool {
	if provider == nil {
		return false
	}
	return provider.View().IsSchedulable()
}

func (s *Generic) isProviderSchedulableForModelSelection(ctx context.Context, provider *gatewayprovider.ExecutionProvider, requestedModel string) bool {
	if provider == nil {
		return false
	}
	routingModel := s.groupMappedModelForProviderLayer(ctx, requestedModel)
	return gatewayprovider.ExecutionModelPolicy(provider).Schedulable(ctx, routingModel)
}

// shouldClearStickySessionForProviderLayer checks stickiness using the group-mapped model.
func (s *Generic) shouldClearStickySessionForProviderLayer(ctx context.Context, provider *gatewayprovider.ExecutionProvider, requestedModel string) bool {
	routingModel := s.groupMappedModelForProviderLayer(ctx, requestedModel)
	return shouldClearStickySession(provider, routingModel)
}

// isProviderEligibleExceptModelSupport checks request eligibility before provider model-support rules.
func (s *Generic) isProviderEligibleExceptModelSupport(ctx context.Context, provider *gatewayprovider.ExecutionProvider, requestedModel string, platform string, excludedIDs map[int64]struct{}, useMixed bool, groupID *int64, schedGroup *routing.Group) bool {
	if !gatewayprovider.ExecutionModelPolicy(provider).AllowsProtocol(ctx) {
		return false
	}
	if provider == nil {
		return false
	}
	if excludedIDs != nil {
		if _, excluded := excludedIDs[provider.Record.ID]; excluded {
			return false
		}
	}
	if !s.isProviderSchedulableForSelection(provider) || !s.isProviderAllowedForPlatform(provider, platform, useMixed) {
		return false
	}
	if schedGroup != nil && schedGroup.RequirePrivacySet && !provider.View().IsPrivacySet() {
		return false
	}
	if !s.isProviderSchedulableForQuota(provider) || !s.isProviderSchedulableForWindowCost(ctx, provider, false) || !s.isProviderSchedulableForRPM(ctx, provider, false) {
		return false
	}
	if groupID != nil && s.needsUpstreamGroupRestrictionCheck(ctx, groupID) &&
		s.isUpstreamModelRestrictedByGroup(ctx, *groupID, provider, requestedModel) {
		return false
	}
	return true
}

// shouldUseGroupModelUnsupportedError reports whether otherwise eligible providers all reject the requested model.
func (s *Generic) shouldUseGroupModelUnsupportedError(ctx context.Context, providers []gatewayprovider.ExecutionProvider, requestedModel string, platform string, excludedIDs map[int64]struct{}, useMixed bool, groupID *int64, schedGroup *routing.Group) bool {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" || len(providers) == 0 {
		return false
	}
	hasRelevantProvider := false
	for i := range providers {
		acc := &providers[i]
		if !s.isProviderEligibleExceptModelSupport(ctx, acc, requestedModel, platform, excludedIDs, useMixed, groupID, schedGroup) {
			continue
		}
		hasRelevantProvider = true
		if s.isModelSupportedByProviderWithContext(ctx, acc, requestedModel) {
			return false
		}
	}
	return hasRelevantProvider
}

// groupModelUnsupportedErrorIfApplicable returns a typed group-model rejection when applicable.
func (s *Generic) groupModelUnsupportedErrorIfApplicable(ctx context.Context, providers []gatewayprovider.ExecutionProvider, requestedModel string, platform string, excludedIDs map[int64]struct{}, useMixed bool, groupID *int64, schedGroup *routing.Group) error {
	if s.shouldUseGroupModelUnsupportedError(ctx, providers, requestedModel, platform, excludedIDs, useMixed, groupID, schedGroup) {
		if err := routing.NewGroupModelRejection(platform, requestedModel, modelRejectionSources(providers)); err != nil {
			return err
		}
	}
	return nil
}

// isProviderInGroup checks if the provider belongs to the specified group.
// A nil or nonpositive group ID is rejected.
func (s *Generic) isProviderInGroup(provider *gatewayprovider.ExecutionProvider, groupID *int64) bool {
	return openAIStickyProviderMatchesGroup(provider, groupID)
}

func (s *Generic) tryAcquireProviderSlot(ctx context.Context, providerID int64, maxConcurrency int) (*schedulercore.AcquireResult, error) {
	if schedulercore.IsSelectOnly(ctx) {
		return &schedulercore.AcquireResult{Acquired: true, ReleaseFunc: func() {}}, nil
	}
	if s.concurrencyService == nil {
		return &schedulercore.AcquireResult{Acquired: true, ReleaseFunc: func() {}}, nil
	}
	return s.concurrencyService.AcquireProviderSlot(ctx, providerID, maxConcurrency)
}

func (s *Generic) withWindowCostPrefetch(ctx context.Context, providers []gatewayprovider.ExecutionProvider) context.Context {
	if ctx == nil || len(providers) == 0 || !s.windowPrefetchAvailable {
		return ctx
	}
	values := make([]billing.CostWindowInput, len(providers))
	for i := range providers {
		values[i] = costWindowInput(&providers[i])
	}
	return s.windowCostGuard().Prefetch(ctx, values)
}

// isProviderSchedulableForQuota checks configured quota limits for API Key and Bedrock providers.
// Other provider types pass this check.
func (s *Generic) isProviderSchedulableForQuota(provider *gatewayprovider.ExecutionProvider) bool {
	if !provider.View().IsAPIKeyOrBedrock() {
		return true
	}
	return !provider.View().IsQuotaExceeded()
}

// isProviderSchedulableForWindowCost checks the Anthropic OAuth/SetupToken window-cost limit.
// Sticky requests can use the configured reserve.
func (s *Generic) isProviderSchedulableForWindowCost(ctx context.Context, provider *gatewayprovider.ExecutionProvider, sticky bool) bool {
	return s.windowCostGuard().Allow(ctx, costWindowInput(provider), sticky)
}

// withRPMPrefetch prefetches RPM counts for Anthropic OAuth/SetupToken candidates with a positive base RPM.
func (s *Generic) withRPMPrefetch(ctx context.Context, providers []gatewayprovider.ExecutionProvider) context.Context {
	if s.rpmCache == nil {
		return ctx
	}

	var ids []int64
	for i := range providers {
		if providers[i].View().IsAnthropicOAuthOrSetupToken() && gatewayprovider.ExecutionRuntimeConfig(&providers[i]).GetBaseRPM() > 0 {
			ids = append(ids, providers[i].Record.ID)
		}
	}
	return schedulercore.PrefetchRPM(ctx, s.rpmCache, ids)
}

// isProviderSchedulableForRPM checks the RPM limit and sticky allowance.
// The check applies to Anthropic OAuth/SetupToken providers.
func (s *Generic) isProviderSchedulableForRPM(ctx context.Context, provider *gatewayprovider.ExecutionProvider, sticky bool) bool {
	if !provider.View().IsAnthropicOAuthOrSetupToken() {
		return true
	}
	return schedulercore.AllowProviderRPM(ctx, s.rpmCache, schedulercore.ProviderRPMInput{ID: provider.Record.ID, Enabled: true, Base: gatewayprovider.ExecutionRuntimeConfig(provider).GetBaseRPM(), Buffer: gatewayprovider.ExecutionRuntimeConfig(provider).GetRPMStickyBuffer(), Strategy: gatewayprovider.ExecutionRuntimeConfig(provider).GetRPMStrategy()}, sticky)
}

// IncrementProviderRPM increments the RPM counter for the given provider.
// Selection reads the count before this increment, so concurrent requests
// can temporarily exceed the RPM limit. This is a soft limit, like window cost;
// the check and increment do not hold a shared lock.
func (s *Generic) IncrementProviderRPM(ctx context.Context, id int64) error {
	return schedulercore.IncrementProviderRPM(ctx, s.rpmCache, id)
}

// checkAndRegisterSession checks and registers an Anthropic OAuth/SetupToken session.
// The session argument is the sticky-session hash.
// It permits existing sessions and new sessions within the configured limit.
// Select-only requests skip registration.
func (s *Generic) checkAndRegisterSession(ctx context.Context, provider *gatewayprovider.ExecutionProvider, session string) bool {
	if schedulercore.IsSelectOnly(ctx) {
		return true
	}
	return schedulercore.RegisterSession(ctx, s.sessionLimitCache, schedulerSessionBinding(provider, session))
}

// ReleaseProviderSession releases a registered session before its idle timeout.
// Handlers call it when an attempt fails or the client disconnects,
// so unsuccessful attempts do not occupy max_sessions capacity
// throughout the idle window.
// It uses the registration eligibility rules and tolerates repeated release calls.
func (s *Generic) ReleaseProviderSession(ctx context.Context, provider *gatewayprovider.ExecutionProvider, session string) {
	if s == nil {
		return
	}
	schedulercore.FinishSession(ctx, s.sessionLimitCache, schedulerSessionBinding(provider, session), schedulercore.AttemptOutcome{}, schedulercore.Diagnostics{
		Logf: logging.LegacyPrintf,

		Event: logging.Event,
	},
	)
}

func schedulerSessionBinding(provider *gatewayprovider.ExecutionProvider, session string) schedulercore.SessionBinding {
	if provider == nil {
		return schedulercore.SessionBinding{}
	}
	return schedulercore.SessionBinding{ProviderID: provider.Record.ID, SessionID: session, Enabled: provider.View().IsAnthropicOAuthOrSetupToken(), Limit: gatewayprovider.ExecutionRuntimeConfig(provider).GetMaxSessions(), IdleTimeout: time.Duration(gatewayprovider.ExecutionRuntimeConfig(provider).GetSessionIdleTimeoutMinutes()) * time.Minute}
}

func (s *Generic) getSchedulableProvider(ctx context.Context, providerID int64) (*gatewayprovider.ExecutionProvider, error) {
	var (
		provider *gatewayprovider.ExecutionProvider
		err      error
	)
	if s.schedulerSnapshot != nil {
		provider, err = readSnapshotProvider(ctx, s.schedulerSnapshot, providerID)
	} else {
		provider, err = s.providerRepo.GetByID(ctx, providerID)
	}
	if err != nil || provider == nil {
		return provider, err
	}
	if s.isProviderBlockedBySchedulingThreshold(ctx, provider) {
		return nil, nil
	}

	if provider.View().IsGrok() {
		if gated := s.filterGrokFreeQuotaProvidersForGateway(ctx, []gatewayprovider.ExecutionProvider{*provider}); len(gated) == 0 {
			return nil, nil
		}
	}
	return provider, nil
}

func (s *Generic) filterProvidersBySchedulingThreshold(ctx context.Context, providers []gatewayprovider.ExecutionProvider) []gatewayprovider.ExecutionProvider {
	if len(providers) == 0 {
		return providers
	}

	filtered := make([]gatewayprovider.ExecutionProvider, 0, len(providers))
	for i := range providers {
		if s.isProviderBlockedBySchedulingThreshold(ctx, &providers[i]) {
			continue
		}
		filtered = append(filtered, providers[i])
	}
	return filtered
}

func (s *Generic) isProviderBlockedBySchedulingThreshold(ctx context.Context, provider *gatewayprovider.ExecutionProvider) bool {
	if s == nil || s.healthObserver == nil || provider == nil {
		return false
	}
	return gatewayprovider.ApplyExecutionSchedulingThreshold(ctx, s.healthObserver, provider)
}

func (s *Generic) hydrateSelectedProvider(ctx context.Context, provider *gatewayprovider.ExecutionProvider) (*gatewayprovider.ExecutionProvider, error) {
	if provider == nil || s.schedulerSnapshot == nil {
		return provider, nil
	}
	var hydrated *gatewayprovider.ExecutionProvider
	var err error
	if s.providerRepo != nil {
		hydrated, err = s.providerRepo.GetByID(ctx, provider.Record.ID)
	} else {
		hydrated, err = readSnapshotProvider(ctx, s.schedulerSnapshot, provider.Record.ID)
	}
	if err != nil {
		return nil, err
	}
	if hydrated == nil {
		return nil, schedulercore.ErrNoAvailableProviders
	}
	if input, ok := ctx.Value(selectionRequestKey{}).(selectionRequest); ok {
		groupID := input.groupID
		if group, ok := requeststate.GroupFromContext(ctx); ok && group != nil {
			groupID = &group.ID
		}
		model := s.groupMappedModelForGroup(ctx, groupID, input.model)
		policy := gatewayprovider.ExecutionModelPolicy(hydrated)
		if !openAIStickyProviderMatchesGroup(hydrated, groupID) || !policy.Schedulable(ctx, model) || !policy.Supports(ctx, model) {
			return nil, schedulercore.ErrNoAvailableProviders
		}
	}
	return hydrated, nil
}

func (s *Generic) newSelectionResult(ctx context.Context, provider *gatewayprovider.ExecutionProvider, acquired bool, release func(), waitPlan *schedulercore.ProviderWaitPlan) (*gatewayprovider.SelectionResult, error) {
	attempt := schedulercore.NewAttemptLease(schedulercore.RequestLease(ctx), nil, release)
	if release != nil {
		release = attempt.Release
	}

	hydrated, err := s.hydrateSelectedProvider(ctx, provider)
	if err != nil {
		if release != nil {
			release()
		}
		return nil, err
	}
	selection := &gatewayprovider.SelectionResult{
		Provider:    hydrated,
		Acquired:    acquired,
		ReleaseFunc: release,
		WaitPlan:    waitPlan,
	}
	if group, ok := requeststate.GroupFromContext(ctx); ok && routing.IsGroupContextValid(group) && group.UsesAdvancedScheduler() {
		selection.AdvancedScheduler = true
		feedback := s.advancedSchedulerEffectiveSettingsForRequest(ctx, &group.ID).Feedback
		selection.AdvancedSchedulerFeedback = &feedback
	}
	return selection, nil
}

func (s *Generic) logDetailedSelectionFailure(
	ctx context.Context,
	groupID *int64,
	sessionHash string,
	requestedModel string,
	platform string,
	providers []gatewayprovider.ExecutionProvider,
	excludedIDs map[int64]struct{},
	allowMixedScheduling bool,
) selectionFailureStats {
	stats := s.collectSelectionFailureStats(ctx, providers, requestedModel, platform, excludedIDs, allowMixedScheduling)
	logging.LegacyPrintf(
		"service.gateway",
		"[SelectProviderDetailed] group_id=%v model=%s platform=%s session=%s total=%d eligible=%d excluded=%d unschedulable=%d platform_filtered=%d model_unsupported=%d model_rate_limited=%d sample_platform_filtered=%v sample_model_unsupported=%v sample_model_rate_limited=%v",
		derefGroupID(groupID),
		requestedModel,
		platform,
		shortSessionHash(sessionHash),
		stats.Total,
		stats.Eligible,
		stats.Excluded,
		stats.Unschedulable,
		stats.PlatformFiltered,
		stats.ModelUnsupported,
		stats.ModelRateLimited,
		stats.SamplePlatformIDs,
		stats.SampleMappingIDs,
		stats.SampleRateLimitIDs,
	)
	return stats
}

func (s *Generic) collectSelectionFailureStats(
	ctx context.Context,
	providers []gatewayprovider.ExecutionProvider,
	requestedModel string,
	platform string,
	excludedIDs map[int64]struct{},
	allowMixedScheduling bool,
) selectionFailureStats {
	stats := selectionFailureStats{
		Total: len(providers),
	}

	for i := range providers {
		acc := &providers[i]
		diagnosis := s.diagnoseSelectionFailure(ctx, acc, requestedModel, platform, excludedIDs, allowMixedScheduling)
		switch diagnosis.Category {
		case "excluded":
			stats.Excluded++
		case "unschedulable":
			stats.Unschedulable++
		case "platform_filtered":
			stats.PlatformFiltered++
			stats.SamplePlatformIDs = appendSelectionFailureSampleID(stats.SamplePlatformIDs, acc.Record.ID)
		case "model_unsupported":
			stats.ModelUnsupported++
			stats.SampleMappingIDs = appendSelectionFailureSampleID(stats.SampleMappingIDs, acc.Record.ID)
		case "model_rate_limited":
			stats.ModelRateLimited++
			routingModel := s.groupMappedModelForProviderLayer(ctx, requestedModel)
			remaining := gatewayprovider.ExecutionModelPolicy(acc).LimitRemaining(ctx, routingModel).Truncate(time.Second)
			stats.SampleRateLimitIDs = appendSelectionFailureRateSample(stats.SampleRateLimitIDs, acc.Record.ID, remaining)
		default:
			stats.Eligible++
		}
	}

	return stats
}

func (s *Generic) diagnoseSelectionFailure(
	ctx context.Context,
	acc *gatewayprovider.ExecutionProvider,
	requestedModel string,
	platform string,
	excludedIDs map[int64]struct{},
	allowMixedScheduling bool,
) selectionFailureDiagnosis {
	if acc == nil {
		return selectionFailureDiagnosis{Category: "unschedulable", Detail: "provider_nil"}
	}
	if _, excluded := excludedIDs[acc.Record.ID]; excluded {
		return selectionFailureDiagnosis{Category: "excluded"}
	}
	if !s.isProviderSchedulableForSelection(acc) {
		return selectionFailureDiagnosis{Category: "unschedulable", Detail: "generic_unschedulable"}
	}
	if isPlatformFilteredForSelection(acc, platform, allowMixedScheduling) {
		return selectionFailureDiagnosis{
			Category: "platform_filtered",
			Detail:   fmt.Sprintf("provider_platform=%s requested_platform=%s", acc.Record.Platform, strings.TrimSpace(platform)),
		}
	}
	if requestedModel != "" && !s.isModelSupportedByProviderWithContext(ctx, acc, requestedModel) {
		return selectionFailureDiagnosis{
			Category: "model_unsupported",
			Detail:   fmt.Sprintf("model=%s", requestedModel),
		}
	}
	if !s.isProviderSchedulableForModelSelection(ctx, acc, requestedModel) {
		routingModel := s.groupMappedModelForProviderLayer(ctx, requestedModel)
		remaining := gatewayprovider.ExecutionModelPolicy(acc).LimitRemaining(ctx, routingModel).Truncate(time.Second)
		return selectionFailureDiagnosis{
			Category: "model_rate_limited",
			Detail:   fmt.Sprintf("remaining=%s", remaining),
		}
	}
	return selectionFailureDiagnosis{Category: "eligible"}
}

func isPlatformFilteredForSelection(acc *gatewayprovider.ExecutionProvider, platform string, allowMixedScheduling bool) bool {
	return acc == nil || platform != "" && acc.Record.Platform != platform
}

func appendSelectionFailureSampleID(samples []int64, id int64) []int64 {
	const limit = 5
	if len(samples) >= limit {
		return samples
	}
	return append(samples, id)
}

func appendSelectionFailureRateSample(samples []string, providerID int64, remaining time.Duration) []string {
	const limit = 5
	if len(samples) >= limit {
		return samples
	}
	return append(samples, fmt.Sprintf("%d(%s)", providerID, remaining))
}

func summarizeSelectionFailureStats(stats selectionFailureStats) string {
	return fmt.Sprintf(
		"total=%d eligible=%d excluded=%d unschedulable=%d platform_filtered=%d model_unsupported=%d model_rate_limited=%d",
		stats.Total,
		stats.Eligible,
		stats.Excluded,
		stats.Unschedulable,
		stats.PlatformFiltered,
		stats.ModelUnsupported,
		stats.ModelRateLimited,
	)
}

// isModelSupportedByProviderWithContext checks provider support after group mapping.
func (s *Generic) isModelSupportedByProviderWithContext(ctx context.Context, provider *gatewayprovider.ExecutionProvider, requestedModel string) bool {
	routingModel := s.groupMappedModelForProviderLayer(ctx, requestedModel)
	return s.isRoutingModelSupportedByProviderWithContext(ctx, provider, routingModel)
}

// isRoutingModelSupportedByProviderWithContext checks an already group-mapped model without applying group mapping again.
func (s *Generic) isRoutingModelSupportedByProviderWithContext(ctx context.Context, provider *gatewayprovider.ExecutionProvider, routingModel string) bool {
	return gatewayprovider.ExecutionModelPolicy(provider).Supports(ctx, routingModel)
}

func (s *Generic) groupMappedModelForProviderLayer(ctx context.Context, requestedModel string) string {
	if s == nil || s.groupPolicies == nil || strings.TrimSpace(requestedModel) == "" {
		return requestedModel
	}
	group, ok := requeststate.GroupFromContext(ctx)
	if !ok || !routing.IsGroupContextValid(group) {
		return requestedModel
	}
	groupID := group.ID
	return s.groupMappedModelForGroup(ctx, &groupID, requestedModel)
}

// NewSessionAttempts creates a request-owned set of session attempts to finish.
func (s *Generic) NewSessionAttempts() *schedulercore.SessionAttempts {
	return schedulercore.NewSessionAttempts(s.sessionLimitCache, schedulercore.Diagnostics{
		Logf: logging.LegacyPrintf,

		Event: logging.Event,
	},
	)
}

// TrackSessionAttempt records a provider and session in the request's attempt set.
func (s *Generic) TrackSessionAttempt(attempts *schedulercore.SessionAttempts, provider *gatewayprovider.ExecutionProvider, session string) {
	attempts.Track(schedulerSessionBinding(provider, session))
}

// costWindowInput converts provider window bounds and limits to billing inputs.
func costWindowInput(a *gatewayprovider.ExecutionProvider) billing.CostWindowInput {
	if a == nil {
		return billing.CostWindowInput{}
	}
	return billing.CostWindowInput{ID: a.Record.ID, Enabled: a.View().IsAnthropicOAuthOrSetupToken(), Limit: gatewayprovider.ExecutionRuntimeConfig(a).GetWindowCostLimit(), Reserve: gatewayprovider.ExecutionRuntimeConfig(a).GetWindowCostStickyReserve(), Start: a.Record.SessionWindowStart, End: a.Record.SessionWindowEnd}
}

// readSchedulingGroup reads full scheduling settings from the group repository.
func (s *Generic) readSchedulingGroup(ctx context.Context, id int64) (*routing.Group, error) {
	if s.groupRepo == nil {
		return nil, nil
	}
	return s.groupRepo.GetByID(ctx, id)
}
