package provider

import (
	"context"
	"maps"
	"strings"
	"time"
)

// refresh_post_actions.go cleans up health state and synchronizes caches after
// successful credential refresh.
// A successful credential exchange can still be followed by an administrator
// changing health or identity before cleanup. Matching clears update local
// health before publishing the exchanged record, while an identity or window
// conflict suppresses scheduler publication and later privacy or
// reauthentication work. Token invalidation is still attempted after that
// conflict.
//
// Success cleanup also clears Antigravity's one-time request through a
// conditional writer and applies its patch locally after a match.
// Temporary-window storage errors allow best-effort runtime cache deletion,
// keeping that error path distinct from a comparison conflict that protects
// newer cached state. SyncWithCleanup gives post-commit invalidation and
// synchronization a separate budget whose context retains parent values but
// ignores parent cancellation.

// RefreshPostActions binds identity-checked health cleanup and success follow-up
// services for credential refresh.
type RefreshPostActions struct {
	Now func() time.Time // active cooldown observation clock
	// Info reports matched health clears and request-flag outcomes. Warn reports
	// health writes, token invalidation and snapshot publication failures. Debug
	// reports successful token invalidation and scheduler publication. Reached
	// paths invoke these callbacks directly, so callers provide each level used
	// by their configured cleanup operations.
	Info, Warn, Debug func(string, ...any)
	// optional post-refresh privacy service
	Privacy *PrivacyService
	// conditional Antigravity flag writer
	RequestClearer RefreshRequestClearer
	// ClearError compares identity and the observed missing-project error before
	// clearing that error. ClearCooldown compares identity and the observed temporary
	// pause before clearing its window. Both return whether the matched state was
	// cleared; a false result suppresses stale-record publication while retaining
	// token invalidation. Each callback is required when its own cleanup branch
	// is reached, and persistence errors are handled separately from comparison misses.
	ClearError, ClearCooldown func(context.Context, *Record) (bool, error)
	// Invalidate evicts cached rotating tokens for eligible credential kinds.
	// SyncProvider publishes the selected record to scheduling snapshots afterward.
	// Both are optional, and an invalidation error still permits publication. Run
	// disables SyncProvider on a health comparison conflict while retaining Invalidate,
	// so an observed stale record cannot overwrite a newer scheduling snapshot.
	Invalidate, SyncProvider func(context.Context, *Record) error
	// optional runtime pause deletion
	DeleteCooldown func(context.Context, int64) error
	// ClearBlock is callable after a matched health clear. NeedsReauth and
	// ClearReauth are callable when the Grok follow-up reaches them; the predicate
	// selects whether the stored reauthentication hint needs clearing.
	ClearBlock func(int64) // matched recovery runtime unblock
	// reached Grok reauthentication predicate
	NeedsReauth func(*Record) bool
	// reached Grok reauthentication clear
	ClearReauth func(context.Context, *Record)
}

// Run performs successful-refresh cleanup and follow-up work using a nonnil
// caller-owned provider record. Callers synchronize its mutable health and Extra
// fields and provide each clock or callback whose branch is reached; optional
// privacy uses PrivacyService's nil-safe methods.
//
// The request flag is cleared first, then matching Antigravity missing-project
// errors and live temporary pauses are cleared before the local record is
// synchronized. A health comparison conflict keeps token invalidation but
// removes scheduler publication and later privacy or reauthentication work.
// Storage errors are logged, and temporary-pause errors still permit runtime
// cache deletion. These cleanup operations commit independently, so an error
// leaves earlier completed cleanup in place.
func (s *RefreshPostActions) Run(ctx context.Context, provider *Record) {
	syncActions := *s
	changed := false
	s.ClearRefreshRequest(ctx, provider, "success")

	// Compare the observed missing-project error before local recovery so an
	// administrator's newer error or authorization remains authoritative if storage
	// reports a conflict; a matched clear permits runtime unblocking.
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
	// Clear the observed live pause before synchronizing local health so the
	// scheduler receives the cleared window after a match, including OAuth 401
	// recovery.
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
		// Distinguish a comparison conflict from storage failure: the conflict
		// protects newer runtime cache state, while a write error still permits
		// the existing best-effort deletion path for the observed pause.
		if (applied || clearErr != nil) && s.DeleteCooldown != nil {
			if err := s.DeleteCooldown(ctx, provider.ID); err != nil {
				s.Warn("token_refresh.clear_temp_unsched_cache_failed", "provider_id", provider.ID, "error", err)
			}
		}
	}
	// A comparison conflict means this exchanged record is stale for publication.
	// Retain token invalidation, then stop follow-up actions that would use its
	// identity.
	if changed {
		syncActions.SyncProvider = nil
	}
	syncActions.Sync(ctx, provider)
	if changed {
		return
	}
	// Privacy maintenance runs after health cleanup reports no comparison
	// conflict. Each platform applies its eligibility and stored-mode rules, then
	// conditionally writes against its own captured identity.
	s.Privacy.RefreshOpenAIPrivacy(ctx, provider)
	s.Privacy.RefreshAntigravityPrivacy(ctx, provider)
	// A successful eligible Grok refresh can clear the administrator-facing
	// reauthentication hint after token and health cleanup have been attempted.
	if provider != nil && provider.Platform == PlatformGrok && s.NeedsReauth(provider) {
		s.ClearReauth(ctx, provider)
	}
}

// SyncWithCleanup supplies a separate two-second deadline context for token
// and snapshot cleanup after persistence. Nil parent selects context.Background;
// otherwise values are retained while parent cancellation is ignored. Callback
// implementations determine I/O cancellation; the production Redis client uses
// its socket timeouts when ContextTimeoutEnabled is false, so this deadline
// alone does not bound the call's wall-clock duration.
func (s *RefreshPostActions) SyncWithCleanup(parent context.Context, provider *Record) {
	// Detach cancellation after persistence so a canceled request can still
	// invalidate tokens and publish its provider record within a separate budget.
	cleanupParent := context.Background()
	if parent != nil {
		cleanupParent = context.WithoutCancel(parent)
	}
	ctx, cancel := context.WithTimeout(cleanupParent, DefaultTokenRefreshCleanupTimeout)
	defer cancel()
	s.Sync(ctx, provider)
}

// Sync attempts token invalidation before scheduler synchronization, using each
// configured callback and the supplied record. OAuth and Qoder COSY records
// select invalidation; callers provide a usable record and reached loggers.
//
// Invalidation errors are logged and still allow the subsequent SyncProvider
// call, so failure of one cleanup step does not prevent the other from being
// attempted. Scheduler errors are also logged, and this method returns no
// failure result. Run can pass a copy with SyncProvider cleared after an
// identity conflict, while SyncWithCleanup supplies a cancellation-independent
// context after persistence.
func (s *RefreshPostActions) Sync(ctx context.Context, provider *Record) {
	// Request invalidation for credential kinds with cached rotating tokens.
	// An error is logged so scheduler publication can still be attempted
	// afterward.
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
	// Attempt scheduler publication after token invalidation, including its error
	// path; callers suppress this callback when their observed identity became
	// stale.
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

// ClearRefreshRequest conditionally clears a set one-time Antigravity OAuth flag
// after success or permanent failure. Nil service, nil provider or an unset
// supported flag returns immediately; reached callers keep the record stable and
// synchronize local Extra mutation.
//
// The non_retryable outcome changes the expected status to error because
// permanent failure persistence runs first. A missing writer, write error or
// comparison miss leaves local request fields intact and logs the outcome. A
// matched write copies the clearing fields into an existing nonnil Extra map,
// then logs success; it does not allocate that local map. Each reached logger
// must be callable, and unsupported mutable credential values retain
// ObserveCredentialVersion's copy limits.
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
	version := ObserveCredentialVersion(provider)
	if outcome == "non_retryable" {
		// Permanent failure has already changed durable health before this flag
		// cleanup, so compare the error status rather than the attempted active
		// status.
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
	// Apply the matched clearing fields to the caller's existing Extra map so
	// later in-process checks observe the same request outcome as durable
	// storage.
	if provider.Extra != nil {
		maps.Copy(provider.Extra, updates)
	}
	s.Info("token_refresh.cleared_antigravity_force_refresh",
		"provider_id", provider.ID,
		"outcome", outcome,
	)
}
