package provider

import (
	"context"
	"sync"
	"time"
)

const geminiPrecheckCacheTTL = time.Minute

// GeminiQuotaUsageReader reads local model usage for quota checks through the usage storage adapter.
type GeminiQuotaUsageReader interface {
	GetModelUsage(context.Context, int64, time.Time, time.Time) ([]GeminiModelUsage, error)
}
type GeminiPrecheckOptions struct {
	Now      func() time.Time
	Location *time.Location
	Info     func(string, ...any)
}

// GeminiPrecheck caches daily usage briefly and checks admission without persisting provider limits or charges.
type GeminiPrecheck struct {
	usageRepo          GeminiQuotaUsageReader
	geminiQuotaService *GeminiQuotaService
	options            GeminiPrecheckOptions
	usageCacheMu       sync.RWMutex
	usageCache         map[int64]*geminiUsageCacheEntry
}

type geminiUsageCacheEntry struct {
	windowStart time.Time
	cachedAt    time.Time
	totals      GeminiUsageTotals
}

type GeminiUsageTotalsBatchReader interface {
	GetGeminiUsageTotalsBatch(ctx context.Context, providerIDs []int64, startTime, endTime time.Time) (map[int64]GeminiUsageTotals, error)
}

func NewGeminiPrecheck(policy *GeminiQuotaService, usage GeminiQuotaUsageReader, options GeminiPrecheckOptions) *GeminiPrecheck {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Info == nil {
		options.Info = func(string, ...any) {}
	}
	if options.Location == nil {
		options.Location = time.FixedZone("PST", -8*3600)
	}
	return &GeminiPrecheck{geminiQuotaService: policy, usageRepo: usage, options: options, usageCache: make(map[int64]*geminiUsageCacheEntry)}
}

// PreCheckUsage checks local quota before dispatch.
// A false result means the caller should skip this provider.
func (s *GeminiPrecheck) PreCheckUsage(ctx context.Context, provider *Record, requestedModel string) (bool, error) {
	if provider == nil || provider.Platform != PlatformGemini {
		return true, nil
	}
	if s.usageRepo == nil || s.geminiQuotaService == nil {
		return true, nil
	}

	quota, ok := s.geminiQuotaService.QuotaForProvider(ctx, provider)
	if !ok {
		return true, nil
	}

	now := s.options.Now()
	modelClass := GeminiModelClassFromName(requestedModel)

	// Daily usage starts at midnight in the configured quota timezone.
	{
		limit := geminiDailyLimit(quota, modelClass)
		if limit > 0 {
			start := GeminiDailyWindowStart(now, s.options.Location)
			totals, ok := s.getGeminiUsageTotals(provider.ID, start, now)
			if !ok {
				stats, err := s.usageRepo.GetModelUsage(ctx, provider.ID, start, now)
				if err != nil {
					return true, err
				}
				totals = AggregateGeminiUsage(stats)
				s.setGeminiUsageTotals(provider.ID, start, now, totals)
			}

			used := geminiUsedRequests(quota, modelClass, totals, true)
			if used >= limit {
				resetAt := GeminiDailyResetTime(now, s.options.Location)
				// Local usage predicts quota exhaustion to reduce upstream 429 responses.
				// Persisted rate_limit_reset_at values come from upstream rate-limit observations.
				s.options.Info("gemini_precheck_daily_quota_reached", "provider_id", provider.ID, "used", used, "limit", limit, "reset_at", resetAt)
				return false, nil
			}
		}
	}

	// Minute quotas use the current fixed minute window.
	{
		limit := geminiMinuteLimit(quota, modelClass)
		if limit > 0 {
			start := now.Truncate(time.Minute)
			stats, err := s.usageRepo.GetModelUsage(ctx, provider.ID, start, now)
			if err != nil {
				return true, err
			}
			totals := AggregateGeminiUsage(stats)

			used := geminiUsedRequests(quota, modelClass, totals, false)
			if used >= limit {
				resetAt := start.Add(time.Minute)
				// The local minute check leaves persisted rate-limit state to upstream observations.
				s.options.Info("gemini_precheck_minute_quota_reached", "provider_id", provider.ID, "used", used, "limit", limit, "reset_at", resetAt)
				return false, nil
			}
		}
	}

	return true, nil
}

// PreCheckUsageBatch checks quota for a batch of providers in one request.
// A false map value means the caller should skip that provider.
func (s *GeminiPrecheck) PreCheckUsageBatch(ctx context.Context, providers []*Record, requestedModel string) (map[int64]bool, error) {
	result := make(map[int64]bool, len(providers))
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		result[provider.ID] = true
	}

	if len(providers) == 0 || requestedModel == "" {
		return result, nil
	}
	if s.usageRepo == nil || s.geminiQuotaService == nil {
		return result, nil
	}

	modelClass := GeminiModelClassFromName(requestedModel)
	now := s.options.Now()
	dailyStart := GeminiDailyWindowStart(now, s.options.Location)
	minuteStart := now.Truncate(time.Minute)

	type quotaProvider struct {
		provider *Record
		quota    GeminiQuota
	}
	quotaProviders := make([]quotaProvider, 0, len(providers))
	for _, provider := range providers {
		if provider == nil || provider.Platform != PlatformGemini {
			continue
		}
		quota, ok := s.geminiQuotaService.QuotaForProvider(ctx, provider)
		if !ok {
			continue
		}
		quotaProviders = append(quotaProviders, quotaProvider{
			provider: provider,
			quota:    quota,
		})
	}
	if len(quotaProviders) == 0 {
		return result, nil
	}

	// Reuse recent daily usage and batch-load cache misses.
	dailyTotalsByID := make(map[int64]GeminiUsageTotals, len(quotaProviders))
	dailyMissIDs := make([]int64, 0, len(quotaProviders))
	for _, item := range quotaProviders {
		limit := geminiDailyLimit(item.quota, modelClass)
		if limit <= 0 {
			continue
		}
		providerID := item.provider.ID
		if totals, ok := s.getGeminiUsageTotals(providerID, dailyStart, now); ok {
			dailyTotalsByID[providerID] = totals
			continue
		}
		dailyMissIDs = append(dailyMissIDs, providerID)
	}
	if len(dailyMissIDs) > 0 {
		totalsBatch, err := s.getGeminiUsageTotalsBatch(ctx, dailyMissIDs, dailyStart, now)
		if err != nil {
			return result, err
		}
		for _, providerID := range dailyMissIDs {
			totals := totalsBatch[providerID]
			dailyTotalsByID[providerID] = totals
			s.setGeminiUsageTotals(providerID, dailyStart, now, totals)
		}
	}
	for _, item := range quotaProviders {
		limit := geminiDailyLimit(item.quota, modelClass)
		if limit <= 0 {
			continue
		}
		providerID := item.provider.ID
		used := geminiUsedRequests(item.quota, modelClass, dailyTotalsByID[providerID], true)
		if used >= limit {
			resetAt := GeminiDailyResetTime(now, s.options.Location)
			s.options.Info("gemini_precheck_daily_quota_reached_batch", "provider_id", providerID, "used", used, "limit", limit, "reset_at", resetAt)
			result[providerID] = false
		}
	}

	// Query minute usage for providers still eligible after the daily check.
	minuteIDs := make([]int64, 0, len(quotaProviders))
	for _, item := range quotaProviders {
		providerID := item.provider.ID
		if !result[providerID] {
			continue
		}
		if geminiMinuteLimit(item.quota, modelClass) <= 0 {
			continue
		}
		minuteIDs = append(minuteIDs, providerID)
	}
	if len(minuteIDs) == 0 {
		return result, nil
	}

	minuteTotalsByID, err := s.getGeminiUsageTotalsBatch(ctx, minuteIDs, minuteStart, now)
	if err != nil {
		return result, err
	}
	for _, item := range quotaProviders {
		providerID := item.provider.ID
		if !result[providerID] {
			continue
		}

		limit := geminiMinuteLimit(item.quota, modelClass)
		if limit <= 0 {
			continue
		}

		used := geminiUsedRequests(item.quota, modelClass, minuteTotalsByID[providerID], false)
		if used >= limit {
			resetAt := minuteStart.Add(time.Minute)
			s.options.Info("gemini_precheck_minute_quota_reached_batch", "provider_id", providerID, "used", used, "limit", limit, "reset_at", resetAt)
			result[providerID] = false
		}
	}

	return result, nil
}

func (s *GeminiPrecheck) getGeminiUsageTotalsBatch(ctx context.Context, providerIDs []int64, start, end time.Time) (map[int64]GeminiUsageTotals, error) {
	result := make(map[int64]GeminiUsageTotals, len(providerIDs))
	if len(providerIDs) == 0 {
		return result, nil
	}

	ids := make([]int64, 0, len(providerIDs))
	seen := make(map[int64]struct{}, len(providerIDs))
	for _, providerID := range providerIDs {
		if providerID <= 0 {
			continue
		}
		if _, ok := seen[providerID]; ok {
			continue
		}
		seen[providerID] = struct{}{}
		ids = append(ids, providerID)
	}
	if len(ids) == 0 {
		return result, nil
	}

	if batchReader, ok := s.usageRepo.(GeminiUsageTotalsBatchReader); ok {
		stats, err := batchReader.GetGeminiUsageTotalsBatch(ctx, ids, start, end)
		if err != nil {
			return nil, err
		}
		for _, providerID := range ids {
			result[providerID] = stats[providerID]
		}
		return result, nil
	}

	for _, providerID := range ids {
		stats, err := s.usageRepo.GetModelUsage(ctx, providerID, start, end)
		if err != nil {
			return nil, err
		}
		result[providerID] = AggregateGeminiUsage(stats)
	}
	return result, nil
}

func geminiDailyLimit(quota GeminiQuota, modelClass GeminiModelClass) int64 {
	if quota.SharedRPD > 0 {
		return quota.SharedRPD
	}
	switch modelClass {
	case GeminiModelFlash:
		return quota.FlashRPD
	default:
		return quota.ProRPD
	}
}

func geminiMinuteLimit(quota GeminiQuota, modelClass GeminiModelClass) int64 {
	if quota.SharedRPM > 0 {
		return quota.SharedRPM
	}
	switch modelClass {
	case GeminiModelFlash:
		return quota.FlashRPM
	default:
		return quota.ProRPM
	}
}

func geminiUsedRequests(quota GeminiQuota, modelClass GeminiModelClass, totals GeminiUsageTotals, daily bool) int64 {
	if daily {
		if quota.SharedRPD > 0 {
			return totals.ProRequests + totals.FlashRequests
		}
	} else {
		if quota.SharedRPM > 0 {
			return totals.ProRequests + totals.FlashRequests
		}
	}
	switch modelClass {
	case GeminiModelFlash:
		return totals.FlashRequests
	default:
		return totals.ProRequests
	}
}

func (s *GeminiPrecheck) getGeminiUsageTotals(providerID int64, windowStart, now time.Time) (GeminiUsageTotals, bool) {
	s.usageCacheMu.RLock()
	defer s.usageCacheMu.RUnlock()

	if s.usageCache == nil {
		return GeminiUsageTotals{}, false
	}

	entry, ok := s.usageCache[providerID]
	if !ok || entry == nil {
		return GeminiUsageTotals{}, false
	}
	if !entry.windowStart.Equal(windowStart) {
		return GeminiUsageTotals{}, false
	}
	if now.Sub(entry.cachedAt) >= geminiPrecheckCacheTTL {
		return GeminiUsageTotals{}, false
	}
	return entry.totals, true
}

func (s *GeminiPrecheck) setGeminiUsageTotals(providerID int64, windowStart, now time.Time, totals GeminiUsageTotals) {
	s.usageCacheMu.Lock()
	defer s.usageCacheMu.Unlock()
	if s.usageCache == nil {
		s.usageCache = make(map[int64]*geminiUsageCacheEntry)
	}
	s.usageCache[providerID] = &geminiUsageCacheEntry{
		windowStart: windowStart,
		cachedAt:    now,
		totals:      totals,
	}
}

// GeminiCooldown returns the tier's 429 cooldown, or five minutes when the provider or policy is unavailable.
func (s *GeminiPrecheck) GeminiCooldown(ctx context.Context, provider *Record) time.Duration {
	if provider == nil {
		return 5 * time.Minute
	}
	if s.geminiQuotaService == nil {
		return 5 * time.Minute
	}
	return s.geminiQuotaService.CooldownForProvider(ctx, provider)
}
