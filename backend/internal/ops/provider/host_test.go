package provider

import (
	"testing"

	"github.com/shirou/gopsutil/v4/mem"
	"github.com/stretchr/testify/require"
)

// testMiB and testGiB construct binary memory quantities for source-selection
// fixtures, matching the sampler integer conversion used in returned amount
// fields.
const (
	testMiB = 1024 * 1024    // bytes per binary MiB
	testGiB = 1024 * testMiB // bytes per binary GiB
)

// TestResolveMemoryStatsCgroupUsageButUnlimitedFallsBackToHost supplies
// container usage with zero capacity and a complete host sample. It checks all
// returned values use the host basis and rejects the diluted
// container-used/host-total result.
func TestResolveMemoryStatsCgroupUsageButUnlimitedFallsBackToHost(t *testing.T) {
	// The nonzero container usage is deliberately smaller than the host fixture,
	// so a mixed-source amount would fail the returned-value assertion.
	const cgroupUsed = uint64(64573440) // memory.current, approximately 61 MiB.
	host := &mem.VirtualMemoryStat{
		Used:        16 * testGiB,
		Total:       24 * testGiB,
		UsedPercent: 66.7,
	}

	usedMB, totalMB, pct := resolveMemoryStats(cgroupUsed, 0 /* memory.max = "max" */, true, host)

	require.NotNil(t, usedMB)
	require.NotNil(t, totalMB)
	require.NotNil(t, pct)

	require.Equal(t, int64(16*1024), *usedMB, "used must be host used, not container used")
	require.Equal(t, int64(24*1024), *totalMB, "total must be host total")
	require.InDelta(t, 66.7, *pct, 0.05, "percent must be host-derived, not container/host mix")

	// The small container counter divided by host capacity would produce about 0.3
	// percent. The lower-bound assertion protects against that mixed-denominator
	// result.
	require.NotEqual(t, int64(cgroupUsed/testMiB), *usedMB, "must not report the container used value")
	require.Greater(t, *pct, 1.0, "percent must not collapse to the ~0.3%% mixed value")
}

// TestResolveMemoryStatsExplicitContainerLimitUsesCgroup supplies 512 MiB
// container usage with a 2 GiB limit and differing host values. It checks the
// container amounts and twenty-five-percent ratio win together.
func TestResolveMemoryStatsExplicitContainerLimitUsesCgroup(t *testing.T) {
	host := &mem.VirtualMemoryStat{
		Used:        16 * testGiB, // contrasting host memory usage
		Total:       24 * testGiB,
		UsedPercent: 66.7,
	}

	usedMB, totalMB, pct := resolveMemoryStats(512*testMiB, 2*testGiB, true, host)

	require.NotNil(t, usedMB)
	require.NotNil(t, totalMB)
	require.NotNil(t, pct)

	require.Equal(t, int64(512), *usedMB)
	require.Equal(t, int64(2048), *totalMB)
	require.InDelta(t, 25.0, *pct, 0.05)
}

// TestResolveMemoryStatsNoCgroupUsesHost supplies no readable container source
// and a complete host sample. It checks used MiB, total MiB, and the
// host-derived percentage are returned together.
func TestResolveMemoryStatsNoCgroupUsesHost(t *testing.T) {
	host := &mem.VirtualMemoryStat{
		Used:        16 * testGiB,
		Total:       24 * testGiB,
		UsedPercent: 66.7,
	}

	usedMB, totalMB, pct := resolveMemoryStats(0, 0, false, host)

	require.NotNil(t, usedMB)
	require.NotNil(t, totalMB)
	require.NotNil(t, pct)
	require.Equal(t, int64(16*1024), *usedMB)
	require.Equal(t, int64(24*1024), *totalMB)
	require.InDelta(t, 66.7, *pct, 0.05)
}

// TestResolveMemoryStatsNoDataReturnsNil supplies unavailable container data
// and no host sample. It checks all three optional measurements remain absent
// rather than presenting unobserved memory as zero.
func TestResolveMemoryStatsNoDataReturnsNil(t *testing.T) {
	usedMB, totalMB, pct := resolveMemoryStats(0, 0, false, nil)
	require.Nil(t, usedMB)
	require.Nil(t, totalMB)
	require.Nil(t, pct)
}

// TestResolveMemoryStatsHostWithoutTotalKeepsGopsutilPercent supplies host
// usage and percentage with zero total capacity. It checks usage and the
// library percentage remain present while total capacity is nil.
func TestResolveMemoryStatsHostWithoutTotalKeepsGopsutilPercent(t *testing.T) {
	host := &mem.VirtualMemoryStat{
		Used:        8 * testGiB,
		Total:       0,
		UsedPercent: 42.5,
	}

	usedMB, totalMB, pct := resolveMemoryStats(0, 0, false, host)

	require.NotNil(t, usedMB)
	require.Nil(t, totalMB)
	require.NotNil(t, pct)
	require.Equal(t, int64(8*1024), *usedMB)
	require.InDelta(t, 42.5, *pct, 0.05)
}
