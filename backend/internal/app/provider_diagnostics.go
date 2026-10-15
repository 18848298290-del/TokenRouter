package app

import (
	"context"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/selection"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	schedulerhttp "github.com/TokenFlux/TokenRouter/internal/scheduler/httpapi"
)

// provider_diagnostics.go connects administrator scheduler diagnostics to
// execution selection rules and shared scheduling state. The source adapter
// converts provider administration reads into execution targets while groups
// remain routing values, so diagnostic evaluation consumes the same shared
// eligibility services as gateway selection.

// providerDiagnosticSource adapts administrator provider and group queries for
// scheduler diagnostics.
type providerDiagnosticSource struct {
	providers *provider.Admin     // provider administration reads
	groups    *routing.GroupAdmin // routing administration reads
}

// provideProviderDiagnostics binds read-only diagnostics to shared feedback,
// parameters, and concurrency. shared supplies the required state, while
// generic and compatible selection provide the execution eligibility rules used
// to explain the candidate pool.
func provideProviderDiagnostics(admin *provider.Admin, groups *routing.GroupAdmin, concurrency *scheduler.ConcurrencyService,

	gateway *selection.Generic, openai *selection.Compatible, shared *schedulerSharedState,
) *selection.Diagnostics {
	return selection.NewDiagnostics(providerDiagnosticSource{providers: admin, groups: groups}, selection.Shared{Concurrency: concurrency, Parameters: shared.Parameters, Feedback: shared.Feedback}, gateway, openai)
}

// provideSchedulerDiagnosticsHTTP exposes selection diagnostics through the
// scheduler administration handler.
func provideSchedulerDiagnosticsHTTP(core *selection.Diagnostics) *schedulerhttp.DiagnosticsHandler {
	return schedulerhttp.NewDiagnosticsHandler(core)
}

// GetProvider converts the administered provider record into a copied execution
// target.
func (s providerDiagnosticSource) GetProvider(ctx context.Context, id int64) (*gatewayprovider.ExecutionProvider, error) {
	value, err := s.providers.GetProvider(ctx, id)
	return gatewayprovider.NewExecutionProvider(value), err
}

// GetGroup delegates the requested routing group read to group administration.
func (s providerDiagnosticSource) GetGroup(ctx context.Context, id int64) (*routing.Group, error) {
	return s.groups.GetGroup(ctx, id)
}

// ListProvidersForSchedulerScoreFilter converts filtered administrator provider
// records for diagnostic selection.
func (s providerDiagnosticSource) ListProvidersForSchedulerScoreFilter(ctx context.Context, platform, kind, status, search string, gid int64, privacy string) ([]gatewayprovider.ExecutionProvider, error) {
	values, err := s.providers.ListProvidersForSchedulerScoreFilter(ctx, platform, kind, status, search, gid, privacy)
	return gatewayprovider.ExecutionProviders(values), err
}

// ListSchedulableProvidersForAdvancedSchedulerScore converts administrator
// candidates for the shared advanced score evaluator.
func (s providerDiagnosticSource) ListSchedulableProvidersForAdvancedSchedulerScore(ctx context.Context, gid *int64, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	values, err := s.providers.ListSchedulableProvidersForAdvancedSchedulerScore(ctx, gid, platform)
	return gatewayprovider.ExecutionProviders(values), err
}
