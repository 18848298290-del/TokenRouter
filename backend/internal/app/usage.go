package app

import (
	"context"
	"database/sql"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	keycore "github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpg "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/config"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/infra/timingwheel"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/settings/preaggregation"
	"github.com/TokenFlux/TokenRouter/internal/usage"
	usagehttp "github.com/TokenFlux/TokenRouter/internal/usage/httpapi"
	usagepg "github.com/TokenFlux/TokenRouter/internal/usage/postgres"
	usageredis "github.com/TokenFlux/TokenRouter/internal/usage/rediscache"
)

// publicBalanceUnit reads the balance display unit through billing.
type publicBalanceUnit struct{ store *settings.Store }

func (r publicBalanceUnit) GetBalanceUnitName(ctx context.Context) string {
	return billing.ReadBalanceUnitName(ctx, r.store)
}

func providePublicUsage(u *usage.UsageService, k *keycore.APIKeyService, users *identity.UserService, store *settings.Store, calendar timezone.Calendar) *usagehttp.PublicUsageHandler {
	return usagehttp.NewPublicUsageHandler(u, k, usagehttp.PublicBalanceQuery(func(ctx context.Context, id int64) (*usagehttp.PublicUserBalance, error) {
		v, e := users.GetByID(ctx, id)
		if e != nil {
			return nil, e
		}
		return &usagehttp.PublicUserBalance{Balance: v.Balance}, nil
	}), publicBalanceUnit{store}, usagehttp.PublicUsageContext{
		Key: func(c *gin.Context) (*keycore.APIKey, bool) {
			value, ok := keyhttp.GetAPIKeyFromContext(c)
			return keycore.CopyAPIKey(value), ok
		},
		Billing: func(c *gin.Context) (*billing.APIKeyBillingContext, bool) {
			return gatewayhttp.GetAPIKeyBillingContext(c)
		},
		Subscription: func(c *gin.Context) (*billing.UserSubscription, bool) {
			return gatewayhttp.SubscriptionFromContext(c)
		},
	}, calendar)
}

func provideUsageOptions(c *config.Config, calendar timezone.Calendar) *usage.Options {
	if c == nil {
		return nil
	}
	return &usage.Options{
		Calendar: calendar,
		Logf:     logger.LegacyPrintf,
		DashboardAgg: usage.DashboardAggregationConfig{
			Enabled:         c.DashboardAgg.Enabled,
			IntervalSeconds: c.DashboardAgg.IntervalSeconds,
			LookbackSeconds: c.DashboardAgg.LookbackSeconds,
			BackfillEnabled: c.DashboardAgg.BackfillEnabled,
			BackfillMaxDays: c.DashboardAgg.BackfillMaxDays,
			RecomputeDays:   c.DashboardAgg.RecomputeDays,
			Retention: usage.DashboardAggregationRetentionConfig{
				UsageLogsDays:         c.DashboardAgg.Retention.UsageLogsDays,
				UsageBillingDedupDays: c.DashboardAgg.Retention.UsageBillingDedupDays,
				HourlyDays:            c.DashboardAgg.Retention.HourlyDays,
				DailyDays:             c.DashboardAgg.Retention.DailyDays,
			},
		},
		UsageCleanup: usage.UsageCleanupConfig{
			Enabled:               c.UsageCleanup.Enabled,
			MaxRangeDays:          c.UsageCleanup.MaxRangeDays,
			BatchSize:             c.UsageCleanup.BatchSize,
			WorkerIntervalSeconds: c.UsageCleanup.WorkerIntervalSeconds,
			TaskTimeoutSeconds:    c.UsageCleanup.TaskTimeoutSeconds,
		},
		Dashboard: usage.DashboardConfig{
			Enabled:                    c.Dashboard.Enabled,
			StatsFreshTTLSeconds:       c.Dashboard.StatsFreshTTLSeconds,
			StatsTTLSeconds:            c.Dashboard.StatsTTLSeconds,
			StatsRefreshTimeoutSeconds: c.Dashboard.StatsRefreshTimeoutSeconds,
		},
	}
}

func provideUsageStore(client *dbent.Client, db *sql.DB, settings *preaggregation.PreAggregationSettingsService, calendar timezone.Calendar) *usagepg.Store {
	return usagepg.NewUsageLogRepository(client, db, settings, calendar)
}

func provideUsageRepository(store *usagepg.Store) usage.UsageLogRepository {
	return store
}

func provideUsageService(store *usagepg.Store) *usage.UsageService {
	return usage.NewUsageService(store)
}

func provideUsageAggregationRepository(db *sql.DB, calendar timezone.Calendar) usage.DashboardAggregationRepository {
	store := usagepg.NewDashboardAggregationRepository(db, calendar, func(ctx context.Context, t time.Time) error { return billingpg.ArchiveUsageDedup(ctx, db, t) })
	if store == nil {
		return nil
	}
	return store
}

func provideUsageCleanupRepository(client *dbent.Client, db *sql.DB) usage.UsageCleanupRepository {
	return usagepg.NewUsageCleanupRepository(client, db)
}

func provideUsageAggregation(repo usage.DashboardAggregationRepository, wheel *timingwheel.Wheel, cache provider.CNMonitorLeader, db *sql.DB, options *usage.Options, settings *preaggregation.PreAggregationSettingsService) *usage.DashboardAggregationService {
	s := usage.NewDashboardAggregationService(repo, wheel, options)
	s.SetSingletonLocker(func(ctx context.Context, key, owner string, ttl time.Duration) (func(), bool) {
		return provider.AcquireSingletonLease(ctx, cache, databaseAdvisoryLease(db), key, owner, ttl)
	})
	s.SetPreAggregationSettings(settings)
	return s
}

func provideUsageCleanup(repo usage.UsageCleanupRepository, wheel *timingwheel.Wheel, agg *usage.DashboardAggregationService, options *usage.Options) *usage.UsageCleanupService {
	return usage.NewUsageCleanupService(repo, wheel, agg, options)
}

func provideUsageDashboard(store *usagepg.Store, agg usage.DashboardAggregationRepository, cache usage.DashboardStatsCache, options *usage.Options, settings *preaggregation.PreAggregationSettingsService, tasks *lifecycle.Tasks) *usage.DashboardService {
	s := usage.NewDashboardService(store, agg, cache, options)
	s.SetBackgroundRunner(tasks.Go)
	s.SetPreAggregationSettings(settings)
	return s
}

// provideUsageDashboardCache binds the configured key prefix and shared Redis client.
func provideUsageDashboardCache(r *redis.Client, cfg *config.Config) usage.DashboardStatsCache {
	prefix := "tokenrouter:"
	if cfg != nil {
		prefix = cfg.Dashboard.KeyPrefix
	}
	return usageredis.NewDashboardCache(r, prefix)
}
