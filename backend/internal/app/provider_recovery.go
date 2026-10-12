package app

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

// providerHealthRuntime holds the health components created by one application assembly.
type providerHealthRuntime struct {
	Health   *provider.HealthService
	Recovery *provider.RecoveryService
	Observer *provideradapter.UpstreamHealth
}

// provideUpstreamHealth returns the shared health observer.
func provideUpstreamHealth(runtime *providerHealthRuntime) *provideradapter.UpstreamHealth {
	return runtime.Observer
}

// provideProviderHealthRuntime binds health configuration, settings readers, and infrastructure.
func provideProviderHealthRuntime(
	store *providerpostgres.ProviderStore,
	cache provider.TempUnschedCache,
	cfg *config.Config,
	settings *provider.RuntimeSettings,
	timeouts provider.TimeoutCounterCache,
	forbidden provider.OpenAI403CounterCache,
	tokens provider.TokenCacheInvalidator,
	state *provider.RuntimeBlockState,
) *providerHealthRuntime {
	options := provider.HealthOptions{
		Now: time.Now, Warn: slog.Warn, Info: slog.Info,
		SessionWindows: store, TimeoutCounter: timeouts, ForbiddenCounter: forbidden,
		UnauthorizedCooldownMinutes: cfg.RateLimit.OAuth401CooldownMinutes,
		CNIntervalMinutes:           cfg.Gateway.CNProviders.IntervalMinutes,
		OverloadMinutes:             cfg.RateLimit.OverloadCooldownMinutes,
		APIKeyHealthWarn:            provideradapter.LogAPIKeyHealthWarning,
	}
	if counter, ok := cache.(provider.OpenAIAPIKeyHealthCache); ok {
		options.APIKeyHealthCounter = counter
	}
	if settings != nil {
		options.APIKeyHealthSettings = settings.GetOpenAIAPIKeyHealthBreakerSettings
		options.RateLimit429Settings = settings.GetRateLimit429CooldownSettings
		options.ForbiddenSettings = settings.GetOpenAI403CooldownSettings
		options.OverloadSettings = settings.GetOverloadCooldownSettings
		options.HasThresholdSettings = func() bool { return true }
		options.Thresholds = settings.GetProviderSchedulingThresholds
		options.StreamSettings = func(ctx context.Context) (*provider.StreamTimeoutSettings, error, bool) {
			value, err := settings.GetStreamTimeoutSettings(ctx)
			return value, err, true
		}
	}
	if tokens != nil {
		options.InvalidateUnauthorizedToken = tokens.InvalidateToken
	}
	if state != nil {
		options.Block = state.BlockProviderScheduling
	}

	// Recovery and window observation call each other; bind both before exposing the components.
	var recovery *provider.RecoveryService
	options.ClearWindowRateLimit = func(ctx context.Context, id int64) error {
		return recovery.ClearRateLimit(ctx, id)
	}
	health := provider.NewHealthService(store, cache, options)
	recoveryOptions := provider.RecoveryOptions{Now: time.Now, Warn: slog.Warn, ResetCounter: health.ResetForbiddenCounter}
	if state != nil {
		recoveryOptions.ClearSchedulingBlock = state.ClearProviderSchedulingBlock
	}
	if tokens != nil {
		recoveryOptions.InvalidateToken = tokens.InvalidateToken
	}
	recovery = provider.NewRecoveryService(store, cache, recoveryOptions)

	limits := &provideradapter.RateLimitObserver{Health: health, Plans: store, NextGeminiDaily: func() *int64 {
		reset := provider.GeminiDailyResetTime(time.Now(), geminiQuotaLocation()).Unix()
		return &reset
	}}
	if state != nil {
		limits.RetryOpenAI = func(value *provider.Record, headers http.Header, body []byte) bool {
			return provideradapter.CanRetryOpenAI429(state, value, headers, body)
		}
	}

	team := provider.NewTeamLinkedHealth(store, provider.TeamLinkedOptions{Now: options.Now, Warn: options.Warn, Block: options.Block})
	models := &provideradapter.ModelHealth{Health: health, IsImageModel: media.IsGPTImageGenerationModel}

	return &providerHealthRuntime{
		Health: health, Recovery: recovery, Observer: &provideradapter.UpstreamHealth{Core: health, Team: team, Limits: limits, Models: models},
	}
}

// provideProviderRecovery returns the shared recovery service.
func provideProviderRecovery(runtime *providerHealthRuntime) *provider.RecoveryService {
	return runtime.Recovery
}

// provideProviderRuntimeState creates scheduling block state shared by recovery, refresh, and execution.
func provideProviderRuntimeState() *provider.RuntimeBlockState {
	return provider.NewRuntimeBlockState(time.Now)
}
