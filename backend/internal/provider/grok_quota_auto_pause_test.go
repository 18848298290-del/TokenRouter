package provider

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// TestShouldAutoPauseGrokProviderByQuota supplies exhausted requests, active
// and expired Retry-After, and stale quota observations. It asserts the
// resulting pause boolean for each case using independent parallel provider
// fixtures.
func TestShouldAutoPauseGrokProviderByQuota(t *testing.T) {
	t.Parallel()

	zero := int64(0)
	limit := int64(10)
	resetFuture := time.Now().Add(time.Minute).Unix()
	retryAfter := 30
	tests := []struct {
		name     string                  // table case display name
		snapshot usageview.QuotaSnapshot // supplied quota observation fixture
		want     bool                    // expected scheduling pause result
	}{
		{
			name: "remaining requests exhausted",
			snapshot: usageview.QuotaSnapshot{
				Requests:  &usageview.QuotaWindow{Limit: &limit, Remaining: &zero, ResetUnix: &resetFuture},
				UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			},
			want: true,
		},
		{
			name: "retry after active",
			snapshot: usageview.QuotaSnapshot{
				RetryAfterSeconds: &retryAfter,
				UpdatedAt:         time.Now().UTC().Format(time.RFC3339),
			},
			want: true,
		},
		{
			name: "retry after expired",
			snapshot: usageview.QuotaSnapshot{
				RetryAfterSeconds: &retryAfter,
				UpdatedAt:         time.Now().Add(-time.Duration(retryAfter+1) * time.Second).UTC().Format(time.RFC3339),
			},
			want: false,
		},
		{
			name: "stale snapshot ignored",
			snapshot: usageview.QuotaSnapshot{
				Requests:  &usageview.QuotaWindow{Limit: &limit, Remaining: &zero, ResetUnix: &resetFuture},
				UpdatedAt: time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339),
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := &Record{
				Platform: capability.PlatformGrok,
				Type:     capability.ProviderTypeOAuth,
				Extra: map[string]any{
					"grok_usage_snapshot": tt.snapshot,
				},
			}
			got, _ := EvaluateGrokQuotaAutoPause(provider, time.Now)
			require.Equal(t, tt.want, got)
		})
	}
}
