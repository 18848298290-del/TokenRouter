package selection

import (
	"context"
	"log/slog"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	schedulercore "github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// modelRejectionSources converts candidates to model-rejection inputs for routing.
func modelRejectionSources(providers []gatewayprovider.ExecutionProvider) []routing.ModelRejectionSource {
	sources := make([]routing.ModelRejectionSource, len(providers))
	for i := range providers {
		value := &providers[i]
		sources[i] = gatewayprovider.ModelRejectionProvider(gatewayprovider.ExecutionRecord(value))
	}
	return sources
}

// readSnapshotProvider reads a snapshot and wraps it as an execution provider.
func readSnapshotProvider(ctx context.Context, source Snapshots, id int64) (*gatewayprovider.ExecutionProvider, error) {
	value, err := source.GetProvider(ctx, id)
	return gatewayprovider.NewExecutionProvider(value), err
}

func readSnapshotProviders(ctx context.Context, source Snapshots, group *int64, platform string, forced bool) ([]gatewayprovider.ExecutionProvider, bool, error) {
	values, mixed, err := source.ListProviders(ctx, group, platform, forced)
	return gatewayprovider.ExecutionProviders(values), mixed, err
}

func (s *Generic) debugModelRoutingEnabled() bool { return s != nil && s.options.DebugRouting }

func (s *Generic) windowCostGuard() *billing.WindowCostGuard { return s.window }

func (s *Compatible) runtimeBlockState() *providercore.RuntimeBlockState { return s.runtime }

func (s *Compatible) getOpenAIProviderModelTransientState() *providercore.ModelTransientState {
	if s == nil {
		return nil
	}
	return s.modelTransient
}

func (s *Compatible) getOpenAIProxyStreamCircuit() *egress.ProxyStreamCircuit {
	if s == nil {
		return nil
	}
	return s.proxyCircuit
}

func (s *Compatible) ResponseStateStore() session.OpenAIWSStateStore {
	if s == nil {
		return nil
	}
	return s.responseState
}

func (s *Compatible) OpenAIHTTPResponseStickyTTL() time.Duration {
	if s != nil && s.options.ResponseTTL > 0 {
		return s.options.ResponseTTL
	}
	return time.Hour
}

func (s *Compatible) stickyStats() *schedulercore.StickyStats {
	if s == nil {
		return &schedulercore.StickyStats{}
	}
	return s.stickyMetrics
}

func resolveProviderUpstreamModel(ctx context.Context, value *gatewayprovider.ExecutionProvider, model string) string {
	return gatewayprovider.ExecutionModelPolicy(value).UpstreamModel(ctx, model)
}

func mapAntigravityModel(value *gatewayprovider.ExecutionProvider, model string) string {
	return provideradapter.MapAntigravityModel(gatewayprovider.ExecutionRecord(value), model)
}

// defaultWindowCostGuard creates a window-cost guard with default options.
func defaultWindowCostGuard() *billing.WindowCostGuard {
	return billing.NewWindowCostGuard(nil, nil, billing.WindowCostGuardOptions{Now: time.Now, Stats: billing.SharedWindowCostMetrics(), Log: func(format string, args ...any) { logging.LegacyPrintf("service.gateway", format, args...) }, Debug: slog.Debug})
}
