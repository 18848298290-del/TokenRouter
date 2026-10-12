package openai

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParseOpenAIRateLimitResetCreditDetails_CompatibleContainers supplies
// credit arrays in each supported container spelling and as a direct array.
// Reduced details must retain the expected expiry text and exclude the fixture
// credit identifier from their serialized output.
func TestParseOpenAIRateLimitResetCreditDetails_CompatibleContainers(t *testing.T) {
	tests := []struct {
		name string   // credit container scenario
		body string   // compatible quota JSON
		want []string // expected expiry strings
	}{
		{
			name: "credits",
			body: `{"credits":[{"id":"secret-id","expires_at":"2026-07-03T04:05:06Z"}]}`,
			want: []string{"2026-07-03T04:05:06Z"},
		},
		{
			name: "rate limit reset credits",
			body: `{"rate_limit_reset_credits":[{"expiresAt":"2026-07-04T04:05:06Z"}]}`,
			want: []string{"2026-07-04T04:05:06Z"},
		},
		{
			name: "items",
			body: `{"items":[{"expires_at":"2026-07-05T04:05:06Z"}]}`,
			want: []string{"2026-07-05T04:05:06Z"},
		},
		{
			name: "data",
			body: `{"data":[{"expires_at":"2026-07-06T04:05:06Z"}]}`,
			want: []string{"2026-07-06T04:05:06Z"},
		},
		{
			name: "array",
			body: `[{"expires_at":"2026-07-07T04:05:06Z"}]`,
			want: []string{"2026-07-07T04:05:06Z"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseOpenAIRateLimitResetCreditDetails([]byte(tt.body))
			require.NoError(t, err)
			require.Len(t, got.Credits, len(tt.want))
			for i := range tt.want {
				require.Equal(t, tt.want[i], got.Credits[i].ExpiresAt)
			}
			encoded, err := json.Marshal(got.Credits)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "secret-id")
		})
	}
}

// TestParseOpenAIRateLimitResetCreditDetails_PreservesAvailableCreditOrder
// supplies a string count alongside redeemed, available, and other-scope credits.
// Parsing must retain the count and the two eligible expiry values in their
// source order.
func TestParseOpenAIRateLimitResetCreditDetails_PreservesAvailableCreditOrder(t *testing.T) {
	body := []byte(`{
		"availableCount":"2",
		"credits":[
			{"reset_type":"codex_rate_limits","status":"redeemed","expires_at":"2026-07-01T04:05:06Z"},
			{"reset_type":"codex_rate_limits","status":"available","expires_at":"2026-07-04T04:05:06Z"},
			{"resetType":"codex_rate_limits","status":"available","expiresAt":"2026-07-03T04:05:06Z"},
			{"reset_type":"other","status":"available","expires_at":"2026-07-02T04:05:06Z"}
		]
	}`)

	details, err := ParseOpenAIRateLimitResetCreditDetails(body)
	require.NoError(t, err)
	require.NotNil(t, details.AvailableCount)
	require.Equal(t, 2, *details.AvailableCount)
	require.Equal(t, []OpenAIRateLimitResetCreditDetail{
		{ExpiresAt: "2026-07-04T04:05:06Z"},
		{ExpiresAt: "2026-07-03T04:05:06Z"},
	}, details.Credits)
}
