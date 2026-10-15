package provider

import (
	"context"
	"log/slog"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// grok_quota_factory.go assembles Grok quota and observed-model services
// around a shared probe runtime. The factory binds platform transport, token
// acquisition, provider storage and local statistics to that runtime, letting
// the core service combine billing observations with quota and media
// eligibility.

// GrokQuotaStore reads providers and writes quota snapshots and rate limits.
type GrokQuotaStore interface {
	// GetByID supplies the provider identity used for a quota or model query.
	// The core returns lookup failures before acquisition and uses later reads
	// for optional model scheduling after a probe result.
	GetByID(context.Context, int64) (*provider.Record, error)
	// UpdateExtra persists observed quota, billing and model metadata for a
	// provider.
	// The core passes the selected observation fields and reports persistence
	// success
	// separately from a completed upstream query.
	UpdateExtra(context.Context, int64, map[string]any) error
	provider.GrokRateLimitWriter // provider-wide deadline write interface
}

// NewGrokQuota binds quota transport and provider reads to one probe runtime.
// A nil requests value becomes an empty transport for option construction.
// Active availability requires token and Do; missing store disables provider
// lookup and observed-model storage. The runtime starts work on demand and is
// shared by quota probes and observed-model scheduling.
func NewGrokQuota(store GrokQuotaStore, token *provider.GrokTokenSource, requests *GrokQuotaTransport, stats provider.LocalUsageStats) *provider.GrokQuotaService {
	// Retain callable option binding when no transport was supplied. The core
	// availability check still rejects network probes until token and Do are
	// configured.
	if requests == nil {
		requests = &GrokQuotaTransport{}
	}
	runtime := &provider.ProbeRuntime{}
	models := provider.GrokModelsOptions{Runtime: runtime, Available: func() bool { return store != nil }, Fetch: requests.FetchModels, Debug: slog.Debug}
	if store != nil {
		models.UpdateExtra = store.UpdateExtra
	}
	if token != nil {
		models.Token = token.GetAccessToken
	}
	options := provider.GrokQuotaOptions{
		Timeout: 20 * time.Second, Available: func() bool { return token != nil && requests != nil && requests.Do != nil },
		Token: token.GetAccessToken, ResolveProxy: requests.ResolveProxy, Active: requests.ActiveQuota, Billing: requests.FetchBilling,
		ProbeModel: func() string { return grok.DefaultResponsesModel }, ScheduleModels: func(value *provider.Record) { provider.ScheduleGrokObservedModels(models, value) },
		MergeBilling: grok.MergeBillingProbeResult, StampBilling: grok.StampBillingSummary,
		StampQuota: func(value *provider.Record, snapshot *grok.QuotaSnapshot, model string) {
			provider.StampGrokQuotaPlan(value, snapshot, model, grok.ResolveGrokTextResponsesModelID, grok.ApplyGrok45ResponsesPlanSignal)
		},
		ResetAt: provider.GrokRateLimitResetAtForProvider, NormalizeResets: provider.NormalizeGrokExhaustedWindowResets,
		PersistLimit: func(ctx context.Context, value *provider.Record, reset time.Time) {
			provider.PersistGrokRateLimit(ctx, store, value, reset, slog.Warn)
		},
		CanRecover: provider.IsSuccessfulGrokRateLimitRecovery, ClearLimit: func(ctx context.Context, value *provider.Record) {
			provider.ClearGrokRateLimitAfterRecovery(ctx, store, value, slog.Warn)
		},
		MediaEligibility: func(value *provider.Record) (bool, string) {
			return provider.GrokMediaGenerationEligibility(value, GrokTierRules())
		},
		LocalStats: func(ctx context.Context, id int64, billing *grok.BillingSummary, now time.Time) (*provider.WindowStats, *provider.WindowStats, *provider.WindowStats) {
			return provider.GrokLocalUsageForQuota(ctx, stats, id, billing, now, slog.Warn)
		},
		MapStatus: requests.MapStatus, Warn: slog.Warn,
	}
	if store != nil {
		options.GetProvider = store.GetByID
		options.UpdateExtra = store.UpdateExtra
	}
	return provider.NewGrokQuotaService(options, runtime)
}
