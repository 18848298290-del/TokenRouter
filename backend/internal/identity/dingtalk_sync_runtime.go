package identity

import (
	"context"
	"strings"
	"time"
)

// dingtalk_sync_runtime.go submits DingTalk profile synchronization through
// the application background runner.
// DingTalk synchronization can continue after the login request finishes using
// the runner supplied by app. Configuration and provider clients are selected
// before submission, while accepted callbacks keep the parent context's values
// and receive a fresh thirty-second work budget when execution starts.

// DingTalkSyncRuntime wires configuration, provider access, and background
// profile synchronization.
type DingTalkSyncRuntime struct {
	// LoadConfig reads current login settings for Pending. Configure this callback
	// before using that entry point; failed reads skip profile submission.
	LoadConfig func(context.Context) (DingTalkOAuthOptions, error)
	// Client selects a provider adapter for the loaded configuration. Pending
	// invokes it directly, so that entry point requires a configured selector.
	Client func(DingTalkOAuthOptions) DingTalkOAuthClient
	// Profiles executes local account updates. Sync delegates directly to this
	// shared instance, so callers configure it before invoking accepted work.
	Profiles *DingTalkProfileSync
	// Run submits profile work to the app-owned task runner. Nil runners skip
	// Async submissions, and rejected submissions are not returned by these entry
	// points.
	Run func(string, func()) bool
	// Observe receives configuration failures and recovered task panics when
	// configured. Nil observers let those skip and recovery paths complete
	// silently.
	Observe ProfileSyncObserver
}

// Async submits a staff synchronization callback to the configured task runner.
// The supplied client and staff snapshot remain shared with accepted work, so
// callers keep them usable and stable until the accepted callback returns. The
// timeout cancels its context; the runner still owns completion tracking.
func (s *DingTalkSyncRuntime) Async(ctx context.Context, cfg DingTalkOAuthOptions, client DingTalkOAuthClient, id int64, staff *DingTalkProfileSnapshot, username bool) {
	RunDingTalkSync(ctx, s.Run, s.Observe, func(ctx context.Context) { s.Sync(ctx, cfg, client, id, staff, username) })
}

// Sync forwards the selected login options and staff data to profile updates. A
// configured Profiles service is required; the username flag is passed through
// to its account-update rules, and its context is forwarded to those profile
// operations for this synchronous call.
func (s *DingTalkSyncRuntime) Sync(ctx context.Context, cfg DingTalkOAuthOptions, client DingTalkOAuthClient, id int64, staff *DingTalkProfileSnapshot, username bool) {
	s.Profiles.Sync(ctx, DingTalkSyncOptions{CorpRestrictionPolicy: cfg.CorpRestrictionPolicy, SyncCorpEmail: cfg.SyncCorpEmail, SyncDisplayName: cfg.SyncDisplayName, SyncDept: cfg.SyncDept, SyncCorpEmailAttrKey: cfg.SyncCorpEmailAttrKey, SyncDisplayNameAttrKey: cfg.SyncDisplayNameAttrKey, SyncDeptAttrKey: cfg.SyncDeptAttrKey}, client, id, staff, username)
}

// FromClaims reconstructs the stored staff snapshot before submitting
// synchronization. Nil claims produce an empty snapshot; typed claim conversion
// completes before Async schedules work, so the queued callback receives that
// reconstructed value.
func (s *DingTalkSyncRuntime) FromClaims(ctx context.Context, cfg DingTalkOAuthOptions, client DingTalkOAuthClient, id int64, claims map[string]any, username bool) {
	s.Async(ctx, cfg, client, id, DingTalkProfileFromClaims(claims), username)
}

// Pending submits profile synchronization for a valid DingTalk pending session.
// Nil sessions, nonpositive user IDs and other provider types are skipped.
// Configured loader and client callbacks are required for eligible sessions; a
// settings failure is diagnosed and stops submission for that login.
func (s *DingTalkSyncRuntime) Pending(ctx context.Context, p *PendingAuthSession, id int64, username bool) {
	if p == nil || id <= 0 || !strings.EqualFold(strings.TrimSpace(p.ProviderType), "dingtalk") {
		return
	}
	cfg, e := s.LoadConfig(ctx)
	if e != nil {
		// Current settings are unavailable, so leave this post-login profile task
		// unsubmitted while the completed authentication result remains with its
		// caller.
		if s.Observe != nil {
			s.Observe("debug", "dingtalk sync: skip post-login sync, config unavailable", "user_id", id, "err", e.Error())
		}
		return
	}
	s.FromClaims(ctx, cfg, s.Client(cfg), id, p.UpstreamIdentityClaims, username)
}

// RunDingTalkSync submits a profile callback carrying the parent context's
// values. A nil runner skips work; accepted work receives an independent
// thirty-second timeout when its callback begins, and cancellation of the login
// request does not cancel that work. The runner owns scheduling and shutdown
// waiting. Callback panics are recovered and optionally diagnosed; the runner's
// acceptance boolean is discarded, so rejection has no synchronous result for
// this caller. A configured recovery observer must return normally after
// receiving a panic.
func RunDingTalkSync(parent context.Context, run func(string, func()) bool, observe ProfileSyncObserver, fn func(context.Context)) {
	if run == nil {
		return
	}
	// Keep login context values after request cancellation so accepted background
	// work can use those values under its own execution timeout and runner
	// lifetime.
	base := context.WithoutCancel(parent)
	run("handler/auth_dingtalk_oauth.go:runDingTalkSyncAsync", func() {
		// A synchronization panic ends this callback. Recover within the submitted
		// work so the runner is not asked to propagate a failed profile update.
		defer func() {
			if r := recover(); r != nil && observe != nil {
				observe("error", "dingtalk sync: panic recovered", "panic", r)
			}
		}()
		// Start the timeout when accepted work begins, so time spent awaiting the
		// runner does not consume the profile update's execution budget; release it
		// afterward.
		ctx, cancel := context.WithTimeout(base, 30*time.Second)
		defer cancel()
		fn(ctx)
	})
}
