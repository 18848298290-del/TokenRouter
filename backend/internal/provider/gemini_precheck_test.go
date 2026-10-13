package provider

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// geminiBatchUsageFixture records serial aggregate calls and supplies fixed
// daily/minute totals.
type geminiBatchUsageFixture struct {
	// minute selects the fixture result for a minute query. Other start
	// values use the fixed daily observation, independent of requested ID
	// contents.
	minute time.Time
	// calls owns copied ID slices and is appended without locking. The
	// scenario makes serial batch calls before inspecting this observed order.
	calls [][]int64
}

// geminiUsageReaderFunc adapts a configured callback to local model usage
// reads.
type geminiUsageReaderFunc func(context.Context, int64, time.Time, time.Time) ([]GeminiModelUsage, error)

// TestGeminiPrecheckSingleQuota varies shared/class daily and minute usage,
// zero limits, and empty model input. It checks admission, ordered query
// starts, requested row/end time, and one clock observation per case.
func TestGeminiPrecheckSingleQuota(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 30, 0, time.UTC)
	dailyStart := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	minuteStart := now.Truncate(time.Minute)
	sharedPolicy := `{"shared_rpd":1000,"rpm":60,"gemini_pro":{"rpd":1,"rpm":1},"gemini_flash":{"rpd":1,"rpm":1}}`

	tests := []struct {
		name  string // quota admission case name
		model string // requested model identifier text
		// policy supplies the case quota-rule fragment. Empty selects local
		// AI Studio defaults, so the expected request limits follow that default
		// table.
		policy string
		// dailyPro, dailyFlash, minutePro, and minuteFlash provide request totals
		// for their class and window. The callback switches by query start while
		// all cases retain the same end-time observation.
		dailyPro    int64 // Pro daily fixture requests
		dailyFlash  int64 // Flash daily fixture requests
		minutePro   int64 // Pro minute fixture requests
		minuteFlash int64 // Flash minute fixture requests
		wantAllowed bool  // expected local admission decision
		wantQueries int   // expected ordered query count
	}{
		{
			name: "shared daily quota", model: "gemini-2.5-flash", policy: sharedPolicy,
			dailyPro: 400, dailyFlash: 600, wantQueries: 1,
		},
		{
			name: "shared minute quota", model: "gemini-2.5-pro", policy: sharedPolicy,
			dailyPro: 400, dailyFlash: 599, minutePro: 30, minuteFlash: 30, wantQueries: 2,
		},
		{
			name: "pro daily quota", model: "gemini-2.5-pro",
			dailyPro: 50, wantQueries: 1,
		},
		{
			name: "flash daily quota", model: "gemini-2.5-flash",
			dailyFlash: 1500, wantQueries: 1,
		},
		{
			name: "pro minute quota", model: "gemini-2.5-pro",
			minutePro: 2, wantQueries: 2,
		},
		{
			name: "flash minute quota", model: "gemini-2.5-flash",
			minuteFlash: 15, wantQueries: 2,
		},
		{
			name:     "empty model uses pro quota",
			dailyPro: 50, wantQueries: 1,
		},
		{
			name: "zero limits skip queries", model: "gemini-2.5-pro",
			policy: `{"gemini_pro":{"rpd":0,"rpm":0}}`, wantAllowed: true,
		},
		{
			name: "pro below quota ignores flash usage", model: "gemini-2.5-pro",
			dailyPro: 49, dailyFlash: 1500, minutePro: 1, minuteFlash: 15,
			wantAllowed: true, wantQueries: 2,
		},
		{
			name: "flash below quota ignores pro usage", model: "gemini-2.5-flash",
			dailyPro: 50, dailyFlash: 1499, minutePro: 2, minuteFlash: 14,
			wantAllowed: true, wantQueries: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queries := make([]time.Time, 0, 2)
			usage := geminiUsageReaderFunc(func(_ context.Context, providerID int64, start, end time.Time) ([]GeminiModelUsage, error) {
				require.Equal(t, int64(1), providerID)
				require.Equal(t, now, end)
				queries = append(queries, start)
				pro, flash := tt.dailyPro, tt.dailyFlash
				if start.Equal(minuteStart) {
					pro, flash = tt.minutePro, tt.minuteFlash
				}
				return []GeminiModelUsage{
					{Model: "gemini-2.5-pro", Requests: pro},
					{Model: "gemini-2.5-flash", Requests: flash},
				}, nil
			})
			policyOptions := GeminiQuotaOptions{}
			if tt.policy != "" {
				policyOptions.StaticPolicy = fmt.Sprintf(`{"quota_rules":{"aistudio_free":%s}}`, tt.policy)
			}
			nowCalls := 0
			core := NewGeminiPrecheck(NewGeminiQuotaService(policyOptions), usage, GeminiPrecheckOptions{
				Now: func() time.Time {
					nowCalls++
					return now
				},
				Location: time.UTC,
			})
			allowed, err := core.PreCheckUsage(context.Background(), &Record{
				ID: 1, Platform: PlatformGemini, Type: ProviderTypeAPIKey,
			}, tt.model)
			require.NoError(t, err)
			require.Equal(t, tt.wantAllowed, allowed)
			require.Equal(t, []time.Time{dailyStart, minuteStart}[:tt.wantQueries], queries)
			require.Equal(t, 1, nowCalls)
		})
	}
}

// GetModelUsage delegates provider and window values to its configured nonnil
// function. This adapter copies no returned rows, so ownership and injected
// failures remain defined by the callback used in each test.
func (f geminiUsageReaderFunc) GetModelUsage(ctx context.Context, providerID int64, start, end time.Time) ([]GeminiModelUsage, error) {
	return f(ctx, providerID, start, end)
}

// GetModelUsage panics if this aggregate-capable fixture is queried per
// provider. That panic makes an unexpected fallback visible, while the
// configured batch method supplies the totals needed by this scenario.
func (q *geminiBatchUsageFixture) GetModelUsage(context.Context, int64, time.Time, time.Time) ([]GeminiModelUsage, error) {
	panic("批量能力存在时不能退回逐提供商查询")
}

// GetGeminiUsageTotalsBatch copies requested IDs into the unlocked call log
// and returns fixed totals. Minute start selects row two; other starts select
// row one, so tests serialize access and the real helper projects requested
// IDs.
func (q *geminiBatchUsageFixture) GetGeminiUsageTotalsBatch(_ context.Context, ids []int64, start, _ time.Time) (map[int64]GeminiUsageTotals, error) {
	q.calls = append(q.calls, append([]int64(nil), ids...))
	out := map[int64]GeminiUsageTotals{}
	if start.Equal(q.minute) {
		out[2] = GeminiUsageTotals{ProRequests: 1}
	} else {
		out[1] = GeminiUsageTotals{ProRequests: 50}
	}
	return out, nil
}

// TestGeminiPrecheckBatchKeepsQueriesAndDailyCache supplies a daily-exhausted
// provider, an eligible provider, and nil. It checks admission results,
// exclusion from minute reads, and repeated-call reuse of daily cache with
// only another minute query.
func TestGeminiPrecheckBatchKeepsQueriesAndDailyCache(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 30, 0, time.UTC)
	queries := &geminiBatchUsageFixture{minute: now.Truncate(time.Minute)}
	core := NewGeminiPrecheck(NewGeminiQuotaService(GeminiQuotaOptions{}), queries, GeminiPrecheckOptions{Now: func() time.Time { return now }, Location: time.UTC})
	values := []*Record{{ID: 1, Platform: PlatformGemini, Type: ProviderTypeAPIKey}, {ID: 2, Platform: PlatformGemini, Type: ProviderTypeAPIKey}, nil}
	allowed, err := core.PreCheckUsageBatch(context.Background(), values, "gemini-2.5-pro")
	require.NoError(t, err)
	require.Equal(t, map[int64]bool{1: false, 2: true}, allowed)
	require.Equal(t, [][]int64{{1, 2}, {2}}, queries.calls)
	_, err = core.PreCheckUsageBatch(context.Background(), values, "gemini-2.5-pro")
	require.NoError(t, err)
	require.Equal(t, [][]int64{{1, 2}, {2}, {2}}, queries.calls)
}
