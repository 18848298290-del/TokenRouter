package provider

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// quotaLifecycleClient blocks serial requests to test cancellation and stop
// result retention.
type quotaLifecycleClient struct {
	// entered is closed by the single fixture query. Tests wait for that signal
	// before shutdown, ensuring cancellation/wait assertions cover admitted work.
	entered chan struct{}
	// release optionally holds the query even after cancellation. The test closes
	// it after a stop timeout so request completion can be observed separately.
	release chan struct{}
}

// GetJSON signals one admitted request before waiting and returns nil with
// ctx.Err afterward. Callers provide an open entered channel and serialize the
// single fixture call.
//
// With release configured, cancellation alone cannot complete the request; the
// test must close release after observing the shutdown timeout. Without
// release, the request waits directly for cancellation. Returning the eventual
// context error after either wait lets lifecycle assertions separate
// cancellation delivery from actual completion, rather than simulate a
// successful quota response.
func (c *quotaLifecycleClient) GetJSON(ctx context.Context, _ string, _ map[string]string) (map[string]any, error) {
	// Signal one admitted query before waiting, letting stop assertions cover
	// active work. Explicit release can hold completion beyond cancellation so
	// the suite can observe its bounded shutdown result first.
	close(c.entered)
	if c.release != nil {
		<-c.release
	} else {
		<-ctx.Done()
	}
	return nil, ctx.Err()
}

// GetJSONRaw reports an unexpected detail call without accessing fixture
// state.
func (*quotaLifecycleClient) GetJSONRaw(context.Context, string, map[string]string) ([]byte, error) {
	return nil, errors.New("unexpected credit detail request")
}

// PostJSON reports an unexpected consumption call without accessing fixture
// state.
func (*quotaLifecycleClient) PostJSON(context.Context, string, map[string]any) (map[string]any, error) {
	return nil, errors.New("unexpected quota reset")
}

// TestOpenAIQuotaLifecycleCancelsAndWaits starts a blocked query then shuts
// down its service. It checks cancellation, repeated stop success, later query
// rejection, and one reader invocation without constructor-side reads.
func TestOpenAIQuotaLifecycleCancelsAndWaits(t *testing.T) {
	client := &quotaLifecycleClient{entered: make(chan struct{})}
	var reads atomic.Int32
	service := NewOpenAIQuotaService(OpenAIQuotaOptions{
		Configured: func() bool { return true },

		Read: func(context.Context, int64) (*Record, error) {
			reads.Add(1)
			return &Record{
				ID:          1,
				Platform:    "openai",
				Type:        "oauth",
				Credentials: map[string]any{"chatgpt_account_id": "fixture-provider", "access_token": "fixture-token"},
			}, nil
		},

		Client: func(context.Context, *Record, string) (OpenAIQuotaClient, error) { return client, nil },
	})
	require.Zero(t, reads.Load())
	finished := make(chan error, 1)
	// Dispatch the tracked query and wait for client entry before stopping, so
	// shutdown must cancel an active request rather than only reject future work.
	go func() { _, err := service.QueryUsage(context.Background(), 1); finished <- err }()
	<-client.entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, service.StopContext(ctx))
	require.ErrorIs(t, <-finished, context.Canceled)
	require.NoError(t, service.StopContext(ctx))
	_, err := service.QueryUsage(context.Background(), 1)
	require.ErrorIs(t, err, ErrOpenAIQuotaStopped)
	require.EqualValues(t, 1, reads.Load())
}

// TestOpenAIQuotaLifecycleReportsUnfinishedRequest blocks a client that
// ignores cancellation until release. It checks bounded stop timeout, eventual
// canceled query, service-labelled diagnostics, and retention of that first
// timeout after completion.
func TestOpenAIQuotaLifecycleReportsUnfinishedRequest(t *testing.T) {
	client := &quotaLifecycleClient{entered: make(chan struct{}), release: make(chan struct{})}
	service := NewOpenAIQuotaService(OpenAIQuotaOptions{
		Configured: func() bool { return true },

		Read: func(context.Context, int64) (*Record, error) {
			return &Record{
				ID:          1,
				Platform:    "openai",
				Type:        "oauth",
				Credentials: map[string]any{"chatgpt_account_id": "fixture-provider", "access_token": "fixture-token"},
			}, nil
		},

		Client: func(context.Context, *Record, string) (OpenAIQuotaClient, error) { return client, nil },
	})
	finished := make(chan error, 1)
	// Dispatch and await client entry before stop, ensuring the held request is
	// registered active work whose completion remains owned until explicit
	// release, separately from cancellation already delivered to its context.
	go func() { _, err := service.QueryUsage(context.Background(), 1); finished <- err }()
	<-client.entered
	// Bound shutdown while the fixture still awaits explicit release, exposing
	// unfinished ownership independently of cancellation already sent to its
	// context. The later release lets its deferred activity completion run.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := service.StopContext(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.True(t, strings.Contains(err.Error(), "OpenAIQuotaService"))
	close(client.release)
	require.ErrorIs(t, <-finished, context.Canceled)
	require.Equal(t, err, service.StopContext(context.Background()))
}

// TestBuildCodexSparkWindowExtraUpdates_ContainsCodexKeys supplies a shorter
// primary and longer secondary Spark observation. It checks both ordinary
// Codex percentage keys and values, and rejects codex_spark_ spelling in all
// emitted keys.
func TestBuildCodexSparkWindowExtraUpdates_ContainsCodexKeys(t *testing.T) {
	now := time.Now().UTC()
	usage := &openai.OpenAIQuotaUsage{
		AdditionalRateLimits: []openai.OpenAIAdditionalRateLimit{
			{
				MeteredFeature: "codex_bengalfox",
				RateLimit: &openai.OpenAIRateLimit{
					PrimaryWindow: &openai.OpenAIRateLimitWindow{
						UsedPercent:        0.42,
						LimitWindowSeconds: 18000, // 300 min = 5 h
						ResetAfterSeconds:  3600,
					},
					SecondaryWindow: &openai.OpenAIRateLimitWindow{
						UsedPercent:        0.15,
						LimitWindowSeconds: 604800, // 7 d
						ResetAfterSeconds:  86400,
					},
				},
			},
		},
	}

	updates := BuildCodexSparkWindowExtraUpdates(usage, now)
	require.NotNil(t, updates, "expected non-nil updates for valid codex_bengalfox entry")

	// Publish the shared Codex window keys on the shadow record.
	require.Contains(t, updates, "codex_5h_used_percent")
	require.Contains(t, updates, "codex_7d_used_percent")

	// Reject separate Spark key spelling because ordinary Codex display/scheduling
	// readers consume the shared fields. This assertion checks all output keys
	// but does not assert each reset timestamp value.
	for k := range updates {
		require.False(t, strings.Contains(k, "codex_spark_"),
			"unexpected Method-Z prefix in key: %s", k)
	}

	// Values follow the window duration: primary is five hours, secondary is
	// seven days.
	// Allow floating representation error in the observed percentages.
	require.InDelta(t, 0.42, updates["codex_5h_used_percent"], 1e-9)
	require.InDelta(t, 0.15, updates["codex_7d_used_percent"], 1e-9)
}

// TestBuildCodexSparkWindowExtraUpdates_NilUsage supplies nil usage to the
// Spark converter. It checks conversion returns nil, keeping that absent
// observation visible to callers of the Extra builder.
func TestBuildCodexSparkWindowExtraUpdates_NilUsage(t *testing.T) {
	require.Nil(t, BuildCodexSparkWindowExtraUpdates(nil, time.Now()))
}

// TestBuildCodexSparkWindowExtraUpdates_NoBengalfox supplies only an unrelated
// metered feature. It checks the converter returns no patch when the specific
// Spark feature observation is unavailable.
func TestBuildCodexSparkWindowExtraUpdates_NoBengalfox(t *testing.T) {
	usage := &openai.OpenAIQuotaUsage{
		AdditionalRateLimits: []openai.OpenAIAdditionalRateLimit{
			{MeteredFeature: "other_feature", RateLimit: &openai.OpenAIRateLimit{}},
		},
	}
	require.Nil(t, BuildCodexSparkWindowExtraUpdates(usage, time.Now()))
}

// TestBuildCodexSparkWindowExtraUpdates_CompleteMaps supplies swapped, partial,
// and duplicate feature observations at a fixed clock. It checks the complete
// normalized patch, including minute truncation, zero values, clamped dates,
// caller time zone, and selection of the first matching feature.
func TestBuildCodexSparkWindowExtraUpdates_CompleteMaps(t *testing.T) {
	// UTC+8 with fractional seconds checks the supplied Spark clock and
	// whole-second RFC3339 output for both selected windows.
	now := time.Date(2026, 10, 14, 13, 2, 3, 456000000, time.FixedZone("UTC+8", 8*60*60))
	shortLong := &openai.OpenAIRateLimit{
		PrimaryWindow: &openai.OpenAIRateLimitWindow{
			UsedPercent:        12.0,
			LimitWindowSeconds: 18000,
			ResetAfterSeconds:  3600,
			ResetAt:            1,
		},
		SecondaryWindow: &openai.OpenAIRateLimitWindow{
			UsedPercent:        88.0,
			LimitWindowSeconds: 604800,
			ResetAfterSeconds:  86400,
			ResetAt:            2,
		},
	}
	later := &openai.OpenAIRateLimit{
		PrimaryWindow: &openai.OpenAIRateLimitWindow{
			UsedPercent:        99.0,
			LimitWindowSeconds: 18000,
			ResetAfterSeconds:  5,
		},
	}
	tests := []struct {
		name  string                   // descriptive fixture name
		usage *openai.OpenAIQuotaUsage // supplied quota observation
		want  map[string]any           // complete expected patch
	}{
		{
			name: "nil usage",
		},
		{
			name: "unrelated feature",
			usage: &openai.OpenAIQuotaUsage{
				AdditionalRateLimits: []openai.OpenAIAdditionalRateLimit{
					{MeteredFeature: "other_feature", RateLimit: shortLong},
				},
			},
		},
		{
			name: "nil first match ignores later match",
			usage: &openai.OpenAIQuotaUsage{
				AdditionalRateLimits: []openai.OpenAIAdditionalRateLimit{
					{MeteredFeature: "codex_bengalfox"},
					{MeteredFeature: "codex_bengalfox", RateLimit: shortLong},
				},
			},
		},
		{
			name: "empty first match ignores later match",
			usage: &openai.OpenAIQuotaUsage{
				AdditionalRateLimits: []openai.OpenAIAdditionalRateLimit{
					{MeteredFeature: "codex_bengalfox", RateLimit: &openai.OpenAIRateLimit{}},
					{MeteredFeature: "codex_bengalfox", RateLimit: shortLong},
				},
			},
		},
		{
			name: "swapped windows truncate durations and use supplied clock",
			usage: &openai.OpenAIQuotaUsage{
				FetchedAt: 1,
				AdditionalRateLimits: []openai.OpenAIAdditionalRateLimit{
					{
						MeteredFeature: "codex_bengalfox",
						RateLimit: &openai.OpenAIRateLimit{
							PrimaryWindow: &openai.OpenAIRateLimitWindow{
								UsedPercent:        88.0,
								LimitWindowSeconds: 604859,
								ResetAfterSeconds:  86400,
								ResetAt:            1,
							},
							SecondaryWindow: &openai.OpenAIRateLimitWindow{
								UsedPercent:        12.0,
								LimitWindowSeconds: 18059,
								ResetAfterSeconds:  3600,
								ResetAt:            2,
							},
						},
					},
				},
			},
			want: map[string]any{
				"codex_5h_used_percent":        12.0,
				"codex_5h_reset_after_seconds": 3600,
				"codex_5h_window_minutes":      300,
				"codex_5h_reset_at":            "2026-10-14T14:02:03+08:00",
				"codex_7d_used_percent":        88.0,
				"codex_7d_reset_after_seconds": 86400,
				"codex_7d_window_minutes":      10080,
				"codex_7d_reset_at":            "2026-10-15T13:02:03+08:00",
				"codex_usage_updated_at":       "2026-10-14T13:02:03+08:00",
			},
		},
		{
			name: "first populated match supplies paired fields",
			usage: &openai.OpenAIQuotaUsage{
				FetchedAt: 2,
				AdditionalRateLimits: []openai.OpenAIAdditionalRateLimit{
					{MeteredFeature: "other_feature", RateLimit: later},
					{MeteredFeature: "codex_bengalfox", RateLimit: shortLong},
					{MeteredFeature: "codex_bengalfox", RateLimit: later},
				},
			},
			want: map[string]any{
				"codex_5h_used_percent":        12.0,
				"codex_5h_reset_after_seconds": 3600,
				"codex_5h_window_minutes":      300,
				"codex_5h_reset_at":            "2026-10-14T14:02:03+08:00",
				"codex_7d_used_percent":        88.0,
				"codex_7d_reset_after_seconds": 86400,
				"codex_7d_window_minutes":      10080,
				"codex_7d_reset_at":            "2026-10-15T13:02:03+08:00",
				"codex_usage_updated_at":       "2026-10-14T13:02:03+08:00",
			},
		},
		{
			name: "present zero window emits zero fields",
			usage: &openai.OpenAIQuotaUsage{
				AdditionalRateLimits: []openai.OpenAIAdditionalRateLimit{
					{
						MeteredFeature: "codex_bengalfox",
						RateLimit: &openai.OpenAIRateLimit{
							PrimaryWindow: &openai.OpenAIRateLimitWindow{},
						},
					},
				},
			},
			want: map[string]any{
				"codex_5h_used_percent":        0.0,
				"codex_5h_reset_after_seconds": 0,
				"codex_5h_window_minutes":      0,
				"codex_5h_reset_at":            "2026-10-14T13:02:03+08:00",
				"codex_usage_updated_at":       "2026-10-14T13:02:03+08:00",
			},
		},
		{
			name: "partial secondary negative reset clamps absolute date",
			usage: &openai.OpenAIQuotaUsage{
				AdditionalRateLimits: []openai.OpenAIAdditionalRateLimit{
					{
						MeteredFeature: "codex_bengalfox",
						RateLimit: &openai.OpenAIRateLimit{
							SecondaryWindow: &openai.OpenAIRateLimitWindow{
								UsedPercent:        0.0,
								LimitWindowSeconds: 18000,
								ResetAfterSeconds:  -15,
							},
						},
					},
				},
			},
			want: map[string]any{
				"codex_5h_used_percent":        0.0,
				"codex_5h_reset_after_seconds": -15,
				"codex_5h_window_minutes":      300,
				"codex_5h_reset_at":            "2026-10-14T13:02:03+08:00",
				"codex_usage_updated_at":       "2026-10-14T13:02:03+08:00",
			},
		},
		{
			name: "negative partial minute remains present as zero",
			usage: &openai.OpenAIQuotaUsage{
				AdditionalRateLimits: []openai.OpenAIAdditionalRateLimit{
					{
						MeteredFeature: "codex_bengalfox",
						RateLimit: &openai.OpenAIRateLimit{
							PrimaryWindow: &openai.OpenAIRateLimitWindow{
								UsedPercent:        12.0,
								LimitWindowSeconds: -59,
								ResetAfterSeconds:  -3,
							},
						},
					},
				},
			},
			want: map[string]any{
				"codex_5h_used_percent":        12.0,
				"codex_5h_reset_after_seconds": -3,
				"codex_5h_window_minutes":      0,
				"codex_5h_reset_at":            "2026-10-14T13:02:03+08:00",
				"codex_usage_updated_at":       "2026-10-14T13:02:03+08:00",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, BuildCodexSparkWindowExtraUpdates(tt.usage, now))
		})
	}
}
