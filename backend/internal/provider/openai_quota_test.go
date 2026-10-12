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

type quotaLifecycleClient struct {
	entered chan struct{}
	release chan struct{}
}

func (c *quotaLifecycleClient) GetJSON(ctx context.Context, _ string, _ map[string]string) (map[string]any, error) {
	close(c.entered)
	if c.release != nil {
		<-c.release
	} else {
		<-ctx.Done()
	}
	return nil, ctx.Err()
}

func (*quotaLifecycleClient) GetJSONRaw(context.Context, string, map[string]string) ([]byte, error) {
	return nil, errors.New("unexpected credit detail request")
}

func (*quotaLifecycleClient) PostJSON(context.Context, string, map[string]any) (map[string]any, error) {
	return nil, errors.New("unexpected quota reset")
}

// TestOpenAIQuotaLifecycleCancelsAndWaits checks cancellation, waiting, and rejection of queries after
// shutdown.
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

// TestOpenAIQuotaLifecycleReportsUnfinishedRequest checks that repeated stops retain the first timeout result.
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
	go func() { _, err := service.QueryUsage(context.Background(), 1); finished <- err }()
	<-client.entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := service.StopContext(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.True(t, strings.Contains(err.Error(), "OpenAIQuotaService"))
	close(client.release)
	require.ErrorIs(t, <-finished, context.Canceled)
	require.Equal(t, err, service.StopContext(context.Background()))
}

// TestBuildCodexSparkWindowExtraUpdates_ContainsCodexKeys checks normalized Spark snapshot fields:
//   - codex_5h_used_percent and codex_7d_used_percent are present.
//   - No codex_spark_ keys are emitted.
//   - The shorter primary window maps to five hours and the longer secondary to seven days.
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

	// Separate codex_spark_ keys would bypass readers of the shared Codex fields.
	for k := range updates {
		require.False(t, strings.Contains(k, "codex_spark_"),
			"unexpected Method-Z prefix in key: %s", k)
	}

	// Values follow the window duration: primary is five hours, secondary is seven days.
	require.InDelta(t, 0.42, updates["codex_5h_used_percent"], 1e-9)
	require.InDelta(t, 0.15, updates["codex_7d_used_percent"], 1e-9)
}

// TestBuildCodexSparkWindowExtraUpdates_NilUsage checks the absent-observation result.
func TestBuildCodexSparkWindowExtraUpdates_NilUsage(t *testing.T) {
	require.Nil(t, BuildCodexSparkWindowExtraUpdates(nil, time.Now()))
}

// TestBuildCodexSparkWindowExtraUpdates_NoBengalfox checks that unrelated metered features produce no updates.
func TestBuildCodexSparkWindowExtraUpdates_NoBengalfox(t *testing.T) {
	usage := &openai.OpenAIQuotaUsage{
		AdditionalRateLimits: []openai.OpenAIAdditionalRateLimit{
			{MeteredFeature: "other_feature", RateLimit: &openai.OpenAIRateLimit{}},
		},
	}
	require.Nil(t, BuildCodexSparkWindowExtraUpdates(usage, time.Now()))
}
