package httpapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Selection cache keys retain the supplied decimal ID sequence, with a distinct
// spelling for an empty selection. oauth_usage.go normalizes IDs before this
// helper in production; these examples protect the helper's exact delimiter and
// prefix representation.

// TestBuildProviderTodayStatsBatchCacheKey covers empty, single-ID and
// multiple-ID selection inputs. The helper returns the exact reserved empty key
// or comma-separated decimal sequence expected by the statistics snapshot
// cache.
func TestBuildProviderTodayStatsBatchCacheKey(t *testing.T) {
	tests := []struct {
		name string  // table scenario description
		ids  []int64 // supplied selection identifier sequence
		want string  // expected exact cache key
	}{
		{"empty", nil, "providers_today_stats_empty"},
		{"single", []int64{42}, "providers_today_stats:42"},
		{"multiple", []int64{1, 2, 3}, "providers_today_stats:1,2,3"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildProviderTodayStatsBatchCacheKey(tc.ids)
			require.Equal(t, tc.want, got)
		})
	}
}
