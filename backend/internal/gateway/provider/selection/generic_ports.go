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

// A scheduler can see several records with one persisted provider ID during a
// single selection: a lightweight candidate, a fresh record, and a database
// recheck. Each conversion receives a separate per-call identifier, so
// recovering an execution target returns the exact record that produced the
// selected metadata. Group conversions follow the same rule while cloning
// scheduling data for the core.
//
// The invocation's lookup maps retain execution targets and original group
// references. Scheduler callbacks retrieve those references for eligibility,
// hydration, and final selection conversion; credentials stay in the gateway
// objects. Slice conversions preserve nil inputs, and result conversions carry
// slot release, waiting, and captured feedback settings across the scheduler
// call.

// projectionScope retains execution targets and groups under per-call
// conversion identifiers. Callers serialize its callbacks within one selection
// invocation because its maps and counter have no synchronization for
// concurrent access.
type projectionScope struct {
	// next allocates identifiers for both providers and groups in this invocation.
	// providers and groups retain each conversion separately, including repeated persisted IDs from later reads.
	next      uint64                                        // per-call conversion counter
	providers map[uint64]*gatewayprovider.ExecutionProvider // associated execution target pointers
	groups    map[uint64]*routing.Group                     // associated source group pointers
}

// provider registers one execution target and returns its scheduler metadata.
// Nil input returns nil, and every nonnil conversion consumes a new per-call
// identifier even when the persisted provider ID matches an earlier target.
//
// The lookup retains the input target pointer, so fresh and database records
// cannot overwrite the candidate's association. A captured route plan and
// copied time values accompany credential-free metadata. Callers use this scope
// again to recover the exact selected execution target.
func (g *projectionScope) provider(value *gatewayprovider.ExecutionProvider) *schedulercore.FlowProvider {
	if value == nil {
		return nil
	}
	// A later record can have the same persisted ID but different credentials or
	// group membership. Give this conversion its own association before exposing metadata.
	g.next++
	id := g.next
	g.providers[id] = value
	var plan *routing.CandidatePlan
	if captured, ok := value.Route.Candidate(); ok {
		plan = &captured
	}
	return &schedulercore.FlowProvider{Plan: plan, ProjectionID: id, ID: value.Record.ID, Name: value.Record.Name, Platform: value.Record.Platform, Type: value.Record.Type, Concurrency: value.Record.Concurrency, Priority: value.Record.Priority, LastUsedAt: cloneFlowTime(value.Record.LastUsedAt), SessionWindowEnd: cloneFlowTime(value.Record.SessionWindowEnd), LoadFactor: value.View().EffectiveLoadFactor(), BaseRPM: gatewayprovider.ExecutionRuntimeConfig(value).GetBaseRPM(), PrivacySet: value.View().IsPrivacySet()}
}

// group registers a group reference and returns cloned scheduling data with its
// own per-call identifier. Nil input returns nil; subsequent conversions of the
// same persisted group remain independently associated.
//
// The original group pointer stays in the invocation lookup for callbacks that
// need gateway policy data. The core receives routing.CloneGroup's copied value
// and fields, so group calculations can use their own data while oldGroup still
// identifies the source of that conversion.
func (g *projectionScope) group(value *routing.Group) *schedulercore.FlowGroup {
	if value == nil {
		return nil
	}
	// A fresh group can differ from a prior read with the same ID. Retain this
	// exact source under a new association while supplying cloned scheduling data.
	g.next++
	id := g.next
	g.groups[id] = value
	return &schedulercore.FlowGroup{ProjectionID: id, Group: *routing.CloneGroup(value)}
}

// oldProvider retrieves the execution target associated with this scheduler
// value's per-call ID. Nil input returns nil. Callers pass values converted by
// this scope: identifiers restart per invocation, so a foreign value can
// resolve to an unrelated target with the same identifier.
func (g *projectionScope) oldProvider(v *schedulercore.FlowProvider) *gatewayprovider.ExecutionProvider {
	if v == nil {
		return nil
	}
	return g.providers[v.ProjectionID]
}

// oldGroup retrieves the source group associated with this scheduler value's
// per-call ID. Nil input returns nil; callers pass values converted by the same
// scope to retain their policy reference.
func (g *projectionScope) oldGroup(v *schedulercore.FlowGroup) *routing.Group {
	if v == nil {
		return nil
	}
	return g.groups[v.ProjectionID]
}

// values converts candidate values to a scheduler slice while registering each
// execution target in this scope. Nil input remains nil; the resulting metadata
// has new slice storage and retained source target references.
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

// oldValues builds an execution-value slice from scheduler records registered
// in this scope. Nil input remains nil; each value copies its target structure
// while referenced provider data keeps its existing ownership.
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

// pointers converts each execution pointer and registers it in this
// invocation's lookup. Nil slices remain nil, and nil elements stay nil so
// scheduler callers retain the source candidate shape.
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

// selection converts a gateway result for scheduler callbacks using this
// scope's provider association. Nil input remains nil; release and wait handles
// stay shared, while present feedback alpha values receive a separate value.
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

// restore converts a scheduler result to the gateway target registered for its
// per-call ID. Nil input remains nil; release and wait handles stay attached
// and present feedback settings are copied by value.
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

// genericSelector creates a scheduler adapter and target lookup for one generic
// selection call. Its callbacks bind this selector's group admission, provider
// reads, mapping, quota, session, and completion rules to the shared core.
//
// Every callback that crosses the scheduler API uses the same scope, retaining
// distinct candidate, hydrated, and group references during the invocation. The
// app-owned cache, concurrency service, and feedback remain shared across
// calls. Callers recover final gateway results through the returned scope after
// the core finishes.
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
			return s.schedulingConfig()
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

// cloneFlowTime returns a separate time.Time value for scheduler metadata. Nil
// input remains nil, while populated values can be replaced through the
// returned pointer independently of the provider's time pointer.
func cloneFlowTime(v *time.Time) *time.Time {
	if v == nil {
		return nil
	}
	copy := *v
	return &copy
}
