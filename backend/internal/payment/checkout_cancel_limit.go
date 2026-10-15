package payment

import (
	"context"
	"fmt"
	"strconv"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// checkout_cancel_limit.go checks cancellation audit counts against the
// configured checkout admission window.
// Repeated user order cancellations can restrict later checkout creation when
// their audit count reaches the configured threshold. The check counts
// ORDER_CANCELLED rows for the user operator at or after a threshold computed
// from the observation time, and storage failure is logged while creation
// remains permitted.
//
// Window mode, unit, and width jointly select that threshold. Fixed windows
// include the current truncated bucket plus earlier buckets, while rolling
// windows subtract the configured width from the observation. Day calculations
// use local calendar dates, so daylight changes can differ from elapsed-hour
// subtraction. Missing width and unit values receive defaults; other units use
// hours and other modes use rolling handling.

// CheckCancelRateLimit rejects checkout creation when user cancellation audits
// at or after the configured threshold reach the limit. Supply a nonnil
// config; disabled or nonpositive limits skip counting, and a query failure is
// logged while creation remains permitted. Error metadata reports the
// configured window and unit values, even when threshold calculation
// substitutes defaults.
func (s *Checkout) CheckCancelRateLimit(ctx context.Context, userID int64, cfg *PaymentConfig) error {
	if !cfg.CancelRateLimitEnabled || cfg.CancelRateLimitMax <= 0 {
		return nil
	}
	windowStart := CancelRateLimitWindowStart(cfg, s.runtime.Now())
	operator := fmt.Sprintf("user:%d", userID)
	count, err := s.store.CancelledCount(ctx, operator, windowStart)
	if err != nil {
		// Cancellation history is advisory for creation when counting fails. Record
		// the storage failure and permit later checkout checks to decide whether the
		// purchase can proceed.
		s.runtime.Error("check cancel rate limit failed", "userID", userID, "error", err)
		return nil
	}
	if count >= cfg.CancelRateLimitMax {
		return infraerrors.TooManyRequests("CANCEL_RATE_LIMITED", "cancel rate limited").
			WithMetadata(map[string]string{
				"max":    strconv.Itoa(cfg.CancelRateLimitMax),
				"window": strconv.Itoa(cfg.CancelRateLimitWindow),
				"unit":   cfg.CancelRateLimitUnit,
			})
	}
	return nil
}

// CancelRateLimitWindowStart returns the inclusive cancellation-history
// threshold selected by mode, unit, and width. It defaults nonpositive width
// to one and empty unit to day; unknown units take the hour branch, and modes
// other than fixed take rolling handling.
//
// Fixed windows truncate minutes/hours or use local calendar midnight, then
// include width minus one earlier units. Rolling windows subtract width from
// the supplied observation time. Day branches use AddDate so calendar day
// transitions can differ from elapsed twenty-four-hour periods, while
// minute/hour branches use duration arithmetic.
func CancelRateLimitWindowStart(cfg *PaymentConfig, now time.Time) time.Time {
	w := cfg.CancelRateLimitWindow
	if w <= 0 {
		w = 1
	}
	unit := cfg.CancelRateLimitUnit
	if unit == "" {
		unit = "day"
	}
	if cfg.CancelRateLimitMode == "fixed" {
		switch unit {
		case "minute":
			// Truncate the current minute before including earlier fixed buckets.
			t := now.Truncate(time.Minute)
			return t.Add(-time.Duration(w-1) * time.Minute)
		case "day":
			y, m, d := now.Date()
			// Use midnight in the observation location so fixed day history includes
			// that calendar day and width minus one earlier calendar dates.
			t := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
			return t.AddDate(0, 0, -(w - 1))
		default: // hour
			// Truncate the current hour before including earlier fixed buckets.
			t := now.Truncate(time.Hour)
			return t.Add(-time.Duration(w-1) * time.Hour)
		}
	}
	// Rolling history starts at the corresponding earlier observation time.
	// Calendar day subtraction keeps its wall-clock fields across date changes,
	// while minute/hour widths subtract elapsed durations.
	switch unit {
	case "minute":
		return now.Add(-time.Duration(w) * time.Minute)
	case "day":
		return now.AddDate(0, 0, -w)
	default: // hour
		return now.Add(-time.Duration(w) * time.Hour)
	}
}
