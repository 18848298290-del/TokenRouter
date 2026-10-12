package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaytelemetry "github.com/TokenFlux/TokenRouter/internal/gateway/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	"github.com/TokenFlux/TokenRouter/internal/requestlog"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// GatewayCompletionRecorders holds the Forward and OpenAI recorders, each with its own rate cache.
type GatewayCompletionRecorders struct{ Forward, OpenAI *completion.Recorder }

// completionProviders resolves a shadow provider to its credential parent for completion statistics.
type completionProviders struct {
	store *providerpostgres.ProviderStore
}

type completionHealth struct{ core *provider.HealthService }

// ProvideGatewayCompletionRecorders binds pricing, settlement, statistics, and post-commit effects to both
// recorders.
func ProvideGatewayCompletionRecorders(
	requests *requestlog.Service,
	rates *gatewayBillingRates,
	calculator *billing.Calculator,
	prices *billing.PriceResolver,
	funds completion.Store,
	logs usage.UsageLogRepository,
	modelConfigs *routing.PricingConfigService,
	eligibility *billing.Eligibility,
	deferred *provider.DeferredService,
	notifications *billing.BalanceNotifyService,
	keys *apikey.APIKeyService,
	providers *providerpostgres.ProviderStore,
	health *providerHealthRuntime,
	settings *gateway.RuntimeSettings,
	tasks *lifecycle.Tasks,
	cfg *config.Config,
) GatewayCompletionRecorders {
	options := completion.RecorderOptions{DefaultMultiplier: 1, Now: timezone.NewCalendar(time.Local).Now}
	if cfg != nil {
		options.DefaultMultiplier = cfg.Default.RateMultiplier
	}
	subscriptions, _ := funds.(completion.SubscriptionReader)
	effects := func() *completion.CommitEffects {
		return gatewayCommitEffects(deferred, eligibility, notifications, keys, tasks, cfg)
	}
	stats := func() *billing.PriceResolver {
		var source billing.ProviderStatsSource
		if modelConfigs != nil {
			source = gatewayprovider.ProviderStatsSource{Service: modelConfigs}
		}
		return billing.NewPriceResolver(nil, calculator, nil, nil, source)
	}
	common := func(rate completion.RateReader) completion.Dependencies {
		return completion.Dependencies{
			RequestRecords: requests.Observe,
			Emit:           gatewaytelemetry.CompletionBillingEvent,
			Calculator:     calculator,
			Prices:         prices,
			ProviderStats:  stats(),
			Funds:          funds,
			Subscriptions:  subscriptions,
			Rates:          rate,
			Models:         gatewayprovider.CompletionModels{},
			Logs:           completion.SnapshotLogWriter(logs),
			Effects:        effects(),
			Observe:        gatewaytelemetry.ObserveCompletion,
		}
	}
	forward := common(rates.Forward)
	if settings != nil {
		forward.CacheInjection = settings
	}
	openai := common(rates.OpenAI)
	openai.Providers = completionProviders{store: providers}
	openai.Health = completionHealth{core: health.Health}
	return GatewayCompletionRecorders{Forward: completion.NewRecorder(forward, options), OpenAI: completion.NewRecorder(openai, options)}
}

func (p completionProviders) CredentialProvider(ctx context.Context, in completion.ProviderSnapshot) (*completion.ProviderSnapshot, error) {
	original := &provider.Record{ID: in.ID, Platform: in.Platform, Type: in.Type, ParentProviderID: in.CredentialProviderID}
	value, err := provider.ResolveCredentialRecord(ctx, p.store.GetByID, original)
	if err != nil {
		return nil, err
	}
	if value == original {
		return &in, nil
	}
	return gatewayprovider.ProjectCompletionProvider(value), nil
}

func (p completionHealth) ResetOpenAI403Counter(ctx context.Context, id int64) {
	p.core.ResetForbiddenCounter(ctx, id)
}

// gatewayCommitEffects creates post-commit handlers whose background work uses the shared application task
// tracker.
func gatewayCommitEffects(deferred *provider.DeferredService, eligibility *billing.Eligibility, notifications *billing.BalanceNotifyService, keys *apikey.APIKeyService, tasks *lifecycle.Tasks, cfg *config.Config) *completion.CommitEffects {
	value := &completion.CommitEffects{
		Activity: deferred,
		Observe:  gatewaytelemetry.ObserveCompletion,
		Funds: billing.SettlementEffects{
			Cache:      eligibility,
			Background: tasks.Go,
			Observe:    logging.LegacyPrintf,
			BalanceWarning: func(id int64, balance float64, err error) {
				slog.Warn("invalidate balance cache after exhausted deduction failed", "user_id", id, "new_balance", balance, "error", err)
			},
		},
	}
	value.Auth = keys
	if notifications != nil {
		value.Notifications = notifications
	}
	return value
}
