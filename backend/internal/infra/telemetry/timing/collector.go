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

// Concurrent database, Redis, and external calls can overlap within one
// request, so adding their durations would overstate blocked elapsed time.
// The collector stores completed intervals and counts independently: two
// ten-millisecond calls overlapping for five milliseconds occupy fifteen
// milliseconds together and still count as two operations. Header
// construction clips these intervals to the request window and subtracts
// their union from total time.
//
// Recording and cache-status updates share a mutex with snapshot copying.
// Header calculation sorts and merges the copied observations after
// unlocking, allowing later work to be recorded for a subsequent header. Each
// metric caps stored intervals while its count continues increasing; fixed
// metrics and dependency totals remain when sorted dependency details reach
// the header limit.

// Header names identify UI timing requests, metric names group completed
// operations, and storage limits bound retained intervals and emitted detail text.
const (
	HeaderName       = "Server-Timing"      // response timing header name
	AdminUIHeader    = "X-Admin-UI-Request" // admin UI timing marker
	UserUIHeader     = "X-User-UI-Request"  // user UI timing marker
	MetricDatabase   = "db"                 // SQL blocking metric label
	MetricRedis      = "redis"              // Redis blocking metric label
	dependencyPrefix = "dep_"               // external dependency metric prefix

	maxMetricNameLength = 48   // normalized label byte limit
	maxIntervals        = 2048 // stored spans per metric
	maxHeaderLength     = 4096 // emitted detail byte budget
)

// contextKey gives collector context values a package-private lookup identity.
type contextKey struct{}

// interval holds one completed blocking span before request-window clipping.
type interval struct {
	// start and end are absolute observations whose span is clipped to the
	// collector's request window when HeaderValue merges elapsed coverage.
	start time.Time // observed operation start
	end   time.Time // observed operation end
}

// metric retains operation counts and bounded blocking spans for one normalized name.
type metric struct {
	// count continues increasing after intervals reaches its storage cap;
	// HeaderValue therefore distinguishes all counted request operations from the
	// elapsed coverage available in retained observations.
	count     int64      // accumulated operation count
	intervals []interval // retained completed blocking spans
}

// Collector stores request timing intervals and supports concurrent access.
type Collector struct {
	startedAt time.Time // fixed request window start

	// mu protects metrics and cacheStatus during updates and snapshot copying.
	// HeaderValue releases it before sorting and merging the copied intervals.
	mu          sync.Mutex         // observation state mutex
	metrics     map[string]*metric // normalized name observations
	cacheStatus string             // last recognized cache status
}

// New creates an empty collector whose request window starts at startedAt,
// using the current time for zero. Share the returned collector through
// context; recording and header snapshots synchronize its mutable
// observations.
func New(startedAt time.Time) *Collector {
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return &Collector{
		startedAt: startedAt,
		metrics:   make(map[string]*metric),
	}
}

// WithCollector attaches the shared collector to ctx and returns the derived
// context. Nil ctx becomes background; a nil collector leaves that context
// unchanged, and callers retain the collector for concurrent request
// observations.
func WithCollector(ctx context.Context, collector *Collector) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if collector == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, collector)
}

// FromContext returns the stored collector and reports whether a non-nil
// collector is present. Nil contexts and absent or differently typed values
// return false, allowing callers to skip optional timing work.
func FromContext(ctx context.Context) (*Collector, bool) {
	if ctx == nil {
		return nil, false
	}
	collector, ok := ctx.Value(contextKey{}).(*Collector)
	return collector, ok && collector != nil
}

// Active reports whether ctx contains a non-nil request timing collector.
func Active(ctx context.Context) bool {
	_, ok := FromContext(ctx)
	return ok
}

// Record adds a completed interval and operation count to ctx's collector. A
// missing collector ignores the observation; nonpositive counts select one
// operation, and invalid spans are rejected by the collector.
func Record(ctx context.Context, name string, startedAt, endedAt time.Time, count int) {
	collector, ok := FromContext(ctx)
	if !ok {
		return
	}
	collector.Record(name, startedAt, endedAt, count)
}

// RecordInterval adds a completed blocking interval to ctx's collector
// without increasing its operation count. Missing collectors are ignored,
// letting row reads and transaction completion contribute duration to an
// already counted logical operation.
func RecordInterval(ctx context.Context, name string, startedAt, endedAt time.Time) {
	collector, ok := FromContext(ctx)
	if !ok {
		return
	}
	collector.record(name, startedAt, endedAt, 0)
}

// Record adds a completed interval to this collector and counts at least one
// operation. Nonpositive count selects one; nil receivers and invalid
// normalized names or spans are ignored by record.
func (c *Collector) Record(name string, startedAt, endedAt time.Time, count int) {
	if count <= 0 {
		count = 1
	}
	c.record(name, startedAt, endedAt, count)
}

// record adds count and a completed interval under the collector lock after
// normalizing its metric name. Nil receivers, empty names, zero starts, and
// reversed times are ignored; negative counts are clamped to zero.
//
// Every accepted observation increases the metric's count even when stored
// intervals have reached their cap. Later header snapshots can therefore show
// all counted operations while duration reflects the retained intervals.
// Callers construct the collector with New so its metric map is initialized.
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
	// Retain bounded duration evidence while counts keep accumulating. Later
	// operations can still appear in header counts after their intervals are omitted.
	if len(m.intervals) < maxIntervals {
		m.intervals = append(m.intervals, interval{start: startedAt, end: endedAt})
	}
	c.mu.Unlock()
}

// Observe starts an interval and returns a completion function that records
// its end at most once. Missing collectors or empty normalized names return a
// no-op; callers finish after the observed operation, and repeated completion
// calls share sync.Once.
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

// ObserveDependency starts a named external interval and returns its
// idempotent completion function. A blank sanitized module selects http,
// while a missing collector produces the same no-op behavior as Observe.
func ObserveDependency(ctx context.Context, module string) func() {
	return Observe(ctx, dependencyMetricName(module))
}

// RecordDependency records one completed external call under the sanitized
// module name. Blank names select http, and missing collectors ignore the
// observation; supplied times follow Record's validity rules.
func RecordDependency(ctx context.Context, module string, startedAt, endedAt time.Time) {
	Record(ctx, dependencyMetricName(module), startedAt, endedAt, 1)
}

// SetCacheStatus stores a recognized hit, miss, or bypass value in ctx's
// collector. Missing collectors and unrecognized values leave stored status
// intact; concurrent accepted writes are serialized with metric recording and
// snapshots.
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

// HeaderValue returns ctx's collector header or empty text when timing is
// absent. The collector applies request-window clipping, cache-status
// selection, and bounded dependency details to the supplied end time.
func HeaderValue(ctx context.Context, endedAt time.Time, cacheStatus string) string {
	collector, ok := FromContext(ctx)
	if !ok {
		return ""
	}
	return collector.HeaderValue(endedAt, cacheStatus)
}

// HeaderValue returns fixed timing metrics and sorted dependency details from
// a consistent copied snapshot. Nil receivers return empty text; zero endedAt
// selects now and an end before the request start is clamped to that start.
//
// Blocked time uses the clipped interval union, while operation counts remain
// additive. A recognized argument cache status wins over the sampled stored
// status, then bypass. Fixed metrics and aggregate dependencies survive when
// the detail budget omits later sorted dependency names; subsequent
// observations belong to later snapshots.
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

	// Copy counts, intervals, and cache status together while writers are
	// excluded, then release the lock before sorting the consistent header inputs.
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

	// Prefer a recognized argument, then the sampled stored status, then bypass
	// so invalid status text cannot enter the emitted Server-Timing description.
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
		// Fixed metrics and aggregate dependency coverage have already been emitted.
		// Omit this sorted detail and later details when the header budget is exhausted.
		if len(candidate) > maxHeaderLength {
			break
		}
		parts = append(parts, part)
	}

	return strings.Join(parts, ", ")
}

// dependencyMetricName normalizes module and supplies one dep_ prefix for
// dependency metrics. It removes an existing prefix first and selects http
// when the remaining normalized module name is empty.
func dependencyMetricName(module string) string {
	module = normalizeMetricName(module)
	module = strings.TrimPrefix(module, dependencyPrefix)
	if module == "" {
		module = "http"
	}
	return dependencyPrefix + module
}

// normalizeMetricName returns lowercase ASCII letters, digits, and
// underscores after trimming input. Hyphens become underscores, other runes
// are dropped, output stops at forty-eight bytes, and leading or trailing
// underscores are removed.
func normalizeMetricName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(min(len(name), maxMetricNameLength))
	// Keep header labels within the ASCII metric alphabet and byte budget. Map
	// hyphens to underscores while dropping other runes so supplied text cannot
	// introduce header separators or multiline metric descriptions.
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

// normalizeCacheStatus returns recognized cache state text after case and
// whitespace normalization. Other values return empty text so callers can
// retain a stored value or select their bypass default.
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

// unionDuration returns elapsed coverage of intervals clipped to the supplied
// request window. Empty input, a nonpositive window, and intervals with no
// positive clipped span contribute zero. Input storage stays unchanged
// because normalized intervals are copied before sorting.
//
// Overlapping or touching spans are merged before their lengths are added.
// For example, ten milliseconds overlapping another ten by five contribute
// fifteen together, so simultaneous dependencies are not charged twice
// against application elapsed time.
func unionDuration(intervals []interval, lowerBound, upperBound time.Time) time.Duration {
	if len(intervals) == 0 || !upperBound.After(lowerBound) {
		return 0
	}
	normalized := make([]interval, 0, len(intervals))
	for _, item := range intervals {
		start := item.start
		end := item.end
		// Keep only positive spans inside this request's window so dependency
		// observations outside it cannot increase blocked time beyond total elapsed time.
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
		// Merge overlapping and touching spans before adding their duration. This
		// counts simultaneous work once while each metric's operation count remains additive.
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

// formatDuration renders a duration in milliseconds with one decimal place.
// Negative values become zero, providing nonnegative header durations while
// floating-point conversion and formatting round fractional milliseconds.
func formatDuration(value time.Duration) string {
	if value < 0 {
		value = 0
	}
	// Convert nanoseconds to decimal milliseconds for the Server-Timing wire value.
	return strconv.FormatFloat(float64(value)/float64(time.Millisecond), 'f', 1, 64)
}
