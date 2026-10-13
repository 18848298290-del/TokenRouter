package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// cnHealthFailureStore records a failed temporary-pause write after runtime
// notification.
type cnHealthFailureStore struct {
	HealthStore // unused health store operations
	// order points to the event slice also appended by the block callback
	// during this serial test.
	order *[]string
}

// SetTempUnschedulable appends a store event and returns the fixture error. The
// order pointer is shared with the block callback, allowing the test to compare
// both operations in one sequence.
func (s cnHealthFailureStore) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	*s.order = append(*s.order, "store")
	return errors.New("fixture persistence failed")
}

// TestCNConcurrencyRepositoryFailureKeepsRuntimeBlock makes temporary-pause
// persistence fail after a Kimi concurrency response. It asserts the block
// callback precedes storage and receives the same record with the configured
// deadline and runtime reason.
func TestCNConcurrencyRepositoryFailureKeepsRuntimeBlock(t *testing.T) {
	var order []string
	var blocked *Record
	now := time.Now()
	core := NewHealthService(cnHealthFailureStore{order: &order}, nil, HealthOptions{Now: func() time.Time { return now }, Block: func(v *Record, until time.Time, reason string) {
		order = append(order, "block")
		blocked = v
		require.Equal(t, now.Add(10*time.Minute), until)
		require.Equal(t, "cn_concurrency_limit", reason)
	}})
	original := &Record{ID: 406, Platform: PlatformKimi, Type: ProviderTypeAPIKey}
	core.ApplyCNConcurrencyLimit(context.Background(), original, "fixture concurrency")
	require.Equal(t, []string{"block", "store"}, order)
	require.Same(t, original, blocked)
}

// TestCNProviderQuotaSnapshotReset supplies expired and future Coding Plan
// resets in identity-valid snapshots. It asserts the earliest future deadline
// wins, wholly expired windows return nil, and pay-as-you-go records ignore
// those windows.
func TestCNProviderQuotaSnapshotReset(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	future5h := now.Add(2 * time.Hour)
	futureWeekly := now.Add(3 * 24 * time.Hour)
	pastWeekly := now.Add(-24 * time.Hour)

	// The future 5h reset wins when the weekly reset has expired.
	used := 100.0
	provider := cnCodingTestProvider(capability.PlatformKimi)
	attachCNMonitorLimits(provider, now, []UpstreamUsageLimit{
		{Name: "5h", Used: &used, ResetAt: &future5h},
		{Name: "7d", Used: &used, ResetAt: &pastWeekly},
	})
	got := CNProviderQuotaSnapshotReset(provider, now)
	require.NotNil(t, got)
	require.True(t, future5h.Equal(*got))

	// When both resets are in the future, choose the earlier one.
	both := cnCodingTestProvider(capability.PlatformKimi)
	attachCNMonitorLimits(both, now, []UpstreamUsageLimit{
		{Name: "5h", Used: &used, ResetAt: &future5h},
		{Name: "7d", Used: &used, ResetAt: &futureWeekly},
	})
	gotBoth := CNProviderQuotaSnapshotReset(both, now)
	require.NotNil(t, gotBoth)
	require.True(t, future5h.Equal(*gotBoth))

	// Both resets have expired.
	expired := cnCodingTestProvider(capability.PlatformKimi)
	attachCNMonitorLimits(expired, now, []UpstreamUsageLimit{
		{Name: "5h", Used: &used, ResetAt: &pastWeekly},
		{Name: "7d", Used: &used, ResetAt: &pastWeekly},
	})
	require.Nil(t, CNProviderQuotaSnapshotReset(expired, now))

	// Pay-as-you-go providers use balance monitoring rather than Coding
	// Plan
	// windows.
	payg := cnCodingTestProvider(capability.PlatformKimi)
	payg.Credentials["provider_mode"] = ProviderModePayG
	require.Nil(t, CNProviderQuotaSnapshotReset(payg, now))
}
