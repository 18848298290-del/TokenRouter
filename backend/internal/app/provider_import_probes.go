package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerhttp "github.com/TokenFlux/TokenRouter/internal/provider/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// provider_import_probes.go constructs Grok import probing and connects
// imported provider snapshots to its queue. Grok import schedules quota
// observations using a provider routing snapshot captured after the import
// write. The queue has its own three-worker, twenty-five-second probe options
// and registered shutdown, while import-side tasks use the application tracker
// and proxy reads retain lookup errors.

// grokQuotaImportProbe narrows shared Grok quota queries into import summaries.
type grokQuotaImportProbe struct {
	Source *provider.GrokQuotaService // imported provider probing
}

// provideProviderImportProbes constructs lazy import probing with three workers
// and a twenty-five-second task budget. Call before lifecycle startup; its stop
// hook waits at producer shutdown level twenty while provider and storage
// dependencies remain usable.
func provideProviderImportProbes(manager *lifecycle.Manager) *provider.GrokImportProbeScheduler {
	queue := provider.NewGrokImportProbeScheduler(provider.GrokImportProbeOptions{Concurrency: 3, Timeout: 25 * time.Second, Debug: slog.Debug, Info: slog.Info, Warn: slog.Warn, Error: slog.Error})
	// Imported snapshots may still be queued after HTTP completion. Register
	// queue draining before the later quota services and shared storage enter their shutdown levels.
	manager.Register(lifecycle.Hook{Name: "ProviderImportProbes", StopOrder: shutdownStageProducers, Stop: queue.StopContext})
	return queue
}

// provideGrokOAuthWithImports assembles authorization, token normalization,
// import persistence, and reconciliation for HTTP. The proxy callback reports
// absence and returns lookup errors, imported snapshots enter the shared probe
// queue, and background import work is registered through tasks.
func provideGrokOAuthWithImports(
	queue *provider.GrokImportProbeScheduler,
	grokOAuthService *provider.GrokAuthorization,
	adminService *provider.Admin,
	proxies *egress.ProxyAdmin,
	tasks *lifecycle.Tasks,
	quotaService *provider.GrokQuotaService,
	reconciler provider.GrokOAuthReconciler,
) *providerhttp.GrokOAuthHandler {
	proxyURL := func(ctx context.Context, id int64) (string, bool, error) {
		value, err := proxies.GetProxy(ctx, id)
		if err != nil || value == nil {
			return "", false, err
		}
		return value.URL(), true, nil
	}
	runTask := func(label string, work func()) { tasks.Go(label, work) }
	var auth *provider.GrokAuthorization
	if grokOAuthService != nil {
		auth = grokOAuthService
	}
	var quota *provider.GrokQuotaService
	if quotaService != nil {
		quota = quotaService
	}
	imports := provider.NewGrokProviderImport(auth, provider.GrokProviderImportOptions{Get: adminService.GetProvider, Create: adminService.CreateProvider, Update: adminService.UpdateProvider, NormalizeToken: grok.NormalizeSSOToken, LogError: slog.Error, RunTask: runTask, Schedule: func(value *provider.Record) {
		snapshot := value.RoutingSnapshot()
		queue.Schedule(grokQuotaImportProbe{quota}, &snapshot)
	}})
	return providerhttp.NewGrokOAuthHandler(auth, imports, quota, providerhttp.GrokOAuthHTTPOptions{ProxyURL: proxyURL, RuntimeSanity: func() any { return grok.RuntimeSanity() }, Reconciler: reconciler})
}

// QueryQuota returns model, response status, and header observations from
// shared quota probing. A nil result remains nil, while any available summary
// is returned together with the original query error.
func (p grokQuotaImportProbe) QueryQuota(ctx context.Context, id int64) (*provider.GrokImportProbeResult, error) {
	v, err := p.Source.QueryQuota(ctx, id)
	if v == nil {
		return nil, err
	}
	return &provider.GrokImportProbeResult{Model: v.Model, StatusCode: v.StatusCode, HeadersObserved: v.HeadersObserved}, err
}
