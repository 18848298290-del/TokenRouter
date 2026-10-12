package timing

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	HeaderName       = "Server-Timing"
	AdminUIHeader    = "X-Admin-UI-Request"
	UserUIHeader     = "X-User-UI-Request"
	MetricDatabase   = "db"
	MetricRedis      = "redis"
	dependencyPrefix = "dep_"

	maxMetricNameLength = 48
	maxIntervals        = 2048
	maxHeaderLength     = 4096
)

type contextKey struct{}

type interval struct {
	start time.Time
	end   time.Time
}

type metric struct {
	count     int64
	intervals []interval
}

// Collector stores request timing intervals and supports concurrent access.
type Collector struct {
	startedAt time.Time

	mu          sync.Mutex
	metrics     map[string]*metric
	cacheStatus string
}

// New creates a collector whose total duration starts at startedAt; zero uses the current time.
func New(startedAt time.Time) *Collector {
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return &Collector{
		startedAt: startedAt,
		metrics:   make(map[string]*metric),
	}
}

// WithCollector attaches a non-nil collector to context.
func WithCollector(ctx context.Context, collector *Collector) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if collector == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, collector)
}

// FromContext returns the request timing collector when present.
func FromContext(ctx context.Context) (*Collector, bool) {
	if ctx == nil {
		return nil, false
	}
	collector, ok := ctx.Value(contextKey{}).(*Collector)
	return collector, ok && collector != nil
}

// Active reports whether context has a timing collector.
func Active(ctx context.Context) bool {
	_, ok := FromContext(ctx)
	return ok
}

// Record adds a completed interval and operation count to the context collector.
func Record(ctx context.Context, name string, startedAt, endedAt time.Time, count int) {
	collector, ok := FromContext(ctx)
	if !ok {
		return
	}
	collector.Record(name, startedAt, endedAt, count)
}

// RecordInterval adds duration without increasing the operation count, allowing
// multiple blocking driver calls to belong to one logical operation.
func RecordInterval(ctx context.Context, name string, startedAt, endedAt time.Time) {
	collector, ok := FromContext(ctx)
	if !ok {
		return
	}
	collector.record(name, startedAt, endedAt, 0)
}

// Record adds a completed interval and counts at least one operation.
func (c *Collector) Record(name string, startedAt, endedAt time.Time, count int) {
	if count <= 0 {
		count = 1
	}
	c.record(name, startedAt, endedAt, count)
}

func (c *Collector) record(name string, startedAt, endedAt time.Time, count int) {
	name = normalizeMetricName(name)
	if c == nil || name == "" || startedAt.IsZero() || endedAt.Before(startedAt) {
		return
	}
	if count < 0 {
		count = 0
	}

	c.mu.Lock()
	m := c.metrics[name]
	if m == nil {
		m = &metric{}
		c.metrics[name] = m
	}
	m.count += int64(count)
	if len(m.intervals) < maxIntervals {
		m.intervals = append(m.intervals, interval{start: startedAt, end: endedAt})
	}
	c.mu.Unlock()
}

// Observe starts an interval and returns an idempotent function that records its end.
func Observe(ctx context.Context, name string) func() {
	collector, ok := FromContext(ctx)
	name = normalizeMetricName(name)
	if !ok || name == "" {
		return func() {}
	}
	startedAt := time.Now()
	var once sync.Once
	return func() {
		once.Do(func() {
			collector.Record(name, startedAt, time.Now(), 1)
		})
	}
}

// ObserveDependency starts an interval for a named external dependency.
func ObserveDependency(ctx context.Context, module string) func() {
	return Observe(ctx, dependencyMetricName(module))
}

// RecordDependency records a completed external dependency call.
func RecordDependency(ctx context.Context, module string, startedAt, endedAt time.Time) {
	Record(ctx, dependencyMetricName(module), startedAt, endedAt, 1)
}

// SetCacheStatus records hit, miss, or bypass, ignoring unrecognized values.
func SetCacheStatus(ctx context.Context, status string) {
	collector, ok := FromContext(ctx)
	if !ok {
		return
	}
	status = normalizeCacheStatus(status)
	if status == "" {
		return
	}
	collector.mu.Lock()
	collector.cacheStatus = status
	collector.mu.Unlock()
}

// HeaderValue returns the context collector's Server-Timing value, or an empty string.
func HeaderValue(ctx context.Context, endedAt time.Time, cacheStatus string) string {
	collector, ok := FromContext(ctx)
	if !ok {
		return ""
	}
	return collector.HeaderValue(endedAt, cacheStatus)
}

// HeaderValue builds a bounded Server-Timing value with sorted dependency names.
// Durations use interval unions so overlapping calls do not double-count elapsed time.
func (c *Collector) HeaderValue(endedAt time.Time, cacheStatus string) string {
	if c == nil {
		return ""
	}
	if endedAt.IsZero() {
		endedAt = time.Now()
	}
	if endedAt.Before(c.startedAt) {
		endedAt = c.startedAt
	}

	c.mu.Lock()
	metrics := make(map[string]metric, len(c.metrics))
	allIntervals := make([]interval, 0)
	dependencyIntervals := make([]interval, 0)
	var dependencyCount int64
	for name, source := range c.metrics {
		copied := metric{count: source.count, intervals: append([]interval(nil), source.intervals...)}
		metrics[name] = copied
		allIntervals = append(allIntervals, copied.intervals...)
		if strings.HasPrefix(name, dependencyPrefix) {
			dependencyIntervals = append(dependencyIntervals, copied.intervals...)
			dependencyCount += copied.count
		}
	}
	storedCacheStatus := c.cacheStatus
	c.mu.Unlock()

	total := endedAt.Sub(c.startedAt)
	blocked := unionDuration(allIntervals, c.startedAt, endedAt)
	app := max(total-blocked, 0)

	cacheStatus = normalizeCacheStatus(cacheStatus)
	if cacheStatus == "" {
		cacheStatus = normalizeCacheStatus(storedCacheStatus)
	}
	if cacheStatus == "" {
		cacheStatus = "bypass"
	}

	database := metrics[MetricDatabase]
	redisMetric := metrics[MetricRedis]
	parts := []string{
		"total;dur=" + formatDuration(total),
		"app;dur=" + formatDuration(app),
		fmt.Sprintf("db;dur=%s;desc=\"queries=%d\"", formatDuration(unionDuration(database.intervals, c.startedAt, endedAt)), database.count),
		fmt.Sprintf("redis;dur=%s;desc=\"commands=%d\"", formatDuration(unionDuration(redisMetric.intervals, c.startedAt, endedAt)), redisMetric.count),
		"cache;desc=\"" + cacheStatus + "\"",
		fmt.Sprintf("deps;dur=%s;desc=\"calls=%d\"", formatDuration(unionDuration(dependencyIntervals, c.startedAt, endedAt)), dependencyCount),
	}

	dependencyNames := make([]string, 0)
	for name := range metrics {
		if strings.HasPrefix(name, dependencyPrefix) {
			dependencyNames = append(dependencyNames, name)
		}
	}
	sort.Strings(dependencyNames)
	for _, name := range dependencyNames {
		m := metrics[name]
		part := fmt.Sprintf("%s;dur=%s;desc=\"calls=%d\"", name, formatDuration(unionDuration(m.intervals, c.startedAt, endedAt)), m.count)
		candidate := strings.Join(append(parts, part), ", ")
		if len(candidate) > maxHeaderLength {
			break
		}
		parts = append(parts, part)
	}

	return strings.Join(parts, ", ")
}

func dependencyMetricName(module string) string {
	module = normalizeMetricName(module)
	module = strings.TrimPrefix(module, dependencyPrefix)
	if module == "" {
		module = "http"
	}
	return dependencyPrefix + module
}

func normalizeMetricName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(min(len(name), maxMetricNameLength))
	for _, r := range name {
		if b.Len() >= maxMetricNameLength {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			_, _ = b.WriteRune(r)
		case r == '_' || r == '-':
			_ = b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func normalizeCacheStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "hit":
		return "hit"
	case "miss":
		return "miss"
	case "bypass":
		return "bypass"
	default:
		return ""
	}
}

func unionDuration(intervals []interval, lowerBound, upperBound time.Time) time.Duration {
	if len(intervals) == 0 || !upperBound.After(lowerBound) {
		return 0
	}
	normalized := make([]interval, 0, len(intervals))
	for _, item := range intervals {
		start := item.start
		end := item.end
		if start.Before(lowerBound) {
			start = lowerBound
		}
		if end.After(upperBound) {
			end = upperBound
		}
		if end.After(start) {
			normalized = append(normalized, interval{start: start, end: end})
		}
	}
	if len(normalized) == 0 {
		return 0
	}
	sort.Slice(normalized, func(i, j int) bool {
		return normalized[i].start.Before(normalized[j].start)
	})

	currentStart := normalized[0].start
	currentEnd := normalized[0].end
	var total time.Duration
	for _, item := range normalized[1:] {
		if !item.start.After(currentEnd) {
			if item.end.After(currentEnd) {
				currentEnd = item.end
			}
			continue
		}
		total += currentEnd.Sub(currentStart)
		currentStart = item.start
		currentEnd = item.end
	}
	total += currentEnd.Sub(currentStart)
	return total
}

func formatDuration(value time.Duration) string {
	if value < 0 {
		value = 0
	}
	return strconv.FormatFloat(float64(value)/float64(time.Millisecond), 'f', 1, 64)
}
