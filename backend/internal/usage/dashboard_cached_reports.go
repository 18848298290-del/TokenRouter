package usage

import (
	"context"
	"time"
)

// Spending rankings and batch cost summaries reuse query results for a fixed
// cache lifetime. Ranking keys contain UTC start and end text at second
// precision plus the limit, while RankingReport retains the range values used
// by the stored result for HTTP display.
//
// Batch-user keys also contain the service calendar's current date,
// separating their today totals across midnight. Batch-key keys contain the
// requested IDs alone, so their today totals remain reusable until
// expiration. These methods perform ordinary lookup, query and insertion;
// overlapping misses can execute separately. querycache copies the supported
// result collections when storing and reading them.

// RankingReport carries spending rows together with their query range for
// HTTP display.
type RankingReport struct {
	Data *UserSpendingRankingResponse // queried spending ranking result
	// Start and End retain the range supplied on the stored query. UTC
	// second-precision keys can match inputs with different locations or
	// fractional seconds.
	Start, End time.Time // Start: query beginning; End: exclusive query ending
}

// GetUserSpendingRankingCached returns ranking data and its stored range,
// with a boolean indicating a cache lookup hit. The caller supplies a
// constructed service and a valid query range; repository errors and
// unexpected payload types are returned.
//
// The key serializes UTC bounds at second precision, while the payload
// retains the supplied time values. A cache hit therefore returns the first
// stored range's locations and fractional components. Lookup and insertion do
// not combine concurrent misses, and querycache copies supported ranking
// collections for cache storage and reads.
func (s *DashboardService) GetUserSpendingRankingCached(ctx context.Context, start, end time.Time, limit int) (RankingReport, bool, error) {
	key := mustMarshalDashboardCacheKey(struct {
		// Start and End serialize UTC bounds at second precision. Their text can
		// identify one ranking entry across caller locations and fractional seconds.
		Start string `json:"start"` // UTC query start text
		End   string `json:"end"`   // UTC query end text
		Limit int    `json:"limit"` // requested ranking row limit
	}{start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339), limit})
	if e, ok := s.queryCaches.ranking.Get(key); ok {
		v, err := snapshotPayloadAs[RankingReport](e.Payload)
		return v, true, err
	}
	data, err := s.GetUserSpendingRanking(ctx, start, end, limit)
	if err != nil {
		return RankingReport{}, false, err
	}
	v := RankingReport{Data: data, Start: start, End: end}
	s.queryCaches.ranking.Set(key, v)
	return v, false, nil
}

// GetBatchUsersUsageCached returns batch user charges and cache-hit status
// for the supplied ID list. The key retains ID order and includes
// Calendar.Today; repository zero-range defaults determine the total-cost
// reporting period.
func (s *DashboardService) GetBatchUsersUsageCached(ctx context.Context, ids []int64) (map[int64]*BatchUserUsageStats, bool, error) {
	key := mustMarshalDashboardCacheKey(struct {
		V       int     `json:"v"`        // batch user key version
		Day     string  `json:"day"`      // calendar today date text
		UserIDs []int64 `json:"user_ids"` // ordered queried user identifiers
	}{2, s.calendar.Today().Format("2006-01-02"), ids})
	if e, ok := s.queryCaches.batchUsers.Get(key); ok {
		v, err := snapshotPayloadAs[map[int64]*BatchUserUsageStats](e.Payload)
		return v, true, err
	}
	v, err := s.GetBatchUserUsageStats(ctx, ids, time.Time{}, time.Time{})
	if err != nil {
		return nil, false, err
	}
	s.queryCaches.batchUsers.Set(key, v)
	return v, false, nil
}

// GetBatchKeysUsageCached returns batch key charges and cache-hit status for
// the supplied ID list. The key retains ID order and omits the date, so today
// totals can remain cached across midnight until expiration.
func (s *DashboardService) GetBatchKeysUsageCached(ctx context.Context, ids []int64) (map[int64]*BatchAPIKeyUsageStats, bool, error) {
	key := mustMarshalDashboardCacheKey(struct {
		APIKeyIDs []int64 `json:"api_key_ids"` // ordered queried key identifiers
	}{ids})
	if e, ok := s.queryCaches.batchKeys.Get(key); ok {
		v, err := snapshotPayloadAs[map[int64]*BatchAPIKeyUsageStats](e.Payload)
		return v, true, err
	}
	v, err := s.GetBatchAPIKeyUsageStats(ctx, ids, time.Time{}, time.Time{})
	if err != nil {
		return nil, false, err
	}
	s.queryCaches.batchKeys.Set(key, v)
	return v, false, nil
}
