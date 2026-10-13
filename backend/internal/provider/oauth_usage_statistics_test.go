package provider

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestWindowPairFailurePreservesPartialStats injects combined failure followed
// by one successful single read. It checks retained first-window totals and
// absent second totals, then canceled combined work skipping all single-read
// retries.
func TestWindowPairFailurePreservesPartialStats(t *testing.T) {
	reader := &windowPairReader{failPair: true}
	stats := NewLocalUsageStatistics(reader, nil, LocalUsageStatisticsOptions{})
	a, b := stats.GetWindowPair(context.Background(), 1, time.Now().Add(-time.Hour), time.Now())
	require.EqualValues(t, 1, a.Requests)
	require.Nil(t, b)
	require.Equal(t, 2, reader.singles)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader = &windowPairReader{cancel: cancel}
	stats = NewLocalUsageStatistics(reader, nil, LocalUsageStatisticsOptions{})
	a, b = stats.GetWindowPair(ctx, 1, time.Now(), time.Now())
	require.Nil(t, a)
	require.Nil(t, b)
	require.Zero(t, reader.singles)
}
