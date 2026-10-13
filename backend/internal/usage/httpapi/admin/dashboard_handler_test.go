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

// Administrator chart handlers can reuse identical service-cache queries or
// separate them by team. Capture fixtures inspect request-type precedence,
// model-source acceptance, ranking limits and user-breakdown selectors before
// the HTTP response is decoded.
//
// Date tests use explicit calendars to check inclusive date input across a
// spring DST transition. Response cases inspect selected fields and limits
// rather than database grouping; for example, the accepted upstream-source
// fixture only checks HTTP success through a repository lacking that optional
// source capability.

// dashboardUsageRepoCacheProbe counts trend loader invocations through atomic
// fixture counters.
type dashboardUsageRepoCacheProbe struct {
	usage.UsageLogRepository              // unused usage repository method slots
	trendCalls               atomic.Int32 // atomic trend loader invocation count
	usersTrendCalls          atomic.Int32 // atomic payer trend loader count
}

// dashboardUsageRepoCapture records parsed category selectors and supplies
// configured spending rows for HTTP tests.
type dashboardUsageRepoCapture struct {
	usage.UsageLogRepository                                 // unused usage repository method slots
	trendRequestType         *int16                          // captured trend category pointer
	trendStream              *bool                           // captured trend stream pointer
	modelRequestType         *int16                          // captured model category pointer
	modelStream              *bool                           // captured model stream pointer
	rankingLimit             int                             // captured applied ranking limit
	ranking                  []usage.UserSpendingRankingItem // configured spending ranking rows
	rankingTotal             float64                         // configured full range charge total
}

// userBreakdownRepoCapture records supplied breakdown dimensions and row
// limits for administrator requests.
type userBreakdownRepoCapture struct {
	usage.UsageLogRepository                              // unused usage repository method slots
	capturedDim              usage.UserBreakdownDimension // captured breakdown query dimensions
	capturedLimit            int                          // captured breakdown result limit
	result                   []usage.UserBreakdownItem    // configured shared payer breakdown rows
}

// GetUsageTrendWithFilters counts the service-cache load and returns fixed
// trend fields for HTTP assertions.
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

// GetUserUsageTrend counts the service-cache load and returns fixed payer
// trend fields for HTTP assertions.
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

// resetDashboardReadCachesForTest replaces the process-wide composite
// snapshot cache with an empty cache. These tests invoke resets serially;
// individual dashboard services construct their own chart caches for each
// fixture.
func resetDashboardReadCachesForTest() {
	dashboardSnapshotV2Cache = newSnapshotCache(30 * time.Second)
}

// TestDashboardHandler_GetUsageTrend_UsesCache repeats identical trend
// requests. It requires a miss followed by a hit, successful responses and
// exactly one repository trend load.
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

// TestDashboardHandler_GetUsageTrend_SeparatesTeams requests otherwise
// identical ranges for two teams. It requires two cache misses and two loader
// calls for those tested IDs.
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

// TestDashboardHandler_GetUserUsageTrend_UsesCache repeats one payer trend
// range and limit. It requires successful miss then hit responses and one
// repository trend load.
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

// GetUsageTrendWithFilters captures request category and stream pointers for
// the synchronous HTTP test query.
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

// GetModelStatsWithFilters captures request category and stream pointers for
// the synchronous model HTTP test query.
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

// GetUserSpendingRanking captures the applied limit and returns configured
// rows plus fixed range totals.
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

// newDashboardRequestTypeTestRouter constructs trend, model and ranking
// routes with the supplied capture fixture.
func newDashboardRequestTypeTestRouter(repo *dashboardUsageRepoCapture) *gin.Engine {
	dashboardSvc := usage.NewDashboardService(repo, nil, nil, nil)
	handler := NewDashboardHandler(dashboardSvc, timezone.NewCalendar(time.Local))
	router := gin.New()
	router.GET("/admin/dashboard/trend", handler.GetUsageTrend)
	router.GET("/admin/dashboard/models", handler.GetModelStats)
	router.GET("/admin/dashboard/users-ranking", handler.GetUserSpendingRanking)
	return router
}

// TestDashboardTrendRequestTypePriority sends WS v2 beside malformed legacy
// stream text. It requires HTTP success, a captured WS v2 category and absent
// stream selector.
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

// TestDashboardTrendInvalidRequestType submits unsupported category text to
// the trend route. It requires a bad-request response from that handler's
// category validation path.
func TestDashboardTrendInvalidRequestType(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/trend?request_type=bad", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestDashboardTrendInvalidStream submits malformed stream text with no
// category. It requires a bad-request response from the trend handler's
// legacy boolean validation path.
func TestDashboardTrendInvalidStream(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/trend?stream=bad", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestDashboardModelStatsRequestTypePriority sends sync beside malformed
// stream text to model grouping. It requires success, the category pointer
// and an absent stream selector.
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

// TestDashboardModelStatsInvalidRequestType submits unsupported category text
// to model grouping. It requires a bad-request response from the handler
// before the request can succeed.
func TestDashboardModelStatsInvalidRequestType(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/models?request_type=bad", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestDashboardModelStatsInvalidStream submits malformed legacy stream text
// without a category. It requires a bad-request response from the model
// grouping handler for that input.
func TestDashboardModelStatsInvalidStream(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/models?stream=bad", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestDashboardModelStatsInvalidModelSource submits an unsupported reporting
// source. It requires a bad-request response from the model handler's source
// validation for the supplied query.
func TestDashboardModelStatsInvalidModelSource(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/models?model_source=invalid", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestDashboardModelStatsValidModelSource submits the supported upstream
// reporting label. It requires HTTP success through this fixture; the
// assertions do not inspect the returned model dimension.
func TestDashboardModelStatsValidModelSource(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/models?model_source=upstream", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

// TestDashboardUsersRankingLimitAndCache submits an oversized ranking limit
// twice. It checks the fifty-row applied limit, full-range totals and the
// response's miss-to-hit cache transition.
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

// GetUserBreakdownStats records dimensions and limit before returning the
// configured result. Missing result data produces an allocated empty slice,
// allowing response-shape cases to inspect an empty successful query.
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

// newUserBreakdownRouter constructs the administrator breakdown route with
// its supplied capture fixture.
func newUserBreakdownRouter(repo *userBreakdownRepoCapture) *gin.Engine {
	svc := usage.NewDashboardService(repo, nil, nil, nil)
	h := NewDashboardHandler(svc, timezone.NewCalendar(time.Local))
	router := gin.New()
	router.GET("/admin/dashboard/user-breakdown", h.GetUserBreakdown)
	return router
}

// TestGetUserBreakdown_GroupIDFilter supplies only a group dimension. It
// checks the captured group, absent other dimensions, default fifty-row limit
// and empty sort text for repository defaulting.
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

// TestGetUserBreakdown_SortBy supplies the total_tokens ordering field. It
// requires successful HTTP handling and that exact sort text in the captured
// repository dimension.
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

// TestGetUserBreakdown_ModelFilter supplies a model without a group selector.
// It checks the captured model, requested-source default and zero group scope
// alongside HTTP success.
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

// TestGetUserBreakdown_ModelSourceFilter supplies a model and upstream source
// label. It requires successful handling and the upstream source in the
// captured breakdown dimension.
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

// TestGetUserBreakdown_InvalidModelSource supplies unsupported dimension text
// to user breakdown. It requires the handler to return a bad-request response
// for that source selection.
func TestGetUserBreakdown_InvalidModelSource(t *testing.T) {
	repo := &userBreakdownRepoCapture{}
	router := newUserBreakdownRouter(repo)

	req := httptest.NewRequest(http.MethodGet,
		"/admin/dashboard/user-breakdown?start_date=2026-03-01&end_date=2026-03-16&model_source=foobar", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

// TestGetUserBreakdown_EndpointFilter supplies an upstream endpoint value and
// type. It requires successful handling and both exact values in the captured
// repository dimension.
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

// TestGetUserBreakdown_DefaultEndpointType supplies endpoint text without an
// endpoint type. It requires successful handling and the captured inbound
// endpoint-type default for repository selection.
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

// TestGetUserBreakdown_CustomLimit supplies one hundred as a valid row limit.
// It requires HTTP success and that value in the captured repository query
// call.
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

// TestGetUserBreakdown_LimitClamped supplies a value above two hundred. It
// requires HTTP success and the default fifty-row limit, rather than a
// two-hundred-row capped result.
func TestGetUserBreakdown_LimitClamped(t *testing.T) {
	repo := &userBreakdownRepoCapture{}
	router := newUserBreakdownRouter(repo)

	req := httptest.NewRequest(http.MethodGet,
		"/admin/dashboard/user-breakdown?start_date=2026-03-01&end_date=2026-03-16&model=test&limit=999", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 50, repo.capturedLimit)
}

// TestGetUserBreakdown_ResponseFormat returns two configured payer rows for
// an explicit range. It checks envelope status, selected first-row values,
// row count and displayed start and end dates.
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
		Code int `json:"code"` // decoded envelope result code
		Data struct {
			Users     []usage.UserBreakdownItem `json:"users"`      // decoded payer breakdown rows
			StartDate string                    `json:"start_date"` // decoded displayed start date
			EndDate   string                    `json:"end_date"`   // decoded displayed end date
		} `json:"data"` // decoded dashboard response payload
	}
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Equal(t, 0, resp.Code)
	require.Len(t, resp.Data.Users, 2)
	require.Equal(t, int64(1), resp.Data.Users[0].UserID)
	require.Equal(t, "alice@test.com", resp.Data.Users[0].Email)
	require.Equal(t, int64(100), resp.Data.Users[0].Requests)
	// Permit the fixture's small floating-point decoding tolerance for this charge.
	require.InDelta(t, 1.2, resp.Data.Users[0].ActualCost, 0.001)
	require.Equal(t, "2026-03-01", resp.Data.StartDate)
	require.Equal(t, "2026-03-16", resp.Data.EndDate)
}

// TestGetUserBreakdown_EmptyResult queries a fixture with no configured rows.
// It requires a successful response whose decoded users collection is empty
// for that range.
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
			Users []usage.UserBreakdownItem `json:"users"` // decoded payer breakdown rows
		} `json:"data"` // decoded dashboard response payload
	}
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Empty(t, resp.Data.Users)
}

// TestGetUserBreakdown_NoFilters supplies an explicit range with no breakdown
// dimension. It requires success and captured zero group, empty model and
// empty endpoint selectors.
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

// TestGetUserBreakdown_RequestTypeStringFilter supplies the four category
// labels listed in its table. It checks successful responses and the
// corresponding stored integer selector for each fixture.
func TestGetUserBreakdown_RequestTypeStringFilter(t *testing.T) {
	cases := []struct {
		name  string // subtest case label
		value string // input category label text
		want  int16  // expected stored category selector
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

// TestGetUserBreakdown_InvalidRequestType submits unsupported category text
// to the breakdown route. It requires a bad-request response for that invalid
// request-type filter value.
func TestGetUserBreakdown_InvalidRequestType(t *testing.T) {
	repo := &userBreakdownRepoCapture{}
	router := newUserBreakdownRouter(repo)

	req := httptest.NewRequest(http.MethodGet,
		"/admin/dashboard/user-breakdown?start_date=2026-03-01&end_date=2026-03-16&request_type=bogus", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

// TestParseTimeRange checks an explicit UTC date pair and malformed start
// text. It requires the exact expanded pair first, then only nonzero fallback
// bounds in the invalid case.
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

// TestParseTimeRangeInjectedCalendar supplies a spring-DST date under server,
// invalid-user and valid-user zones. It checks exact local midnights and
// their expected twenty-three or twenty-four-hour durations.
func TestParseTimeRangeInjectedCalendar(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	calendar := timezone.NewCalendar(newYork)
	for _, tc := range []struct {
		name     string         // subtest case label
		query    string         // input timezone query suffix
		location *time.Location // expected date parsing location
		duration time.Duration  // expected elapsed range duration
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
