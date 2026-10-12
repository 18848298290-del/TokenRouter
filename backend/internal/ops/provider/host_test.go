package provider

import (
	"testing"

	"github.com/shirou/gopsutil/v4/mem"
	"github.com/stretchr/testify/require"
)

const (
	testMiB = 1024 * 1024
	testGiB = 1024 * testMiB
)

// TestResolveMemoryStatsCgroupUsageButUnlimitedFallsBackToHost checks that memory.max="max"
// selects host usage, capacity, and percentage together.
func TestResolveMemoryStatsCgroupUsageButUnlimitedFallsBackToHost(t *testing.T) {
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

	// All three measurements come from the host.
	require.Equal(t, int64(16*1024), *usedMB, "used must be host used, not container used")
	require.Equal(t, int64(24*1024), *totalMB, "total must be host total")
	require.InDelta(t, 66.7, *pct, 0.05, "percent must be host-derived, not container/host mix")

	// Mixing container usage with host capacity would incorrectly report about 0.3%.
	require.NotEqual(t, int64(cgroupUsed/testMiB), *usedMB, "must not report the container used value")
	require.Greater(t, *pct, 1.0, "percent must not collapse to the ~0.3%% mixed value")
}

// TestResolveMemoryStatsExplicitContainerLimitUsesCgroup checks selection of a finite container limit.
// 512 MiB of usage with a 2 GiB limit yields 25%.
func TestResolveMemoryStatsExplicitContainerLimitUsesCgroup(t *testing.T) {
	host := &mem.VirtualMemoryStat{
		Used:        16 * testGiB, // Host usage differs from container usage.
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

// TestResolveMemoryStatsNoCgroupUsesHost checks host measurements when cgroup data is unavailable.
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

// TestResolveMemoryStatsNoDataReturnsNil checks missing measurements from both sources.
func TestResolveMemoryStatsNoDataReturnsNil(t *testing.T) {
	usedMB, totalMB, pct := resolveMemoryStats(0, 0, false, nil)
	require.Nil(t, usedMB)
	require.Nil(t, totalMB)
	require.Nil(t, pct)
}

// TestResolveMemoryStatsHostWithoutTotalKeepsGopsutilPercent checks that missing host capacity
// still permits returning usage and gopsutil's percentage.
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
