package provider

import (
	"context"
	"sync"
	"time"
)

// Gemini local quota admission compares recorded request totals with the
// policy selected for each provider and requested model class. Daily checks
// run before minute checks, and exhaustion skips the provider without writing
// a durable rate limit; missing dependencies or unavailable quota leave
// admission open.
//
// Daily observations are cached by row ID and calendar start for less than one
// minute, while minute reads use the current fixed clock-minute window.
// Batches reuse daily cache entries and prefer aggregate readers, then query
// minute totals only for records still admitted. Single checks treat empty
// model text as the model classifier’s default, while empty-model batches skip
// quota work. Mutable records, callback clocks, and shared readers remain
// stable throughout each check; cached totals are scalar copies under a map
// lock.

// geminiPrecheckCacheTTL bounds reuse of daily totals for an unchanged
// provider row and calendar window before another usage observation is
// required.
const geminiPrecheckCacheTTL = time.Minute

// GeminiQuotaUsageReader obtains local model request totals for quota
// admission windows.
type GeminiQuotaUsageReader interface {
	// GetModelUsage reads a provider within the supplied start/end range.
	// Successful rows are aggregated by model class, nil rows mean zero totals,
	// and query failure is left to precheck callers to handle.
	GetModelUsage(context.Context, int64, time.Time, time.Time) ([]GeminiModelUsage, error)
}

// GeminiPrecheckOptions supplies quota calendar, observation clock, and
// exhaustion diagnostics.
type GeminiPrecheckOptions struct {
	// Now supplies one check’s window end and cache timestamp. Nil selects
	// time.Now in construction; keep the callback stable with its quota calendar.
	Now func() time.Time
	// Location selects daily calendar midnight. Nil construction input selects
	// fixed PST, while request totals must use this same configured calendar.
	Location *time.Location
	// Info observes exhausted quota decisions and defaults to a no-op callback.
	// Its output changes no persistent scheduling limit or local request totals.
	Info func(string, ...any)
}

// GeminiPrecheck caches daily totals and evaluates local provider quota
// admission.
type GeminiPrecheck struct {
	// usageRepo and geminiQuotaService are retained shared dependencies, while
	// options copies clock/calendar callbacks. Configure them before activity
	// and keep record observations stable during both quota phases.
	usageRepo          GeminiQuotaUsageReader // shared model usage reader
	geminiQuotaService *GeminiQuotaService    // shared quota policy service
	options            GeminiPrecheckOptions  // captured clock and calendar
	// usageCacheMu protects usageCache lookups and replacement. Entries own
	// scalar totals and are matched to both row ID and calendar start before
	// reuse.
	usageCacheMu sync.RWMutex // daily observation synchronization mutex
	// daily observations by row
	usageCache map[int64]*geminiUsageCacheEntry
}

// geminiUsageCacheEntry associates scalar totals with their calendar start and
// observation age.
type geminiUsageCacheEntry struct {
	// windowStart and cachedAt identify the daily calendar and reuse age of
	// totals. A hit requires both the same calendar start and an observation
	// younger than the cache lifetime.
	windowStart time.Time         // selected daily calendar start
	cachedAt    time.Time         // cached observation clock value
	totals      GeminiUsageTotals // scalar model-class usage totals
}

// GeminiUsageTotalsBatchReader optionally supplies aggregate totals for
// multiple provider window queries.
type GeminiUsageTotalsBatchReader interface {
	// GetGeminiUsageTotalsBatch reads class totals for requested provider IDs
	// under one range. Missing successful IDs mean zero totals; a whole-query
	// error aborts this quota phase instead of applying partial results.
	GetGeminiUsageTotalsBatch(ctx context.Context, providerIDs []int64, startTime, endTime time.Time) (map[int64]GeminiUsageTotals, error)
}

// NewGeminiPrecheck retains policy and usage readers and initializes its daily
// cache. Missing clock and logger select time.Now and a no-op, while missing
// location selects fixed UTC-minus-eight PST for quota dates.
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

// PreCheckUsage reports whether a stable provider can dispatch under local
// quota estimates. Nil, other platforms, missing dependencies, and unavailable
// policy remain allowed; a query error returns true plus the error for caller
// handling.
//
// The requested model determines a class, including the classifier default for
// empty text. A positive shared daily limit sums class requests; otherwise its
// selected class limit applies, and nonpositive returned limits skip that
// check. Daily exhaustion returns before minute querying. Successful daily
// totals are briefly cached, while minute totals are freshly read for a fixed
// clock minute. False with nil error means modeled quota exhaustion and leaves
// persistent rate-limit state unchanged.
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

	// Daily totals use the configured calendar start. A cache miss reloads that
	// same window before admission, while a hit keeps minute querying separate
	// from the reused daily observation.
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
				// Local usage predicts quota exhaustion to
				// reduce upstream 429 responses. Persisted
				// rate_limit_reset_at values come from upstream
				// rate-limit observations.
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
				// The local minute check leaves persisted
				// rate-limit state to upstream observations.
				s.options.Info("gemini_precheck_minute_quota_reached", "provider_id", provider.ID, "used", used, "limit", limit, "reset_at", resetAt)
				return false, nil
			}
		}
	}

	return true, nil
}

// PreCheckUsageBatch returns admission by row ID for stable provider
// observations. Nil entries are ignored; empty model/input, absent readers, or
// unavailable quota leaves initialized entries true. Callers use consistent
// record identity for repeated IDs.
//
// Daily cache misses prefer aggregate reads and populate scalar observations
// before quota comparison. Only daily-admitted rows with positive minute
// limits enter the second query. A daily read error returns the initialized
// map, while a minute error retains prior daily rejections; callers decide how
// to handle that error. The aggregate helper queries unique positive IDs and
// normalizes absent successful totals to zero, and all rate-limit persistence
// remains with upstream observations.
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

	// quotaProvider pairs one stable provider observation with its resolved
	// limits.
	type quotaProvider struct {
		// provider and quota remain paired through both quota phases so row-based
		// totals are compared with the limits selected for this exact observation.
		provider *Record     // selected provider row
		quota    GeminiQuota // resolved request quota
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

	// Keep cache hits with their selected daily window and load only misses.
	// Comparing all daily totals before minute admission prevents daily-exhausted
	// providers from participating in the later minute query.
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

	// Daily rejection takes precedence over minute work. Query only admitted
	// rows with positive minute limits so exhausted daily accounts need no
	// additional statistics read.
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

// getGeminiUsageTotalsBatch reads unique positive IDs for one supplied window.
// Empty input returns an empty map, and caller-provided records remain the
// source of quota policy rather than this aggregate reader.
//
// When the repository supports aggregate reads, returned struct totals are
// projected to requested IDs; missing rows become zero. Otherwise ordered
// per-row model queries are aggregated into the same shape. Any query error
// aborts the helper without a partial totals map, letting the surrounding
// batch retain only decisions made before this query.
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

	// Without aggregate capability, collect the same requested identities
	// through serial model queries. A failure aborts this phase instead of
	// treating unobserved requests as zero usage.
	for _, providerID := range ids {
		stats, err := s.usageRepo.GetModelUsage(ctx, providerID, start, end)
		if err != nil {
			return nil, err
		}
		result[providerID] = AggregateGeminiUsage(stats)
	}
	return result, nil
}

// geminiDailyLimit returns positive shared daily quota before model-class
// limits. Flash selects FlashRPD when shared is nonpositive; every other class
// selects ProRPD, and callers enforce only a positive returned value.
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

// geminiMinuteLimit returns positive shared minute quota before model-class
// limits. Flash selects FlashRPM when shared is nonpositive; other classes
// select ProRPM, and callers skip enforcement for a nonpositive result.
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

// geminiUsedRequests selects request totals matching daily or minute quota
// mode. Positive shared quota for that duration combines Pro and Flash;
// otherwise Flash chooses its own requests and other classes choose Pro.
//
// The duration flag is evaluated independently because daily and minute
// sharing can differ in one policy. Limit selectors and this count selector
// therefore use the same positive shared gate, keeping a class quota paired
// with class usage and a shared quota paired with combined usage. It returns
// scalar counts without modifying the cached observations.
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

// getGeminiUsageTotals returns copied scalar daily totals under the cache read
// lock. Missing entries, another calendar start, or age at least one minute
// miss; future timestamps can still satisfy that age check.
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

// setGeminiUsageTotals replaces one row observation under the cache write
// lock. Supplied start and now identify its calendar and age, and its scalar
// totals are copied for independently usable later reads.
func (s *GeminiPrecheck) setGeminiUsageTotals(providerID int64, windowStart, now time.Time, totals GeminiUsageTotals) {
	s.usageCacheMu.Lock()
	defer s.usageCacheMu.Unlock()
	if s.usageCache == nil {
		s.usageCache = make(map[int64]*geminiUsageCacheEntry)
	}
	// Replace the complete observation under the map lock so readers receive
	// totals with the same calendar and age. Concurrent loads publish in the
	// order this setter obtains the lock.
	s.usageCache[providerID] = &geminiUsageCacheEntry{
		windowStart: windowStart,
		cachedAt:    now,
		totals:      totals,
	}
}

// GeminiCooldown returns the current policy duration or five minutes when
// record or policy is unavailable. Quota service handles other-platform and
// unresolved-tier fallbacks, so this value remains separate from local
// admission results.
func (s *GeminiPrecheck) GeminiCooldown(ctx context.Context, provider *Record) time.Duration {
	if provider == nil {
		return 5 * time.Minute
	}
	if s.geminiQuotaService == nil {
		return 5 * time.Minute
	}
	return s.geminiQuotaService.CooldownForProvider(ctx, provider)
}
