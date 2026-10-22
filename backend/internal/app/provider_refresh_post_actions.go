package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/rediscache/codec"
)

// provider_refresh_post_actions.go binds post-refresh cleanup and publication
// to shared provider storage and caches. Successful token refresh can trigger
// privacy checks, identity-matched error or cooldown clearing, and cache
// publication. The application binds those provider-owned post actions to
// shared state, adding token invalidation, snapshot writes, and cooldown
// deletion only when their capabilities are available.

// provideRefreshPostActions assembles post-refresh handling around shared
// provider storage. Error and cooldown clearing use observed identity versions;
// optional invalidator, snapshot cache, cooldown cache, and runtime unblocker
// add the corresponding effects for the provider core to order.
func provideRefreshPostActions(store *postgres.ProviderStore, privacy *provider.PrivacyService, invalidator provider.TokenCacheInvalidator, cache scheduler.SnapshotCache, cooldown provider.TempUnschedCache, blocker provider.RuntimeUnblocker) *provider.RefreshPostActions {
	post := &provider.RefreshPostActions{
		Now: time.Now, Info: slog.Info, Warn: slog.Warn, Debug: slog.Debug,
		Privacy: privacy, RequestClearer: store, NeedsReauth: provider.GrokNeedsReauth,
		ClearReauth: func(ctx context.Context, value *provider.Record) {
			provider.ClearGrokNeedsReauth(ctx, store, value.ID)
		},
		ClearError: func(ctx context.Context, value *provider.Record) (bool, error) {
			return store.ClearUsageErrorIfUnchanged(ctx, provider.UsageRecoveryVersion{
				CredentialVersion: provider.ObserveCredentialVersion(value), ErrorMessage: value.ErrorMessage,
			})
		},
		ClearCooldown: func(ctx context.Context, value *provider.Record) (bool, error) {
			return store.ClearRefreshCooldownIfUnchanged(ctx, provider.ObserveRefreshCooldown(value))
		},
		ClearBlock: func(id int64) {
			if blocker != nil && id > 0 {
				blocker.ClearProviderSchedulingBlock(id)
			}
		},
	}
	if invalidator != nil {
		post.Invalidate = invalidator.InvalidateToken
	}
	if cache != nil {
		post.SyncProvider = func(ctx context.Context, value *provider.Record) error {
			return cache.SetProvider(ctx, codec.WrapRecord(value))
		}
	}
	if cooldown != nil {
		post.DeleteCooldown = cooldown.DeleteTempUnsched
	}
	return post
}
