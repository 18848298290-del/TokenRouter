package app

import (
	"context"
	"database/sql"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	egresshttp "github.com/TokenFlux/TokenRouter/internal/egress/httpapi"
	egresspostgres "github.com/TokenFlux/TokenRouter/internal/egress/postgres"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	schedulerpostgres "github.com/TokenFlux/TokenRouter/internal/scheduler/postgres"
)

func provideEgressProxyStore(client *dbent.Client, db *sql.DB) *egresspostgres.ProxyStore {
	return egresspostgres.NewProxyStore(client, db, egresspostgres.ProxyStoreOptions{
		Providers: func(exec postgresinfra.Executor) egresspostgres.ProxyProviderParticipant {
			return providerpostgres.ProxyChangesInTx(exec)
		},
		Enqueue: func(ctx context.Context, exec postgresinfra.Executor, payload any) error {
			return schedulerpostgres.EnqueueSchedulerChange(ctx, exec, scheduler.SchedulerOutboxEventProviderBulkChanged, nil, nil, payload)
		},
	})
}

func provideEgressProbe(cfg *config.Config) egress.ProxyExitInfoProber {
	options := egressprovider.ProxyProbeOptions{ValidateResolvedIP: true}
	if cfg != nil {
		options.InsecureSkipVerify = cfg.Security.ProxyProbe.InsecureSkipVerify
		options.AllowPrivateHosts = cfg.Security.URLAllowlist.AllowPrivateHosts
		options.ValidateResolvedIP = cfg.Security.URLAllowlist.Enabled
		options.MaxResponseBytes = cfg.Gateway.ProxyProbeResponseReadMaxBytes
		for _, target := range cfg.Security.ProxyProbe.URLs {
			options.Targets = append(options.Targets, egressprovider.ProxyProbeTarget{URL: target.URL, Parser: target.Parser})
		}
	}
	return egressprovider.NewProxyExitInfoProber(options)
}

func provideEgressAdmin(store *egresspostgres.ProxyStore, prober egress.ProxyExitInfoProber, cache egress.ProxyLatencyCache, tasks *lifecycle.Tasks) *egress.ProxyAdmin {
	return egress.NewProxyAdmin(store, prober, cache, egressprovider.ProxyQualityHTTP{}, egress.ProxyAdminOptions{Now: time.Now, Tasks: tasks, Diagnostics: egress.Diagnostics{Logf: logging.LegacyPrintf}})
}

func provideEgressProfiles(repo egress.TLSFingerprintProfileRepository, cache egress.TLSFingerprintProfileCache) *egress.TLSFingerprintProfileService {
	return egress.NewTLSFingerprintProfileService(repo, cache, egress.Diagnostics{Logf: logging.LegacyPrintf})
}

func provideEgressRouters(repo egress.TLSFingerprintRouterRepository, cache egress.TLSFingerprintRouterCache) *egress.TLSFingerprintRouterService {
	return egress.NewTLSFingerprintRouterService(repo, cache, egress.Diagnostics{Logf: logging.LegacyPrintf})
}

func provideEgressCollector(cfg *config.Config) *egressprovider.TLSFingerprintCollectorService {
	options := egressprovider.CollectorOptions{Now: time.Now, Diagnostics: egress.Diagnostics{Logf: logging.LegacyPrintf}}
	if cfg != nil {
		v := cfg.Server.TLSFingerprintCollector
		options.Host = v.Host
		options.Port = v.Port
		options.PublicBaseURL = v.PublicBaseURL
		options.CertFile = v.CertFile
		options.KeyFile = v.KeyFile
		options.SessionTTLSeconds = v.SessionTTLSeconds
		options.MaxRecordsPerSession = v.MaxRecordsPerSession
	}
	return egressprovider.NewTLSFingerprintCollectorService(options)
}

func provideEgressProfileHTTP(core *egress.TLSFingerprintProfileService, collector *egressprovider.TLSFingerprintCollectorService) *egresshttp.TLSFingerprintProfileHandler {
	return egresshttp.NewTLSFingerprintProfileHandler(core, collector)
}

func provideProxyTransfer(admin *egress.ProxyAdmin, tasks *lifecycle.Tasks) *egress.ProxyTransfer {
	return egress.NewProxyTransfer(admin, tasks, time.Now)
}

func provideProxyHTTP(admin *egress.ProxyAdmin, transfers *egress.ProxyTransfer) *egresshttp.ProxyHandler {
	return egresshttp.NewProxyHandler(admin, transfers)
}

// provideProxyExpiry supplies the shared proxy store to the one-minute expiry worker.
func provideProxyExpiry(repo egress.ProxyRepository) *egress.ProxyExpiryService {
	return egress.NewProxyExpiryService(repo, time.Minute)
}
