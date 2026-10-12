package app

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/config"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

// providerRefreshRegistrations contains one refresh executor registration per platform.
type providerRefreshRegistrations []provider.RefreshRegistration

// provideManagedRefresh binds manual credential exchange to the shared refresh coordinator.
func provideManagedRefresh(admin *provider.Admin, privacy *provider.PrivacyService, coordinator *provider.OAuthRefreshAPI, transport httpclient.UpstreamTransport, profiles *egressprovider.TLSProfiles, claude *provider.ClaudeAuthorization, openai *provider.OpenAIAuthorization, gemini *provider.GeminiAuthorization, ag *provider.AntigravityAuthorization, grok provider.GrokRefreshTokenService, invalidator provider.TokenCacheInvalidator) *provider.ManagedRefreshService {
	qoder := provideradapter.NewQoderTokenRefresher(provideradapter.QoderRefreshOptions{Transport: transport, Profiles: profiles})
	source := &provider.ManualCredentialExchange{Claude: claude, OpenAI: openai, Gemini: gemini, Antigravity: ag, Grok: grok, Qoder: qoder.Refresh}
	exchange := func(ctx context.Context, value *provider.Record) (provider.ManagedRefreshObservation, error) {
		credentials, missing, err := source.Refresh(ctx, value)
		return provider.ManagedRefreshObservation{Credentials: credentials, ProjectIDMissing: missing}, err
	}
	var invalidate func(context.Context, *provider.Record) error
	if invalidator != nil {
		invalidate = func(ctx context.Context, value *provider.Record) error {
			return invalidator.InvalidateToken(ctx, value)
		}
	}
	return provider.NewManagedRefreshService(provider.ManagedRefreshOptions{Store: admin, Privacy: privacy, Coordinate: coordinator.WithManagedRefresh, CacheKey: provideradapter.ManagedRefreshCacheKey, Exchange: exchange, Invalidate: invalidate, Log: log.Printf, Warn: slog.Warn, Error: slog.Error})
}

// provideProviderRefresh binds conditional provider writes to the shared refresh coordinator.
func provideProviderRefresh(store *providerpostgres.ProviderStore, cache provider.AccessTokenCache, manager *lifecycle.Manager) *provider.OAuthRefreshAPI {
	coordinator := provider.NewOAuthRefreshAPI(store, cache, provider.RefreshOptions{
		Now: time.Now, Warn: slog.Warn, Info: slog.Info, Error: slog.Error,
		Platform: provider.ProviderRefreshPlatformPolicy(),
	})
	manager.Register(lifecycle.Hook{Name: "ProviderRefreshCoordinator", StopOrder: 25, Stop: coordinator.StopContext})
	return coordinator
}

func provideRefreshPlatforms(claude *provider.ClaudeAuthorization, openai *provider.OpenAIAuthorization, gemini *provider.GeminiAuthorization, antigravity *provider.AntigravityAuthorization, qoder *provideradapter.QoderAuthorization, grok *provider.GrokAuthorization, transport httpclient.UpstreamTransport, profiles *egressprovider.TLSProfiles) providerRefreshRegistrations {
	claudeRefresh := &provider.ClaudeTokenRefresher{Authorization: claude}
	openaiRefresh := &provider.OpenAITokenRefresher{Authorization: openai}
	geminiRefresh := &provider.GeminiTokenRefresher{Authorization: gemini, Key: provideradapter.GeminiTokenCacheKey}
	agRefresh := &provider.AntigravityRefreshRules{
		RefreshProviderToken:     antigravity.RefreshProviderToken,
		BuildProviderCredentials: antigravity.BuildProviderCredentials,
		Printf:                   func(format string, args ...any) { _, _ = fmt.Printf(format, args...) },
		Logf:                     log.Printf,
	}
	qoderRefresh := provideradapter.NewQoderTokenRefresher(provideradapter.QoderRefreshOptions{
		Transport: transport, Profiles: profiles, BuildCredentials: qoder.Core.BuildProviderCredentials,
	})
	grokRefresh := provider.NewGrokTokenRefresher(grok)
	return providerRefreshRegistrations{
		{Platform: provider.PlatformAnthropic, Refresher: claudeRefresh, Executor: claudeRefresh},
		{Platform: provider.PlatformOpenAI, Refresher: openaiRefresh, Executor: openaiRefresh},
		{Platform: provider.PlatformGemini, Refresher: geminiRefresh, Executor: geminiRefresh},
		{Platform: provider.PlatformAntigravity, Refresher: agRefresh, Executor: agRefresh},
		{Platform: provider.PlatformQoder, Refresher: qoderRefresh, Executor: qoderRefresh},
		{Platform: provider.PlatformGrok, Refresher: grokRefresh, Executor: grokRefresh},
	}
}

// provideBackgroundRefresh creates the shared refresh service; lifecycle startup runs its maintenance tasks.
func provideBackgroundRefresh(store *providerpostgres.ProviderStore, refresh *provider.OAuthRefreshAPI, cfg *config.Config, registrations providerRefreshRegistrations, post *provider.RefreshPostActions, observer provider.RefreshFailureObserver) *provider.BackgroundRefreshService {
	v := cfg.TokenRefresh
	tuning := &provider.RefreshTuning{
		Enabled: v.Enabled, CheckIntervalMinutes: v.CheckIntervalMinutes,
		RefreshBeforeExpiryHours: v.RefreshBeforeExpiryHours, MaxRetries: v.MaxRetries,
		RetryBackoffSeconds: v.RetryBackoffSeconds, CandidatePageSize: v.CandidatePageSize,
		ProviderConcurrency: v.ProviderConcurrency, ProviderQPS: v.ProviderQPS,
		ProviderFailureThreshold: v.ProviderFailureThreshold,
		AttemptTimeoutSeconds:    v.AttemptTimeoutSeconds, CycleTimeoutSeconds: v.CycleTimeoutSeconds,
	}
	lease, configured := refresh.LockLease()
	prepare := func(value *provider.Record) func(time.Time, string) {
		return provider.PrepareRefreshFailureNotice(observer, value)
	}
	attempts := provider.RefreshAttempts{
		API: refresh, Tuning: tuning, Policy: provider.DefaultBackgroundRefreshPolicy(),
		AttemptTimeout: tuning.AttemptTimeout(0, lease, configured),
		Now:            time.Now, Info: slog.Info, Warn: slog.Warn, Error: slog.Error,
		NonRetryable:         provideradapter.IsNonRetryableRefreshError,
		SharedProviderError:  provideradapter.IsSharedProviderRefreshError,
		AmbiguousEntitlement: provideradapter.IsAmbiguousGrokEntitlementRefreshError,
		FailureWriter:        store, GrokMutation: store, Invalidate: post.Invalidate,
		PrepareFailure: prepare, ClearRefreshRequest: post.ClearRefreshRequest,
		PostActions: post.Run, SyncCleanup: post.SyncWithCleanup,
		Persist: func(ctx context.Context, value *provider.Record, credentials map[string]any) error {
			_, err := provider.PersistCredentials(ctx, store, value, credentials, slog.Warn)
			return err
		},
	}
	return provider.NewBackgroundRefreshService(provider.BackgroundRefreshOptions{
		Tuning: tuning, Pager: store, Registrations: registrations, Attempts: attempts,
		Debug: slog.Debug, Info: slog.Info, Warn: slog.Warn, Error: slog.Error,
		Reconciliation: provider.GrokReconciliationOptions{
			Reader: store, ConditionalError: store, Now: time.Now,
			Skew: provider.GrokTokenRefreshSkew, PrepareFailure: prepare, Invalidate: post.Invalidate,
		},
	})
}
