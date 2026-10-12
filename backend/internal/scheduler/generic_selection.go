package scheduler

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

const stickySessionTTL = time.Hour

var ErrNoAvailableProviders = errors.New("no available providers")

// FlowProvider contains selection data; ProjectionID lets the caller find the corresponding execution target.
type FlowProvider struct {
	Plan                                       *routing.CandidatePlan
	ProjectionID                               uint64
	ID                                         int64
	Name, Platform, Type                       string
	Concurrency, Priority, LoadFactor, BaseRPM int
	PrivacySet                                 bool
	LastUsedAt, SessionWindowEnd               *time.Time
}

type FlowGroup struct {
	routing.Group
	ProjectionID uint64
}
type ProviderWaitPlan struct {
	ProviderID     int64
	MaxConcurrency int
	Timeout        time.Duration
	MaxWaiting     int
}
type FlowSelection struct {
	Provider                  *FlowProvider
	Acquired                  bool
	ReleaseFunc               func()
	WaitPlan                  *ProviderWaitPlan
	AdvancedScheduler         bool
	AdvancedSchedulerFeedback *policy.FeedbackConfig
}
type FlowLoad struct {
	Provider *FlowProvider
	LoadInfo *ProviderLoadInfo
}
type FlowOptions struct {
	LoadBatchEnabled, PreferSoonestReset          bool
	FallbackMaxWaiting, StickySessionMaxWaiting   int
	FallbackSelectionMode                         string
	FallbackWaitTimeout, StickySessionWaitTimeout time.Duration
}

// GenericSelectionPorts supplies eligibility checks, routing conversions, and provider access. GenericSelector orders these operations.
type GenericSelectionPorts struct {
	ReadGroup                    func(context.Context, int64) (*FlowGroup, error)
	ForcePlatform                func(context.Context) (string, bool)
	ResolveGroupByID             func(context.Context, int64) (*FlowGroup, error)
	ResolveGatewayGroup          func(context.Context, *int64) (*FlowGroup, *int64, error)
	HydrateSelectedProvider      func(context.Context, *FlowProvider) (*FlowProvider, error)
	RoutingProviderIDsForRequest func(context.Context, *int64, string, string) []int64
	GetSchedulableProvider       func(context.Context, int64) (*FlowProvider, error)
	IsProviderInGroup            func(*FlowProvider, *int64) bool
	LogDetailedSelectionFailure  func(context.Context, *int64, string, string, string, []FlowProvider, map[int64]struct{}, bool) string

	SelectProviderForModelWithExclusions         func(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}) (*FlowProvider, error)
	AdvancedSchedulerEffectiveSettingsForRequest func(ctx context.Context, groupID *int64) policy.EffectiveSettings
	AdvancedSchedulerStats                       func() *RuntimeStats
	GroupMappedModelForProviderLayer             func(ctx context.Context, requestedModel string) string
	CheckAndRegisterSession                      func(ctx context.Context, provider *FlowProvider, session string) bool
	CheckGroupModelRestriction                   func(ctx context.Context, groupID *int64, requestedModel string) bool
	CheckClaudeCodeRestriction                   func(ctx context.Context, groupID *int64) (*FlowGroup, *int64, error)
	DebugModelRoutingEnabled                     func() bool
	GroupModelUnsupportedErrorIfApplicable       func(ctx context.Context, providers []FlowProvider, requestedModel string, platform string, excludedIDs map[int64]struct{}, useMixed bool, groupID *int64, schedGroup *FlowGroup) error
	IsProviderAllowedForPlatform                 func(provider *FlowProvider, platform string, useMixed bool) bool
	IsProviderSchedulableForModelSelection       func(ctx context.Context, provider *FlowProvider, requestedModel string) bool
	IsProviderSchedulableForQuota                func(provider *FlowProvider) bool
	IsProviderSchedulableForRPM                  func(ctx context.Context, provider *FlowProvider, sticky bool) bool
	IsProviderSchedulableForSelection            func(provider *FlowProvider) bool
	IsProviderSchedulableForWindowCost           func(ctx context.Context, provider *FlowProvider, sticky bool) bool
	IsModelSupportedByProviderWithContext        func(ctx context.Context, provider *FlowProvider, requestedModel string) bool
	IsUpstreamModelRestrictedByGroup             func(ctx context.Context, groupID int64, provider *FlowProvider, requestedModel string) bool
	ListSchedulableProviders                     func(ctx context.Context, groupID *int64, platform string, hasForcePlatform bool) ([]FlowProvider, bool, error)
	NeedsUpstreamGroupRestrictionCheck           func(ctx context.Context, groupID *int64) bool
	NewSelectionResult                           func(ctx context.Context, provider *FlowProvider, acquired bool, release func(), waitPlan *ProviderWaitPlan) (*FlowSelection, error)
	ResolvePlatform                              func(ctx context.Context, groupID *int64, group *FlowGroup) (string, bool, error)
	SchedulingConfig                             func() FlowOptions
	ShouldClearStickySessionForProviderLayer     func(ctx context.Context, provider *FlowProvider, requestedModel string) bool
	TryAcquireProviderSlot                       func(ctx context.Context, providerID int64, maxConcurrency int) (*AcquireResult, error)
	WithGroupContext                             func(ctx context.Context, group *FlowGroup) context.Context
	WithRPMPrefetch                              func(ctx context.Context, providers []FlowProvider) context.Context
	WithWindowCostPrefetch                       func(ctx context.Context, providers []FlowProvider) context.Context
	SetProviderError                             func(context.Context, int64, string) error
	PrefetchedSticky                             func(context.Context, *int64) int64
}

// GenericSelector selects providers using the counters, sticky cache, and feedback supplied by app.
type GenericSelector struct {
	ports              GenericSelectionPorts
	cache              StickyCache
	concurrencyService *ConcurrencyService
	diagnostics        Diagnostics
	now                func() time.Time
}

func (a *FlowProvider) IsPrivacySet() bool       { return a.PrivacySet }
func (a *FlowProvider) EffectiveLoadFactor() int { return a.LoadFactor }
func (a *FlowProvider) GetBaseRPM() int          { return a.BaseRPM }

func NewGenericSelector(ports GenericSelectionPorts, cache StickyCache, concurrency *ConcurrencyService, diagnostics Diagnostics, now func() time.Time) *GenericSelector {
	return &GenericSelector{ports: ports, cache: cache, concurrencyService: concurrency, diagnostics: diagnostics, now: now}
}

func derefGroupID(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
}

func (s *GenericSelector) Select(ctx context.Context, input SelectionInput) (*FlowSelection, error) {
	groupID, sessionHash, requestedModel, excludedIDs := input.GroupID, input.SessionHash, input.RequestedModel, input.ExcludedIDs

	excludedIDsList := make([]int64, 0, len(excludedIDs))
	for id := range excludedIDs {
		excludedIDsList = append(excludedIDsList, id)
	}
	s.diagnostics.event("debug", "provider_scheduling_starting",
		"group_id", derefGroupID(groupID),
		"model", requestedModel,
		"session", shortFlowSessionHash(sessionHash),
		"excluded_ids", excludedIDsList)

	cfg := s.ports.SchedulingConfig()

	group, groupID, err := s.ports.CheckClaudeCodeRestriction(ctx, groupID)
	if err != nil {
		return nil, err
	}
	ctx = s.ports.WithGroupContext(ctx, group)
	usesAdvancedScheduler := group != nil && group.UsesAdvancedScheduler()
	// Weighted stickiness contributes to advanced scores; otherwise the hard binding is tried first.
	advancedStickyWeighted := usesAdvancedScheduler && s.ports.AdvancedSchedulerEffectiveSettingsForRequest(ctx, groupID).StickyWeightedEnabled

	if s.ports.CheckGroupModelRestriction(ctx, groupID, requestedModel) {
		s.diagnostics.event("warn", "group model restriction blocked request",
			"group_id", derefGroupID(groupID),
			"model", requestedModel)
		return nil, fmt.Errorf("%w supporting model: %s (group model restriction)", ErrNoAvailableProviders, requestedModel)
	}

	var stickyProviderID int64
	var stickySource string
	if prefetch := s.ports.PrefetchedSticky(ctx, groupID); prefetch > 0 {
		stickyProviderID = prefetch
		stickySource = "prefetch"
	} else if sessionHash != "" && s.cache != nil {
		if providerID, err := s.cache.GetSessionProviderID(ctx, derefGroupID(groupID), sessionHash); err == nil {
			stickyProviderID = providerID
			stickySource = "cache"
		}
	}
	stickyEscaped := false
	if usesAdvancedScheduler && !advancedStickyWeighted && stickyProviderID > 0 {
		escapeCfg := s.ports.AdvancedSchedulerEffectiveSettingsForRequest(ctx, groupID).StickyEscape
		if reason, errorRate, ttft, shouldEscape := ShouldEscapeSticky(s.ports.AdvancedSchedulerStats(), stickyProviderID, escapeCfg); shouldEscape {
			stickyEscaped = true
			ctx = WithPreservedSticky(ctx)
			s.diagnostics.event("info", "sticky_escape_triggered",
				"provider_id", stickyProviderID,
				"reason", reason,
				"error_rate", errorRate,
				"ttft", ttft,
			)
		}
	}

	s.diagnostics.event("info", "sticky.scheduler_entry",
		"group_id", derefGroupID(groupID),
		"session_hash", shortFlowSessionHash(sessionHash),
		"sticky_provider_id", stickyProviderID,
		"sticky_source", stickySource,
		"model", requestedModel,
		"load_batch", cfg.LoadBatchEnabled,
		"has_concurrency_svc", s.concurrencyService != nil,
		"excluded_count", len(excludedIDs),
	)

	if s.ports.DebugModelRoutingEnabled() && requestedModel != "" {
		groupPlatform := ""
		if group != nil {
			groupPlatform = ""
		}
		s.diagnostics.printf("service.gateway", "[ModelRoutingDebug] select entry: group_id=%v forced_platform=%s model=%s session=%s sticky_provider=%d load_batch=%v concurrency=%v",
			derefGroupID(groupID), groupPlatform, requestedModel, shortFlowSessionHash(sessionHash), stickyProviderID, cfg.LoadBatchEnabled, s.concurrencyService != nil)
	}

	// Basic selection tries candidates individually when batch load reads are unavailable. Advanced scoring uses neutral load values.
	if !usesAdvancedScheduler && (s.concurrencyService == nil || !cfg.LoadBatchEnabled) {
		localExcluded := make(map[int64]struct{})
		maps.Copy(localExcluded, excludedIDs)

		for {
			provider, err := s.selectRoutes(ctx, groupID, sessionHash, requestedModel, localExcluded)
			if err != nil {
				return nil, err
			}

			result, err := s.ports.TryAcquireProviderSlot(ctx, provider.ID, provider.Concurrency)
			if err == nil && result.Acquired {
				if !s.ports.CheckAndRegisterSession(ctx, provider, sessionHash) {
					result.ReleaseFunc()
					localExcluded[provider.ID] = struct{}{}
					continue
				}
				return s.ports.NewSelectionResult(ctx, provider, true, result.ReleaseFunc, nil)
			}

			if !s.ports.CheckAndRegisterSession(ctx, provider, sessionHash) {
				localExcluded[provider.ID] = struct{}{}
				continue
			}

			if stickyProviderID > 0 && stickyProviderID == provider.ID && s.concurrencyService != nil {
				waitingCount, _ := s.concurrencyService.GetProviderWaitingCount(ctx, provider.ID)
				if waitingCount < cfg.StickySessionMaxWaiting {
					return s.ports.NewSelectionResult(ctx, provider, false, nil, &ProviderWaitPlan{
						ProviderID:     provider.ID,
						MaxConcurrency: provider.Concurrency,
						Timeout:        cfg.StickySessionWaitTimeout,
						MaxWaiting:     cfg.StickySessionMaxWaiting,
					})
				}
			}
			return s.ports.NewSelectionResult(ctx, provider, false, nil, &ProviderWaitPlan{
				ProviderID:     provider.ID,
				MaxConcurrency: provider.Concurrency,
				Timeout:        cfg.FallbackWaitTimeout,
				MaxWaiting:     cfg.FallbackMaxWaiting,
			})
		}
	}

	platform, hasForcePlatform, err := s.ports.ResolvePlatform(ctx, groupID, group)
	if err != nil {
		return nil, err
	}
	preferOAuth := platform == capability.PlatformGemini
	if s.ports.DebugModelRoutingEnabled() && platform == capability.PlatformAnthropic && requestedModel != "" {
		s.diagnostics.printf("service.gateway", "[ModelRoutingDebug] load-aware enabled: group_id=%v model=%s session=%s platform=%s", derefGroupID(groupID), requestedModel, shortFlowSessionHash(sessionHash), platform)
	}

	providers, useMixed, err := s.ports.ListSchedulableProviders(ctx, groupID, platform, hasForcePlatform)
	if err != nil {
		return nil, err
	}
	if len(providers) == 0 {
		return nil, ErrNoAvailableProviders
	}
	ctx = s.ports.WithWindowCostPrefetch(ctx, providers)
	ctx = s.ports.WithRPMPrefetch(ctx, providers)

	providerByID := make(map[int64]*FlowProvider, len(providers))
	for i := range providers {
		providerByID[providers[i].ID] = &providers[i]
	}
	isExcluded := func(providerID int64) bool {
		if excludedIDs == nil {
			return false
		}
		_, excluded := excludedIDs[providerID]
		return excluded
	}
	// Upstream model restrictions use each provider's final model across load-aware selection paths.
	needsUpstreamCheck := s.ports.NeedsUpstreamGroupRestrictionCheck(ctx, groupID)
	isUpstreamAllowed := func(provider *FlowProvider) bool {
		return !needsUpstreamCheck || !s.ports.IsUpstreamModelRestrictedByGroup(ctx, *groupID, provider, requestedModel)
	}

	var routingProviderIDs []int64
	if group != nil && requestedModel != "" {
		routingModel := s.ports.GroupMappedModelForProviderLayer(ctx, requestedModel)
		routingProviderIDs = group.GetRoutingProviderIDs(routingModel)
		if s.ports.DebugModelRoutingEnabled() {
			s.diagnostics.printf("service.gateway", "[ModelRoutingDebug] context group routing: group_id=%d model=%s enabled=%v rules=%d matched_ids=%v session=%s sticky_provider=%d",
				group.ID, requestedModel, group.ModelRoutingEnabled, len(group.ModelRouting), routingProviderIDs, shortFlowSessionHash(sessionHash), stickyProviderID)
			if len(routingProviderIDs) == 0 && group.ModelRoutingEnabled && len(group.ModelRouting) > 0 {
				keys := make([]string, 0, len(group.ModelRouting))
				for k := range group.ModelRouting {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				const maxKeys = 20
				if len(keys) > maxKeys {
					keys = keys[:maxKeys]
				}
				s.diagnostics.printf("service.gateway", "[ModelRoutingDebug] context group routing miss: group_id=%d model=%s patterns(sample)=%v", group.ID, requestedModel, keys)
			}
		}
	}

	if len(routingProviderIDs) > 0 && (s.concurrencyService != nil || usesAdvancedScheduler) {
		var routingCandidates []*FlowProvider
		var filteredExcluded, filteredMissing, filteredUnsched, filteredPlatform, filteredModelScope, filteredModelMapping, filteredWindowCost int
		var modelScopeSkippedIDs []int64
		for _, routingProviderID := range routingProviderIDs {
			if isExcluded(routingProviderID) {
				filteredExcluded++
				continue
			}
			provider, ok := providerByID[routingProviderID]
			if !ok || !s.ports.IsProviderSchedulableForSelection(provider) {
				if !ok {
					filteredMissing++
				} else {
					filteredUnsched++
				}
				continue
			}
			if group != nil && group.RequirePrivacySet && !provider.IsPrivacySet() {
				_ = s.ports.SetProviderError(ctx, provider.ID,
					fmt.Sprintf("Privacy not set, required by group [%s]", group.Name))
				continue
			}
			if !s.ports.IsProviderAllowedForPlatform(provider, platform, useMixed) {
				filteredPlatform++
				continue
			}
			if requestedModel != "" && !s.ports.IsModelSupportedByProviderWithContext(ctx, provider, requestedModel) {
				filteredModelMapping++
				continue
			}
			if !isUpstreamAllowed(provider) {
				continue
			}
			if !s.ports.IsProviderSchedulableForModelSelection(ctx, provider, requestedModel) {
				filteredModelScope++
				modelScopeSkippedIDs = append(modelScopeSkippedIDs, provider.ID)
				continue
			}

			if !s.ports.IsProviderSchedulableForQuota(provider) {
				continue
			}

			weightedSticky := advancedStickyWeighted && provider.ID == stickyProviderID
			if !s.ports.IsProviderSchedulableForWindowCost(ctx, provider, weightedSticky) {
				filteredWindowCost++
				continue
			}

			if !s.ports.IsProviderSchedulableForRPM(ctx, provider, weightedSticky) {
				continue
			}
			routingCandidates = append(routingCandidates, provider)
		}

		if s.ports.DebugModelRoutingEnabled() {
			s.diagnostics.printf("service.gateway", "[ModelRoutingDebug] routed candidates: group_id=%v model=%s routed=%d candidates=%d filtered(excluded=%d missing=%d unsched=%d platform=%d model_scope=%d model_mapping=%d window_cost=%d)",
				derefGroupID(groupID), requestedModel, len(routingProviderIDs), len(routingCandidates),
				filteredExcluded, filteredMissing, filteredUnsched, filteredPlatform, filteredModelScope, filteredModelMapping, filteredWindowCost)
			if len(modelScopeSkippedIDs) > 0 {
				s.diagnostics.printf("service.gateway", "[ModelRoutingDebug] model_rate_limited providers skipped: group_id=%v model=%s provider_ids=%v",
					derefGroupID(groupID), requestedModel, modelScopeSkippedIDs)
			}
		}

		if len(routingCandidates) > 0 {
			if !stickyEscaped && (!usesAdvancedScheduler || !advancedStickyWeighted) && sessionHash != "" && stickyProviderID > 0 {
				s.diagnostics.event("debug", "sticky.layer1_5_checking",
					"sticky_provider_id", stickyProviderID,
					"in_routing_list", slices.Contains(routingProviderIDs, stickyProviderID),
					"is_excluded", isExcluded(stickyProviderID),
					"in_provider_map", func() bool { _, ok := providerByID[stickyProviderID]; return ok }(),
					"session", shortFlowSessionHash(sessionHash),
				)
				if slices.Contains(routingProviderIDs, stickyProviderID) && !isExcluded(stickyProviderID) {
					if stickyProvider, ok := providerByID[stickyProviderID]; ok {
						var stickyCacheMissReason string

						gatePass := s.ports.IsProviderSchedulableForSelection(stickyProvider) &&
							s.ports.IsProviderAllowedForPlatform(stickyProvider, platform, useMixed) &&
							(requestedModel == "" || s.ports.IsModelSupportedByProviderWithContext(ctx, stickyProvider, requestedModel)) &&
							isUpstreamAllowed(stickyProvider) &&
							s.ports.IsProviderSchedulableForModelSelection(ctx, stickyProvider, requestedModel) &&
							s.ports.IsProviderSchedulableForQuota(stickyProvider) &&
							s.ports.IsProviderSchedulableForWindowCost(ctx, stickyProvider, true)

						rpmPass := gatePass && s.ports.IsProviderSchedulableForRPM(ctx, stickyProvider, true)

						if rpmPass {
							result, err := s.ports.TryAcquireProviderSlot(ctx, stickyProviderID, stickyProvider.Concurrency)
							if err == nil && result.Acquired {
								if !s.ports.CheckAndRegisterSession(ctx, stickyProvider, sessionHash) {
									result.ReleaseFunc()
									stickyCacheMissReason = "session_limit"
								} else {
									s.diagnostics.event("debug", "sticky.layer1_5_hit",
										"provider_id", stickyProviderID,
										"session", shortFlowSessionHash(sessionHash),
										"result", "slot_acquired",
									)
									if s.ports.DebugModelRoutingEnabled() {
										s.diagnostics.printf("service.gateway", "[ModelRoutingDebug] routed sticky hit: group_id=%v model=%s session=%s provider=%d", derefGroupID(groupID), requestedModel, shortFlowSessionHash(sessionHash), stickyProviderID)
									}
									return s.ports.NewSelectionResult(ctx, stickyProvider, true, result.ReleaseFunc, nil)
								}
							}

							if stickyCacheMissReason == "" {
								waitingCount, _ := s.concurrencyService.GetProviderWaitingCount(ctx, stickyProviderID)
								if waitingCount < cfg.StickySessionMaxWaiting {
									if !s.ports.CheckAndRegisterSession(ctx, stickyProvider, sessionHash) {
										stickyCacheMissReason = "session_limit"
									} else {
										// Use NewSelectionResult to hydrate credentials before returning the provider.
										// Scheduling snapshots strip credentials such as OAuth tokens.
										// Returning the snapshot directly would leave forwarding without authentication credentials.
										return s.ports.NewSelectionResult(ctx, stickyProvider, false, nil, &ProviderWaitPlan{
											ProviderID:     stickyProviderID,
											MaxConcurrency: stickyProvider.Concurrency,
											Timeout:        cfg.StickySessionWaitTimeout,
											MaxWaiting:     cfg.StickySessionMaxWaiting,
										})
									}
								} else {
									stickyCacheMissReason = "wait_queue_full"
								}
							}
						} else if !gatePass {
							stickyCacheMissReason = "gate_check"
						} else {
							stickyCacheMissReason = "rpm_red"
						}

						if stickyCacheMissReason != "" {
							baseRPM := stickyProvider.GetBaseRPM()
							var currentRPM int
							if count, ok := PrefetchedRPM(ctx, stickyProvider.ID); ok {
								currentRPM = count
							}
							s.diagnostics.printf("service.gateway", "[StickyCacheMiss] reason=%s provider_id=%d session=%s current_rpm=%d base_rpm=%d",
								stickyCacheMissReason, stickyProviderID, shortFlowSessionHash(sessionHash), currentRPM, baseRPM)
						}
					} else {
						_ = s.cache.DeleteSessionProviderID(ctx, derefGroupID(groupID), sessionHash)
						s.diagnostics.printf("service.gateway", "[StickyCacheMiss] reason=provider_cleared provider_id=%d session=%s current_rpm=0 base_rpm=0",
							stickyProviderID, shortFlowSessionHash(sessionHash))
					}
				}
			}

			if usesAdvancedScheduler && (s.concurrencyService == nil || !cfg.LoadBatchEnabled) {
				if selection, ok, selectErr := s.TryAdvancedWithoutLoad(ctx, groupID, sessionHash, routingCandidates); selectErr != nil {
					return nil, selectErr
				} else if ok {
					return selection, nil
				}
				return nil, ErrNoAvailableProviders
			}

			routingLoads := make([]ProviderWithConcurrency, 0, len(routingCandidates))
			for _, acc := range routingCandidates {
				routingLoads = append(routingLoads, ProviderWithConcurrency{
					ID:             acc.ID,
					MaxConcurrency: acc.EffectiveLoadFactor(),
				})
			}
			routingLoadMap, _ := s.concurrencyService.GetProvidersLoadBatch(ctx, routingLoads)

			var routingAvailable []FlowLoad
			for _, acc := range routingCandidates {
				loadInfo := routingLoadMap[acc.ID]
				if loadInfo == nil && !usesAdvancedScheduler {
					loadInfo = &ProviderLoadInfo{ProviderID: acc.ID}
				}
				if usesAdvancedScheduler || loadInfo == nil || loadInfo.LoadRate < 100 {
					routingAvailable = append(routingAvailable, FlowLoad{Provider: acc, LoadInfo: loadInfo})
				}
			}

			if len(routingAvailable) > 0 {
				// Model routing supplies candidates that satisfy eligibility checks; advanced groups score them together.
				// Exhausting these candidates returns no available provider.
				if usesAdvancedScheduler {
					if selection, ok, selectErr := s.TryAdvanced(ctx, groupID, sessionHash, routingAvailable); selectErr != nil {
						return nil, selectErr
					} else if ok {
						return selection, nil
					}
					return nil, ErrNoAvailableProviders
				}

				sort.SliceStable(routingAvailable, func(i, j int) bool {
					a, b := routingAvailable[i], routingAvailable[j]
					if a.Provider.Priority != b.Provider.Priority {
						return a.Provider.Priority < b.Provider.Priority
					}
					if a.LoadInfo.LoadRate != b.LoadInfo.LoadRate {
						return a.LoadInfo.LoadRate < b.LoadInfo.LoadRate
					}
					switch {
					case a.Provider.LastUsedAt == nil && b.Provider.LastUsedAt != nil:
						return true
					case a.Provider.LastUsedAt != nil && b.Provider.LastUsedAt == nil:
						return false
					case a.Provider.LastUsedAt == nil && b.Provider.LastUsedAt == nil:
						return false
					default:
						return a.Provider.LastUsedAt.Before(*b.Provider.LastUsedAt)
					}
				})
				flowShuffleWithinSortGroups(routingAvailable)

				for _, item := range routingAvailable {
					result, err := s.ports.TryAcquireProviderSlot(ctx, item.Provider.ID, item.Provider.Concurrency)
					if err == nil && result.Acquired {
						if !s.ports.CheckAndRegisterSession(ctx, item.Provider, sessionHash) {
							result.ReleaseFunc()
							continue
						}
						if sessionHash != "" && s.cache != nil {
							_ = s.cache.SetSessionProviderID(ctx, derefGroupID(groupID), sessionHash, item.Provider.ID, stickySessionTTL)
						}
						if s.ports.DebugModelRoutingEnabled() {
							s.diagnostics.printf("service.gateway", "[ModelRoutingDebug] routed select: group_id=%v model=%s session=%s provider=%d", derefGroupID(groupID), requestedModel, shortFlowSessionHash(sessionHash), item.Provider.ID)
						}
						return s.ports.NewSelectionResult(ctx, item.Provider, true, result.ReleaseFunc, nil)
					}
				}

				for _, item := range routingAvailable {
					if !s.ports.CheckAndRegisterSession(ctx, item.Provider, sessionHash) {
						continue
					}
					if s.ports.DebugModelRoutingEnabled() {
						s.diagnostics.printf("service.gateway", "[ModelRoutingDebug] routed wait: group_id=%v model=%s session=%s provider=%d", derefGroupID(groupID), requestedModel, shortFlowSessionHash(sessionHash), item.Provider.ID)
					}
					return s.ports.NewSelectionResult(ctx, item.Provider, false, nil, &ProviderWaitPlan{
						ProviderID:     item.Provider.ID,
						MaxConcurrency: item.Provider.Concurrency,
						Timeout:        cfg.StickySessionWaitTimeout,
						MaxWaiting:     cfg.StickySessionMaxWaiting,
					})
				}
			}

			s.diagnostics.printf("service.gateway", "[ModelRouting] All routed providers unavailable for model=%s, falling back to normal selection", requestedModel)
		}
	}

	if !stickyEscaped && len(routingProviderIDs) == 0 && (!usesAdvancedScheduler || !advancedStickyWeighted) && sessionHash != "" && stickyProviderID > 0 && !isExcluded(stickyProviderID) {
		providerID := stickyProviderID
		if providerID > 0 && !isExcluded(providerID) {
			provider, ok := providerByID[providerID]
			if ok {
				clearSticky := s.ports.ShouldClearStickySessionForProviderLayer(ctx, provider, requestedModel)
				if clearSticky {
					s.diagnostics.event("debug", "sticky.layer1_5_no_routing_clear",
						"provider_id", providerID,
						"reason", "should_clear_sticky_session",
						"session", shortFlowSessionHash(sessionHash),
					)
					_ = s.cache.DeleteSessionProviderID(ctx, derefGroupID(groupID), sessionHash)
				}

				platformOK := s.ports.IsProviderAllowedForPlatform(provider, platform, useMixed)
				modelSupported := requestedModel == "" || s.ports.IsModelSupportedByProviderWithContext(ctx, provider, requestedModel)
				upstreamAllowed := isUpstreamAllowed(provider)
				modelSchedulable := s.ports.IsProviderSchedulableForModelSelection(ctx, provider, requestedModel)
				quotaOK := s.ports.IsProviderSchedulableForQuota(provider)
				windowCostOK := s.ports.IsProviderSchedulableForWindowCost(ctx, provider, true)
				rpmOK := s.ports.IsProviderSchedulableForRPM(ctx, provider, true)
				schedulable := s.ports.IsProviderSchedulableForSelection(provider)

				s.diagnostics.event("debug", "sticky.layer1_5_no_routing_checks",
					"provider_id", providerID,
					"session", shortFlowSessionHash(sessionHash),
					"clear_sticky", clearSticky,
					"schedulable", schedulable,
					"platform_ok", platformOK,
					"model_supported", modelSupported,
					"upstream_allowed", upstreamAllowed,
					"model_schedulable", modelSchedulable,
					"quota_ok", quotaOK,
					"window_cost_ok", windowCostOK,
					"rpm_ok", rpmOK,
				)

				if !clearSticky && platformOK && modelSupported && upstreamAllowed && modelSchedulable && quotaOK && windowCostOK && rpmOK && schedulable {
					result, err := s.ports.TryAcquireProviderSlot(ctx, providerID, provider.Concurrency)
					if err == nil && result.Acquired {
						if !s.ports.CheckAndRegisterSession(ctx, provider, sessionHash) {
							result.ReleaseFunc()
							s.diagnostics.event("debug", "sticky.layer1_5_no_routing_miss",
								"provider_id", providerID,
								"reason", "session_limit",
								"session", shortFlowSessionHash(sessionHash),
							)
						} else {
							s.diagnostics.event("debug", "sticky.layer1_5_no_routing_hit",
								"provider_id", providerID,
								"session", shortFlowSessionHash(sessionHash),
								"result", "slot_acquired",
							)
							if s.cache != nil {
								_ = s.cache.RefreshSessionTTL(ctx, derefGroupID(groupID), sessionHash, stickySessionTTL)
							}
							return s.ports.NewSelectionResult(ctx, provider, true, result.ReleaseFunc, nil)
						}
					} else {
						s.diagnostics.event("debug", "sticky.layer1_5_no_routing_slot_busy",
							"provider_id", providerID,
							"session", shortFlowSessionHash(sessionHash),
						)
					}

					waitingCount, _ := s.concurrencyService.GetProviderWaitingCount(ctx, providerID)
					if waitingCount < cfg.StickySessionMaxWaiting {
						if !s.ports.CheckAndRegisterSession(ctx, provider, sessionHash) {
						} else {
							s.diagnostics.event("debug", "sticky.layer1_5_no_routing_hit",
								"provider_id", providerID,
								"session", shortFlowSessionHash(sessionHash),
								"result", "wait_plan",
							)
							return s.ports.NewSelectionResult(ctx, provider, false, nil, &ProviderWaitPlan{
								ProviderID:     providerID,
								MaxConcurrency: provider.Concurrency,
								Timeout:        cfg.StickySessionWaitTimeout,
								MaxWaiting:     cfg.StickySessionMaxWaiting,
							})
						}
					}
				} else if !clearSticky {
					s.diagnostics.event("debug", "sticky.layer1_5_no_routing_miss",
						"provider_id", providerID,
						"reason", "gate_check_failed",
						"session", shortFlowSessionHash(sessionHash),
					)
				}
			} else {
				s.diagnostics.event("debug", "sticky.layer1_5_no_routing_miss",
					"provider_id", providerID,
					"reason", "provider_not_in_map",
					"session", shortFlowSessionHash(sessionHash),
				)
			}
		}
	} else if len(routingProviderIDs) == 0 && sessionHash != "" {
		s.diagnostics.event("debug", "sticky.layer1_5_no_routing_skip",
			"sticky_provider_id", stickyProviderID,
			"is_excluded", func() bool { return stickyProviderID > 0 && isExcluded(stickyProviderID) }(),
			"session", shortFlowSessionHash(sessionHash),
			"reason", func() string {
				if stickyProviderID == 0 {
					return "no_sticky_binding"
				}
				return "sticky_provider_excluded"
			}(),
		)
	}

	s.diagnostics.event("debug", "sticky.layer2_fallback",
		"session", shortFlowSessionHash(sessionHash),
		"sticky_provider_id", stickyProviderID,
		"reason", "sticky_not_used_falling_back_to_load_balance",
		"total_providers", len(providers),
	)
	candidates := make([]*FlowProvider, 0, len(providers))
	for i := range providers {
		acc := &providers[i]
		if isExcluded(acc.ID) {
			continue
		}

		if !s.ports.IsProviderSchedulableForSelection(acc) {
			continue
		}
		if group != nil && group.RequirePrivacySet && !acc.IsPrivacySet() {
			_ = s.ports.SetProviderError(ctx, acc.ID,
				fmt.Sprintf("Privacy not set, required by group [%s]", group.Name))
			continue
		}
		if !s.ports.IsProviderAllowedForPlatform(acc, platform, useMixed) {
			continue
		}
		if requestedModel != "" && !s.ports.IsModelSupportedByProviderWithContext(ctx, acc, requestedModel) {
			continue
		}
		if !isUpstreamAllowed(acc) {
			continue
		}
		if !s.ports.IsProviderSchedulableForModelSelection(ctx, acc, requestedModel) {
			continue
		}

		if !s.ports.IsProviderSchedulableForQuota(acc) {
			continue
		}

		weightedSticky := advancedStickyWeighted && acc.ID == stickyProviderID
		if !s.ports.IsProviderSchedulableForWindowCost(ctx, acc, weightedSticky) {
			continue
		}

		if !s.ports.IsProviderSchedulableForRPM(ctx, acc, weightedSticky) {
			continue
		}
		candidates = append(candidates, acc)
	}

	if len(candidates) == 0 {
		if err := s.ports.GroupModelUnsupportedErrorIfApplicable(ctx, providers, requestedModel, platform, excludedIDs, useMixed, groupID, group); err != nil {
			return nil, err
		}
		return nil, ErrNoAvailableProviders
	}

	if usesAdvancedScheduler && (s.concurrencyService == nil || !cfg.LoadBatchEnabled) {
		if selection, ok, selectErr := s.TryAdvancedWithoutLoad(ctx, groupID, sessionHash, candidates); selectErr != nil {
			return nil, selectErr
		} else if ok {
			return selection, nil
		}
		return nil, ErrNoAvailableProviders
	}

	providerLoads := make([]ProviderWithConcurrency, 0, len(candidates))
	for _, acc := range candidates {
		providerLoads = append(providerLoads, ProviderWithConcurrency{
			ID:             acc.ID,
			MaxConcurrency: acc.EffectiveLoadFactor(),
		})
	}

	loadMap, err := s.concurrencyService.GetProvidersLoadBatch(ctx, providerLoads)
	if err != nil {
		if group != nil && group.UsesAdvancedScheduler() {
			if result, ok, advancedErr := s.TryAdvancedWithoutLoad(ctx, groupID, sessionHash, candidates); advancedErr != nil {
				return nil, advancedErr
			} else if ok {
				return result, nil
			}
			return nil, ErrNoAvailableProviders
		}
		if result, ok, legacyErr := s.TryLegacyOrder(ctx, candidates, groupID, sessionHash, preferOAuth); legacyErr != nil {
			return nil, legacyErr
		} else if ok {
			return result, nil
		}
	} else {
		var available []FlowLoad
		for _, acc := range candidates {
			loadInfo := loadMap[acc.ID]
			if loadInfo == nil && !usesAdvancedScheduler {
				loadInfo = &ProviderLoadInfo{ProviderID: acc.ID}
			}
			if usesAdvancedScheduler || loadInfo == nil || loadInfo.LoadRate < 100 {
				available = append(available, FlowLoad{
					Provider: acc,
					LoadInfo: loadInfo,
				})
			}
		}

		if group != nil && group.UsesAdvancedScheduler() {
			if selection, ok, selectErr := s.TryAdvanced(ctx, groupID, sessionHash, available); selectErr != nil {
				return nil, selectErr
			} else if ok {
				return selection, nil
			}
			// The advanced path has exhausted its candidates and wait plans.
			return nil, ErrNoAvailableProviders
		}

		for len(available) > 0 {
			candidates := flowFilterByMinPriority(available)

			if cfg.PreferSoonestReset {
				candidates = flowFilterBySoonestReset(candidates, s.now)
			}

			candidates = flowFilterByMinLoadRate(candidates)

			selected := flowSelectByLRU(candidates, preferOAuth)
			if selected == nil {
				break
			}

			result, err := s.ports.TryAcquireProviderSlot(ctx, selected.Provider.ID, selected.Provider.Concurrency)
			if err == nil && result.Acquired {
				if !s.ports.CheckAndRegisterSession(ctx, selected.Provider, sessionHash) {
					result.ReleaseFunc()
				} else {
					if sessionHash != "" && s.cache != nil {
						_ = s.cache.SetSessionProviderID(ctx, derefGroupID(groupID), sessionHash, selected.Provider.ID, stickySessionTTL)
					}
					return s.ports.NewSelectionResult(ctx, selected.Provider, true, result.ReleaseFunc, nil)
				}
			}

			selectedID := selected.Provider.ID
			newAvailable := make([]FlowLoad, 0, len(available)-1)
			for _, acc := range available {
				if acc.Provider.ID != selectedID {
					newAvailable = append(newAvailable, acc)
				}
			}
			available = newAvailable
		}
	}

	s.SortCandidatesForFallback(candidates, preferOAuth, cfg.FallbackSelectionMode)
	for _, acc := range candidates {
		if !s.ports.CheckAndRegisterSession(ctx, acc, sessionHash) {
			continue
		}
		return s.ports.NewSelectionResult(ctx, acc, false, nil, &ProviderWaitPlan{
			ProviderID:     acc.ID,
			MaxConcurrency: acc.Concurrency,
			Timeout:        cfg.FallbackWaitTimeout,
			MaxWaiting:     cfg.FallbackMaxWaiting,
		})
	}
	return nil, ErrNoAvailableProviders
}

func (s *GenericSelector) TryAdvancedWithoutLoad(
	ctx context.Context,
	groupID *int64,
	sessionHash string,
	providers []*FlowProvider,
) (*FlowSelection, bool, error) {
	available := make([]FlowLoad, 0, len(providers))
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		available = append(available, FlowLoad{
			Provider: provider,
			// A failed batch read leaves load as a neutral scoring signal.
			LoadInfo: nil,
		})
	}
	return s.TryAdvanced(ctx, groupID, sessionHash, available)
}

func (s *GenericSelector) TryAdvanced(
	ctx context.Context,
	groupID *int64,
	sessionHash string,
	available []FlowLoad,
) (*FlowSelection, bool, error) {
	if s == nil || len(available) == 0 {
		return nil, false, nil
	}
	providers := make([]*FlowProvider, 0, len(available))
	loadMap := make(map[int64]*ProviderLoadInfo, len(available))
	for _, item := range available {
		if item.Provider == nil {
			continue
		}
		providers = append(providers, item.Provider)
		loadMap[item.Provider.ID] = item.LoadInfo
	}
	if len(providers) == 0 {
		return nil, false, nil
	}

	stickyProviderID := int64(0)
	if sessionHash != "" && s.cache != nil {
		stickyProviderID, _ = s.cache.GetSessionProviderID(ctx, derefGroupID(groupID), sessionHash)
	}
	effectiveSettings := s.ports.AdvancedSchedulerEffectiveSettingsForRequest(ctx, groupID)
	candidates, _ := flowScoreCandidates(
		providers,
		loadMap,
		s.ports.AdvancedSchedulerStats(),
		effectiveSettings.Weights,
		ScoreInput{
			GroupID:          groupID,
			SessionHash:      sessionHash,
			StickyProviderID: stickyProviderID,
			StickyWeighted:   effectiveSettings.StickyWeightedEnabled,
			TopK:             effectiveSettings.TopK,
		},
		s.now(),
	)
	selectionOrder := flowBuildSelectionOrder(candidates, ScoreInput{
		GroupID:          groupID,
		SessionHash:      sessionHash,
		StickyProviderID: stickyProviderID,
		StickyWeighted:   effectiveSettings.StickyWeightedEnabled,
		TopK:             effectiveSettings.TopK,
	})
	for _, candidate := range selectionOrder {
		if candidate.Provider == nil {
			continue
		}
		result, err := s.ports.TryAcquireProviderSlot(ctx, candidate.Provider.ID, candidate.Provider.Concurrency)
		if err != nil || result == nil || !result.Acquired {
			continue
		}
		if !s.ports.CheckAndRegisterSession(ctx, candidate.Provider, sessionHash) {
			result.ReleaseFunc()
			continue
		}
		if sessionHash != "" && s.cache != nil && !PreserveStickyFromContext(ctx) {
			_ = s.cache.SetSessionProviderID(ctx, derefGroupID(groupID), sessionHash, candidate.Provider.ID, stickySessionTTL)
		}
		selection, selectErr := s.ports.NewSelectionResult(ctx, candidate.Provider, true, result.ReleaseFunc, nil)
		if selectErr != nil {
			result.ReleaseFunc()
			return nil, true, selectErr
		}
		selection.AdvancedScheduler = true
		feedback := effectiveSettings.Feedback
		selection.AdvancedSchedulerFeedback = &feedback
		return selection, true, nil
	}
	for _, candidate := range selectionOrder {
		if candidate.Provider == nil || !s.ports.CheckAndRegisterSession(ctx, candidate.Provider, sessionHash) {
			continue
		}
		selection, selectErr := s.ports.NewSelectionResult(ctx, candidate.Provider, false, nil, &ProviderWaitPlan{
			ProviderID:     candidate.Provider.ID,
			MaxConcurrency: candidate.Provider.Concurrency,
			Timeout:        s.ports.SchedulingConfig().FallbackWaitTimeout,
			MaxWaiting:     s.ports.SchedulingConfig().FallbackMaxWaiting,
		})
		if selectErr != nil {
			return nil, true, selectErr
		}
		selection.AdvancedScheduler = true
		feedback := effectiveSettings.Feedback
		selection.AdvancedSchedulerFeedback = &feedback
		return selection, true, nil
	}
	return nil, false, nil
}

func (s *GenericSelector) TryLegacyOrder(ctx context.Context, candidates []*FlowProvider, groupID *int64, sessionHash string, preferOAuth bool) (*FlowSelection, bool, error) {
	ordered := append([]*FlowProvider(nil), candidates...)
	flowSortProvidersByPriorityAndLastUsed(ordered, preferOAuth)

	for _, acc := range ordered {
		result, err := s.ports.TryAcquireProviderSlot(ctx, acc.ID, acc.Concurrency)
		if err == nil && result.Acquired {
			// Check the session limit before accepting the slot.
			if !s.ports.CheckAndRegisterSession(ctx, acc, sessionHash) {
				result.ReleaseFunc() // Return the slot before trying another provider.
				continue
			}
			if sessionHash != "" && s.cache != nil {
				_ = s.cache.SetSessionProviderID(ctx, derefGroupID(groupID), sessionHash, acc.ID, stickySessionTTL)
			}
			selection, err := s.ports.NewSelectionResult(ctx, acc, true, result.ReleaseFunc, nil)
			if err != nil {
				return nil, false, err
			}
			return selection, true, nil
		}
	}

	return nil, false, nil
}

func (s *GenericSelector) SortCandidatesForFallback(providers []*FlowProvider, preferOAuth bool, mode string) {
	if mode == "random" {
		// Sort by priority, then shuffle within equal priorities.
		flowSortProvidersByPriorityOnly(providers, preferOAuth)
		flowShuffleWithinPriority(providers, s.now)
	} else {
		// Use last-used time for the default ordering.
		flowSortProvidersByPriorityAndLastUsed(providers, preferOAuth)
	}
}

func shortFlowSessionHash(sessionHash string) string {
	if sessionHash == "" {
		return ""
	}
	if len(sessionHash) <= 8 {
		return sessionHash
	}
	return sessionHash[:8]
}

// SelectOnly follows routing and eligibility checks for callers that need no slots or session registration.
func (s *GenericSelector) SelectOnly(ctx context.Context, input SelectionInput) (*FlowProvider, error) {
	return s.selectRoutes(WithSelectOnly(ctx), input.GroupID, input.SessionHash, input.RequestedModel, input.ExcludedIDs)
}
