package provider

import (
	"context"
	"strings"
	"time"
)

// CNConcurrencyLimitReason identifies concurrency-limit scheduling pauses.
const CNConcurrencyLimitReason = "cn_concurrency_limit"

// ApplyCNConcurrencyLimit briefly pauses a Kimi provider after a concurrency limit.
// The caller can fail over without incrementing the accumulated 403 count.
func (s *HealthService) ApplyCNConcurrencyLimit(
	ctx context.Context,
	provider *Record, reason string,
) {
	until := s.options.Now().Add(time.Duration(OpenAI403CooldownMinutesDefault) * time.Minute)
	s.notifyProviderSchedulingBlocked(provider, until, CNConcurrencyLimitReason)
	if err := s.providerRepo.SetTempUnschedulable(ctx, provider.ID, until, reason); err != nil {
		s.options.Warn("cn_concurrency_limit_set_temp_unschedulable_failed", "provider_id", provider.ID, "error", err)
		return
	}
	s.options.Info("cn_provider_concurrency_limited",
		"provider_id", provider.ID,
		"platform", provider.Platform,
		"until", until.UTC(),
	)
}

// ApplyCNInsufficientBalance records a recoverable, identity-bound scheduling pause.
// The cooldown spans two balance-check intervals; periodic monitoring can clear it after recovery.
// Scheduling-block notification occurs before the function returns.
func (s *HealthService) ApplyCNInsufficientBalance(
	ctx context.Context,
	provider *Record,
	upstreamMsg string,
) {
	identityHash := CNUsageMonitorIdentityFingerprint(provider)
	if identityHash == "" {
		identityHash = "unknown"
	}
	msg := CNUsageMonitorReason(identityHash)
	if upstreamMsg = strings.TrimSpace(upstreamMsg); upstreamMsg != "" {
		msg += ": " + upstreamMsg
	}

	until := s.options.Now().Add(CNBalanceCooldownDuration(s.options.CNIntervalMinutes))
	s.notifyProviderSchedulingBlocked(provider, until, "cn_insufficient_balance")
	if err := s.providerRepo.SetTempUnschedulable(ctx, provider.ID, until, msg); err != nil {
		s.options.Warn("cn_balance_set_temp_unschedulable_failed", "provider_id", provider.ID, "error", err)
		return
	}
	s.options.Info("cn_provider_insufficient_balance",
		"provider_id", provider.ID,
		"platform", provider.Platform,
		"until", until.UTC(),
	)
}

// CNBalanceCooldownDuration returns twice the balance-check interval, or 20 minutes by default.
// Periodic monitoring can clear the pause early when the balance recovers.
// The duration covers the next scheduled balance check.
func CNBalanceCooldownDuration(intervalMinutes int) time.Duration {
	minutes := 10
	if intervalMinutes > 0 {
		minutes = intervalMinutes
	}
	cooldown := time.Duration(minutes) * time.Minute * 2
	if cooldown < time.Minute {
		cooldown = 10 * time.Minute
	}
	return cooldown
}

// CNProviderQuotaSnapshotReset returns the earliest future Coding Plan window reset.
// Choosing the earlier 5h or weekly reset avoids pausing until the weekly reset
// when a shorter window caused the 429 response.
// Periodic quota monitoring can extend the pause if the weekly quota remains exhausted.
// It returns nil when no valid snapshot or future reset exists.
func CNProviderQuotaSnapshotReset(provider *Record, now time.Time) *time.Time {
	if provider == nil || !provider.IsCNProvider() || !provider.IsCodingPlan() {
		return nil
	}
	snapshot := ValidCNUsageMonitorSnapshot(provider)
	if snapshot == nil || snapshot.Mode != "limits" {
		return nil
	}
	var earliest *time.Time
	for _, limit := range snapshot.Limits {
		t := clonePointer(limit.ResetAt)
		if t == nil || !t.After(now) {
			continue
		}
		if earliest == nil || t.Before(*earliest) {
			earliest = t
		}
	}
	return earliest
}

func (s *HealthService) ApplyCNQuotaSnapshotCooldown(ctx context.Context, provider *Record) bool {
	if provider == nil || !provider.IsCNProvider() {
		return false
	}
	// For Coding Plans, pause until the earliest future reset in the snapshot.
	// Periodic quota monitoring reevaluates whether a later window still blocks scheduling.
	if provider.IsCodingPlan() {
		if until := CNProviderQuotaSnapshotReset(provider, s.options.Now()); until != nil {
			s.notifyProviderSchedulingBlocked(provider, *until, "429")
			if err := s.providerRepo.SetRateLimited(ctx, provider.ID, *until); err != nil {
				s.options.Warn("rate_limit_set_failed", "provider_id", provider.ID, "error", err)
				return true
			}
			s.options.Info("cn_coding_plan_rate_limited",
				"provider_id", provider.ID,
				"platform", provider.Platform,
				"reset_at", *until,
			)
			return true
		}
	}
	return false
}
