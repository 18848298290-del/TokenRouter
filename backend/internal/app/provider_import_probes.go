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

// grokQuotaImportProbe returns quota probe summaries to the import queue.
type grokQuotaImportProbe struct{ Source *provider.GrokQuotaService }

// provideProviderImportProbes registers shutdown for the lazily started import probe queue.
func provideProviderImportProbes(manager *lifecycle.Manager) *provider.GrokImportProbeScheduler {
	queue := provider.NewGrokImportProbeScheduler(provider.GrokImportProbeOptions{Concurrency: 3, Timeout: 25 * time.Second, Debug: slog.Debug, Info: slog.Info, Warn: slog.Warn, Error: slog.Error})
	manager.Register(lifecycle.Hook{Name: "ProviderImportProbes", StopOrder: 20, Stop: queue.StopContext})
	return queue
}

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

func (p grokQuotaImportProbe) QueryQuota(ctx context.Context, id int64) (*provider.GrokImportProbeResult, error) {
	v, err := p.Source.QueryQuota(ctx, id)
	if v == nil {
		return nil, err
	}
	return &provider.GrokImportProbeResult{Model: v.Model, StatusCode: v.StatusCode, HeadersObserved: v.HeadersObserved}, err
}
