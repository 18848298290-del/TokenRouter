package selection

import (
	"context"
	"log/slog"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	schedulercore "github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// projectionScope assigns per-call IDs to retain distinct snapshots of the same provider or group.
type projectionScope struct {
	next      uint64
	providers map[uint64]*gatewayprovider.ExecutionProvider
	groups    map[uint64]*routing.Group
}

func (g *projectionScope) provider(value *gatewayprovider.ExecutionProvider) *schedulercore.FlowProvider {
	if value == nil {
		return nil
	}
	g.next++
	id := g.next
	g.providers[id] = value
	var plan *routing.CandidatePlan
	if captured, ok := value.Route.Candidate(); ok {
		plan = &captured
	}
	return &schedulercore.FlowProvider{Plan: plan, ProjectionID: id, ID: value.Record.ID, Name: value.Record.Name, Platform: value.Record.Platform, Type: value.Record.Type, Concurrency: value.Record.Concurrency, Priority: value.Record.Priority, LastUsedAt: cloneFlowTime(value.Record.LastUsedAt), SessionWindowEnd: cloneFlowTime(value.Record.SessionWindowEnd), LoadFactor: value.View().EffectiveLoadFactor(), BaseRPM: gatewayprovider.ExecutionRuntimeConfig(value).GetBaseRPM(), PrivacySet: value.View().IsPrivacySet()}
}

func (g *projectionScope) group(value *routing.Group) *schedulercore.FlowGroup {
	if value == nil {
		return nil
	}
	g.next++
	id := g.next
	g.groups[id] = value
	return &schedulercore.FlowGroup{ProjectionID: id, Group: *routing.CloneGroup(value)}
}

func (g *projectionScope) oldProvider(v *schedulercore.FlowProvider) *gatewayprovider.ExecutionProvider {
	if v == nil {
		return nil
	}
	return g.providers[v.ProjectionID]
}

func (g *projectionScope) oldGroup(v *schedulercore.FlowGroup) *routing.Group {
	if v == nil {
		return nil
	}
	return g.groups[v.ProjectionID]
}

func (g *projectionScope) values(values []gatewayprovider.ExecutionProvider) []schedulercore.FlowProvider {
	if values == nil {
		return nil
	}
	out := make([]schedulercore.FlowProvider, len(values))
	for i := range values {
		out[i] = *g.provider(&values[i])
	}
	return out
}

func (g *projectionScope) oldValues(values []schedulercore.FlowProvider) []gatewayprovider.ExecutionProvider {
	if values == nil {
		return nil
	}
	out := make([]gatewayprovider.ExecutionProvider, len(values))
	for i := range values {
		out[i] = *g.oldProvider(&values[i])
	}
	return out
}

func (g *projectionScope) pointers(values []*gatewayprovider.ExecutionProvider) []*schedulercore.FlowProvider {
	if values == nil {
		return nil
	}
	out := make([]*schedulercore.FlowProvider, len(values))
	for i, a := range values {
		out[i] = g.provider(a)
	}
	return out
}

func (g *projectionScope) selection(value *gatewayprovider.SelectionResult) *schedulercore.FlowSelection {
	if value == nil {
		return nil
	}
	out := &schedulercore.FlowSelection{Provider: g.provider(value.Provider), Acquired: value.Acquired, ReleaseFunc: value.ReleaseFunc, WaitPlan: value.WaitPlan, AdvancedScheduler: value.AdvancedScheduler}
	if v := value.AdvancedSchedulerFeedback; v != nil {
		out.AdvancedSchedulerFeedback = &policy.FeedbackConfig{ErrorRateAlpha: v.ErrorRateAlpha, TtftAlpha: v.TtftAlpha}
	}
	return out
}

func (g *projectionScope) restore(value *schedulercore.FlowSelection) *gatewayprovider.SelectionResult {
	if value == nil {
		return nil
	}
	out := &gatewayprovider.SelectionResult{Provider: g.oldProvider(value.Provider), Acquired: value.Acquired, ReleaseFunc: value.ReleaseFunc, WaitPlan: value.WaitPlan, AdvancedScheduler: value.AdvancedScheduler}
	if v := value.AdvancedSchedulerFeedback; v != nil {
		out.AdvancedSchedulerFeedback = &policy.FeedbackConfig{ErrorRateAlpha: v.ErrorRateAlpha, TtftAlpha: v.TtftAlpha}
	}
	return out
}

func (s *Generic) genericSelector() (*schedulercore.GenericSelector, *projectionScope) {
	scope := &projectionScope{providers: map[uint64]*gatewayprovider.ExecutionProvider{}, groups: map[uint64]*routing.Group{}}
	diagnostics := schedulercore.Diagnostics{Logf: logging.LegacyPrintf, Event: logging.Event}

	diagnostics.Event = func(level, event string, args ...any) {
		switch level {
		case "info":
			slog.Info(event, args...)
		case "warn":
			slog.Warn(event, args...)
		case "error":
			slog.Error(event, args...)
		default:
			slog.Debug(event, args...)
		}
	}
	ports := schedulercore.GenericSelectionPorts{
		ForcePlatform: func(ctx context.Context) (string, bool) {
			value, ok := apikey.ForcePlatformFromContext(ctx)
			return value, ok
		},
		ResolveGroupByID: func(ctx context.Context, id int64) (*schedulercore.FlowGroup, error) {
			v, err := currentSelectionGroup(ctx, &id, s.resolveGroupByID)
			return scope.group(v), err
		},
		ResolveGatewayGroup: func(ctx context.Context, id *int64) (*schedulercore.FlowGroup, *int64, error) {
			v, finalID, err := s.resolveGatewayGroup(ctx, id)
			return scope.group(v), finalID, err
		},
		HydrateSelectedProvider: func(ctx context.Context, a *schedulercore.FlowProvider) (*schedulercore.FlowProvider, error) {
			v, err := s.hydrateSelectedProvider(ctx, scope.oldProvider(a))
			return scope.provider(v), err
		},
		RoutingProviderIDsForRequest: s.routingProviderIDsForRequest,
		GetSchedulableProvider: func(ctx context.Context, id int64) (*schedulercore.FlowProvider, error) {
			v, err := s.getSchedulableProvider(ctx, id)
			return scope.provider(v), err
		},
		IsProviderInGroup: func(a *schedulercore.FlowProvider, id *int64) bool {
			return s.isProviderInGroup(scope.oldProvider(a), id)
		},
		LogDetailedSelectionFailure: func(ctx context.Context, id *int64, hash, model, platform string, values []schedulercore.FlowProvider, excluded map[int64]struct{}, mixed bool) string {
			return summarizeSelectionFailureStats(s.logDetailedSelectionFailure(ctx, id, hash, model, platform, scope.oldValues(values), excluded, mixed))
		},
		AdvancedSchedulerStats: func() *schedulercore.RuntimeStats { return s.advancedSchedulerStats() },
		AdvancedSchedulerEffectiveSettingsForRequest: func(ctx context.Context, id *int64) policy.EffectiveSettings {
			return s.advancedSchedulerEffectiveSettingsForRequest(ctx, id)
		},
		CheckGroupModelRestriction:         s.checkGroupModelRestriction,
		GroupMappedModelForProviderLayer:   s.groupMappedModelForProviderLayer,
		DebugModelRoutingEnabled:           s.debugModelRoutingEnabled,
		NeedsUpstreamGroupRestrictionCheck: s.needsUpstreamGroupRestrictionCheck,
		TryAcquireProviderSlot:             s.tryAcquireProviderSlot,
		PrefetchedSticky:                   prefetchedStickyProviderIDFromContext,
		SetProviderError: func(ctx context.Context, id int64, message string) error {
			return s.setProviderError(ctx, id, message)
		},
		SchedulingConfig: func() schedulercore.FlowOptions {
			v := s.schedulingConfig()
			return schedulercore.FlowOptions{LoadBatchEnabled: v.LoadBatchEnabled, PreferSoonestReset: v.PreferSoonestReset, FallbackMaxWaiting: v.FallbackMaxWaiting, StickySessionMaxWaiting: v.StickySessionMaxWaiting, FallbackSelectionMode: v.FallbackSelectionMode, FallbackWaitTimeout: v.FallbackWaitTimeout, StickySessionWaitTimeout: v.StickySessionWaitTimeout}
		},

		CheckClaudeCodeRestriction: func(ctx context.Context, id *int64) (*schedulercore.FlowGroup, *int64, error) {
			g, finalID, err := s.checkClaudeCodeRestriction(ctx, id)
			return scope.group(g), finalID, err
		},
		WithGroupContext: func(ctx context.Context, g *schedulercore.FlowGroup) context.Context {
			return s.withGroupContext(ctx, scope.oldGroup(g))
		},
		ResolvePlatform: func(ctx context.Context, id *int64, g *schedulercore.FlowGroup) (string, bool, error) {
			return s.resolvePlatform(ctx, id, scope.oldGroup(g))
		},
		ListSchedulableProviders: func(ctx context.Context, id *int64, platform string, forced bool) ([]schedulercore.FlowProvider, bool, error) {
			v, mixed, err := s.listSchedulableProviders(ctx, id, platform, forced)
			return scope.values(v), mixed, err
		},
		WithRPMPrefetch: func(ctx context.Context, v []schedulercore.FlowProvider) context.Context {
			return s.withRPMPrefetch(ctx, scope.oldValues(v))
		},
		WithWindowCostPrefetch: func(ctx context.Context, v []schedulercore.FlowProvider) context.Context {
			return s.withWindowCostPrefetch(ctx, scope.oldValues(v))
		},
		IsProviderAllowedForPlatform: func(a *schedulercore.FlowProvider, platform string, mixed bool) bool {
			return s.isProviderAllowedForPlatform(scope.oldProvider(a), platform, mixed)
		},
		IsProviderSchedulableForSelection: func(a *schedulercore.FlowProvider) bool {
			return s.isProviderSchedulableForSelection(scope.oldProvider(a))
		},
		IsProviderSchedulableForQuota: func(a *schedulercore.FlowProvider) bool { return s.isProviderSchedulableForQuota(scope.oldProvider(a)) },
		IsProviderSchedulableForModelSelection: func(ctx context.Context, a *schedulercore.FlowProvider, model string) bool {
			return s.isProviderSchedulableForModelSelection(ctx, scope.oldProvider(a), model)
		},
		IsProviderSchedulableForRPM: func(ctx context.Context, a *schedulercore.FlowProvider, sticky bool) bool {
			return s.isProviderSchedulableForRPM(ctx, scope.oldProvider(a), sticky)
		},
		IsProviderSchedulableForWindowCost: func(ctx context.Context, a *schedulercore.FlowProvider, sticky bool) bool {
			return s.isProviderSchedulableForWindowCost(ctx, scope.oldProvider(a), sticky)
		},
		IsModelSupportedByProviderWithContext: func(ctx context.Context, a *schedulercore.FlowProvider, model string) bool {
			return s.isModelSupportedByProviderWithContext(ctx, scope.oldProvider(a), model)
		},
		IsUpstreamModelRestrictedByGroup: func(ctx context.Context, id int64, a *schedulercore.FlowProvider, model string) bool {
			return s.isUpstreamModelRestrictedByGroup(ctx, id, scope.oldProvider(a), model)
		},
		ShouldClearStickySessionForProviderLayer: func(ctx context.Context, a *schedulercore.FlowProvider, model string) bool {
			return s.shouldClearStickySessionForProviderLayer(ctx, scope.oldProvider(a), model)
		},
		CheckAndRegisterSession: func(ctx context.Context, a *schedulercore.FlowProvider, session string) bool {
			return s.checkAndRegisterSession(ctx, scope.oldProvider(a), session)
		},
		GroupModelUnsupportedErrorIfApplicable: func(ctx context.Context, providers []schedulercore.FlowProvider, model, platform string, excluded map[int64]struct{}, mixed bool, id *int64, g *schedulercore.FlowGroup) error {
			return s.groupModelUnsupportedErrorIfApplicable(ctx, scope.oldValues(providers), model, platform, excluded, mixed, id, scope.oldGroup(g))
		},
		NewSelectionResult: func(ctx context.Context, a *schedulercore.FlowProvider, acquired bool, release func(), wait *schedulercore.ProviderWaitPlan) (*schedulercore.FlowSelection, error) {
			v, err := s.newSelectionResult(ctx, scope.oldProvider(a), acquired, release, wait)
			return scope.selection(v), err
		},
	}
	if s.groupRepo != nil {
		ports.ReadGroup = func(ctx context.Context, id int64) (*schedulercore.FlowGroup, error) {
			v, err := s.groupRepo.GetByID(ctx, id)
			return scope.group(v), err
		}
	}
	return schedulercore.NewGenericSelector(ports, s.cache, s.concurrencyService, diagnostics, time.Now), scope
}

func cloneFlowTime(v *time.Time) *time.Time {
	if v == nil {
		return nil
	}
	copy := *v
	return &copy
}
