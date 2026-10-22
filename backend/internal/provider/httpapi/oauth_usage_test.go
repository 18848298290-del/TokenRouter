package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// passiveUsageReader supplies a stable provider row for passive HTTP observation tests.
type passiveUsageReader struct {
	record *provider.Record // borrowed fixture provider record
}

// oauth_usage_test.go checks passive-query behavior and selection cache identity.
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

// GetByID returns the fixture row without persisting any observation changes.
func (r passiveUsageReader) GetByID(context.Context, int64) (*provider.Record, error) {
	return r.record, nil
}

// GetByIDs returns the fixture selection for the usage reader interface.
func (r passiveUsageReader) GetByIDs(context.Context, []int64) ([]*provider.Record, error) {
	return []*provider.Record{r.record}, nil
}

// TestPassiveUsageEndpointNeverSelectsActiveMaintenance submits active-looking query flags to the passive route.
// The stored observation is returned without probing upstream or recovering the provider's existing error status.
func TestPassiveUsageEndpointNeverSelectsActiveMaintenance(t *testing.T) {
	row := &provider.Record{ID: 1, Platform: "anthropic", Type: "oauth", Status: provider.StatusError, ErrorMessage: "token refresh failed", Extra: map[string]any{}}
	activeCalls := 0
	reader := passiveUsageReader{row}
	cache := provider.NewOAuthUsageCache()
	stats := provider.NewLocalUsageStatistics(reader, cache, provider.LocalUsageStatisticsOptions{Now: time.Now, Today: time.Now, Log: func(string, ...any) {}})
	core := provider.NewOAuthUsageService(reader, cache, stats, provider.OAuthUsageOptions{Anthropic: func(context.Context, *provider.Record) (*provider.ClaudeUsageResponse, error) {
		activeCalls++
		return nil, nil
	}})
	handler := NewOAuthUsageHandler(core, nil)
	router := gin.New()
	router.GET("/providers/:id/usage/passive", handler.GetPassiveUsage)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/providers/1/usage/passive?source=active&force=true", nil))
	require.Equal(t, 200, response.Code, response.Body.String())
	require.True(t, strings.Contains(response.Body.String(), `"source":"passive"`))
	require.Zero(t, activeCalls)
	require.Equal(t, provider.StatusError, row.Status)
	for _, id := range []string{"providers.usage.list", "providers.usage.batch.create", "grok.providers.quota.list"} {
		require.False(t, ManagementOperations[id].ReadOnly, id)
	}
	require.True(t, ManagementOperations["providers.usage.passive.list"].ReadOnly)
}

// GetProviderWindowStats returns empty local totals for the passive observation fixture.
func (r passiveUsageReader) GetProviderWindowStats(context.Context, int64, time.Time) (*provider.WindowStats, error) {
	return &provider.WindowStats{}, nil
}

// GetProviderTodayStats returns empty daily totals for the passive observation fixture.
func (r passiveUsageReader) GetProviderTodayStats(context.Context, int64) (*provider.WindowStats, error) {
	return &provider.WindowStats{}, nil
}
