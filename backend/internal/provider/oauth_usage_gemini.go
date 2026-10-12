package provider

import (
	"context"
	"fmt"
	"time"
)

// GeminiUsageOptions supplies quota settings, model usage totals, and the quota timezone.
type GeminiUsageOptions struct {
	Quota    func(context.Context, *Record) (GeminiQuota, bool)
	Totals   func(context.Context, int64, time.Time, time.Time) (GeminiUsageTotals, error)
	Location func() *time.Location
}

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

	// A shared daily quota combines Pro and Flash usage; separate quotas retain their own totals.
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
