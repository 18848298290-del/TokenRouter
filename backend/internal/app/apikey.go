package app

import (
	"context"
	"database/sql"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keypostgres "github.com/TokenFlux/TokenRouter/internal/apikey/postgres"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	routingpostgres "github.com/TokenFlux/TokenRouter/internal/routing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/settings/preaggregation"
	"github.com/TokenFlux/TokenRouter/internal/team"
	usagepostgres "github.com/TokenFlux/TokenRouter/internal/usage/postgres"
)

// provideKeyStore binds bulk usage queries to the API key transaction store.
func provideKeyStore(client *dbent.Client, db *sql.DB, settings *preaggregation.PreAggregationSettingsService) *keypostgres.KeyStore {
	return keypostgres.NewKeyStore(client, db, func(ctx context.Context, ids []int64) (map[int64]float64, error) {
		return usagepostgres.ReadAPIKeyUsageTotals(ctx, db, settings, ids)
	})
}

func provideKeyRepository(keys *keypostgres.KeyStore) apikey.APIKeyRepository {
	return keys
}

// provideKeys supplies routing and billing dependencies. Start creates the L1 cache and subscriptions.
func provideKeys(
	keys *keypostgres.KeyStore,
	users *identitypostgres.UserStore,
	groups *routingpostgres.GroupStore,
	subs billing.UserSubscriptionRepository,
	rates billing.UserGroupRateRepository,
	cache apikey.APIKeyCache,
	cfg *config.Config,
	billingCache *billing.Eligibility,
	concurrency *scheduler.ConcurrencyService,
	teams team.TeamRepository,
	calendar timezone.Calendar,
) *apikey.APIKeyService {
	options := &apikey.Options{
		Now:      time.Now,
		Calendar: calendar,
		APIKeyAuth: apikey.APIKeyAuthCacheConfig{
			L1Size:             cfg.APIKeyAuth.L1Size,
			L1TTLSeconds:       cfg.APIKeyAuth.L1TTLSeconds,
			L2TTLSeconds:       cfg.APIKeyAuth.L2TTLSeconds,
			NegativeTTLSeconds: cfg.APIKeyAuth.NegativeTTLSeconds,
			JitterPercent:      cfg.APIKeyAuth.JitterPercent,
			Singleflight:       cfg.APIKeyAuth.Singleflight,
			LookupConcurrency:  cfg.APIKeyAuth.LookupConcurrency,
			InvalidAbuse:       apikey.InvalidAuthAbuseConfig(cfg.APIKeyAuth.InvalidAbuse),
		},
		GroupFastPolicy: keyGroupFastPolicy,
	}
	options.Default.APIKeyPrefix = cfg.Default.APIKeyPrefix
	options.Team.Enabled = cfg.Team.Enabled
	core := apikey.NewAPIKeyService(keys, users, keyGroups{Repository: groups}, subs, rates, cache, options)
	core.SetRateLimitCacheInvalidator(billingCache)
	core.SetConcurrencyService(concurrency)
	core.SetTeamRepository(teams)
	return core
}

func provideKeyInvalidator(core *apikey.APIKeyService) apikey.APIKeyAuthCacheInvalidator {
	return core
}
