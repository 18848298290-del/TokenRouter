package provider

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	providerusageview "github.com/TokenFlux/TokenRouter/internal/provider/usageview"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// grokQuotaUsageLogRepo records serial window requests and returns configured
// borrowed totals.
type grokQuotaUsageLogRepo struct {
	// stats is returned as the configured borrowed totals observation. Keep its
	// fields stable until the production helper finishes copying that result.
	stats      *WindowStats // borrowed configured window totals
	err        error        // configured window read failure
	calls      int          // serial window query count
	startTimes []time.Time  // captured window query origins
}

// GetProviderWindowStats increments and records the serial request before
// returning configured totals/error. The totals pointer stays borrowed; tests
// keep fixture state stable until the production helper makes its copy.
func (r *grokQuotaUsageLogRepo) GetProviderWindowStats(_ context.Context, _ int64, start time.Time) (*WindowStats, error) {
	r.calls++
	r.startTimes = append(r.startTimes, start)
	return r.stats, r.err
}

// GetProviderTodayStats returns nil successful totals for this window-only
// fixture.
func (r *grokQuotaUsageLogRepo) GetProviderTodayStats(context.Context, int64) (*WindowStats, error) {
	return nil, nil
}

// TestGrokLocalUsage24hUsesRollingUTCWindow supplies a non-UTC clock and
// configured read outcomes. It checks exact rolling UTC start, returned token
// count, failed/missing-reader absence, and invalid-row query suppression.
func TestGrokLocalUsage24hUsesRollingUTCWindow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 14, 20, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))

	t.Run("returns usage from exact rolling window", func(t *testing.T) {
		repo := &grokQuotaUsageLogRepo{stats: &WindowStats{Tokens: 1_250_000}}
		stats := GrokLocalUsage24h(context.Background(), repo, 57, now, slog.Warn)

		require.NotNil(t, stats)
		require.EqualValues(t, 1_250_000, stats.Tokens)
		require.Equal(t, []time.Time{now.UTC().Add(-24 * time.Hour)}, repo.startTimes)
	})

	t.Run("query failure returns no stats", func(t *testing.T) {
		repo := &grokQuotaUsageLogRepo{err: context.DeadlineExceeded}
		stats := GrokLocalUsage24h(context.Background(), repo, 57, now, slog.Warn)

		require.Nil(t, stats)
		require.Equal(t, []time.Time{now.UTC().Add(-24 * time.Hour)}, repo.startTimes)
	})

	t.Run("missing repository returns no stats", func(t *testing.T) {
		require.Nil(t, GrokLocalUsage24h(context.Background(), nil, 57, now, slog.Warn))
	})

	t.Run("invalid provider returns no stats without query", func(t *testing.T) {
		repo := &grokQuotaUsageLogRepo{}
		require.Nil(t, GrokLocalUsage24h(context.Background(), repo, 0, now, slog.Warn))
		require.Zero(t, repo.calls)
	})
}

// TestGrokLocalUsageForQuotaSelectsFreeOrPaidWindows varies billing authority
// while retaining known date ranges. It checks exclusive rolling or
// weekly/monthly results and the exact ordered query starts for those
// configured summaries.
func TestGrokLocalUsageForQuotaSelectsFreeOrPaidWindows(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	billing := &usageview.BillingSummary{
		PeriodType:         "weekly",
		PeriodStart:        now.Add(-4 * 24 * time.Hour).Format(time.RFC3339),
		PeriodEnd:          now.Add(3 * 24 * time.Hour).Format(time.RFC3339),
		BillingPeriodStart: now.Add(-13 * 24 * time.Hour).Format(time.RFC3339),
		BillingPeriodEnd:   now.Add(17 * 24 * time.Hour).Format(time.RFC3339),
	}

	t.Run("free queries only rolling 24h", func(t *testing.T) {
		repo := &grokQuotaUsageLogRepo{stats: &WindowStats{Tokens: 500_000}}
		rolling, weekly, monthly := GrokLocalUsageForQuota(context.Background(), repo, 57, billing, now, slog.Warn)

		require.NotNil(t, rolling)
		require.Nil(t, weekly)
		require.Nil(t, monthly)
		require.Equal(t, []time.Time{now.Add(-24 * time.Hour)}, repo.startTimes)
	})

	t.Run("paid queries only billing windows", func(t *testing.T) {
		usagePercent := 25.0
		paidBilling := *billing
		paidBilling.UsagePercent = &usagePercent
		repo := &grokQuotaUsageLogRepo{stats: &WindowStats{Tokens: 500_000}}
		rolling, weekly, monthly := GrokLocalUsageForQuota(context.Background(), repo, 57, &paidBilling, now, slog.Warn)

		require.Nil(t, rolling)
		require.NotNil(t, weekly)
		require.NotNil(t, monthly)
		require.Equal(t, []time.Time{
			now.Add(-4 * 24 * time.Hour),
			now.Add(-13 * 24 * time.Hour),
		}, repo.startTimes)
	})
}

// TestGrokLocalUsageForBillingOnlyReturnsAvailableWindows provides a weekly
// range, a read error, or missing billing data. It checks partial
// observations, returned token totals, and query counts for those availability
// cases.
func TestGrokLocalUsageForBillingOnlyReturnsAvailableWindows(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	billing := &usageview.BillingSummary{
		PeriodType:  "weekly",
		PeriodStart: now.Add(-4 * 24 * time.Hour).Format(time.RFC3339),
		PeriodEnd:   now.Add(3 * 24 * time.Hour).Format(time.RFC3339),
	}

	t.Run("valid weekly window", func(t *testing.T) {
		repo := &grokQuotaUsageLogRepo{stats: &WindowStats{Tokens: 1_500_000}}
		weekly, monthly := GrokLocalUsageForBilling(context.Background(), repo, 57, billing, now, slog.Warn)
		require.NotNil(t, weekly)
		require.EqualValues(t, 1_500_000, weekly.Tokens)
		require.Nil(t, monthly)
		require.Equal(t, 1, repo.calls)
	})

	t.Run("query failure", func(t *testing.T) {
		repo := &grokQuotaUsageLogRepo{err: context.DeadlineExceeded}
		weekly, monthly := GrokLocalUsageForBilling(context.Background(), repo, 57, billing, now, slog.Warn)
		require.Nil(t, weekly)
		require.Nil(t, monthly)
		require.Equal(t, 1, repo.calls)
	})

	t.Run("missing billing window", func(t *testing.T) {
		repo := &grokQuotaUsageLogRepo{}
		weekly, monthly := GrokLocalUsageForBilling(context.Background(), repo, 57, nil, now, slog.Warn)
		require.Nil(t, weekly)
		require.Nil(t, monthly)
		require.Zero(t, repo.calls)
	})
}

// TestGrokUsageOwnsProviderAndProbeResults reuses shared billing/display/probe
// objects across forced queries. It mutates returned weekly totals and plan,
// then checks retained source values and independent later results for those
// fields.
func TestGrokUsageOwnsProviderAndProbeResults(t *testing.T) {
	billing := &providerusageview.BillingSummary{Plan: "SuperGrok"}
	shared := &UsageInfo{SevenDay: &UsageProgress{}, GrokBilling: billing}
	probe := &GrokUsageProbe{Billing: billing, LocalUsage7d: &WindowStats{Requests: 12}}
	core := NewOAuthUsageService(nil, nil, nil, OAuthUsageOptions{Grok: GrokUsageOptions{
		Available:      func() bool { return true },
		StatsAvailable: func() bool { return false },
		Probe:          func(context.Context, int64) (*GrokUsageProbe, error) { return probe, nil },
		Build:          func(*Record) *UsageInfo { return shared },
		Enrich:         func(*UsageInfo, *Record) {},
	}})
	record := &Record{
		ID:          5,
		Platform:    PlatformGrok,
		Type:        ProviderTypeOAuth,
		Credentials: map[string]any{"refresh_token": "fixture", "access_token": "fixture", "auth_mode": "oauth"},
	}
	require.True(t, record.IsGrokOAuth())
	first, err := core.GetGrokUsage(context.Background(), record, true)
	require.NoError(t, err)
	require.NotNil(t, first.GrokLocalUsage7d)
	first.GrokLocalUsage7d.Requests = 999
	first.GrokBilling.Plan = "caller"
	require.Nil(t, shared.SevenDay.WindowStats)
	require.Empty(t, shared.GrokQuotaSnapshotState)
	require.Equal(t, int64(12), probe.LocalUsage7d.Requests)
	require.Equal(t, "SuperGrok", billing.Plan)
	second, err := core.GetGrokUsage(context.Background(), record, true)
	require.NoError(t, err)
	require.Equal(t, int64(12), second.GrokLocalUsage7d.Requests)
	require.Equal(t, "SuperGrok", second.GrokBilling.Plan)
}
