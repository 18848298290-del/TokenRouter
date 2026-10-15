package provider

import (
	"context"
	"strings"
	"time"
)

// grok_spending_reauth.go reads Grok reauthorization signals, clears stored
// flags, and selects spending-limit reset times.
// Grok spending-limit failures can require reauthorization or another check at
// the billing period end. Stored error text and Extra flags drive that display
// decision; successful credential replacement uses a bounded best-effort patch
// to clear the flags.

// grokSpendingLimitProbeCooldown bounds the next spending-limit check when
// billing data provides no parseable future period end.
const grokSpendingLimitProbeCooldown = 10 * time.Minute

// GrokReauthWriter supplies the Extra update used to clear spending-limit
// reauthorization flags.
type GrokReauthWriter interface {
	// UpdateExtra stores the reauthorization patch for the specified
	// provider
	// ID. ClearGrokNeedsReauth passes false and empty strings so all three
	// fields are cleared together by the writer.
	UpdateExtra(context.Context, int64, map[string]any) error
}

// ClearGrokNeedsReauth attempts to clear the three spending-limit
// reauthorization fields for a positive provider ID. Its five-second write
// budget retains supplied context values while ignoring caller cancellation,
// and update failure is discarded.
func ClearGrokNeedsReauth(ctx context.Context, writer GrokReauthWriter, id int64) {
	if writer == nil || id <= 0 {
		return
	}
	base := context.Background()
	if ctx != nil {
		// Reauthorization cleanup can follow a completed exchange after
		// the
		// HTTP caller disconnects; retain values for the bounded write.
		base = context.WithoutCancel(ctx)
	}
	stateCtx, cancel := context.WithTimeout(base, 5*time.Second)
	defer cancel()
	// Cleanup is best effort after credential replacement; its failure is
	// discarded so the completed authorization remains usable.
	_ = writer.UpdateExtra(stateCtx, id, map[string]any{
		"grok_needs_reauth":        false,
		"grok_needs_reauth_reason": "",
		"grok_needs_reauth_at":     "",
	})
}

// GrokSpendingLimitResetAt returns the first future billing period end,
// checking PeriodEnd before BillingPeriodEnd. Invalid, missing, or expired
// billing data produces a deadline ten minutes after now for another spending-
// limit check.
func GrokSpendingLimitResetAt(provider *Record, now time.Time) time.Time {
	if provider != nil {
		if billing, err := ParseGrokBillingSnapshot(provider.Extra); err == nil && billing != nil {
			for _, raw := range []string{billing.PeriodEnd, billing.BillingPeriodEnd} {
				if resetAt, err := time.Parse(time.RFC3339, strings.TrimSpace(raw)); err == nil && resetAt.After(now) {
					return resetAt
				}
			}
		}
	}
	// Unusable period ends cannot supply a billing deadline; schedule
	// another
	// check using the bounded probe cooldown.
	return now.Add(grokSpendingLimitProbeCooldown)
}

// GrokNeedsReauth reports a spending-limit or reauthorization signal from the
// provider's error or Extra flag. Nil returns false; string flags accept case-
// insensitive "true" or exactly "1", without whitespace trimming.
func GrokNeedsReauth(provider *Record) bool {
	if provider == nil {
		return false
	}
	if provider.Status == StatusError {
		msg := strings.ToLower(provider.ErrorMessage)
		if strings.Contains(msg, "spending limit") || strings.Contains(msg, "reauthorize") {
			return true
		}
	}
	if v, ok := provider.Extra["grok_needs_reauth"].(bool); ok && v {
		return true
	}
	if s, ok := provider.Extra["grok_needs_reauth"].(string); ok {
		return strings.EqualFold(s, "true") || s == "1"
	}
	return false
}
