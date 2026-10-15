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

// generic_selection.go selects providers and acquires capacity for requests
// within an admitted group.
// A generic request selects within the group already admitted by the gateway.
// Model routes form a preferred pool, while session bindings, model and quota
// gates, and physical capacity determine whether a candidate can serve the
// request. Basic groups order eligible providers by priority, load, optional
// window reset, and last use; advanced groups score eligible providers
// together, including those with fully occupied observed capacity.
//
// Observed load is a hint whose denominator can differ from the physical slot
// limit. Acquisition therefore decides capacity after ordering. A successful
// slot still needs session admission and execution hydration, with cleanup
// delegated through the adapter. Exhausted capacity can produce a wait plan
// rather than an occupied slot. Advanced results capture this request's
// effective feedback settings, while several basic paths write stickiness
// before result hydration.

// stickySessionTTL sets the lifetime requested when generic selection creates
// or refreshes a session's provider binding.
const stickySessionTTL = time.Hour

// ErrNoAvailableProviders identifies selection exhaustion and is wrapped with
// model or group rejection details for gateway callers.
var ErrNoAvailableProviders = errors.New("no available providers")

// FlowProvider is credential-free candidate metadata associated with one
// adapter invocation's execution target.
type FlowProvider struct {
	// Plan belongs to the associated execution candidate; selection passes this
	// reference through, so callers retain its request-specific model and
	// protocol decisions.
	Plan *routing.CandidatePlan // candidate routing decisions
	// ProjectionID associates this conversion with an execution target, while ID
	// identifies the persisted provider. A later fresh record with the same ID
	// can have a different ProjectionID.
	ProjectionID uint64 // per-invocation target association
	ID           int64  // persisted provider identity
	// provider display name, provider platform identity, and provider
	// authentication type
	Name, Platform, Type string
	// Concurrency limits actual acquisition; LoadFactor supplies the
	// observed-load denominator. Priority compares lower values first, while
	// BaseRPM supports sticky miss diagnostics through prefetched RPM counts.
	// physical slot limit, global sort priority, observed-load percentage
	// denominator, and provider RPM baseline
	Concurrency, Priority, LoadFactor, BaseRPM int
	PrivacySet                                 bool // provider privacy requirement satisfied
	// LastUsedAt uses nil for never used; SessionWindowEnd uses nil for an
	// unavailable reset signal. Selection reads these time pointers and does not
	// modify their values.
	// last provider-use timestamp and session-window reset endpoint
	LastUsedAt, SessionWindowEnd *time.Time
}

// FlowGroup carries scheduling policy and a per-invocation association with
// its source group.
type FlowGroup struct {
	routing.Group // copied routing group policy
	// ProjectionID resolves this group through the same adapter invocation that
	// supplied its copied routing.Group scheduling policy.
	ProjectionID uint64 // per-invocation source group association
}

// ProviderWaitPlan describes the provider capacity and queue policy for later
// admission.
type ProviderWaitPlan struct {
	ProviderID     int64         // provider to await
	MaxConcurrency int           // physical provider slot limit
	Timeout        time.Duration // time.Duration
	MaxWaiting     int           // queue admission count limit
}

// FlowSelection contains an execution candidate, capacity cleanup or wait
// intent, and optional feedback settings.
type FlowSelection struct {
	Provider *FlowProvider // hydrated selection metadata
	// Acquired, ReleaseFunc, and WaitPlan describe the adapter's admission
	// result. Waiting results carry a plan instead of acquired capacity;
	// select-only adapters may report acquired with no physical slot.
	Acquired bool // adapter reports capacity acquired
	// ReleaseFunc is returned by the completion adapter. Its caller runs cleanup
	// when the selected attempt finishes, including aborted forwarding.
	ReleaseFunc       func()            // selected capacity cleanup handle
	WaitPlan          *ProviderWaitPlan // deferred provider admission plan
	AdvancedScheduler bool              // advanced selection feedback enabled
	// AdvancedSchedulerFeedback copies the selected request's effective alpha
	// values, so completion uses that group's settings even if runtime
	// configuration changes meanwhile.
	AdvancedSchedulerFeedback *policy.FeedbackConfig // captured effective feedback settings
}

// FlowLoad pairs candidate metadata with an optional observed provider load
// sample.
type FlowLoad struct {
	Provider *FlowProvider // associated candidate metadata pointer
	// LoadInfo may be nil after absent or failed load reads; advanced scoring
	// treats that sample as an unknown load signal.
	LoadInfo *ProviderLoadInfo // observed provider load pointer
}

// FlowOptions contains generic basic ordering switches and the two queue
// policies.
type FlowOptions struct {
	// batch-load selection enabled and earliest session-reset preference
	LoadBatchEnabled, PreferSoonestReset bool
	// FallbackWaitTimeout and FallbackMaxWaiting govern ordinary waits;
	// StickySessionWaitTimeout and StickySessionMaxWaiting govern sticky waits
	// and the routed basic wait path after immediate acquisition is exhausted.
	FallbackMaxWaiting, StickySessionMaxWaiting int // ordinary and sticky queue count limits
	// FallbackSelectionMode selects priority-group shuffling only for the literal
	// random value; every other value uses priority and last-used ordering.
	FallbackSelectionMode string // ordinary queue ordering mode
	// ordinary queue wait timeout and sticky queue wait timeout
	FallbackWaitTimeout, StickySessionWaitTimeout time.Duration
}

// GenericSelectionPorts binds gateway admission, provider reads, eligibility,
// capacity, and execution completion to the core. Callbacks share the adapter
// invocation's provider and group lookup and are called serially here.
type GenericSelectionPorts struct {
	// ReadGroup, ForcePlatform, ResolveGroupByID, and ResolveGatewayGroup supply
	// the group scope used by candidate reads. The gateway admits any fallback
	// before invoking these selection callbacks.
	//
	// ReadGroup is optional in SelectPlatform; read errors leave schedGroup
	// unavailable, so that auxiliary path skips privacy filtering based on the
	// missing group.
	// auxiliary scheduling group read
	ReadGroup func(context.Context, int64) (*FlowGroup, error)
	// context forced platform lookup
	ForcePlatform func(context.Context) (string, bool)
	// admitted group identity resolver
	ResolveGroupByID func(context.Context, int64) (*FlowGroup, error)
	// ResolveGatewayGroup validates the current admitted group and returns the ID
	// used by subsequent reads; group fallback authorization belongs to the
	// gateway.
	// current gateway group resolver
	ResolveGatewayGroup func(context.Context, *int64) (*FlowGroup, *int64, error)
	// HydrateSelectedProvider supplies execution data for a selected candidate;
	// it resolves the metadata through this invocation's target association.
	// execution candidate completion callback
	HydrateSelectedProvider func(context.Context, *FlowProvider) (*FlowProvider, error)
	// preferred model route IDs
	RoutingProviderIDsForRequest func(context.Context, *int64, string, string) []int64
	// current sticky provider reader
	GetSchedulableProvider func(context.Context, int64) (*FlowProvider, error)
	// sticky candidate membership check
	IsProviderInGroup func(*FlowProvider, *int64) bool
	// final candidate failure summary
	LogDetailedSelectionFailure func(context.Context, *int64, string, string, string, []FlowProvider, map[int64]struct{}, bool) string

	// SelectProviderForModelWithExclusions is retained for adapters; the
	// selection implementation in this file performs its own routed and
	// load-aware ordering.
	// adapter model selection callback
	SelectProviderForModelWithExclusions func(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}) (*FlowProvider, error)
	// request effective advanced settings
	AdvancedSchedulerEffectiveSettingsForRequest func(ctx context.Context, groupID *int64) policy.EffectiveSettings
	// shared runtime feedback reader
	AdvancedSchedulerStats func() *RuntimeStats
	// group-mapped model route lookup
	GroupMappedModelForProviderLayer func(ctx context.Context, requestedModel string) string
	// CheckAndRegisterSession runs before result completion and may register a
	// session. A false result rejects the candidate and requires returning any
	// acquired physical slot.
	// selected provider session admission
	CheckAndRegisterSession func(ctx context.Context, provider *FlowProvider, session string) bool
	// admitted group model rejection
	CheckGroupModelRestriction func(ctx context.Context, groupID *int64, requestedModel string) bool
	// CheckClaudeCodeRestriction supplies the admitted group used by Select;
	// rejected input stops selection before provider reads and capacity attempts.
	// current group admission check
	CheckClaudeCodeRestriction func(ctx context.Context, groupID *int64) (*FlowGroup, *int64, error)
	DebugModelRoutingEnabled   func() bool // model routing diagnostic switch
	// group model diagnostic error
	GroupModelUnsupportedErrorIfApplicable func(ctx context.Context, providers []FlowProvider, requestedModel string, platform string, excludedIDs map[int64]struct{}, useMixed bool, groupID *int64, schedGroup *FlowGroup) error
	// forced platform candidate eligibility
	IsProviderAllowedForPlatform func(provider *FlowProvider, platform string, useMixed bool) bool
	// runtime model availability check
	IsProviderSchedulableForModelSelection func(ctx context.Context, provider *FlowProvider, requestedModel string) bool
	// provider quota admission check
	IsProviderSchedulableForQuota func(provider *FlowProvider) bool
	// IsProviderSchedulableForRPM receives sticky=true for hard affinity and the
	// weighted affinity candidate, retaining any adapter-defined sticky-only
	// admission interval.
	// ordinary or sticky RPM eligibility
	IsProviderSchedulableForRPM func(ctx context.Context, provider *FlowProvider, sticky bool) bool
	// general provider eligibility check
	IsProviderSchedulableForSelection func(provider *FlowProvider) bool
	// IsProviderSchedulableForWindowCost receives the same sticky admission
	// distinction as RPM; other candidates use the ordinary cost threshold.
	// ordinary or sticky cost eligibility
	IsProviderSchedulableForWindowCost func(ctx context.Context, provider *FlowProvider, sticky bool) bool
	// request model support check
	IsModelSupportedByProviderWithContext func(ctx context.Context, provider *FlowProvider, requestedModel string) bool
	// provider-layer final model rejection
	IsUpstreamModelRestrictedByGroup func(ctx context.Context, groupID int64, provider *FlowProvider, requestedModel string) bool
	// ListSchedulableProviders supplies metadata associated with this invocation,
	// plus the mixed-platform eligibility flag consumed by
	// IsProviderAllowedForPlatform.
	// group-scoped candidate source callback
	ListSchedulableProviders func(ctx context.Context, groupID *int64, platform string, hasForcePlatform bool) ([]FlowProvider, bool, error)
	// NeedsUpstreamGroupRestrictionCheck returns false for a missing group ID; a
	// true result permits the selector's closures to dereference that ID.
	// upstream restriction stage required
	NeedsUpstreamGroupRestrictionCheck func(ctx context.Context, groupID *int64) bool
	// NewSelectionResult completes execution data and adopts acquired cleanup.
	// Basic callers rely on it to return the physical slot when hydration fails.
	// Completion failure can follow session registration, and this core offers
	// no separate callback to unregister that session on the error.
	// hydrated execution result constructor
	NewSelectionResult func(ctx context.Context, provider *FlowProvider, acquired bool, release func(), waitPlan *ProviderWaitPlan) (*FlowSelection, error)
	// current candidate platform resolver
	ResolvePlatform func(ctx context.Context, groupID *int64, group *FlowGroup) (string, bool, error)
	// generic basic scheduling policy
	SchedulingConfig func() FlowOptions
	// stale binding invalidation decision
	ShouldClearStickySessionForProviderLayer func(ctx context.Context, provider *FlowProvider, requestedModel string) bool
	// TryAcquireProviderSlot supplies acquired status and idempotent physical
	// cleanup, which advanced completion failure may invoke again. Basic paths
	// expect a nonnil result on nil error; advanced paths also handle a nil
	// result as a missed acquisition.
	// physical provider admission callback
	TryAcquireProviderSlot func(ctx context.Context, providerID int64, maxConcurrency int) (*AcquireResult, error)
	// admitted group context attachment
	WithGroupContext func(ctx context.Context, group *FlowGroup) context.Context
	// candidate RPM context prefetch
	WithRPMPrefetch func(ctx context.Context, providers []FlowProvider) context.Context
	// candidate cost context prefetch
	WithWindowCostPrefetch func(ctx context.Context, providers []FlowProvider) context.Context
	// administrative provider error writer
	SetProviderError func(context.Context, int64, string) error
	// PrefetchedSticky supplies a positive bound provider ID before Select
	// attempts a cache read, including when no session hash was supplied.
	// prefetched group binding lookup
	PrefetchedSticky func(context.Context, *int64) int64
}

// GenericSelector orders group-scoped eligibility, sticky reuse, capacity
// acquisition, and optional advanced scoring.
type GenericSelector struct {
	// ports retains callbacks whose lookup maps belong to this adapter
	// invocation; callers serialize use and keep associated execution records
	// available until selection completes.
	ports GenericSelectionPorts // invocation-bound gateway callbacks
	// cache may be nil for selection without stored binding work. Hard-sticky
	// invalidation reached through a positive prefetched ID calls deletion
	// directly, so that path requires the adapter's binding cache.
	cache StickyCache // shared session binding cache
	// concurrencyService enables load and waiting-count reads; physical
	// acquisition still uses ports.TryAcquireProviderSlot and its adapter's
	// select-only behavior.
	concurrencyService *ConcurrencyService // shared provider load service
	diagnostics        Diagnostics         // selection diagnostic output callbacks
	// now is retained as supplied and is called by scoring and reset ordering;
	// construction provides no clock fallback.
	now func() time.Time // ordering and scoring clock
}

// IsPrivacySet reports the provider metadata's resolved privacy qualification
// flag.
func (a *FlowProvider) IsPrivacySet() bool { return a.PrivacySet }

// EffectiveLoadFactor returns the supplied denominator used for observed load
// percentages.
func (a *FlowProvider) EffectiveLoadFactor() int { return a.LoadFactor }

// GetBaseRPM returns the provider's configured baseline for RPM diagnostics.
func (a *FlowProvider) GetBaseRPM() int { return a.BaseRPM }

// NewGenericSelector retains the invocation callbacks and app-owned cache,
// load service, and diagnostics. Callers supply the required callbacks and
// clock; construction validates none of them and supplies no clock default.
// Hard-sticky queue reads need a load service if acquisition is denied or
// fails. The production adapter bypasses physical acquisition when that service
// is absent.
func NewGenericSelector(ports GenericSelectionPorts, cache StickyCache, concurrency *ConcurrencyService, diagnostics Diagnostics, now func() time.Time) *GenericSelector {
	return &GenericSelector{ports: ports, cache: cache, concurrencyService: concurrency, diagnostics: diagnostics, now: now}
}

// derefGroupID returns the pointed-to group ID, or zero when absent. Cache
// namespaces and diagnostic fields use this value; it does not establish that
// the group has a candidate pool.
func derefGroupID(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
}

// Select returns an acquired provider or a wait plan within the input's
// admitted group. The adapter checks the group before selection and completes
// execution data before a successful return. Basic ordering gives routed and
// eligible hard-sticky candidates their own attempts; advanced groups apply
// effective scoring and optional weighted affinity.
//
// Load percentages use EffectiveLoadFactor, while acquisition uses
// Concurrency. Basic paths exclude observed full capacity from their immediate
// pool; advanced paths keep those providers for slot probes and possible
// waits. Acquisition errors commonly advance selection or yield a wait plan.
// Session admission follows acquisition, and a rejected session returns the
// slot before another candidate is tried.
//
// Several basic branches and successful advanced acquisition attempt binding
// before NewSelectionResult hydrates the target. The adapter owns physical
// cleanup on completion failure, while session registration can already have
// occurred. Advanced feedback settings are captured in the returned result. A
// preserved-sticky context suppresses advanced rebinding after escape; basic
// spillover paths have their own binding behavior.
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
		// A failed binding read leaves the request free to select candidates. A
		// prefetched positive ID above already takes precedence over this cache
		// lookup.
		if providerID, err := s.cache.GetSessionProviderID(ctx, derefGroupID(groupID), sessionHash); err == nil {
			stickyProviderID = providerID
			stickySource = "cache"
		}
	}
	stickyEscaped := false
	if usesAdvancedScheduler && !advancedStickyWeighted && stickyProviderID > 0 {
		escapeCfg := s.ports.AdvancedSchedulerEffectiveSettingsForRequest(ctx, groupID).StickyEscape
		if reason, errorRate, ttft, shouldEscape := ShouldEscapeSticky(s.ports.AdvancedSchedulerStats(), stickyProviderID, escapeCfg); shouldEscape {
			// Feedback escape applies to this request. Mark the context before later
			// advanced acquisition so the existing binding survives a different
			// selected provider.
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

	// Without batch observations, basic selection repeats route selection with a
	// private exclusion set. Session rejection releases acquired capacity and
	// excludes that provider, while a busy or failed acquisition can become a
	// wait plan.
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
				// The waiting-count error is ignored, leaving the returned count in this
				// comparison. Redis failures return zero; the later waiting layer still
				// admits the plan under its limits.
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
	// The adapter enables this stage only with an available group ID. All
	// load-aware pools then evaluate each provider's final upstream model under
	// that group policy.
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
				// Diagnostics retain only a bounded sample of sorted routing patterns.
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
				// Privacy rejection is effective for this request even if recording the
				// provider's administrative error fails; that write does not admit the
				// candidate.
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

						// Hard routed affinity probes capacity before registering the session.
						// Session rejection returns the acquired slot; otherwise completion
						// hydrates the associated execution target.
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
								// Ignore the queue-read error and compare its returned count; Redis
								// failures return zero. Later queue admission still enforces the
								// plan's capacity and count limits.
								waitingCount, _ := s.concurrencyService.GetProviderWaitingCount(ctx, stickyProviderID)
								if waitingCount < cfg.StickySessionMaxWaiting {
									if !s.ports.CheckAndRegisterSession(ctx, stickyProvider, sessionHash) {
										stickyCacheMissReason = "session_limit"
									} else {
										// A wait still needs an execution target before forwarding resumes.
										// The completion adapter hydrates this credential-free candidate
										// even though no slot was acquired.
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
						// The routed ID no longer resolves in this candidate map. Delete the
						// stale binding best effort while continuing through this group's other
						// choices.
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
			// A routed batch read failure leaves load samples unknown. Basic selection
			// synthesizes zero load; advanced scoring uses its neutral missing-load
			// factor.
			routingLoadMap, _ := s.concurrencyService.GetProvidersLoadBatch(ctx, routingLoads)

			var routingAvailable []FlowLoad
			for _, acc := range routingCandidates {
				loadInfo := routingLoadMap[acc.ID]
				// A missing basic observation becomes an empty load record, so sorting can
				// read zero load. Advanced candidates retain nil to represent an unknown
				// sample.
				if loadInfo == nil && !usesAdvancedScheduler {
					loadInfo = &ProviderLoadInfo{ProviderID: acc.ID}
				}
				// The observation denominator can differ from physical Concurrency. Basic
				// selection admits sub-full samples for immediate probing, while advanced
				// scoring retains every hard-eligible candidate.
				if usesAdvancedScheduler || loadInfo == nil || loadInfo.LoadRate < 100 {
					routingAvailable = append(routingAvailable, FlowLoad{Provider: acc, LoadInfo: loadInfo})
				}
			}

			if len(routingAvailable) > 0 {
				// Advanced routed candidates include observed full capacity. Once their
				// sampled acquisitions and session-admitted wait choices are exhausted,
				// this advanced branch returns exhaustion to its caller.
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

				// Try the routed priority/load order, returning slots on session
				// rejection. Accepted sessions attempt binding before the completion
				// adapter hydrates the execution target.
				for _, item := range routingAvailable {
					result, err := s.ports.TryAcquireProviderSlot(ctx, item.Provider.ID, item.Provider.Concurrency)
					if err == nil && result.Acquired {
						if !s.ports.CheckAndRegisterSession(ctx, item.Provider, sessionHash) {
							result.ReleaseFunc()
							continue
						}
						if sessionHash != "" && s.cache != nil {
							// Binding is best effort after routed session admission. A failed cache
							// write keeps the slot selected while the completion adapter hydrates
							// its target.
							_ = s.cache.SetSessionProviderID(ctx, derefGroupID(groupID), sessionHash, item.Provider.ID, stickySessionTTL)
						}
						if s.ports.DebugModelRoutingEnabled() {
							s.diagnostics.printf("service.gateway", "[ModelRoutingDebug] routed select: group_id=%v model=%s session=%s provider=%d", derefGroupID(groupID), requestedModel, shortFlowSessionHash(sessionHash), item.Provider.ID)
						}
						return s.ports.NewSelectionResult(ctx, item.Provider, true, result.ReleaseFunc, nil)
					}
				}

				// After routed acquisition attempts fail, reuse that eligible order for
				// waiting. Session denial advances the order before any routed wait plan
				// is returned.
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

			// A preferred basic pool can lack load-eligible or session-eligible
			// choices. Continue with ordinary candidates from the same admitted group's
			// already loaded provider list.
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
					// The adapter has invalidated hard affinity for this request. Failed
					// cache deletion does not reverse that decision or stop ordinary
					// candidate selection.
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

				// Hard affinity accepts a slot only after session admission. A rejected
				// session returns capacity; accepted capacity refreshes binding lifetime
				// best effort before execution completion.
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
								// The accepted hard binding remains this request's choice if
								// refreshing its expiry fails; the forwarding result can still be
								// completed.
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

					// A queue-read error leaves its returned count in this comparison.
					// Redis failures return zero, and a returned wait plan defers actual
					// queue admission to the waiting layer.
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
			// Persisting the privacy diagnostic is best effort. The request rejects
			// this provider on the resolved group's requirement regardless of the write
			// outcome.
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

	// If ordinary load observations fail, advanced scoring keeps unknown samples
	// and basic acquisition uses priority/LRU order. Neither path treats that
	// observation failure as provider ineligibility.
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
			// Basic selection represents absent observations as zero load for its
			// comparator. Advanced selection retains missing samples so scoring uses
			// the unknown-load factor.
			if loadInfo == nil && !usesAdvancedScheduler {
				loadInfo = &ProviderLoadInfo{ProviderID: acc.ID}
			}
			// Load percentages use EffectiveLoadFactor, which can differ from the
			// physical slot limit. Advanced scoring keeps full observations for later
			// admission; basic probing starts with sub-full observations.
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
			// Advanced selection has already considered its sampled acquisition and
			// session-admitted wait choices. Stop here so basic fallback ordering
			// cannot replace that group's advanced policy.
			return nil, ErrNoAvailableProviders
		}

		// Basic load ordering probes one candidate at a time. Session rejection
		// returns capacity, and each unsuccessful provider ID is removed so a busy
		// record is not selected repeatedly.
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
						// The basic acquired candidate becomes the binding before hydration. A
						// failed cache write leaves capacity and session admission accepted.
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

	// Immediate basic acquisition exhausted its eligible order. Reorder all
	// hard-eligible candidates for ordinary waiting, then require session
	// admission before producing the plan.
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

// TryAdvancedWithoutLoad scores nonnil providers with unknown load samples and
// delegates acquisition to TryAdvanced. Empty or entirely nil input produces
// an unhandled result, so callers decide whether to report exhaustion.
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
			// Absent observations keep this candidate in advanced scoring with a
			// neutral load factor, allowing real acquisition to decide whether capacity
			// exists.
			LoadInfo: nil,
		})
	}
	return s.TryAdvanced(ctx, groupID, sessionHash, available)
}

// TryAdvanced scores eligible metadata with the current request's effective
// settings and tries its sampled Top-K order. Empty or entirely nil candidates
// return ok=false. Acquisition errors, missing results, and denied capacity
// advance the order; rejected sessions return acquired capacity.
//
// After a successful slot, binding is attempted unless escape preserved the
// prior binding, then the adapter completes execution data. Completion errors
// return ok=true because this path handled selection, and the physical cleanup
// is invoked. If every slot attempt fails, the same order can produce a wait
// plan after session admission. Both successful result forms capture feedback
// alpha values from these effective settings.
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
		// Ignore the read error while retaining its returned provider ID for
		// scoring. A positive ID returned alongside an error can therefore still
		// contribute the configured affinity bonus.
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
	// The sampled order is the complete advanced acquisition pool. Acquisition
	// failures advance it, session failures return capacity, and accepted binding
	// precedes adapter hydration.
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
			// The request may have escaped an older binding; the preceding preserve
			// check protects it. Otherwise this write is best effort before completion.
			_ = s.cache.SetSessionProviderID(ctx, derefGroupID(groupID), sessionHash, candidate.Provider.ID, stickySessionTTL)
		}
		selection, selectErr := s.ports.NewSelectionResult(ctx, candidate.Provider, true, result.ReleaseFunc, nil)
		if selectErr != nil {
			// The completion adapter may already have returned this slot. Its public
			// cleanup is idempotent, allowing this failure path to finish capacity
			// cleanup again.
			result.ReleaseFunc()
			return nil, true, selectErr
		}
		selection.AdvancedScheduler = true
		feedback := effectiveSettings.Feedback
		selection.AdvancedSchedulerFeedback = &feedback
		return selection, true, nil
	}
	// No sampled provider supplied usable capacity. The same order now admits
	// sessions for wait plans, preserving the selected advanced pool and captured
	// feedback settings.
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

// TryLegacyOrder attempts a copied candidate order when basic load reads fail.
// It sorts by priority and last use, with the requested OAuth preference,
// while leaving the caller's slice order intact. Providers are expected to be
// nonnil.
//
// Each acquired slot still requires session admission; rejection returns
// capacity before trying the next candidate. A successful candidate attempts
// binding before result hydration, and cache errors do not reject it.
// Completion failure returns an error and relies on NewSelectionResult's
// physical cleanup. Exhausted acquisition returns ok=false so the caller can
// construct a wait plan.
func (s *GenericSelector) TryLegacyOrder(ctx context.Context, candidates []*FlowProvider, groupID *int64, sessionHash string, preferOAuth bool) (*FlowSelection, bool, error) {
	ordered := append([]*FlowProvider(nil), candidates...)
	flowSortProvidersByPriorityAndLastUsed(ordered, preferOAuth)

	// The failed-load basic path ignores each acquisition error and continues.
	// Session rejection returns acquired capacity before another candidate can
	// attempt admission.
	for _, acc := range ordered {
		result, err := s.ports.TryAcquireProviderSlot(ctx, acc.ID, acc.Concurrency)
		if err == nil && result.Acquired {
			if !s.ports.CheckAndRegisterSession(ctx, acc, sessionHash) {
				result.ReleaseFunc()
				continue
			}
			if sessionHash != "" && s.cache != nil {
				// Load failure selected this candidate by priority/LRU. Binding failure
				// leaves the selected capacity usable while execution completion can still
				// fail.
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

// SortCandidatesForFallback mutates the supplied slice for basic wait
// selection. The random mode shuffles equal priorities after sorting,
// including OAuth peers; other modes retain the priority and last-used order.
func (s *GenericSelector) SortCandidatesForFallback(providers []*FlowProvider, preferOAuth bool, mode string) {
	if mode == "random" {
		// Random waiting keeps lower priority groups ahead while shuffling all
		// equal-priority peers. That shuffle can change the OAuth order established
		// by the initial sort.
		flowSortProvidersByPriorityOnly(providers, preferOAuth)
		flowShuffleWithinPriority(providers, s.now)
	} else {
		flowSortProvidersByPriorityAndLastUsed(providers, preferOAuth)
	}
}

// shortFlowSessionHash returns an empty string or at most eight leading bytes
// for diagnostics. This byte prefix is not a unique session identity and can
// cut inside a multibyte character.
func shortFlowSessionHash(sessionHash string) string {
	if sessionHash == "" {
		return ""
	}
	if len(sessionHash) <= 8 {
		return sessionHash
	}
	// The log sample truncates bytes, so callers cannot use it for Unicode-safe
	// display or complete session correlation.
	return sessionHash[:8]
}

// SelectOnly returns a provider after routing, eligibility, and execution
// hydration with select-only context. The production adapter skips capacity
// and session registration under that context; selection can still read or
// write session bindings.
func (s *GenericSelector) SelectOnly(ctx context.Context, input SelectionInput) (*FlowProvider, error) {
	return s.selectRoutes(WithSelectOnly(ctx), input.GroupID, input.SessionHash, input.RequestedModel, input.ExcludedIDs)
}
