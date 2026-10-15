package provider

import (
	"context"
	"strings"
	"time"
)

// cn_health.go applies CN concurrency, balance, and Coding Plan cooldowns to
// runtime scheduling and stored provider state.
// CN rejection handling keeps concurrency, balance, and Coding Plan pauses
// distinct. A concurrency response uses the shared 403 cooldown length without
// updating the forbidden-response counter. A balance response carries the
// current query fingerprint in its stored reason so the monitor can recognize
// the identity responsible for the pause.
//
// Runtime block notification precedes both temporary-pause and rate-limit
// persistence. Failed writes therefore leave any immediate scheduling block
// installed. Coding Plan rate limits use the earliest future reset in a
// matching limits snapshot, even when another window resets later; the caller
// receives a handled result once that deadline is selected.

// CNConcurrencyLimitReason labels the runtime scheduling block raised for a CN
// concurrency rejection. The stored pause receives the caller's reason
// separately.
const CNConcurrencyLimitReason = "cn_concurrency_limit"

// ApplyCNConcurrencyLimit notifies scheduling and attempts a temporary pause
// after a concurrency rejection. Callers provide a valid provider and store;
// the deadline uses the shared ten-minute 403 cooldown duration, while the
// reason supplied to persistence can contain the upstream explanation.
//
// The scheduling callback runs before the store write. A failed write logs a
// warning and leaves any callback-installed block in place, so the caller can
// switch providers immediately even when persistence fails.
func (s *HealthService) ApplyCNConcurrencyLimit(
	ctx context.Context,
	provider *Record, reason string,
) {
	// Interpret the shared 403 cooldown in minutes.
	until := s.options.Now().Add(time.Duration(OpenAI403CooldownMinutesDefault) * time.Minute)
	// Notify the request-side blocker before persistence so store failure
	// still
	// leaves the observed provider temporarily excluded.
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

// ApplyCNInsufficientBalance notifies scheduling and attempts an identity-
// labelled pause for an insufficient-balance response. Callers provide the
// observed provider; an unavailable CN query fingerprint is represented by
// "unknown", and trimmed upstream text is appended to the stored reason.
//
// The deadline spans two configured monitor intervals, using
// CNBalanceCooldownDuration for defaults. Notification precedes persistence, so
// a failed store write leaves any runtime block in effect. Later balance
// monitoring clears a pause only when its reason exactly matches the monitor
// reason for that fingerprint. An appended upstream explanation leaves this
// request-side reason different, so that recovery branch does not clear it.
func (s *HealthService) ApplyCNInsufficientBalance(
	ctx context.Context,
	provider *Record,
	upstreamMsg string,
) {
	identityHash := CNUsageMonitorIdentityFingerprint(provider)
	if identityHash == "" {
		// Fingerprint validation can fail for unsupported query
		// identities;
		// retain a recognizable balance reason for this request-side
		// pause.
		identityHash = "unknown"
	}
	msg := CNUsageMonitorReason(identityHash)
	if upstreamMsg = strings.TrimSpace(upstreamMsg); upstreamMsg != "" {
		msg += ": " + upstreamMsg
	}

	until := s.options.Now().Add(CNBalanceCooldownDuration(s.options.CNIntervalMinutes))
	// Publish the immediate block before storing its fingerprint reason; a
	// failed write cannot retract the callback result.
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

// CNBalanceCooldownDuration returns twice a positive monitoring interval in
// minutes, defaulting to twenty minutes. A computed duration below one minute
// is replaced by ten minutes before the caller stores a balance pause.
func CNBalanceCooldownDuration(intervalMinutes int) time.Duration {
	minutes := 10
	if intervalMinutes > 0 {
		minutes = intervalMinutes
	}
	// Cover two polling intervals before the pause expires.
	cooldown := time.Duration(minutes) * time.Minute * 2
	if cooldown < time.Minute {
		// Replace an undersized computed pause with ten minutes.
		cooldown = 10 * time.Minute
	}
	return cooldown
}

// CNProviderQuotaSnapshotReset returns a copied earliest future reset from an
// identity-valid CN Coding Plan limits snapshot. Nil, other platforms, pay-as-
// you-go records, and snapshots with another mode return nil.
//
// All windows contribute reset times regardless of their recorded utilization.
// Expired or missing resets are skipped, so a short-window reset can end the
// pause before a later weekly reset. Callers can reevaluate remaining quota
// after that earlier deadline; nil means this snapshot supplies no future
// deadline.
func CNProviderQuotaSnapshotReset(provider *Record, now time.Time) *time.Time {
	if provider == nil || !provider.IsCNProvider() || !provider.IsCodingPlan() {
		return nil
	}
	snapshot := ValidCNUsageMonitorSnapshot(provider)
	if snapshot == nil || snapshot.Mode != "limits" {
		return nil
	}
	var earliest *time.Time
	// Include every observed window because a 429 response does not
	// identify
	// which window exhausted its allocation here.
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

// ApplyCNQuotaSnapshotCooldown applies a Coding Plan snapshot deadline and
// reports whether that path handled the response. Nil or non-CN providers, pay-
// as-you-go records, and snapshots without a future reset return false for the
// caller's other rate-limit handling.
//
// A selected deadline is sent to the scheduling callback before persistence.
// Store failure is logged and still returns true, because this snapshot path
// already issued its block notification. Success records the rate-limit
// deadline and logs the selected reset.
func (s *HealthService) ApplyCNQuotaSnapshotCooldown(ctx context.Context, provider *Record) bool {
	if provider == nil || !provider.IsCNProvider() {
		return false
	}
	// For Coding Plans, pause until the earliest future reset in the
	// snapshot.
	// Periodic quota monitoring reevaluates whether a later window still
	// blocks
	// scheduling.
	if provider.IsCodingPlan() {
		if until := CNProviderQuotaSnapshotReset(provider, s.options.Now()); until != nil {
			// Issue the scheduling block first; a persistence
			// failure still
			// counts as handling this snapshot-derived rate-limit
			// response.
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
