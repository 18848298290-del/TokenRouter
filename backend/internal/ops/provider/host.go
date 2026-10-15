package provider

import (
	"context"
	"database/sql"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// host.go samples host, container, and connection-pool health for Ops metric
// collection.
// Host and container resources can describe different denominators, so memory
// usage and capacity are selected together. Readable cgroup usage is useful
// only with a positive capacity; otherwise the sampler uses the host memory
// triple. CPU requires two cumulative counter samples and a positive quota
// before it can report a container-relative percentage.
//
// The CPU baseline belongs to one observer and advances on skipped readable
// intervals; callers serialize sampling because its fields have no lock.
// Missing container CPU measurements use the host library, while disk always
// samples the process root. Resource failures leave optional values absent and
// pool probes return health booleans, so a successful collection result is
// best-effort evidence rather than proof that every source was read.

// bytesPerMB converts byte counters to binary MiB for the monitoring fields
// named MB. Integer division discards fractional MiB.
const bytesPerMB = 1024 * 1024

// HostObserver reads process resources and borrowed pools while retaining a
// cgroup CPU baseline.
type HostObserver struct {
	// db is retained for pool statistics and SELECT 1 probes. The caller owns its
	// lifetime and closes the pool after observations have stopped.
	db *sql.DB // borrowed database connection pool
	// redisClient is retained for pool snapshots and PING probes. The caller keeps
	// the client usable during collection; HostObserver does not close it.
	redisClient *redis.Client // borrowed Redis connection client
	// lastCgroupCPUUsageNanos and lastCgroupCPUSampleAt advance together after a
	// readable counter sample. They have no internal lock, so callers serialize
	// TryCgroupCPUPercent and CollectSystemStats for one observer.
	lastCgroupCPUUsageNanos uint64    // prior accepted CPU usage counter
	lastCgroupCPUSampleAt   time.Time // prior CPU baseline sample time
}

// NewHostObserver constructs a sampler retaining the supplied database and
// Redis clients without launching work. Nil clients are accepted and their
// probe/pool methods report unavailable results; callers own resource closing
// and serialize CPU-baseline sampling.
func NewHostObserver(db *sql.DB, r *redis.Client) *HostObserver {
	return &HostObserver{db: db, redisClient: r}
}

// DbPoolStats returns the borrowed database pool InUse and Idle counts from one
// statistics read. Nil observer or absent pool returns two zeros, so those
// values alone do not establish pool availability.
func (c *HostObserver) DbPoolStats() (active int, idle int) {
	if c == nil || c.db == nil {
		return 0, 0
	}
	stats := c.db.Stats()
	return stats.InUse, stats.Idle
}

// RedisPoolStats returns total and idle connection counts when the borrowed
// client supplies a statistics snapshot. Nil observer, absent client, or nil
// snapshot returns ok false; this method does not probe Redis connectivity.
func (c *HostObserver) RedisPoolStats() (total int, idle int, ok bool) {
	if c == nil || c.redisClient == nil {
		return 0, 0, false
	}
	stats := c.redisClient.PoolStats()
	if stats == nil {
		return 0, 0, false
	}
	return int(stats.TotalConns), int(stats.IdleConns), true
}

// CheckRedis reports whether PING succeeds for the borrowed Redis client.
// Missing observer/client returns false, nil context uses Background, and any
// command error, including cancellation, becomes the same false diagnostic
// result.
func (c *HostObserver) CheckRedis(ctx context.Context) bool {
	if c == nil || c.redisClient == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// The collector stores Redis health as a bool. A PING error marks it
	// unavailable here without returning command details to the sampling caller.
	return c.redisClient.Ping(ctx).Err() == nil
}

// CheckDB reports whether SELECT 1 succeeds and scans the expected value from
// the borrowed pool. Missing observer/pool returns false, nil context uses
// Background, and query/scan failures become false.
func (c *HostObserver) CheckDB(ctx context.Context) bool {
	if c == nil || c.db == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var one int
	// The collector consumes a health bool, so query and scan failures share the
	// unavailable result while detailed database diagnostics remain outside this
	// probe.
	if err := c.db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		return false
	}
	return one == 1
}

// readIntFile returns a signed base-ten integer from trimmed file text, with ok
// distinguishing accepted zero from failure. File errors, empty text, malformed
// values, and signed-range overflow return ok false.
func readIntFile(path string) (int64, bool) {
	// Cgroup source selection treats this reader failure as unavailable input,
	// allowing its caller to try another hierarchy or resource basis.
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return 0, false
	}
	// Reject unparseable signed text as unavailable quota metadata; returning zero
	// with ok false keeps failure separate from an accepted zero value.
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// readUintFile returns an unsigned base-ten integer from trimmed file text,
// retaining accepted zero with ok true. File errors, empty or negative text,
// malformed values, and unsigned-range overflow return ok false.
func readUintFile(path string) (uint64, bool) {
	// Unsigned cgroup counters use ok to retain measured zero. A failed file read
	// instead signals unavailable input for the caller alternate-source decision.
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return 0, false
	}
	// Reject malformed unsigned text as absent counter evidence, keeping read
	// failures separate from a valid zero counter through the availability flag.
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// readCgroupCPULimitCores returns the quota/period ratio from accepted positive
// operands, or zero when no limit is found. A parseable positive v2 cpu.max
// pair wins; unlimited, unavailable, or invalid v2 input permits the v1 files
// to be tried.
//
// The v1 path requires positive signed quota and period values. Both formats
// use matching units within their ratio, so the result supplies core capacity
// for delta normalization. V2 floating-point parsing checks positivity but not
// finite values. TryCgroupCPUPercent treats a nonpositive result as unavailable
// capacity.
func readCgroupCPULimitCores() float64 {
	// A positive v2 quota/period pair supplies the normalization basis. Unlimited
	// or unparseable v2 content leaves the v1 read path available.
	if raw, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		fields := strings.Fields(string(raw))
		if len(fields) >= 2 && fields[0] != "max" {
			quota, err1 := strconv.ParseFloat(fields[0], 64)
			period, err2 := strconv.ParseFloat(fields[1], 64)
			if err1 == nil && err2 == nil && quota > 0 && period > 0 {
				return quota / period
			}
		}
	}

	// Try the v1 quota and period only after v2 yields no accepted limit; their
	// matching time units cancel in the core-capacity ratio.
	quota, okQuota := readIntFile("/sys/fs/cgroup/cpu/cpu.cfs_quota_us")
	period, okPeriod := readIntFile("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
	if okQuota && okPeriod && quota > 0 && period > 0 {
		return float64(quota) / float64(period)
	}

	return 0
}

// readCgroupCPUUsageNanos returns the cumulative CPU counter in nanoseconds
// with an availability flag. A v2 cpu.stat usage_usec line is parsed first and
// multiplied by one thousand; malformed lines are skipped while searching that
// file.
//
// When no accepted v2 counter is found, the v1 cpuacct.usage value is used
// directly as nanoseconds. File or parse failure leaves ok false, and accepted
// zero remains available. The unit multiplication uses uint64 arithmetic with
// no overflow check.
func readCgroupCPUUsageNanos() (usageNanos uint64, ok bool) {
	// The v2 counter uses microseconds, while the retained sampling baseline uses
	// nanoseconds. Ignore other stat lines and malformed usage text before trying
	// v1.
	if raw, err := os.ReadFile("/sys/fs/cgroup/cpu.stat"); err == nil {
		lines := strings.SplitSeq(string(raw), "\n")
		for line := range lines {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				continue
			}
			if fields[0] != "usage_usec" {
				continue
			}
			v, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				continue
			}
			// Convert the accepted microsecond counter to the baseline nanosecond unit.
			return v * 1000, true
		}
	}

	// With no accepted v2 counter, read the v1 nanosecond value directly. Its ok
	// flag retains a zero counter as readable.
	if v, ok := readUintFile("/sys/fs/cgroup/cpuacct/cpuacct.usage"); ok {
		return v, true
	}

	return 0, false
}

// readCgroupMemoryBytes returns usage and any accepted capacity in bytes, with
// ok describing whether usage was readable. Readable v2 memory.current wins
// even if memory.max is absent, unlimited, or invalid; those capacity cases
// leave total zero.
//
// If v2 usage is unavailable, the v1 usage counter is tried. Positive v1 limits
// below the large-value sentinel are retained, while other limits leave total
// zero. resolveMemoryStats later requires positive capacity before selecting
// the container measurements together.
func readCgroupMemoryBytes() (usedBytes uint64, totalBytes uint64, ok bool) {
	// Readable v2 usage selects this result even when its capacity cannot be
	// parsed. Zero total tells memory selection to use the host basis later.
	if used, ok1 := readUintFile("/sys/fs/cgroup/memory.current"); ok1 {
		usedBytes = used
		rawMax, err := os.ReadFile("/sys/fs/cgroup/memory.max")
		if err == nil {
			s := strings.TrimSpace(string(rawMax))
			if s != "" && s != "max" {
				if v, err := strconv.ParseUint(s, 10, 64); err == nil {
					totalBytes = v
				}
			}
		}
		return usedBytes, totalBytes, true
	}

	// Only an unavailable v2 usage counter reaches v1; retain usage availability
	// separately from whether its capacity can be trusted for normalization.
	if used, ok1 := readUintFile("/sys/fs/cgroup/memory/memory.usage_in_bytes"); ok1 {
		usedBytes = used
		if limit, ok2 := readUintFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); ok2 {
			// Treat limits at or above this sentinel as unavailable capacity, so
			// resolveMemoryStats does not dilute container usage with that extreme
			// denominator.
			if limit > 0 && limit < (1<<60) {
				totalBytes = limit
			}
		}
		return usedBytes, totalBytes, true
	}

	return 0, 0, false
}

// TryCgroupCPUPercent returns a rounded quota-relative CPU percentage after two
// usable baseline observations, or nil when the interval cannot be measured.
// Callers use a nonnil observer and serialize its baseline mutations. A failed
// counter read leaves the old baseline, while the first sample, nonpositive
// elapsed time, or decreasing counter advances the baseline and returns nil.
//
// After a positive interval, the baseline advances before capacity lookup.
// Missing positive capacity therefore discards that interval and lets
// CollectSystemStats try host CPU. Counter delta is divided by elapsed seconds
// and the current core quota. Finite results are clamped to 0-100 and rounded
// to one decimal; quota parsing has no finite-value validation.
func (c *HostObserver) TryCgroupCPUPercent(now time.Time) *float64 {
	usageNanos, ok := readCgroupCPUUsageNanos()
	if !ok {
		return nil
	}

	// The first readable counter has no elapsed comparison. Save both baseline
	// values together so a later serialized call can measure their interval.
	if c.lastCgroupCPUSampleAt.IsZero() {
		c.lastCgroupCPUUsageNanos = usageNanos
		c.lastCgroupCPUSampleAt = now
		return nil
	}

	elapsed := now.Sub(c.lastCgroupCPUSampleAt)
	// A nonadvancing caller clock cannot define an interval. Replace both baseline
	// values so the next call starts from this observation.
	if elapsed <= 0 {
		c.lastCgroupCPUUsageNanos = usageNanos
		c.lastCgroupCPUSampleAt = now
		return nil
	}

	// Advance the retained pair before validating the delta or quota, so a skipped
	// interval does not get accumulated into a later successful sample.
	prev := c.lastCgroupCPUUsageNanos
	c.lastCgroupCPUUsageNanos = usageNanos
	c.lastCgroupCPUSampleAt = now

	if usageNanos < prev {
		// A decreasing counter can represent a reset. The new baseline is already
		// installed, so omit this delta and resume from it next time.
		return nil
	}

	// Convert cumulative nanoseconds to seconds for the elapsed-time and
	// core-quota calculation.
	deltaUsageSec := float64(usageNanos-prev) / 1e9
	elapsedSec := elapsed.Seconds()
	if elapsedSec <= 0 {
		return nil
	}

	cores := readCgroupCPULimitCores()
	if cores <= 0 {
		// A positive core quota is required for a container-relative percentage.
		// Return nil after baseline advancement so CollectSystemStats can use its
		// host CPU path.
		return nil
	}

	// Normalize this interval using the quota read now; quota changes during the
	// interval are not reconstructed from earlier configuration.
	pct := (deltaUsageSec / (elapsedSec * cores)) * 100
	if pct < 0 {
		pct = 0
	}
	// Cap percentages above one hundred for the display range. NaN bypasses this
	// comparison and remains supplied to the rounding helper.
	if pct > 100 {
		pct = 100
	}
	v := ops.RoundTo1DP(pct)
	return &v
}

// resolveMemoryStats returns used/total MiB and percentage from one selected
// memory basis. A readable cgroup usage with positive capacity supplies all
// three values, including zero usage. Missing container capacity selects the
// host object for every returned memory measurement, preventing container usage
// from being divided by host capacity.
//
// A nil host then returns three nils. Positive host total yields derived
// capacity and percentage; zero total leaves capacity nil and retains host
// UsedPercent. Byte amounts are truncated to whole MiB and percentages are
// rounded to one decimal.
func resolveMemoryStats(cgroupUsed, cgroupTotal uint64, cgroupOK bool, host *mem.VirtualMemoryStat) (usedMB *int64, totalMB *int64, usagePercent *float64) {
	if cgroupOK && cgroupTotal > 0 {
		u := int64(cgroupUsed / bytesPerMB)
		t := int64(cgroupTotal / bytesPerMB)
		p := ops.RoundTo1DP(float64(cgroupUsed) / float64(cgroupTotal) * 100)
		// Select container usage and capacity together; a positive container limit
		// makes its ratio a different basis from host memory.
		return &u, &t, &p
	}

	if host == nil {
		return nil, nil, nil
	}

	u := int64(host.Used / bytesPerMB)
	usedMB = &u
	if host.Total > 0 {
		t := int64(host.Total / bytesPerMB)
		totalMB = &t
		p := ops.RoundTo1DP(float64(host.Used) / float64(host.Total) * 100)
		usagePercent = &p
	} else {
		// Host usage can still be reported when total capacity is absent. Keep the
		// library percentage, while nil total distinguishes that missing capacity
		// from measured zero.
		p := ops.RoundTo1DP(host.UsedPercent)
		usagePercent = &p
	}
	return usedMB, totalMB, usagePercent
}

// CollectSystemStats returns a nonnil best-effort resource sample with nil
// error, using Background for a nil context. Callers serialize it with other
// baseline sampling on the same observer. CPU first tries the cgroup delta,
// then nonblocking host sampling; memory selects a consistent container or host
// triple, and disk samples the process root path.
//
// Each resource can use a different source and read time. Probe errors leave
// the corresponding optional fields absent and are suppressed, including errors
// caused by context cancellation. Cgroup file reads do not use the context.
// Amounts use whole MiB and percentages use one-decimal rounding; this method
// collects no pool or goroutine fields.
func (c *HostObserver) CollectSystemStats(ctx context.Context) (*ops.CollectedSystemStats, error) {
	out := &ops.CollectedSystemStats{}
	if ctx == nil {
		ctx = context.Background()
	}

	sampleAt := time.Now().UTC()

	// A cgroup delta can be absent on its first read, after a reset, or with no
	// quota. Host CPU supplies the alternative sample for this collection.
	if cpuPct := c.TryCgroupCPUPercent(sampleAt); cpuPct != nil {
		out.CpuUsagePercent = cpuPct
	}
	if out.CpuUsagePercent == nil {
		// Host CPU errors or empty samples leave CPU absent while this nonnil
		// collection continues with memory and root-disk observations.
		if cpuPercents, err := cpu.PercentWithContext(ctx, 0, false); err == nil && len(cpuPercents) > 0 {
			v := ops.RoundTo1DP(cpuPercents[0])
			out.CpuUsagePercent = &v
		}
	}

	// Missing cgroup usage or capacity selects host memory for the full triple.
	// Suppress host-read errors as absence so other resource observations can
	// still be returned.
	cgroupUsed, cgroupTotal, cgroupOK := readCgroupMemoryBytes()
	var host *mem.VirtualMemoryStat
	if !cgroupOK || cgroupTotal == 0 {
		if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
			host = vm
		}
	}
	out.MemoryUsedMB, out.MemoryTotalMB, out.MemoryUsagePercent = resolveMemoryStats(cgroupUsed, cgroupTotal, cgroupOK, host)

	// The disk observation describes the process root filesystem independently of
	// memory source selection. A failed root-usage read leaves only disk fields
	// absent.
	if usage, err := disk.UsageWithContext(ctx, "/"); err == nil && usage != nil {
		// Disk byte counters are truncated to whole binary MiB for the monitoring
		// amount fields.
		usedMB := int64(usage.Used / bytesPerMB)
		totalMB := int64(usage.Total / bytesPerMB)
		out.DiskUsedMB = &usedMB
		out.DiskTotalMB = &totalMB
		if usage.Total > 0 {
			pct := ops.RoundTo1DP(usage.UsedPercent)
			out.DiskUsagePercent = &pct
		}
	}

	return out, nil
}

// GoroutineCount returns the current process count reported by the Go runtime.
func (c *HostObserver) GoroutineCount() int { return runtime.NumGoroutine() }
