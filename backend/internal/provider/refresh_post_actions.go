package provider

import (
	"context"
	"maps"
	"strings"
	"time"
)

// RefreshPostActions orders successful refresh cleanup, token invalidation, scheduler sync, and privacy updates.
type RefreshPostActions struct {
	Now                       func() time.Time
	Info, Warn, Debug         func(string, ...any)
	Privacy                   *PrivacyService
	RequestClearer            RefreshRequestClearer
	ClearError, ClearCooldown func(context.Context, *Record) (bool, error)
	Invalidate, SyncProvider  func(context.Context, *Record) error
	DeleteCooldown            func(context.Context, int64) error
	ClearBlock                func(int64)
	NeedsReauth               func(*Record) bool
	ClearReauth               func(context.Context, *Record)
}

// Run performs post-refresh health cleanup, cache invalidation, and follow-up updates.
func (s *RefreshPostActions) Run(ctx context.Context, provider *Record) {
	syncActions := *s
	changed := false
	s.ClearRefreshRequest(ctx, provider, "success")

	// After a successful Antigravity refresh, conditionally clear an earlier missing-project error.
	if provider.Platform == PlatformAntigravity &&
		provider.Status == StatusError &&
		strings.Contains(provider.ErrorMessage, "missing_project_id:") {
		if applied, clearErr := s.ClearError(ctx, provider); clearErr != nil {
			s.Warn("token_refresh.clear_provider_error_failed",
				"provider_id", provider.ID,
				"error", clearErr,
			)
		} else if applied {
			provider.Status = StatusActive
			provider.ErrorMessage = ""
			s.Info("token_refresh.cleared_missing_project_id_error", "provider_id", provider.ID)
			s.ClearBlock(provider.ID)
		} else {
			changed = true
		}
	}
	// Clear a matching temporary block after refresh, including OAuth 401 recovery.
	if provider.TempUnschedulableUntil != nil && s.Now().Before(*provider.TempUnschedulableUntil) {
		applied, clearErr := s.ClearCooldown(ctx, provider)
		if clearErr != nil {
			s.Warn("token_refresh.clear_temp_unschedulable_failed", "provider_id", provider.ID, "error", clearErr)
		} else if applied {
			provider.TempUnschedulableUntil = nil
			provider.TempUnschedulableReason = ""
			s.Info("token_refresh.cleared_temp_unschedulable", "provider_id", provider.ID)
			s.ClearBlock(provider.ID)
		} else {
			changed = true
		}
		// A storage error still permits best-effort cache deletion; an identity or cooldown mismatch protects the
		// newer cache state.
		if (applied || clearErr != nil) && s.DeleteCooldown != nil {
			if err := s.DeleteCooldown(ctx, provider.ID); err != nil {
				s.Warn("token_refresh.clear_temp_unsched_cache_failed", "provider_id", provider.ID, "error", err)
			}
		}
	}
	// On an identity or health-window mismatch, invalidate exchanged tokens but skip scheduler sync and later actions.
	if changed {
		syncActions.SyncProvider = nil
	}
	syncActions.Sync(ctx, provider)
	if changed {
		return
	}
	// Apply OpenAI privacy settings after refresh when training sharing has not been disabled.
	s.Privacy.RefreshOpenAIPrivacy(ctx, provider)
	// Apply Antigravity privacy settings after refresh when needed.
	s.Privacy.RefreshAntigravityPrivacy(ctx, provider)
	// Clear the Grok reauthentication hint after a successful refresh.
	if provider != nil && provider.Platform == PlatformGrok && s.NeedsReauth(provider) {
		s.ClearReauth(ctx, provider)
	}
}

func (s *RefreshPostActions) SyncWithCleanup(parent context.Context, provider *Record) {
	cleanupParent := context.Background()
	if parent != nil {
		cleanupParent = context.WithoutCancel(parent)
	}
	ctx, cancel := context.WithTimeout(cleanupParent, DefaultTokenRefreshCleanupTimeout)
	defer cancel()
	s.Sync(ctx, provider)
}

func (s *RefreshPostActions) Sync(ctx context.Context, provider *Record) {
	// Invalidate OAuth and Qoder COSY tokens through the platform-aware callback.
	if s.Invalidate != nil && (provider.Type == ProviderTypeOAuth || provider.IsQoderCosy()) {
		if err := s.Invalidate(ctx, provider); err != nil {
			s.Warn("token_refresh.invalidate_token_cache_failed",
				"provider_id", provider.ID,
				"error", err,
			)
		} else {
			s.Debug("token_refresh.token_cache_invalidated", "provider_id", provider.ID)
		}
	}
	// Publish refreshed provider credentials to the scheduler cache.
	if s.SyncProvider != nil {
		if err := s.SyncProvider(ctx, provider); err != nil {
			s.Warn("token_refresh.sync_scheduler_cache_failed",
				"provider_id", provider.ID,
				"error", err,
			)
		} else {
			s.Debug("token_refresh.scheduler_cache_synced", "provider_id", provider.ID)
		}
	}
}

// ClearRefreshRequest conditionally clears the one-time refresh flag after success or permanent failure.
func (s *RefreshPostActions) ClearRefreshRequest(ctx context.Context, provider *Record, outcome string) {
	if s == nil || provider == nil || !NeedsAntigravityRefreshRequest(provider) {
		return
	}
	updates := ClearedAntigravityRefreshRequest()
	clearer := s.RequestClearer
	ok := clearer != nil
	if !ok {
		s.Warn("token_refresh.clear_antigravity_force_refresh_failed", "provider_id", provider.ID, "outcome", outcome, "error", "conditional refresh request clearer is not configured")
		return
	}
	version := FailureVersion(provider).CredentialVersion
	if outcome == "non_retryable" {
		version.Status = StatusError
	}
	applied, err := clearer.ClearAntigravityRefreshRequest(ctx, version)
	if err != nil {
		s.Warn("token_refresh.clear_antigravity_force_refresh_failed",
			"provider_id", provider.ID,
			"outcome", outcome,
			"error", err,
		)
		return
	}
	if !applied {
		s.Info("token_refresh.antigravity_force_refresh_clear_skipped_stale_credentials", "provider_id", provider.ID)
		return
	}
	if provider.Extra != nil {
		maps.Copy(provider.Extra, updates)
	}
	s.Info("token_refresh.cleared_antigravity_force_refresh",
		"provider_id", provider.ID,
		"outcome", outcome,
	)
}
