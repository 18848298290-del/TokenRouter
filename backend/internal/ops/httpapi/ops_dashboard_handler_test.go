package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestParseOpsTokenStatsDuration covers each supported preset and an
// unsupported seven-day value. It checks both elapsed durations and recognition
// flags so invalid presets cannot become an accepted zero-length range.
func TestParseOpsTokenStatsDuration(t *testing.T) {
	tests := []struct {
		input string        // supplied preset value
		want  time.Duration // expected elapsed duration
		ok    bool          // expected preset recognition
	}{
		{input: "30m", want: 30 * time.Minute, ok: true},
		{input: "1h", want: time.Hour, ok: true},
		{input: "1d", want: 24 * time.Hour, ok: true},
		{input: "15d", want: 15 * 24 * time.Hour, ok: true},
		{input: "30d", want: 30 * 24 * time.Hour, ok: true},
		{input: "7d", want: 0, ok: false},
	}

	for _, tt := range tests {
		got, ok := parseOpsTokenStatsDuration(tt.input)
		require.Equal(t, tt.ok, ok, "input=%s", tt.input)
		require.Equal(t, tt.want, got, "input=%s", tt.input)
	}
}

// TestParseOpsTokenStatsFilter_Defaults parses an empty request and checks the
// thirty-day interval, page one, and size twenty. It also checks
// leading-model mode and optional filters remain unset.
func TestParseOpsTokenStatsFilter_Defaults(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	before := time.Now().UTC()
	filter, err := parseOpsTokenStatsFilter(c)
	after := time.Now().UTC()

	require.NoError(t, err)
	require.NotNil(t, filter)
	require.Equal(t, "30d", filter.TimeRange)
	require.Equal(t, 1, filter.Page)
	require.Equal(t, 20, filter.PageSize)
	require.Equal(t, 0, filter.TopN)
	require.Nil(t, filter.GroupID)
	require.Equal(t, "", filter.Platform)
	require.True(t, filter.StartTime.Before(filter.EndTime))
	// Allow two seconds of wall-clock movement around parsing while checking that
	// the default start and end reflect the sampled window.
	require.WithinDuration(t, before.Add(-30*24*time.Hour), filter.StartTime, 2*time.Second)
	require.WithinDuration(t, after, filter.EndTime, 2*time.Second)
}

// TestParseOpsTokenStatsFilter_WithTopN supplies a one-hour range, platform,
// positive group, and leading-model limit. It checks those values while
// pagination stays zero so the repository selects the limit mode.
func TestParseOpsTokenStatsFilter_WithTopN(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(
		http.MethodGet,
		"/?time_range=1h&platform=anthropic&group_id=12&top_n=50",
		nil,
	)

	filter, err := parseOpsTokenStatsFilter(c)
	require.NoError(t, err)
	require.Equal(t, "1h", filter.TimeRange)
	require.Equal(t, "anthropic", filter.Platform)
	require.NotNil(t, filter.GroupID)
	require.Equal(t, int64(12), *filter.GroupID)
	require.Equal(t, 50, filter.TopN)
	require.Equal(t, 0, filter.Page)
	require.Equal(t, 0, filter.PageSize)
}

// TestParseOpsTokenStatsFilter_InvalidParams exercises unsupported presets,
// invalid identities, ranking limits, and conflicting pagination parameters.
// Every listed input must return a parsing error for the requested ranking
// configuration.
func TestParseOpsTokenStatsFilter_InvalidParams(t *testing.T) {
	tests := []string{
		"/?time_range=7d",
		"/?group_id=0",
		"/?group_id=abc",
		"/?top_n=0",
		"/?top_n=101",
		"/?top_n=10&page=1",
		"/?top_n=10&page_size=20",
		"/?page=0",
		"/?page_size=0",
		"/?page_size=101",
	}

	for _, rawURL := range tests {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, rawURL, nil)

		_, err := parseOpsTokenStatsFilter(c)
		require.Error(t, err, "url=%s", rawURL)
	}
}

// TestParseOpsLatencyBucketBoundaries compares omitted and valid custom cutoff
// lists with their normalized results. It also rejects wrong counts, zero,
// duplicate, excessive, and nonnumeric cutoff values.
func TestParseOpsLatencyBucketBoundaries(t *testing.T) {
	defaults, err := parseOpsLatencyBucketBoundaries("")
	require.NoError(t, err)
	require.Equal(t, []int64{100, 200, 500, 1000, 2000}, defaults)

	custom, err := parseOpsLatencyBucketBoundaries("1000, 2000,5000,10000,20000")
	require.NoError(t, err)
	require.Equal(t, []int64{1000, 2000, 5000, 10000, 20000}, custom)

	invalid := []string{
		"100,200",
		"0,200,500,1000,2000",
		"100,200,200,1000,2000",
		"100,200,500,1000,86400001",
		"100,abc,500,1000,2000",
	}
	for _, raw := range invalid {
		_, err := parseOpsLatencyBucketBoundaries(raw)
		require.Error(t, err, "raw=%s", raw)
	}
}

// TestPickThroughputBucketSeconds supplies short, medium, and long dashboard
// spans to the trend selector. It checks the corresponding minute, five-minute,
// and hourly widths used by chart queries.
func TestPickThroughputBucketSeconds(t *testing.T) {
	require.Equal(t, 60, pickThroughputBucketSeconds(30*time.Minute))
	require.Equal(t, 300, pickThroughputBucketSeconds(6*time.Hour))
	require.Equal(t, 3600, pickThroughputBucketSeconds(48*time.Hour))
}
