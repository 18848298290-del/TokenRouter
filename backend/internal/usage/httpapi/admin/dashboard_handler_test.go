package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

type dashboardUsageRepoCacheProbe struct {
	usage.UsageLogRepository
	trendCalls      atomic.Int32
	usersTrendCalls atomic.Int32
}

type dashboardUsageRepoCapture struct {
	usage.UsageLogRepository
	trendRequestType *int16
	trendStream      *bool
	modelRequestType *int16
	modelStream      *bool
	rankingLimit     int
	ranking          []usage.UserSpendingRankingItem
	rankingTotal     float64
}

type userBreakdownRepoCapture struct {
	usage.UsageLogRepository
	capturedDim   usage.UserBreakdownDimension
	capturedLimit int
	result        []usage.UserBreakdownItem
}

func (r *dashboardUsageRepoCacheProbe) GetUsageTrendWithFilters(
	ctx context.Context,
	startTime, endTime time.Time,
	granularity string,
	userID, apiKeyID, providerID, groupID int64,
	model string,
	requestType *int16,
	stream *bool,
	billingType *int8,
) ([]usage.TrendDataPoint, error) {
	r.trendCalls.Add(1)
	return []usage.TrendDataPoint{{
		Date:        "2026-03-11",
		Requests:    1,
		TotalTokens: 2,
		Cost:        3,
		ActualCost:  4,
	}}, nil
}

func (r *dashboardUsageRepoCacheProbe) GetUserUsageTrend(
	ctx context.Context,
	startTime, endTime time.Time,
	granularity string,
	limit int,
) ([]usage.UserUsageTrendPoint, error) {
	r.usersTrendCalls.Add(1)
	return []usage.UserUsageTrendPoint{{
		Date:       "2026-03-11",
		UserID:     1,
		Email:      "cache@test.dev",
		Requests:   2,
		Tokens:     20,
		Cost:       2,
		ActualCost: 1,
	}}, nil
}

func resetDashboardReadCachesForTest() {
	dashboardSnapshotV2Cache = newSnapshotCache(30 * time.Second)
}

func TestDashboardHandler_GetUsageTrend_UsesCache(t *testing.T) {
	t.Cleanup(resetDashboardReadCachesForTest)
	resetDashboardReadCachesForTest()

	repo := &dashboardUsageRepoCacheProbe{}
	dashboardSvc := usage.NewDashboardService(repo, nil, nil, nil)
	handler := NewDashboardHandler(dashboardSvc, timezone.NewCalendar(time.Local))
	router := gin.New()
	router.GET("/admin/dashboard/trend", handler.GetUsageTrend)

	req1 := httptest.NewRequest(http.MethodGet, "/admin/dashboard/trend?start_date=2026-03-01&end_date=2026-03-07&granularity=day", nil)
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)
	require.Equal(t, http.StatusOK, rec1.Code)
	require.Equal(t, "miss", rec1.Header().Get("X-Snapshot-Cache"))

	req2 := httptest.NewRequest(http.MethodGet, "/admin/dashboard/trend?start_date=2026-03-01&end_date=2026-03-07&granularity=day", nil)
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusOK, rec2.Code)
	require.Equal(t, "hit", rec2.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, int32(1), repo.trendCalls.Load())
}

// TestDashboardHandler_GetUsageTrend_SeparatesTeams checks separate queries and cache entries for different teams.
func TestDashboardHandler_GetUsageTrend_SeparatesTeams(t *testing.T) {
	t.Cleanup(resetDashboardReadCachesForTest)
	resetDashboardReadCachesForTest()

	repo := &dashboardUsageRepoCacheProbe{}
	dashboardSvc := usage.NewDashboardService(repo, nil, nil, nil)
	handler := NewDashboardHandler(dashboardSvc, timezone.NewCalendar(time.Local))
	router := gin.New()
	router.GET("/admin/dashboard/trend", handler.GetUsageTrend)

	for _, teamID := range []string{"7", "8"} {
		req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/trend?start_date=2026-03-01&end_date=2026-03-07&granularity=day&team_id="+teamID, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "miss", rec.Header().Get("X-Snapshot-Cache"))
	}

	require.Equal(t, int32(2), repo.trendCalls.Load())
}

func TestDashboardHandler_GetUserUsageTrend_UsesCache(t *testing.T) {
	t.Cleanup(resetDashboardReadCachesForTest)
	resetDashboardReadCachesForTest()

	repo := &dashboardUsageRepoCacheProbe{}
	dashboardSvc := usage.NewDashboardService(repo, nil, nil, nil)
	handler := NewDashboardHandler(dashboardSvc, timezone.NewCalendar(time.Local))
	router := gin.New()
	router.GET("/admin/dashboard/users-trend", handler.GetUserUsageTrend)

	req1 := httptest.NewRequest(http.MethodGet, "/admin/dashboard/users-trend?start_date=2026-03-01&end_date=2026-03-07&granularity=day&limit=8", nil)
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)
	require.Equal(t, http.StatusOK, rec1.Code)
	require.Equal(t, "miss", rec1.Header().Get("X-Snapshot-Cache"))

	req2 := httptest.NewRequest(http.MethodGet, "/admin/dashboard/users-trend?start_date=2026-03-01&end_date=2026-03-07&granularity=day&limit=8", nil)
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusOK, rec2.Code)
	require.Equal(t, "hit", rec2.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, int32(1), repo.usersTrendCalls.Load())
}

func (s *dashboardUsageRepoCapture) GetUsageTrendWithFilters(
	ctx context.Context,
	startTime, endTime time.Time,
	granularity string,
	userID, apiKeyID, providerID, groupID int64,
	model string,
	requestType *int16,
	stream *bool,
	billingType *int8,
) ([]usage.TrendDataPoint, error) {
	s.trendRequestType = requestType
	s.trendStream = stream
	return []usage.TrendDataPoint{}, nil
}

func (s *dashboardUsageRepoCapture) GetModelStatsWithFilters(
	ctx context.Context,
	startTime, endTime time.Time,
	userID, apiKeyID, providerID, groupID int64,
	requestType *int16,
	stream *bool,
	billingType *int8,
) ([]usage.ModelStat, error) {
	s.modelRequestType = requestType
	s.modelStream = stream
	return []usage.ModelStat{}, nil
}

func (s *dashboardUsageRepoCapture) GetUserSpendingRanking(
	ctx context.Context,
	startTime, endTime time.Time,
	limit int,
) (*usage.UserSpendingRankingResponse, error) {
	s.rankingLimit = limit
	return &usage.UserSpendingRankingResponse{
		Ranking:         s.ranking,
		TotalActualCost: s.rankingTotal,
		TotalRequests:   44,
		TotalTokens:     1234,
	}, nil
}

func newDashboardRequestTypeTestRouter(repo *dashboardUsageRepoCapture) *gin.Engine {
	dashboardSvc := usage.NewDashboardService(repo, nil, nil, nil)
	handler := NewDashboardHandler(dashboardSvc, timezone.NewCalendar(time.Local))
	router := gin.New()
	router.GET("/admin/dashboard/trend", handler.GetUsageTrend)
	router.GET("/admin/dashboard/models", handler.GetModelStats)
	router.GET("/admin/dashboard/users-ranking", handler.GetUserSpendingRanking)
	return router
}

func TestDashboardTrendRequestTypePriority(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/trend?request_type=ws_v2&stream=bad", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, repo.trendRequestType)
	require.Equal(t, int16(usage.RequestTypeWSV2), *repo.trendRequestType)
	require.Nil(t, repo.trendStream)
}

func TestDashboardTrendInvalidRequestType(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/trend?request_type=bad", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDashboardTrendInvalidStream(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/trend?stream=bad", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDashboardModelStatsRequestTypePriority(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/models?request_type=sync&stream=bad", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, repo.modelRequestType)
	require.Equal(t, int16(usage.RequestTypeSync), *repo.modelRequestType)
	require.Nil(t, repo.modelStream)
}

func TestDashboardModelStatsInvalidRequestType(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/models?request_type=bad", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDashboardModelStatsInvalidStream(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/models?stream=bad", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDashboardModelStatsInvalidModelSource(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/models?model_source=invalid", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDashboardModelStatsValidModelSource(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/models?model_source=upstream", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

func TestDashboardUsersRankingLimitAndCache(t *testing.T) {
	repo := &dashboardUsageRepoCapture{
		ranking: []usage.UserSpendingRankingItem{
			{UserID: 7, Email: "rank@example.com", ActualCost: 10.5, Requests: 3, Tokens: 300},
		},
		rankingTotal: 88.8,
	}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/users-ranking?limit=100&start_date=2025-01-01&end_date=2025-01-02", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 50, repo.rankingLimit)
	require.Contains(t, rec.Body.String(), "\"total_actual_cost\":88.8")
	require.Contains(t, rec.Body.String(), "\"total_requests\":44")
	require.Contains(t, rec.Body.String(), "\"total_tokens\":1234")
	require.Equal(t, "miss", rec.Header().Get("X-Snapshot-Cache"))

	req2 := httptest.NewRequest(http.MethodGet, "/admin/dashboard/users-ranking?limit=100&start_date=2025-01-01&end_date=2025-01-02", nil)
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)

	require.Equal(t, http.StatusOK, rec2.Code)
	require.Equal(t, "hit", rec2.Header().Get("X-Snapshot-Cache"))
}

func (r *userBreakdownRepoCapture) GetUserBreakdownStats(
	_ context.Context, _, _ time.Time,
	dim usage.UserBreakdownDimension, limit int,
) ([]usage.UserBreakdownItem, error) {
	r.capturedDim = dim
	r.capturedLimit = limit
	if r.result != nil {
		return r.result, nil
	}
	return []usage.UserBreakdownItem{}, nil
}

func newUserBreakdownRouter(repo *userBreakdownRepoCapture) *gin.Engine {
	svc := usage.NewDashboardService(repo, nil, nil, nil)
	h := NewDashboardHandler(svc, timezone.NewCalendar(time.Local))
	router := gin.New()
	router.GET("/admin/dashboard/user-breakdown", h.GetUserBreakdown)
	return router
}

func TestGetUserBreakdown_GroupIDFilter(t *testing.T) {
	repo := &userBreakdownRepoCapture{}
	router := newUserBreakdownRouter(repo)

	req := httptest.NewRequest(http.MethodGet,
		"/admin/dashboard/user-breakdown?start_date=2026-03-01&end_date=2026-03-16&group_id=42", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, int64(42), repo.capturedDim.GroupID)
	require.Empty(t, repo.capturedDim.Model)
	require.Empty(t, repo.capturedDim.Endpoint)
	require.Equal(t, 50, repo.capturedLimit)  // Default result limit.
	require.Empty(t, repo.capturedDim.SortBy) // The repository selects the default sort when sort_by is absent.
}

func TestGetUserBreakdown_SortBy(t *testing.T) {
	repo := &userBreakdownRepoCapture{}
	router := newUserBreakdownRouter(repo)

	req := httptest.NewRequest(http.MethodGet,
		"/admin/dashboard/user-breakdown?start_date=2026-03-01&end_date=2026-03-16&sort_by=total_tokens", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "total_tokens", repo.capturedDim.SortBy)
}

func TestGetUserBreakdown_ModelFilter(t *testing.T) {
	repo := &userBreakdownRepoCapture{}
	router := newUserBreakdownRouter(repo)

	req := httptest.NewRequest(http.MethodGet,
		"/admin/dashboard/user-breakdown?start_date=2026-03-01&end_date=2026-03-16&model=claude-opus-4-6", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "claude-opus-4-6", repo.capturedDim.Model)
	require.Equal(t, usage.ModelSourceRequested, repo.capturedDim.ModelType)
	require.Equal(t, int64(0), repo.capturedDim.GroupID)
}

func TestGetUserBreakdown_ModelSourceFilter(t *testing.T) {
	repo := &userBreakdownRepoCapture{}
	router := newUserBreakdownRouter(repo)

	req := httptest.NewRequest(http.MethodGet,
		"/admin/dashboard/user-breakdown?start_date=2026-03-01&end_date=2026-03-16&model=claude-opus-4-6&model_source=upstream", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, usage.ModelSourceUpstream, repo.capturedDim.ModelType)
}

func TestGetUserBreakdown_InvalidModelSource(t *testing.T) {
	repo := &userBreakdownRepoCapture{}
	router := newUserBreakdownRouter(repo)

	req := httptest.NewRequest(http.MethodGet,
		"/admin/dashboard/user-breakdown?start_date=2026-03-01&end_date=2026-03-16&model_source=foobar", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGetUserBreakdown_EndpointFilter(t *testing.T) {
	repo := &userBreakdownRepoCapture{}
	router := newUserBreakdownRouter(repo)

	req := httptest.NewRequest(http.MethodGet,
		"/admin/dashboard/user-breakdown?start_date=2026-03-01&end_date=2026-03-16&endpoint=/v1/messages&endpoint_type=upstream", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "/v1/messages", repo.capturedDim.Endpoint)
	require.Equal(t, "upstream", repo.capturedDim.EndpointType)
}

func TestGetUserBreakdown_DefaultEndpointType(t *testing.T) {
	repo := &userBreakdownRepoCapture{}
	router := newUserBreakdownRouter(repo)

	req := httptest.NewRequest(http.MethodGet,
		"/admin/dashboard/user-breakdown?start_date=2026-03-01&end_date=2026-03-16&endpoint=/chat", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "inbound", repo.capturedDim.EndpointType)
}

func TestGetUserBreakdown_CustomLimit(t *testing.T) {
	repo := &userBreakdownRepoCapture{}
	router := newUserBreakdownRouter(repo)

	req := httptest.NewRequest(http.MethodGet,
		"/admin/dashboard/user-breakdown?start_date=2026-03-01&end_date=2026-03-16&model=test&limit=100", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 100, repo.capturedLimit)
}

func TestGetUserBreakdown_LimitClamped(t *testing.T) {
	repo := &userBreakdownRepoCapture{}
	router := newUserBreakdownRouter(repo)

	// limit > 200 should fall back to default 50
	req := httptest.NewRequest(http.MethodGet,
		"/admin/dashboard/user-breakdown?start_date=2026-03-01&end_date=2026-03-16&model=test&limit=999", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 50, repo.capturedLimit)
}

func TestGetUserBreakdown_ResponseFormat(t *testing.T) {
	repo := &userBreakdownRepoCapture{
		result: []usage.UserBreakdownItem{
			{UserID: 1, Email: "alice@test.com", Requests: 100, TotalTokens: 50000, Cost: 1.5, ActualCost: 1.2},
			{UserID: 2, Email: "bob@test.com", Requests: 50, TotalTokens: 25000, Cost: 0.8, ActualCost: 0.6},
		},
	}
	router := newUserBreakdownRouter(repo)

	req := httptest.NewRequest(http.MethodGet,
		"/admin/dashboard/user-breakdown?start_date=2026-03-01&end_date=2026-03-16&group_id=1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Code int `json:"code"`
		Data struct {
			Users     []usage.UserBreakdownItem `json:"users"`
			StartDate string                    `json:"start_date"`
			EndDate   string                    `json:"end_date"`
		} `json:"data"`
	}
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Equal(t, 0, resp.Code)
	require.Len(t, resp.Data.Users, 2)
	require.Equal(t, int64(1), resp.Data.Users[0].UserID)
	require.Equal(t, "alice@test.com", resp.Data.Users[0].Email)
	require.Equal(t, int64(100), resp.Data.Users[0].Requests)
	require.InDelta(t, 1.2, resp.Data.Users[0].ActualCost, 0.001)
	require.Equal(t, "2026-03-01", resp.Data.StartDate)
	require.Equal(t, "2026-03-16", resp.Data.EndDate)
}

func TestGetUserBreakdown_EmptyResult(t *testing.T) {
	repo := &userBreakdownRepoCapture{}
	router := newUserBreakdownRouter(repo)

	req := httptest.NewRequest(http.MethodGet,
		"/admin/dashboard/user-breakdown?start_date=2026-03-01&end_date=2026-03-16&group_id=999", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data struct {
			Users []usage.UserBreakdownItem `json:"users"`
		} `json:"data"`
	}
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Empty(t, resp.Data.Users)
}

func TestGetUserBreakdown_NoFilters(t *testing.T) {
	repo := &userBreakdownRepoCapture{}
	router := newUserBreakdownRouter(repo)

	req := httptest.NewRequest(http.MethodGet,
		"/admin/dashboard/user-breakdown?start_date=2026-03-01&end_date=2026-03-16", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, int64(0), repo.capturedDim.GroupID)
	require.Empty(t, repo.capturedDim.Model)
	require.Empty(t, repo.capturedDim.Endpoint)
}

func TestGetUserBreakdown_RequestTypeStringFilter(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  int16
	}{
		{"ws_v2", "ws_v2", int16(usage.RequestTypeWSV2)},
		{"stream", "stream", int16(usage.RequestTypeStream)},
		{"sync", "sync", int16(usage.RequestTypeSync)},
		{"cyber", "cyber", int16(usage.RequestTypeCyberBlocked)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &userBreakdownRepoCapture{}
			router := newUserBreakdownRouter(repo)

			req := httptest.NewRequest(http.MethodGet,
				"/admin/dashboard/user-breakdown?start_date=2026-03-01&end_date=2026-03-16&request_type="+tc.value, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code)
			require.NotNil(t, repo.capturedDim.RequestType, "request_type=%s should set filter", tc.value)
			require.Equal(t, tc.want, *repo.capturedDim.RequestType)
		})
	}
}

func TestGetUserBreakdown_InvalidRequestType(t *testing.T) {
	repo := &userBreakdownRepoCapture{}
	router := newUserBreakdownRouter(repo)

	req := httptest.NewRequest(http.MethodGet,
		"/admin/dashboard/user-breakdown?start_date=2026-03-01&end_date=2026-03-16&request_type=bogus", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestParseTimeRange(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/?start_date=2024-01-01&end_date=2024-01-02&timezone=UTC", nil)
	c.Request = req

	start, end := parseTimeRange(c, timezone.NewCalendar(time.Local))
	require.Equal(t, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), start)
	require.Equal(t, time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC), end)

	req = httptest.NewRequest(http.MethodGet, "/?start_date=bad&timezone=UTC", nil)
	c.Request = req
	start, end = parseTimeRange(c, timezone.NewCalendar(time.Local))
	require.False(t, start.IsZero())
	require.False(t, end.IsZero())
}

// TestParseTimeRangeInjectedCalendar checks server defaults, user overrides, and calendar-day end expansion.
func TestParseTimeRangeInjectedCalendar(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	calendar := timezone.NewCalendar(newYork)
	for _, tc := range []struct {
		name     string
		query    string
		location *time.Location
		duration time.Duration
	}{
		{name: "server_default", location: newYork, duration: 23 * time.Hour},
		{name: "invalid_user_fallback", query: "&timezone=invalid-zone", location: newYork, duration: 23 * time.Hour},
		{name: "user_override", query: "&timezone=Asia%2FShanghai", location: shanghai, duration: 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/?start_date=2026-03-08&end_date=2026-03-08"+tc.query, nil)
			start, end := parseTimeRange(c, calendar)
			require.Equal(t, time.Date(2026, 3, 8, 0, 0, 0, 0, tc.location), start)
			require.Equal(t, time.Date(2026, 3, 9, 0, 0, 0, 0, tc.location), end)
			require.Equal(t, tc.duration, end.Sub(start))
		})
	}
}
