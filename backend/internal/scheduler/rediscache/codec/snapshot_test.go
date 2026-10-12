package codec

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// geminiQuotaUsageFixture exceeds the free daily request allowance while reporting no requests in the current minute.
type geminiQuotaUsageFixture struct {
	minute time.Time
	calls  int
}

func (f *geminiQuotaUsageFixture) GetModelUsage(_ context.Context, _ int64, start, _ time.Time) ([]provider.GeminiModelUsage, error) {
	f.calls++
	if start.Equal(f.minute) {
		return nil, nil
	}
	return []provider.GeminiModelUsage{{Model: "gemini-3.8-flash", Requests: 1510}}, nil
}

// TestProviderCodecGeminiQuotaPrecheck checks source and tier fields in individual and batch quota checks after cache decoding.
func TestProviderCodecGeminiQuotaPrecheck(t *testing.T) {
	for _, tc := range []struct {
		name        string
		kind        string
		credentials map[string]any
		allowed     bool
		usageReads  bool
	}{
		{
			name: "third_party",
			kind: provider.ProviderTypeAPIKey,
			credentials: map[string]any{
				"provider_type": provider.GeminiProviderTypeThirdParty,
			},
			allowed: true,
		},
		{
			name:       "official_free_default",
			kind:       provider.ProviderTypeAPIKey,
			usageReads: true,
		},
		{
			name: "official_paid",
			kind: provider.ProviderTypeAPIKey,
			credentials: map[string]any{
				"provider_type": "official",
				"tier_id":       "aistudio_paid",
			},
			allowed:    true,
			usageReads: true,
		},
		{
			name: "google_one_ultra",
			kind: provider.ProviderTypeOAuth,
			credentials: map[string]any{
				"oauth_type": "google_one",
				"tier_id":    "google_ai_ultra",
			},
			allowed:    true,
			usageReads: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := &provider.Record{
				ID:          3726,
				Platform:    provider.PlatformGemini,
				Type:        tc.kind,
				Credentials: tc.credentials,
			}
			codec := ProviderCodec{}
			full, metadata, err := codec.Encode(WrapRecord(value))
			require.NoError(t, err)
			for name, payload := range map[string][]byte{"full": full, "metadata": metadata} {
				t.Run(name, func(t *testing.T) {
					snapshot, err := codec.Decode(payload)
					require.NoError(t, err)
					decoded, err := RecordValue(snapshot)
					require.NoError(t, err)
					for _, batch := range []bool{false, true} {
						now := time.Date(2026, 10, 8, 6, 0, 30, 0, time.UTC)
						usage := &geminiQuotaUsageFixture{minute: now.Truncate(time.Minute)}
						precheck := provider.NewGeminiPrecheck(
							provider.NewGeminiQuotaService(provider.GeminiQuotaOptions{}),
							usage,
							provider.GeminiPrecheckOptions{Now: func() time.Time { return now }, Location: time.UTC},
						)
						ctx := context.Background()
						if batch {
							allowed, err := precheck.PreCheckUsageBatch(ctx, []*provider.Record{decoded}, "gemini-3.8-flash")
							require.NoError(t, err)
							require.Equal(t, tc.allowed, allowed[value.ID], "批量预检")
						} else {
							allowed, err := precheck.PreCheckUsage(ctx, decoded, "gemini-3.8-flash")
							require.NoError(t, err)
							require.Equal(t, tc.allowed, allowed, "单条预检")
						}
						require.Equal(t, tc.usageReads, usage.calls > 0, "第三方提供商跳过本地官方配额统计")
					}
				})
			}
		})
	}
}
