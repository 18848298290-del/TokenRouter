package ops

import (
	"context"
	"encoding/json"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/TokenFlux/TokenRouter/internal/pkg/querycache"
)

// dashboard_snapshot.go loads dashboard components concurrently and caches
// complete snapshot generations.
// A dashboard snapshot combines overview, throughput, and error timelines into
// one cached generation. Each component query progresses independently, and the
// snapshot becomes available after all three complete successfully; their
// observations can still represent different database read times. A required
// query error cancels peer contexts and prevents publication after the
// remaining calls finish.
//
// The service cache holds generations for thirty seconds and uses supplied
// time/platform/group/bucket selections, with time endpoints formatted to whole
// seconds. Query-mode and ignored-status policy are resolved inside component
// calls and are absent from the key. Concurrent cache misses are independent
// loads. Cache insertion and reads copy supported snapshot fields through
// querycache.Clone, whose internal or unsupported references retain their
// copying restrictions.

// DashboardSnapshot carries the three completed dashboard query results and
// their assembly timestamp.
type DashboardSnapshot struct {
	// GeneratedAt is set to UTC time after all three queries complete. Cache hits
	// retain that assembly timestamp, so it identifies the cached generation
	// rather than the current read.
	GeneratedAt string `json:"generated_at"` // RFC3339

	// Overview, ThroughputTrend, and ErrorTrend come from independently executed
	// service queries. The snapshot is published after all three return
	// successfully; optional overview diagnostics can still be absent under its
	// own read rules.
	// traffic and optional health overview
	Overview *OpsDashboardOverview `json:"overview"`
	// time-bucketed traffic observation results
	ThroughputTrend *OpsThroughputTrendResponse `json:"throughput_trend"`
	// time-bucketed error observation results
	ErrorTrend *OpsErrorTrendResponse `json:"error_trend"`
}

// opsDashboardSnapshotV2CacheKey encodes the supplied selections used to reuse
// dashboard snapshots.
type opsDashboardSnapshotV2CacheKey struct {
	// StartTime and EndTime use UTC RFC3339 text at second precision. Queries
	// retain their supplied time values, so ranges with equal formatted seconds
	// can reuse the same key despite fractional differences.
	StartTime string `json:"start_time"` // UTC RFC3339 start key
	EndTime   string `json:"end_time"`   // UTC RFC3339 end key
	Platform  string `json:"platform"`   // supplied platform filter text
	// GroupID is JSON-encoded by value when supplied and as null when nil. Key
	// construction does not validate whether that identity is positive.
	GroupID *int64 `json:"group_id"` // supplied group identity pointer
	// BucketSecond retains the requested width without normalization. Trend
	// repositories can normalize an unsupported width while the cache key still
	// uses the supplied value.
	BucketSecond int `json:"bucket_second"` // requested bucket width seconds
}

// CachedDashboardSnapshot returns a dashboard snapshot and whether a cached
// payload of that type was read. Completed generations are cached for thirty
// seconds. Callers supply a nonnil service, context, and filter, keeping filter
// fields and their referenced values stable until return. Cache hits bypass
// query validation and policy resolution; the HTTP caller checks monitoring
// before this call.
//
// On a miss, independent shallow filter copies feed three concurrent service
// queries. An error cancels the errgroup context, and Wait joins all queries
// before returning the error without publication. Concurrent misses each
// perform their own queries. Keys omit QueryMode and IgnoredStatusCodes and use
// second-resolution endpoints. Get and Set clone supported snapshot fields
// under querycache.Clone rules; time.Time retains internal location data, and
// unsupported shared references stay immutable.
func (s *OpsService) CachedDashboardSnapshot(ctx context.Context, filter *OpsDashboardFilter, bucketSeconds int) (*DashboardSnapshot, bool, error) {
	// Initialize this service cache once; each successful insertion receives a
	// fixed thirty-second lifetime checked during subsequent cache reads.
	s.snapshotOnce.Do(func() { s.snapshotCache = querycache.NewCache(30 * time.Second) })
	// The key schema contains strings, an integer, and an optional integer
	// pointer, so its JSON encoding has no unsupported payload kind. Query policy
	// is resolved later.
	keyRaw, _ := json.Marshal(opsDashboardSnapshotV2CacheKey{
		StartTime:    filter.StartTime.UTC().Format(time.RFC3339),
		EndTime:      filter.EndTime.UTC().Format(time.RFC3339),
		Platform:     filter.Platform,
		GroupID:      filter.GroupID,
		BucketSecond: bucketSeconds,
	})
	cacheKey := string(keyRaw)

	// Cache.Get supplies a cloned entry. Accept the expected payload type here;
	// any other cached type falls through to fresh query assembly.
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
	// Each goroutine owns its filter value copy while pointer fields remain
	// shared. The errgroup context cancels peer query work after a reported error,
	// and Wait joins all three.
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
	// Publication follows the joined result: a required query error discards the
	// assembled generation even if another query already produced its response.
	if err := g.Wait(); err != nil {
		return nil, false, err
	}

	resp := &DashboardSnapshot{
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
		Overview:        overview,
		ThroughputTrend: trend,
		ErrorTrend:      errTrend,
	}

	// Set stores its cloned generation before returning this assembled response.
	// Later hits clone supported fields again, keeping ordinary result pointers
	// and slices separate from cache storage.
	s.snapshotCache.Set(cacheKey, resp)
	return resp, false, nil
}
