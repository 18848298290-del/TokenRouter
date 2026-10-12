package provider

import (
	"context"
	"time"

	openaiprotocol "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// ApplyObservedRateLimit installs a runtime block before persisting the rate limit.
func (s *HealthService) ApplyObservedRateLimit(ctx context.Context, value *Record, reset time.Time) bool {
	s.notifyProviderSchedulingBlocked(value, reset, "429")
	if err := s.providerRepo.SetRateLimited(ctx, value.ID, reset); err != nil {
		s.options.Warn("rate_limit_set_failed", "provider_id", value.ID, "error", err)
		return false
	}
	return true
}

// UpdateRejectedSessionWindow updates the five-hour session window in a separate write.
func (s *HealthService) UpdateRejectedSessionWindow(ctx context.Context, value *Record, end time.Time) {
	s.updateAnthropicRejectedSessionWindow(ctx, value, &QuotaWindowObservation{ResetAt: end})
}

// PersistCodexObservation stores response usage observations; shadow providers obtain windows through usage queries.
func (s *HealthService) PersistCodexObservation(ctx context.Context, value *Record, snapshot *openaiprotocol.OpenAICodexUsageSnapshot) {
	if s == nil || s.providerRepo == nil || value == nil || value.IsShadow() || snapshot == nil {
		return
	}
	updates := BuildCodexUsageExtraUpdates(snapshot, s.options.Now())
	if len(updates) == 0 {
		return
	}
	if err := s.options.SessionWindows.UpdateExtra(ctx, value.ID, updates); err != nil {
		s.options.Warn("openai_codex_snapshot_persist_failed", "provider_id", value.ID, "error", err)
	}
}
