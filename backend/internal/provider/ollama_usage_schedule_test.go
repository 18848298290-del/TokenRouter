package provider

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestOllamaCloudUsageIsAutoRefreshDue supplies missing, successful, and failed
// snapshots with several activity timestamps. It checks new activity, debounce,
// maximum wait, and failure backoff jointly determine eligibility, while
// missing reference data requests a fetch.
func TestOllamaCloudUsageIsAutoRefreshDue(t *testing.T) {
	debounce := time.Minute
	maxWait := time.Hour
	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	fetched := now.Add(-30 * time.Minute)
	ptr := func(ts time.Time) *time.Time { return &ts }

	require.True(t, OllamaCloudUsageIsAutoRefreshDue(nil, nil, now, debounce, maxWait), "missing snapshot first due")
	require.True(t, OllamaCloudUsageIsAutoRefreshDue(&OllamaCloudUsageSnapshot{Status: "bogus"}, nil, now, debounce, maxWait), "invalid status first due")

	okSnap := &OllamaCloudUsageSnapshot{
		Status: OllamaCloudUsageStatusOK, FetchedAt: ptr(fetched),
		LastAttemptAt: fetched, NextRefreshAt: fetched.Add(maxWait),
	}
	require.False(t, OllamaCloudUsageIsAutoRefreshDue(okSnap, nil, now, debounce, maxWait), "no request after success")
	require.False(t, OllamaCloudUsageIsAutoRefreshDue(okSnap, ptr(fetched), now, debounce, maxWait), "request not after fetched_at")
	require.False(t, OllamaCloudUsageIsAutoRefreshDue(okSnap, ptr(now.Add(-30*time.Second)), now, debounce, maxWait), "debounce not elapsed")
	require.True(t, OllamaCloudUsageIsAutoRefreshDue(okSnap, ptr(now.Add(-time.Minute)), now, debounce, maxWait), "single request quiet for debounce")

	// Continuous requests still become due when the maximum wait since the
	// last fetch expires.
	oldFetched := now.Add(-2 * time.Hour)
	oldSnap := &OllamaCloudUsageSnapshot{
		Status: OllamaCloudUsageStatusOK, FetchedAt: ptr(oldFetched),
		LastAttemptAt: oldFetched, NextRefreshAt: oldFetched.Add(maxWait),
	}
	require.True(t, OllamaCloudUsageIsAutoRefreshDue(oldSnap, ptr(now), now, debounce, maxWait), "max-wait forces due while requests continue")
	// The first request after a stale snapshot is immediately due once
	// fetchedAt+maxWait has passed.
	require.True(t, OllamaCloudUsageIsAutoRefreshDue(oldSnap, ptr(now.Add(-time.Second)), now, debounce, maxWait), "stale snapshot first request immediate")

	failSnap := &OllamaCloudUsageSnapshot{
		Status: OllamaCloudUsageStatusFailed, FetchedAt: ptr(fetched),
		LastAttemptAt: now.Add(-10 * time.Minute), NextRefreshAt: now.Add(20 * time.Minute),
	}
	require.False(t, OllamaCloudUsageIsAutoRefreshDue(failSnap, nil, now, debounce, maxWait), "failure without new request")
	require.False(t, OllamaCloudUsageIsAutoRefreshDue(failSnap, ptr(now.Add(-time.Minute)), now, debounce, maxWait), "failure blocked by backoff")
	failSnap.NextRefreshAt = now.Add(-time.Second)
	require.True(t, OllamaCloudUsageIsAutoRefreshDue(failSnap, ptr(now.Add(-time.Minute)), now, debounce, maxWait), "failure after backoff with new request")

	require.True(t, OllamaCloudUsageIsAutoRefreshDue(&OllamaCloudUsageSnapshot{
		Status: OllamaCloudUsageStatusOK, LastAttemptAt: now,
	}, nil, now, debounce, maxWait), "ok without fetched_at fails open")
}

// TestOllamaCloudUsageAutoRefreshDueAtHonoursMinFetchInterval places successful
// snapshots before and after the fifteen-minute spacing floor. It checks
// debounce cannot bypass that floor and stale data still becomes due after
// maximum wait when activity exists.
func TestOllamaCloudUsageAutoRefreshDueAtHonoursMinFetchInterval(t *testing.T) {
	debounce := time.Minute
	maxWait := time.Hour
	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	ptr := func(ts time.Time) *time.Time { return &ts }

	// Debounce has elapsed, but the last successful fetch remains within
	// the minimum interval.
	recent := now.Add(-5 * time.Minute)
	recentSnap := &OllamaCloudUsageSnapshot{
		Status: OllamaCloudUsageStatusOK, FetchedAt: ptr(recent), LastAttemptAt: recent,
	}
	dueAt, ok := OllamaCloudUsageAutoRefreshDueAt(recentSnap, ptr(now.Add(-2*time.Minute)), debounce, maxWait)
	require.True(t, ok)
	require.Equal(t, recent.Add(OllamaCloudUsageMinFetchInterval), dueAt,
		"due time must be clamped to fetched_at + min fetch interval")
	require.False(t, OllamaCloudUsageIsAutoRefreshDue(recentSnap, ptr(now.Add(-2*time.Minute)), now, debounce, maxWait),
		"debounce alone must not refresh within the min fetch interval")

	// After the minimum interval, a quiet debounce window can trigger
	// refresh.
	atFloor := now.Add(-OllamaCloudUsageMinFetchInterval)
	floorSnap := &OllamaCloudUsageSnapshot{
		Status: OllamaCloudUsageStatusOK, FetchedAt: ptr(atFloor), LastAttemptAt: atFloor,
	}
	require.True(t, OllamaCloudUsageIsAutoRefreshDue(floorSnap, ptr(now.Add(-2*time.Minute)), now, debounce, maxWait),
		"past the floor a quiet debounce window is due")

	// A maximum wait longer than the minimum interval still triggers
	// refresh for stale data.
	stale := now.Add(-2 * time.Hour)
	staleSnap := &OllamaCloudUsageSnapshot{
		Status: OllamaCloudUsageStatusOK, FetchedAt: ptr(stale), LastAttemptAt: stale,
	}
	require.True(t, OllamaCloudUsageIsAutoRefreshDue(staleSnap, ptr(now), now, debounce, maxWait),
		"max-wait still forces due on a stale snapshot")
}
