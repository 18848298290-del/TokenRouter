package ops

import (
	"context"
	"encoding/json"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/TokenFlux/TokenRouter/internal/pkg/querycache"
)

type DashboardSnapshot struct {
	GeneratedAt string `json:"generated_at"`

	Overview        *OpsDashboardOverview       `json:"overview"`
	ThroughputTrend *OpsThroughputTrendResponse `json:"throughput_trend"`
	ErrorTrend      *OpsErrorTrendResponse      `json:"error_trend"`
}
type opsDashboardSnapshotV2CacheKey struct {
	StartTime    string `json:"start_time"`
	EndTime      string `json:"end_time"`
	Platform     string `json:"platform"`
	GroupID      *int64 `json:"group_id"`
	BucketSecond int    `json:"bucket_second"`
}

// CachedDashboardSnapshot runs three queries concurrently and caches their combined result for thirty seconds.
// The boolean reports a cache hit. Treat the returned snapshot as read-only because it is shared with the cache.
func (s *OpsService) CachedDashboardSnapshot(ctx context.Context, filter *OpsDashboardFilter, bucketSeconds int) (*DashboardSnapshot, bool, error) {
	s.snapshotOnce.Do(func() { s.snapshotCache = querycache.NewCache(30 * time.Second) })
	keyRaw, _ := json.Marshal(opsDashboardSnapshotV2CacheKey{
		StartTime:    filter.StartTime.UTC().Format(time.RFC3339),
		EndTime:      filter.EndTime.UTC().Format(time.RFC3339),
		Platform:     filter.Platform,
		GroupID:      filter.GroupID,
		BucketSecond: bucketSeconds,
	})
	cacheKey := string(keyRaw)

	if entry, ok := s.snapshotCache.Get(cacheKey); ok {
		if snapshot, valid := entry.Payload.(*DashboardSnapshot); valid {
			return snapshot, true, nil
		}
	}
	var (
		overview *OpsDashboardOverview
		trend    *OpsThroughputTrendResponse
		errTrend *OpsErrorTrendResponse
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		f := *filter
		result, err := s.GetDashboardOverview(gctx, &f)
		if err != nil {
			return err
		}
		overview = result
		return nil
	})
	g.Go(func() error {
		f := *filter
		result, err := s.GetThroughputTrend(gctx, &f, bucketSeconds)
		if err != nil {
			return err
		}
		trend = result
		return nil
	})
	g.Go(func() error {
		f := *filter
		result, err := s.GetErrorTrend(gctx, &f, bucketSeconds)
		if err != nil {
			return err
		}
		errTrend = result
		return nil
	})
	if err := g.Wait(); err != nil {
		return nil, false, err
	}

	resp := &DashboardSnapshot{
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
		Overview:        overview,
		ThroughputTrend: trend,
		ErrorTrend:      errTrend,
	}

	s.snapshotCache.Set(cacheKey, resp)
	return resp, false, nil
}
