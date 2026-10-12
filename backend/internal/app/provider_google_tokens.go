package app

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/protocol/google"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/upstream/gemini/codeassist"
	usageerrors "github.com/TokenFlux/TokenRouter/internal/usage"
)

func provideGeminiAuthorization(proxies egress.ProxyRepository, client provider.GeminiOAuthClient, discovery provider.GeminiCodeAssistClient, drive codeassist.DriveClient, cfg *config.Config) *provider.GeminiAuthorization {
	options := provideradapter.GeminiAuthorizationOptions(func() google.OAuthConfig {
		return google.OAuthConfig{ClientID: cfg.Gemini.OAuth.ClientID, ClientSecret: cfg.Gemini.OAuth.ClientSecret, Scopes: cfg.Gemini.OAuth.Scopes}
	}, func(ctx context.Context, id int64) (string, bool) {
		proxy, err := proxies.GetByID(ctx, id)
		if err != nil || proxy == nil {
			return "", false
		}
		return proxy.URL(), true
	})
	return provider.NewGeminiAuthorization(client, discovery, drive, options)
}

// provideGeminiQuotaPolicy binds static quotas and a dynamic policy reader to the shared quota cache.
func provideGeminiQuotaPolicy(cfg *config.Config, store *settings.Store) *provider.GeminiQuotaService {
	tiers := make(map[string]provider.GeminiTierQuotaOverride, len(cfg.Gemini.Quota.Tiers))
	for id, v := range cfg.Gemini.Quota.Tiers {
		tiers[id] = provider.GeminiTierQuotaOverride{ProRPD: v.ProRPD, FlashRPD: v.FlashRPD, CooldownMinutes: v.CooldownMinutes}
	}
	return provider.NewGeminiQuotaService(provider.GeminiQuotaOptions{StaticTiers: tiers, StaticPolicy: cfg.Gemini.Quota.Policy, Now: time.Now, Log: log.Printf, NotFound: settings.ErrSettingNotFound, LoadPolicy: func(ctx context.Context) (string, error) {
		return store.GetValue(ctx, provider.GeminiQuotaPolicySettingKey)
	}})
}

// provideGeminiPrecheck uses Los Angeles time and its own daily usage cache.
func provideGeminiPrecheck(policy *provider.GeminiQuotaService, usage usageerrors.UsageLogRepository) *provider.GeminiPrecheck {
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		location = time.FixedZone("PST", -8*3600)
	}
	return provider.NewGeminiPrecheck(policy, newProviderGeminiUsageReader(usage), provider.GeminiPrecheckOptions{Now: time.Now, Location: location, Info: slog.Info})
}

// provideGeminiTokens binds project lookup, Vertex token exchange, and credential persistence.
func provideGeminiTokens(store *postgres.ProviderStore, cache provider.AccessTokenCache, authorization *provider.GeminiAuthorization, refresh *provider.OAuthRefreshAPI) *provider.GeminiTokenSource {
	executor := &provider.GeminiTokenRefresher{Authorization: authorization, Key: provideradapter.GeminiTokenCacheKey}
	return &provider.GeminiTokenSource{Options: provider.GeminiTokenOptions{
		Cache: cache, Repository: store, Policy: provider.GeminiProviderRefreshPolicy(),
		Debug: slog.Debug, Warn: slog.Warn, Logf: log.Printf,
		Project: authorization.FetchProject, ResolveProxy: authorization.Options.ResolveProxy,
		Vertex: func(ctx context.Context, value *provider.Record) (string, error) {
			return provideradapter.VertexServiceAccountAccessToken(ctx, cache, value)
		},
		Persist: func(ctx context.Context, value *provider.Record, credentials map[string]any) error {
			_, err := provider.PersistCredentials(ctx, store, value, credentials, slog.Warn)
			return err
		},
		Refresh: func(ctx context.Context, value *provider.Record, window time.Duration) (*provider.OAuthRefreshResult, error) {
			return refresh.RefreshIfNeeded(ctx, value, executor, window)
		},
	}}
}

// provideAntigravityTokens binds project backfill, the cooldown cache, and the shared refresh coordinator.
func provideAntigravityTokens(store *postgres.ProviderStore, cache provider.AccessTokenCache, authorization *provider.AntigravityAuthorization, refresh *provider.OAuthRefreshAPI, cooldown provider.TempUnschedCache) *provider.AntigravityTokenSource {
	executor := &provider.AntigravityRefreshRules{
		RefreshProviderToken:     authorization.RefreshProviderToken,
		BuildProviderCredentials: authorization.BuildProviderCredentials,
		Printf:                   func(format string, args ...any) { _, _ = fmt.Printf(format, args...) },
		Logf:                     log.Printf,
	}
	return &provider.AntigravityTokenSource{Options: provider.AntigravityTokenOptions{
		Cache: cache, Repository: store, Policy: provider.AntigravityProviderRefreshPolicy(),
		Debug: slog.Debug, Warn: slog.Warn, TempUnschedCache: cooldown,
		SetTempUnschedulable: store.SetTempUnschedulable,
		FillProject:          authorization.FillProjectID,
		Persist: func(ctx context.Context, value *provider.Record, credentials map[string]any) error {
			_, err := provider.PersistCredentials(ctx, store, value, credentials, slog.Warn)
			return err
		},
		Refresh: func(ctx context.Context, value *provider.Record, window time.Duration) (*provider.OAuthRefreshResult, error) {
			return refresh.RefreshIfNeeded(ctx, value, executor, window)
		},
	}}
}
