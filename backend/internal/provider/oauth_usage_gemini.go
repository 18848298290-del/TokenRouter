package provider

import (
	"context"
	"fmt"
	"time"
)

// Gemini local quota display separates daily and fixed-minute observations and
// combines Pro with Flash only for positive shared limits. Per-class windows
// otherwise retain their own requests, tokens, and provider cost; nonpositive
// limits produce no local window.
//
// The configured timezone supplies the daily origin, while
// GeminiDailyResetTime adds fixed twenty-four-hour durations. Minute windows
// truncate the clock to its current minute. Both totals queries must succeed
// for a returned display, and each duration selects its shared limit
// independently for class aggregation. Missing quota or totals callbacks
// yields an observation-time-only result. Callers supply a stable record, reached
// location callback, and clocks consistent with the totals source.

// GeminiUsageOptions supplies local quota settings, totals queries, and daily
// timezone selection.
type GeminiUsageOptions struct {
	// Quota optionally selects applicable local settings. Missing callback or
	// false qualification returns an empty dated view before statistics reads.
	Quota func(context.Context, *Record) (GeminiQuota, bool)
	// Totals is required alongside Quota for local display queries. Daily and
	// minute errors propagate instead of returning only the successful duration.
	Totals func(context.Context, int64, time.Time, time.Time) (GeminiUsageTotals, error)
	// Location is required when applicable quota queries run and supplies the
	// daily calendar. Return a nonnil location because the daily helpers call
	// time.Time.In with that value and panic when it is nil. Day-start and reset
	// calculations call it separately, so keep the returned timezone consistent
	// within one query.
	Location func() *time.Location
}

// GetGeminiUsage builds caller-owned daily and minute quota windows for a
// stable nonnil record. Missing quota/totals callbacks or an unavailable quota
// returns a dated empty view; reached queries require Location to return a
// nonnil timezone for both daily calculations.
//
// Positive shared limits sum Pro and Flash request, token, and provider-cost
// totals. Other limits create per-class views, with nonpositive limits omitted
// by BuildGeminiUsageProgress. Daily and minute queries fail the whole
// operation independently; the daily reset uses fixed-duration arithmetic and
// the minute reset follows clock truncation. Provide quota settings and totals
// in matching units and calendar context.
func (s *OAuthUsageService) GetGeminiUsage(ctx context.Context, provider *Record) (*UsageInfo, error) {
	now := s.options.Now()
	usage := &UsageInfo{
		UpdatedAt: &now,
	}

	if s.options.Gemini.Quota == nil || s.options.Gemini.Totals == nil {
		return usage, nil
	}

	quota, ok := s.options.Gemini.Quota(ctx, provider)
	if !ok {
		return usage, nil
	}

	dayStart := GeminiDailyWindowStart(now, s.options.Gemini.Location())
	dayTotals, err := s.options.Gemini.Totals(ctx, provider.ID, dayStart, now)
	if err != nil {
		return nil, fmt.Errorf("get gemini usage stats failed: %w", err)
	}

	dailyResetAt := GeminiDailyResetTime(now, s.options.Gemini.Location())

	// Positive shared daily limits combine both classes across requests, tokens,
	// and provider cost. Per-class limits retain those separate observations
	// before display conversion.
	if quota.SharedRPD > 0 {
		totalReq := dayTotals.ProRequests + dayTotals.FlashRequests
		totalTokens := dayTotals.ProTokens + dayTotals.FlashTokens
		totalCost := dayTotals.ProCost + dayTotals.FlashCost
		usage.GeminiSharedDaily = BuildGeminiUsageProgress(totalReq, quota.SharedRPD, dailyResetAt, totalTokens, totalCost, now)
	} else {
		usage.GeminiProDaily = BuildGeminiUsageProgress(dayTotals.ProRequests, quota.ProRPD, dailyResetAt, dayTotals.ProTokens, dayTotals.ProCost, now)
		usage.GeminiFlashDaily = BuildGeminiUsageProgress(dayTotals.FlashRequests, quota.FlashRPD, dailyResetAt, dayTotals.FlashTokens, dayTotals.FlashCost, now)
	}

	// Approximate the minute quota with the current fixed clock-minute window.
	minuteStart := now.Truncate(time.Minute)
	minuteResetAt := minuteStart.Add(time.Minute)
	minuteTotals, err := s.options.Gemini.Totals(ctx, provider.ID, minuteStart, now)
	if err != nil {
		return nil, fmt.Errorf("get gemini minute usage stats failed: %w", err)
	}

	// Minute sharing is selected independently from daily sharing, so its
	// positive limit combines class totals even when the daily display uses
	// separate quota windows.
	if quota.SharedRPM > 0 {
		totalReq := minuteTotals.ProRequests + minuteTotals.FlashRequests
		totalTokens := minuteTotals.ProTokens + minuteTotals.FlashTokens
		totalCost := minuteTotals.ProCost + minuteTotals.FlashCost
		usage.GeminiSharedMinute = BuildGeminiUsageProgress(totalReq, quota.SharedRPM, minuteResetAt, totalTokens, totalCost, now)
	} else {
		usage.GeminiProMinute = BuildGeminiUsageProgress(minuteTotals.ProRequests, quota.ProRPM, minuteResetAt, minuteTotals.ProTokens, minuteTotals.ProCost, now)
		usage.GeminiFlashMinute = BuildGeminiUsageProgress(minuteTotals.FlashRequests, quota.FlashRPM, minuteResetAt, minuteTotals.FlashTokens, minuteTotals.FlashCost, now)
	}

	return usage, nil
}
