package selection

import (
	"context"
	"slices"
	"time"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
	schedulercore "github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// diagnosticScope maps diagnostic inputs to execution targets, registering same-ID snapshots separately.
type diagnosticScope struct {
	source         DiagnosticSource
	next           uint64
	providerValues map[uint64]*gatewayprovider.ExecutionProvider
	groupValues    map[uint64]*routing.Group
}

func (s *Diagnostics) diagnosticCore() (*schedulercore.DiagnosticService, *diagnosticScope) {
	scope := &diagnosticScope{source: s.source, providerValues: map[uint64]*gatewayprovider.ExecutionProvider{}, groupValues: map[uint64]*routing.Group{}}
	var source schedulercore.DiagnosticSource
	if s.source != nil {
		source = scope
	}
	core := schedulercore.NewDiagnosticService(source, s.concurrencyService, schedulercore.DiagnosticPorts{
		Now: time.Now,
		Stats: func() *schedulercore.RuntimeStats {
			return s.feedback
		},
		Effective: func(ctx context.Context, g *schedulercore.DiagnosticGroup) (policy.EffectiveSettings, policy.RuntimeSettings) {
			effective, runtime := s.effectiveSettings(ctx, scope.originalGroup(g))
			return effective, runtime
		},
		Prepare: func(ctx context.Context, g *schedulercore.DiagnosticGroup, providers []schedulercore.DiagnosticProvider) context.Context {
			var values []gatewayprovider.ExecutionProvider
			if providers != nil {
				values = make([]gatewayprovider.ExecutionProvider, len(providers))
				for i, v := range providers {
					values[i] = *scope.providerValues[v.ProjectionID]
				}
			}
			return s.prepareEligibilityContext(ctx, scope.originalGroup(g), values)
		},
		Filter: func(ctx context.Context, a *schedulercore.DiagnosticProvider, g *schedulercore.DiagnosticGroup, request schedulercore.AdvancedSchedulerScoreDiagnosticRequest, now time.Time) string {
			return s.diagnosticPlatformFilterReason(ctx, scope.providerValues[a.ProjectionID], scope.originalGroup(g), request, now)
		},
		Quota: func(id uint64, now time.Time) float64 {
			return openAIQuotaHeadroomFactor(scope.providerValues[id], now)
		},
	})
	return core, scope
}

func (s *diagnosticScope) originalGroup(g *schedulercore.DiagnosticGroup) *routing.Group {
	if g == nil {
		return nil
	}
	return s.groupValues[g.ProjectionID]
}

func (s *diagnosticScope) group(v *routing.Group) *schedulercore.DiagnosticGroup {
	if v == nil {
		return nil
	}
	s.next++
	id := s.next
	s.groupValues[id] = v
	return &schedulercore.DiagnosticGroup{ProjectionID: id, ID: v.ID, Name: v.Name, SortOrder: v.SortOrder, Advanced: v.UsesAdvancedScheduler(), RequirePrivacySet: v.RequirePrivacySet, AdvancedSchedulerOverrides: accessview.CloneGroupAdvancedSchedulerOverrides(v.AdvancedSchedulerOverrides)}
}

func (s *diagnosticScope) provider(v *gatewayprovider.ExecutionProvider) *schedulercore.DiagnosticProvider {
	if v == nil {
		return nil
	}
	s.next++
	id := s.next
	s.providerValues[id] = v
	a := &schedulercore.DiagnosticProvider{ProjectionID: id, ID: v.Record.ID, Name: v.Record.Name, Platform: v.Record.Platform, Type: v.Record.Type, Status: v.Record.Status, Priority: v.Record.Priority, LoadFactor: v.View().EffectiveLoadFactor(), Schedulable: v.Record.Schedulable, AutoPauseOnExpired: v.Record.AutoPauseOnExpired, PrivacySet: v.View().IsPrivacySet(), SubscriptionPriority: v.View().IsOpenAIChatGPTSubscription(), ExpiresAt: v.Record.ExpiresAt, OverloadUntil: v.Record.OverloadUntil, RateLimitResetAt: v.Record.RateLimitResetAt, TempUnschedulableUntil: v.Record.TempUnschedulableUntil, SessionWindowEnd: v.Record.SessionWindowEnd, GroupIDs: slices.Clone(v.Record.GroupIDs)}
	if v.Record.ProviderGroups != nil {
		a.ProviderGroups = make([]schedulercore.DiagnosticProviderGroup, len(v.Record.ProviderGroups))
		for i, g := range v.Record.ProviderGroups {
			a.ProviderGroups[i] = schedulercore.DiagnosticProviderGroup{Group: s.group((*routing.Group)(g.Group))}
		}
	}
	if v.Record.Groups != nil {
		a.Groups = make([]*schedulercore.DiagnosticGroup, len(v.Record.Groups))
		for i, g := range v.Record.Groups {
			a.Groups[i] = s.group((*routing.Group)(g))
		}
	}
	return a
}

func (s *diagnosticScope) providerSlice(values []gatewayprovider.ExecutionProvider) []schedulercore.DiagnosticProvider {
	if values == nil {
		return nil
	}
	out := make([]schedulercore.DiagnosticProvider, len(values))
	for i := range values {
		out[i] = *s.provider(&values[i])
	}
	return out
}

func (s *diagnosticScope) GetProvider(ctx context.Context, id int64) (*schedulercore.DiagnosticProvider, error) {
	v, err := s.source.GetProvider(ctx, id)
	return s.provider(v), err
}

func (s *diagnosticScope) GetGroup(ctx context.Context, id int64) (*schedulercore.DiagnosticGroup, error) {
	v, err := s.source.GetGroup(ctx, id)
	return s.group(v), err
}

func (s *diagnosticScope) ListProvidersForSchedulerScoreFilter(ctx context.Context, platform, kind, status, search string, groupID int64, privacy string) ([]schedulercore.DiagnosticProvider, error) {
	v, err := s.source.ListProvidersForSchedulerScoreFilter(ctx, platform, kind, status, search, groupID, privacy)
	return s.providerSlice(v), err
}

func (s *diagnosticScope) ListSchedulableProvidersForAdvancedSchedulerScore(ctx context.Context, groupID *int64, platform string) ([]schedulercore.DiagnosticProvider, error) {
	v, err := s.source.ListSchedulableProvidersForAdvancedSchedulerScore(ctx, groupID, platform)
	return s.providerSlice(v), err
}
