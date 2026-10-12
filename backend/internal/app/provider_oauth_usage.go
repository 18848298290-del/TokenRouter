package app

import (
	"context"
	"database/sql"
	"log"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerhttp "github.com/TokenFlux/TokenRouter/internal/provider/httpapi"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	openaiws "github.com/TokenFlux/TokenRouter/internal/upstream/openai/ws"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
	usagepostgres "github.com/TokenFlux/TokenRouter/internal/usage/postgres"
)

// provideCNUsageMonitor binds provider storage, usage queries, and database advisory locks.
func provideCNUsageMonitor(store *providerpostgres.ProviderStore, queries *provider.UpstreamUsageService, cfg *config.Config, leader provider.CNMonitorLeader, db *sql.DB) *provider.CNUsageMonitor {
	options := provider.CNMonitorOptions{Now: time.Now, InstanceID: uuid.NewString(), BalanceThreshold: 0.5, Leader: leader, Warn: slog.Warn, Debug: slog.Debug}
	if cfg != nil {
		v := cfg.Gateway.CNProviders
		options.Enabled = v.MonitorEnabled
		options.Interval = time.Duration(v.IntervalMinutes) * time.Minute
		options.ProbeTimeout = time.Duration(v.ProbeTimeoutSeconds) * time.Second
		options.RoundTimeout = time.Duration(v.RoundTimeoutSeconds) * time.Second
		options.Concurrency = v.Concurrency
		options.BalanceThreshold = v.BalanceThreshold
		h := cfg.Security.URLAllowlist
		options.HostPolicy = egress.MonitorHostPolicy{Enabled: h.Enabled, AllowInsecureHTTP: h.AllowInsecureHTTP, AllowPrivate: h.AllowPrivateHosts, AllowedHosts: h.UpstreamHosts}
	}
	if db != nil {
		options.Advisory = func(ctx context.Context, key string) (func(), bool) {
			return postgresinfra.TryAcquireDBAdvisoryLock(ctx, db, postgresinfra.HashAdvisoryLockID(key))
		}
	}
	return provider.NewCNUsageMonitor(store, queries, options)
}

// provideOAuthUsageStats binds batch statistics reads and the shared display cache.
func provideOAuthUsageStats(store *usagepostgres.Store, cache *provider.OAuthUsageCache, calendar timezone.Calendar) *provider.LocalUsageStatistics {
	return provider.NewLocalUsageStatistics(newProviderLocalUsageStats(store), cache, provider.LocalUsageStatisticsOptions{Now: time.Now, Today: calendar.Today, Log: log.Printf})
}

// provideOAuthUsageCore binds local and upstream usage readers and registers query shutdown.
func provideOAuthUsageCore(store *providerpostgres.ProviderStore, usageStore *usagepostgres.Store, cache *provider.OAuthUsageCache, stats *provider.LocalUsageStatistics, gemini *provider.GeminiQuotaService, antigravity *provider.AntigravityQuota, grokView *provider.GrokQuotaView, grok *provider.GrokQuotaService, openAI *provider.OpenAIQuotaService, fetcher provideradapter.ClaudeUsageClient, fingerprints anthropic.FingerprintCache, profiles *egressprovider.TLSProfiles, transport httpclient.UpstreamTransport, settings *provider.QuotaSettingsCache, connections *openaiws.OpenAIWSConnections, manager *lifecycle.Manager, coordinator *provider.OpenAITaskCoordinator) *provider.OAuthUsageService {
	taskOptions := provider.OpenAITaskOptions{
		Read: store.GetByID,
		Register: func(ctx context.Context, value *provider.Record) (string, error) {
			return provideradapter.RegisterAgentIdentityTask(ctx, value, "https://auth.openai.com/api/accounts")
		},
		Persist: func(ctx context.Context, value *provider.Record, credentials map[string]any) error {
			_, err := provider.PersistCredentials(ctx, store, value, credentials, slog.Warn)
			return err
		},
		Invalidate: connections.InvalidateProvider,
	}
	requests := &provideradapter.OAuthUsageTransport{
		Transport: transport, Profiles: profiles, Fingerprints: fingerprints,
		Tasks: coordinator, TaskOptions: taskOptions,
	}
	// Usage queries have a separate Qoder session cache.
	sessions := provideradapter.NewQoderTokenProvider(qoder.SessionBuilder{})
	sessions.SetHTTPUpstream(transport, profiles)
	qoderQuery := &provideradapter.QoderUsage{Sessions: sessions, Transport: transport, Profiles: profiles}
	qoderOptions := qoderQuery.Options()
	qoderOptions.Enrich = provideradapter.EnrichUsageWithProviderError
	options := provider.OAuthUsageOptions{
		Now: time.Now, Jitter: rand.Int64N, Log: log.Printf, Warn: slog.Warn,
		Qoder: qoderOptions,
		Anthropic: func(ctx context.Context, value *provider.Record) (*provider.ClaudeUsageResponse, error) {
			return requests.FetchAnthropic(ctx, value, fetcher)
		},
		OpenAI: provider.OpenAIUsageOptions{
			Probe: requests.ProbeOpenAI,
			Shadow: func(ctx context.Context, id int64, now time.Time) (map[string]any, error) {
				value, err := openAI.QueryUsage(ctx, id)
				if err != nil {
					return nil, err
				}
				return provider.BuildCodexSparkWindowExtraUpdates(value, now), nil
			},
		},
		OpenAIQuotaPause: func(ctx context.Context, value *provider.Record, info *provider.UsageInfo) {
			if value != nil && info != nil && value.IsOpenAI() {
				info.QuotaAutoPaused, _ = provider.EvaluateQuotaAutoPause(value.Platform, value.Extra, settings.GetOpenAIQuotaAutoPauseSettings(ctx), time.Now())
			}
		},
		Antigravity: provider.AntigravityUsageOptions{
			CanFetch: antigravity.CanFetch,
			Fetch: func(ctx context.Context, value *provider.Record) (*provider.UsageInfo, error) {
				result, err := antigravity.FetchQuota(ctx, value, antigravity.GetProxyURL(ctx, value))
				if result == nil {
					return nil, err
				}
				return result.UsageInfo, err
			},
			Degrade: provideradapter.AntigravityDegradedUsage, Enrich: provideradapter.EnrichUsageWithProviderError,
		},
		Gemini: provider.GeminiUsageOptions{
			Location: geminiQuotaLocation, Quota: gemini.QuotaForProvider,
			Totals: func(ctx context.Context, id int64, start, end time.Time) (provider.GeminiUsageTotals, error) {
				rows, err := usageStore.GetModelStatsWithFilters(ctx, start, end, 0, 0, id, 0, nil, nil, nil)
				if err != nil {
					return provider.GeminiUsageTotals{}, err
				}
				values := projectGeminiModelUsage(rows)
				return provider.AggregateGeminiUsage(values), nil
			},
		},
		Grok: provider.GrokUsageOptions{
			Available: func() bool { return true }, StatsAvailable: func() bool { return true },
			Build: grokView.BuildUsageInfo, Enrich: provideradapter.EnrichUsageWithProviderError,
			Probe: func(ctx context.Context, id int64) (*provider.GrokUsageProbe, error) {
				value, err := grok.ProbeBilling(ctx, id)
				if value == nil {
					return nil, err
				}
				return &provider.GrokUsageProbe{Billing: value.Billing, LocalUsage24h: value.LocalUsage24h, LocalUsage7d: value.LocalUsage7d, LocalUsageMonthly: value.LocalUsageMonthly}, err
			},
		},
	}
	core := provider.NewOAuthUsageService(store, cache, stats, options)
	manager.Register(lifecycle.Hook{Name: "ProviderOAuthUsage", StopOrder: 25, Stop: core.StopContext})
	return core
}

// geminiQuotaLocation returns Los Angeles time, or fixed UTC-8 if zone loading fails.
func geminiQuotaLocation() *time.Location {
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		return time.FixedZone("PST", -8*3600)
	}
	return location
}

// provideOllamaUsage binds shared provider storage, encryption, dynamic settings, and upstream requests.
func provideOllamaUsage(store *providerpostgres.ProviderStore, upstream httpclient.UpstreamTransport, settings *provider.RuntimeSettings, cipher identity.SecretEncryptor, cfg *config.Config, leader provider.CNMonitorLeader, db *sql.DB) *provider.OllamaCloudUsageService {
	var do func(*http.Request, string, int64, int) (*http.Response, error)
	if upstream != nil {
		do = upstream.Do
	}
	var advisory func(context.Context, string) (func(), bool)
	if db != nil {
		advisory = func(ctx context.Context, key string) (func(), bool) {
			return postgresinfra.TryAcquireDBAdvisoryLock(ctx, db, postgresinfra.HashAdvisoryLockID(key))
		}
	}
	options := provider.OllamaUsageOptions{EncryptionKeyConfigured: cfg != nil && cfg.Totp.EncryptionKeyConfigured, Now: time.Now, Jitter: rand.Int64N, InstanceID: uuid.NewString(), Fetch: provideradapter.OllamaUsageFetcher(do), Log: func(format string, args ...any) {
		logging.LegacyPrintf("service.ollama_cloud_usage", format, args...)
	}, Lease: func(ctx context.Context, key, owner string, ttl time.Duration) (func(), bool) {
		return provider.AcquireSingletonLease(ctx, leader, advisory, key, owner, ttl)
	}}
	return provider.NewOllamaCloudUsageService(store, settings, cipher, options)
}

// provideUpstreamUsage binds storage and platform queries and registers their shutdown wait.
func provideUpstreamUsage(store *providerpostgres.ProviderStore, upstream httpclient.UpstreamTransport, cfg *config.Config, tls *egressprovider.TLSProfiles, manager *lifecycle.Manager) *provider.UpstreamUsageService {
	options := provideradapter.UsageHTTPOptions{Available: store != nil && upstream != nil}
	if cfg != nil {
		value := cfg.Security.URLAllowlist
		options.Policy = egress.UsageURLPolicy{Configured: true, Enabled: value.Enabled, AllowInsecureHTTP: value.AllowInsecureHTTP, AllowPrivateHosts: value.AllowPrivateHosts, UpstreamHosts: value.UpstreamHosts}
	}
	if upstream != nil {
		options.Do = upstream.DoWithTLS
	}
	if tls != nil {
		options.ResolveTLS = tls.ResolveRequestTLS
	}
	source := provideradapter.NewUpstreamUsageHTTPExecution(options)
	core := provider.NewUpstreamUsageService(store, source, provider.UpstreamUsageOptions{Now: time.Now})
	// Queries finish after periodic producers stop and before SQL and HTTP dependencies close.
	manager.Register(lifecycle.Hook{Name: "ProviderUpstreamUsage", StopOrder: 25, Stop: core.StopContext})
	return core
}

// provideUpstreamUsageHTTP exposes upstream usage queries through HTTP.
func provideUpstreamUsageHTTP(source *provider.UpstreamUsageService) *providerhttp.UpstreamUsageHandler {
	return providerhttp.NewUpstreamUsageHandler(source)
}

// provideProviderUsage shares provider usage storage between administration and usage accumulation.
func provideProviderUsage(db *sql.DB, store *providerpostgres.ProviderStore, cache scheduler.SnapshotCache) *billingpostgres.ProviderUsageStore {
	return billingpostgres.NewProviderUsageStore(db, providerUsageEvents(store, cache, db))
}
