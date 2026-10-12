package app

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/batchimage"
	batchpostgres "github.com/TokenFlux/TokenRouter/internal/batchimage/postgres"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	billingadapter "github.com/TokenFlux/TokenRouter/internal/billing/provider"
	billingredis "github.com/TokenFlux/TokenRouter/internal/billing/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/creative"
	creativepostgres "github.com/TokenFlux/TokenRouter/internal/creative/postgres"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/modelidentity"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	catalogprovider "github.com/TokenFlux/TokenRouter/internal/modelcatalog/provider"
	notificationcore "github.com/TokenFlux/TokenRouter/internal/notification"
	paymentpostgres "github.com/TokenFlux/TokenRouter/internal/payment/postgres"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	routingpostgres "github.com/TokenFlux/TokenRouter/internal/routing/postgres"
	schedulerpostgres "github.com/TokenFlux/TokenRouter/internal/scheduler/postgres"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
)

func billingEligibilityOptions(c *config.Config, calendar timezone.Calendar) billing.EligibilityOptions {
	return billing.EligibilityOptions{Dates: billing.DateRuntime{Now: time.Now, Calendar: &calendar}, Billing: billing.BillingOptions{MinimumBalanceReserve: c.Billing.MinimumBalanceReserve, CircuitBreaker: billing.CircuitBreakerOptions{Enabled: c.Billing.CircuitBreaker.Enabled, FailureThreshold: c.Billing.CircuitBreaker.FailureThreshold, ResetTimeoutSeconds: c.Billing.CircuitBreaker.ResetTimeoutSeconds, HalfOpenRequests: c.Billing.CircuitBreaker.HalfOpenRequests}}}
}

// provideBillingEligibility shares the balance and API key limit admission cache.
func provideBillingEligibility(cache billing.BillingCache, users *identitypostgres.UserStore, keys apikey.APIKeyRepository, cfg *config.Config, tasks *lifecycle.Tasks, calendar timezone.Calendar) *billing.Eligibility {
	options := billingEligibilityOptions(cfg, calendar)
	return billing.NewEligibility(cache, billingIdentityUsers{Repository: users}, keys, func() billing.EligibilityOptions { return options }, logging.LegacyPrintf, func(name string, fn func()) { tasks.Go(name, fn) })
}

func provideBillingSubscriptions(groups *routingpostgres.GroupStore, repo billing.UserSubscriptionRepository, client *dbent.Client, calendar timezone.Calendar) *billing.SubscriptionService {
	return billing.NewSubscriptionService(billingGroups{Repository: groups}, repo, billingpostgres.NewSubscriptionMutations(client), billing.DateRuntime{Now: time.Now, Calendar: &calendar})
}

func provideSettlementStore(db *sql.DB, calendar timezone.Calendar) *billingpostgres.SettlementStore {
	return billingpostgres.NewSettlementStore(db, calendar, schedulerpostgres.EnqueueProviderQuotaChangedInTx, billingpostgres.TaskProjectionFactories{
		creative.FundingScope: func(tx *sql.Tx, ref billing.TaskReference) billingpostgres.TaskProjection {
			return creativepostgres.NewFundingParticipant(tx, ref.ID)
		},
		batchimage.FundingScope: func(tx *sql.Tx, ref billing.TaskReference) billingpostgres.TaskProjection {
			return batchpostgres.NewFundingParticipant(tx, ref.ID)
		},
	})
}

func provideBillingFunds(store *billingpostgres.SettlementStore) *billing.Funds {
	return billing.NewFunds(store)
}

func provideBillingRedeem(repo billing.RedeemCodeRepository, users *identitypostgres.UserStore, subs *billing.SubscriptionService, cache billing.RedeemCache, eligibility *billing.Eligibility, client *dbent.Client, auth apikey.APIKeyAuthCacheInvalidator, affiliate *promotion.AffiliateService, tasks *lifecycle.Tasks) *billing.RedeemService {
	// Redemption mutations and paid-order lookup use stores built from the shared Ent client.
	mutations := billingpostgres.NewRedeemMutations(client, billingpostgres.RedeemWriters{
		Balances:    billingpostgres.NewBalanceStore(client),
		Concurrency: identitypostgres.NewConcurrencyStore(client),
	})
	runtime := billing.RedeemRuntime{
		HasPaidOrder: paymentpostgres.NewOrderStore(client).HasPaidOrder,
		Now:          time.Now,
		Observe:      logging.LegacyPrintf,
		Background:   func(name string, fn func()) { tasks.Go(name, fn) },
	}
	return billing.NewRedeemService(repo, billingIdentityUsers{Repository: users}, subs, cache, eligibility, mutations, auth, affiliate, runtime)
}

func provideRedeemAdministration(repo billing.RedeemCodeRepository, client *dbent.Client) *billing.RedeemAdmin {
	return billing.NewRedeemAdmin(repo, billingpostgres.NewRedeemAdministrationMutations(client), time.Now)
}

func provideBalanceAdjuster(client *dbent.Client) billing.BalanceAdjuster {
	return billingpostgres.NewBalanceStore(client)
}

func provideBillingPlans(client *dbent.Client, orders *paymentpostgres.InstanceStore) *billing.Plans {
	return billing.NewPlans(billingpostgres.NewPlanStore(client), orders)
}

// provideSubscriptionExpiry binds notifications and leader leases; lifecycle startup runs the reminder worker.
func provideSubscriptionExpiry(repo billing.UserSubscriptionRepository, settings settingscore.Repository, notification *notificationcore.NotificationEmailService, lock provider.CNMonitorLeader, db *sql.DB) *billing.SubscriptionExpiryService {
	return billing.NewSubscriptionExpiryService(repo, billing.ExpiryOptions{Interval: time.Minute, Owner: uuid.NewString(), Now: time.Now, Observe: func(format string, args ...any) { logging.LegacyPrintf("service.subscription_expiry", format, args...) }, Settings: settings, Notifier: expiryNotifications{Service: notification}, Lease: func(ctx context.Context, key, owner string, ttl time.Duration) (func(), bool) {
		return provider.AcquireSingletonLease(ctx, lock, databaseAdvisoryLease(db), key, owner, ttl)
	}})
}

// provideWindowCostCache uses the shared Redis client for billing window costs.
func provideWindowCostCache(rdb *redis.Client) billing.WindowCostCache {
	return billingredis.NewWindowCostCache(rdb)
}

// provideBillingCalculator supplies the shared model catalog and application clock.
func provideBillingCalculator(catalog *catalogprovider.Service, calendar timezone.Calendar) *billing.Calculator {
	return billing.NewCalculator(catalog, billing.CalculatorOptions{Now: calendar.Now, LoadLocation: billingadapter.LoadPricingLocation})
}

func provideBillingPriceResolver(modelConfigs *routing.PricingConfigService, calculator *billing.Calculator) *billing.PriceResolver {
	return billing.NewPriceResolver(modelConfigs, calculator, modelidentity.Identity, func(model string, err error) {
		slog.DebugContext(context.Background(), "model catalog pricing unavailable", "model", model, "error", err)
	}, gatewayprovider.ProviderStatsSource{Service: modelConfigs})
}
