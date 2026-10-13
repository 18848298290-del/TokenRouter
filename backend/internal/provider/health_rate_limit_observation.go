package provider

import (
	"context"
	"time"

	openaiprotocol "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// Response reset observations feed provider rate-limit columns, five-hour
// session columns and Codex Extra snapshots through separate writes. The
// rate-limit command notifies the configured runtime blocker before its
// repository call, so a failed write can leave immediate process state
// different from persisted state. A successful call confirms repository success
// while row matching and publication remain repository behavior.
//
// Session rejection delegates the window calculation to the shared Anthropic
// helper, and Codex observations use the snapshot builder's raw/normalized
// measurement patch. Shadow records skip direct Codex persistence; nonnil
// snapshots can supply a timestamp even when measurements are absent. These
// auxiliary window and Extra operations log failures independently, keeping
// their observation outcome separate from the response already handled by the
// caller.

// ApplyObservedRateLimit attempts the supplied provider-wide reset and reports
// whether its store call succeeds. Supply a usable service and record; reset is
// passed directly to both the configured blocker and SetRateLimited.
//
// Notification runs synchronously before persistence and can survive a logged
// store error, which returns false. A nil repository error returns true even
// when the store matches no row. The production writer replaces its stored
// reset and publishes health separately, so this result is not a combined
// runtime/database transaction or affected-row count.
func (s *HealthService) ApplyObservedRateLimit(ctx context.Context, value *Record, reset time.Time) bool {
	// SetRateLimited can replace the provider reset and publishes it separately.
	// Notify the configured blocker first so its process-local effect can precede
	// those persistent and publication operations.
	s.notifyProviderSchedulingBlocked(value, reset, "429")
	if err := s.providerRepo.SetRateLimited(ctx, value.ID, reset); err != nil {
		s.options.Warn("rate_limit_set_failed", "provider_id", value.ID, "error", err)
		return false
	}
	return true
}

// UpdateRejectedSessionWindow delegates a five-hour rejected-window update
// using end as its deadline. Nil service, record or health store is skipped by
// the helper; configure SessionWindows when a usable write is reached. Store
// failures are logged.
func (s *HealthService) UpdateRejectedSessionWindow(ctx context.Context, value *Record, end time.Time) {
	s.updateAnthropicRejectedSessionWindow(ctx, value, &QuotaWindowObservation{ResetAt: end})
}

// PersistCodexObservation attempts the builder's Codex Extra patch for a
// nonshadow record and stable snapshot. Missing inputs or health storage return
// early; configure SessionWindows for usable patches. Nonnil snapshots include
// a chosen observation timestamp even when measurements are absent, and write
// failures are logged.
func (s *HealthService) PersistCodexObservation(ctx context.Context, value *Record, snapshot *openaiprotocol.OpenAICodexUsageSnapshot) {
	if s == nil || s.providerRepo == nil || value == nil || value.IsShadow() || snapshot == nil {
		return
	}
	updates := BuildCodexUsageExtraUpdates(snapshot, s.options.Now())
	if len(updates) == 0 {
		return
	}
	// Codex display publication is an auxiliary Extra write after response
	// observation. Log failed storage while retaining the caller's
	// already-observed upstream result and any earlier health effects.
	if err := s.options.SessionWindows.UpdateExtra(ctx, value.ID, updates); err != nil {
		s.options.Warn("openai_codex_snapshot_persist_failed", "provider_id", value.ID, "error", err)
	}
}
