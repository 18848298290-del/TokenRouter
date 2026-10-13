package provider

import (
	"log"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestBuildCodexUsageProgressFromExtra_ZerosExpiredWindow supplies explicit
// reset times around a fixed expiry reference. It checks expired
// percentages/countdown and retained active percentage, with countdown using
// the independent real clock callback.
func TestBuildCodexUsageProgressFromExtra_ZerosExpiredWindow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)

	t.Run("expired 5h window zeroes utilization", func(t *testing.T) {
		extra := map[string]any{
			"codex_5h_used_percent": 42.0,
			"codex_5h_reset_at":     "2026-03-16T10:00:00Z", // 2h ago
		}
		progress := BuildCodexUsageProgressFromExtra(extra, "5h", now, time.Now)
		if progress == nil {
			t.Fatal("expected non-nil progress")
			return
		}
		if progress.Utilization != 0 {
			t.Fatalf("expected Utilization=0 for expired window, got %v", progress.Utilization)
		}
		if progress.RemainingSeconds != 0 {
			t.Fatalf("expected RemainingSeconds=0, got %v", progress.RemainingSeconds)
		}
	})

	t.Run("active 5h window keeps utilization", func(t *testing.T) {
		resetAt := now.Add(2 * time.Hour).Format(time.RFC3339)
		extra := map[string]any{
			"codex_5h_used_percent": 42.0,
			"codex_5h_reset_at":     resetAt,
		}
		progress := BuildCodexUsageProgressFromExtra(extra, "5h", now, time.Now)
		if progress == nil {
			t.Fatal("expected non-nil progress")
			return
		}
		if progress.Utilization != 42.0 {
			t.Fatalf("expected Utilization=42, got %v", progress.Utilization)
		}
	})

	t.Run("expired 7d window zeroes utilization", func(t *testing.T) {
		extra := map[string]any{
			"codex_7d_used_percent": 88.0,
			"codex_7d_reset_at":     "2026-03-15T00:00:00Z", // yesterday
		}
		progress := BuildCodexUsageProgressFromExtra(extra, "7d", now, time.Now)
		if progress == nil {
			t.Fatal("expected non-nil progress")
			return
		}
		if progress.Utilization != 0 {
			t.Fatalf("expected Utilization=0 for expired 7d window, got %v", progress.Utilization)
		}
	})
}

// TestCodexWindowStatsStart supplies future, missing, nil, and expired reset
// observations. It checks future-reset alignment and the configured rolling
// fallback for each other case using a fixed reference time.
func TestCodexWindowStatsStart(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	window := 5 * time.Hour
	activeReset := now.Add(2 * time.Hour)

	tests := []struct {
		name string // window-start scenario name
		// progress is borrowed by the case and may be nil or omit reset timing.
		// The test keeps any referenced reset time stable during comparison.
		progress *UsageProgress
		want     time.Time // expected statistics start time
	}{
		{
			name:     "active reset window",
			progress: &UsageProgress{ResetsAt: &activeReset},
			want:     activeReset.Add(-window),
		},
		{
			name:     "missing reset falls back",
			progress: &UsageProgress{},
			want:     now.Add(-window),
		},
		{
			name:     "nil progress falls back",
			progress: nil,
			want:     now.Add(-window),
		},
	}

	expiredReset := now.Add(-time.Minute)
	tests = append(tests, struct {
		name string // window-start scenario name
		// progress is borrowed by the case and carries an expired reset timing.
		// The test keeps its referenced reset stable during start-time comparison.
		progress *UsageProgress
		want     time.Time // expected statistics start time
	}{
		name:     "expired reset falls back",
		progress: &UsageProgress{ResetsAt: &expiredReset},
		want:     now.Add(-window),
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CodexWindowStatsStart(tt.progress, window, now); !got.Equal(tt.want) {
				t.Fatalf("codexWindowStatsStart() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestBuildUsageInfo_SevenDayFable supplies a future overage-included reset
// and then an empty response. It checks Fable percentage, exact parsed reset,
// positive countdown, and absence when the reset observation is missing.
func TestBuildUsageInfo_SevenDayFable(t *testing.T) {
	now := time.Now()

	// Match the timestamp representation's whole seconds for exact reset equality.
	resetAt := now.Add(72 * time.Hour).UTC().Truncate(time.Second)
	var resp ClaudeUsageResponse
	resp.FiveHour.Utilization = 10
	resp.SevenDayOverageIncluded = ClaudeUsageWindow{
		Utilization: 88,
		ResetsAt:    resetAt.Format(time.RFC3339),
	}

	info := BuildUsageInfo(&resp, &now, time.Now, log.Printf)
	require.NotNil(t, info.SevenDayFable)
	require.Equal(t, 88.0, info.SevenDayFable.Utilization)
	require.NotNil(t, info.SevenDayFable.ResetsAt)
	require.True(t, info.SevenDayFable.ResetsAt.Equal(resetAt))
	require.Greater(t, info.SevenDayFable.RemainingSeconds, 0)

	// Missing Fable data leaves the window absent.
	var empty ClaudeUsageResponse
	empty.FiveHour.Utilization = 10
	info = BuildUsageInfo(&empty, &now, time.Now, log.Printf)
	require.Nil(t, info.SevenDayFable)
}

// TestBuildPassiveUsageWindow varies combined usage/reset observations,
// utilization-only data, missing data, and an expired reset. It checks
// converted percentages, reset identity, positive/zero countdown, and nil
// observations for the configured missing cases.
func TestBuildPassiveUsageWindow(t *testing.T) {
	future := time.Now().Add(48 * time.Hour).Unix()

	t.Run("utilization and reset", func(t *testing.T) {
		window := BuildPassiveUsageWindow(map[string]any{
			"passive_usage_7d_oi_utilization": 0.87,
			"passive_usage_7d_oi_reset":       float64(future),
		}, "passive_usage_7d_oi_utilization", "passive_usage_7d_oi_reset", time.Now)
		require.NotNil(t, window)
		// Allow floating representation error in fractional percentage conversion.
		require.InDelta(t, 87.0, window.Utilization, 1e-9)
		require.NotNil(t, window.ResetsAt)
		require.Equal(t, future, window.ResetsAt.Unix())
		require.Greater(t, window.RemainingSeconds, 0)
	})

	t.Run("no data returns nil", func(t *testing.T) {
		require.Nil(t, BuildPassiveUsageWindow(nil, "u", "r", time.Now))
		require.Nil(t, BuildPassiveUsageWindow(map[string]any{}, "u", "r", time.Now))
	})

	t.Run("expired reset clamps remaining to zero", func(t *testing.T) {
		past := time.Now().Add(-time.Hour).Unix()
		window := BuildPassiveUsageWindow(map[string]any{
			"u": 0.5,
			"r": float64(past),
		}, "u", "r", time.Now)
		require.NotNil(t, window)
		require.Equal(t, 0, window.RemainingSeconds)
	})

	t.Run("utilization only", func(t *testing.T) {
		window := BuildPassiveUsageWindow(map[string]any{"u": 0.25}, "u", "r", time.Now)
		require.NotNil(t, window)
		// Allow floating representation error in fractional percentage conversion.
		require.InDelta(t, 25.0, window.Utilization, 1e-9)
		require.Nil(t, window.ResetsAt)
	})
}

// TestEstimateSetupTokenUsage_ExpiredWindowZeroes supplies a past session end
// with sampled fraction. It checks the retained FiveHour object has zero
// usage/countdown and no reset pointer after expiry.
func TestEstimateSetupTokenUsage_ExpiredWindowZeroes(t *testing.T) {
	t.Parallel()

	past := time.Now().Add(-2 * time.Hour)
	info := EstimateSetupTokenUsage(&Record{
		SessionWindowEnd: &past,
		Extra: map[string]any{
			"session_window_utilization": 0.53,
		},
	}, time.Now)

	if info.FiveHour == nil {
		t.Fatal("expected non-nil FiveHour info")
	}
	if info.FiveHour.Utilization != 0 {
		t.Fatalf("expected Utilization=0 for expired window, got %v", info.FiveHour.Utilization)
	}
	if info.FiveHour.ResetsAt != nil {
		t.Fatalf("expected ResetsAt=nil for expired window, got %v", info.FiveHour.ResetsAt)
	}
	if info.FiveHour.RemainingSeconds != 0 {
		t.Fatalf("expected RemainingSeconds=0 for expired window, got %v", info.FiveHour.RemainingSeconds)
	}
}

// TestEstimateSetupTokenUsage_ActiveWindowPreservesUtilization supplies a
// future session end and sampled fraction. It checks percentage conversion,
// reset equality, and positive countdown in the resulting FiveHour estimate.
func TestEstimateSetupTokenUsage_ActiveWindowPreservesUtilization(t *testing.T) {
	t.Parallel()

	future := time.Now().Add(3 * time.Hour)
	info := EstimateSetupTokenUsage(&Record{
		SessionWindowEnd: &future,
		Extra: map[string]any{
			"session_window_utilization": 0.53,
		},
	}, time.Now)

	if info.FiveHour == nil {
		t.Fatal("expected non-nil FiveHour info")
	}
	if info.FiveHour.Utilization != 53 {
		t.Fatalf("expected Utilization=53, got %v", info.FiveHour.Utilization)
	}
	if info.FiveHour.ResetsAt == nil || !info.FiveHour.ResetsAt.Equal(future) {
		t.Fatalf("expected ResetsAt=%v, got %v", future, info.FiveHour.ResetsAt)
	}
	if info.FiveHour.RemainingSeconds <= 0 {
		t.Fatalf("expected positive RemainingSeconds, got %v", info.FiveHour.RemainingSeconds)
	}
}
