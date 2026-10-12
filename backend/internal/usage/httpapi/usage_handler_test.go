package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/usage"
	"github.com/TokenFlux/TokenRouter/internal/usage/httpapi/ports"
)

type dailyUsageRepoStub struct {
	usage.UsageLogRepository
	trend []usage.TrendDataPoint

	called      bool
	startTime   time.Time
	endTime     time.Time
	granularity string
	userID      int64
	apiKeyID    int64
}

type dailyUsageAPIKeyRepoStub struct {
	ports.KeyReader
	keys map[int64]*ports.KeyReference
}

type dailyUsageHandlerResponse struct {
	Code int `json:"code"`
	Data struct {
		Items []usage.APIKeyDailyUsagePoint `json:"items"`
		Days  int                           `json:"days"`
	} `json:"data"`
}

type userUsageRepoCapture struct {
	usage.UsageLogRepository
	listParams   pagination.PaginationParams
	listFilters  usage.UsageLogFilters
	statsFilters usage.UsageLogFilters
	trendFilters usage.UsageLogFilters
	groupFilters usage.UsageLogFilters
	listRows     []usage.UsageLog
	stats        *usage.UsageStats
	modelStats   []usage.ModelStat
	groupStats   []usage.GroupStat
}

type usageRankingSettingRepoStub struct {
	settings.Repository
	values map[string]string
}

type usageRankingRepoCapture struct {
	usage.UsageLogRepository
	called bool
	sortBy usage.UsageRankingSortBy
}

func (s *dailyUsageRepoStub) GetUsageTrendWithFilters(
	ctx context.Context,
	startTime, endTime time.Time,
	granularity string,
	userID, apiKeyID, providerID, groupID int64,
	model string,
	requestType *int16,
	stream *bool,
	billingType *int8,
) ([]usage.TrendDataPoint, error) {
	s.called = true
	s.startTime = startTime
	s.endTime = endTime
	s.granularity = granularity
	s.userID = userID
	s.apiKeyID = apiKeyID
	return s.trend, nil
}

func (s *dailyUsageAPIKeyRepoStub) GetByID(ctx context.Context, id int64) (*ports.KeyReference, error) {
	key, ok := s.keys[id]
	if !ok {
		return nil, apikey.ErrAPIKeyNotFound
	}
	clone := *key
	return &clone, nil
}

func newDailyUsageTestRouter(usageRepo *dailyUsageRepoStub, apiKeyRepo *dailyUsageAPIKeyRepoStub, userID int64) *gin.Engine {
	usageSvc := usage.NewUsageService(usageRepo)
	// Key lookup returns ownership data for cross-user access checks.
	handler := NewUsageHandler(usageSvc, apiKeyRepo, nil, nil, timezone.NewCalendar(time.Local))
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: userID})
		c.Next()
	})
	router.GET("/user/api-keys/:id/usage/daily", handler.GetMyAPIKeyDailyUsage)
	return router
}

func TestGetMyAPIKeyDailyUsageRejectsCrossUserAccess(t *testing.T) {
	usageRepo := &dailyUsageRepoStub{}
	apiKeyRepo := &dailyUsageAPIKeyRepoStub{
		keys: map[int64]*ports.KeyReference{
			7: {ID: 7, UserID: 99},
		},
	}
	router := newDailyUsageTestRouter(usageRepo, apiKeyRepo, 42)

	req := httptest.NewRequest(http.MethodGet, "/user/api-keys/7/usage/daily?days=30", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.False(t, usageRepo.called)
}

func TestGetMyAPIKeyDailyUsageRejectsInvalidDays(t *testing.T) {
	for _, path := range []string{
		"/user/api-keys/7/usage/daily?days=0",
		"/user/api-keys/7/usage/daily?days=91",
	} {
		t.Run(path, func(t *testing.T) {
			usageRepo := &dailyUsageRepoStub{}
			apiKeyRepo := &dailyUsageAPIKeyRepoStub{
				keys: map[int64]*ports.KeyReference{
					7: {ID: 7, UserID: 42},
				},
			}
			router := newDailyUsageTestRouter(usageRepo, apiKeyRepo, 42)

			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.False(t, usageRepo.called)
		})
	}
}

func TestGetMyAPIKeyDailyUsageReturnsEmptyData(t *testing.T) {
	usageRepo := &dailyUsageRepoStub{trend: []usage.TrendDataPoint{}}
	apiKeyRepo := &dailyUsageAPIKeyRepoStub{
		keys: map[int64]*ports.KeyReference{
			7: {ID: 7, UserID: 42},
		},
	}
	router := newDailyUsageTestRouter(usageRepo, apiKeyRepo, 42)

	req := httptest.NewRequest(http.MethodGet, "/user/api-keys/7/usage/daily", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var got dailyUsageHandlerResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, 30, got.Data.Days)
	require.Empty(t, got.Data.Items)
}

func TestGetMyAPIKeyDailyUsageAggregatesByDayForOwnedKey(t *testing.T) {
	usageRepo := &dailyUsageRepoStub{
		trend: []usage.TrendDataPoint{
			{
				Date:                "2026-05-19",
				Requests:            3,
				InputTokens:         10,
				OutputTokens:        20,
				CacheCreationTokens: 4,
				CacheReadTokens:     6,
				TotalTokens:         40,
				Cost:                0.5,
				ActualCost:          0.4,
			},
		},
	}
	apiKeyRepo := &dailyUsageAPIKeyRepoStub{
		keys: map[int64]*ports.KeyReference{
			7: {ID: 7, UserID: 42},
		},
	}
	router := newDailyUsageTestRouter(usageRepo, apiKeyRepo, 42)

	req := httptest.NewRequest(http.MethodGet, "/user/api-keys/7/usage/daily?days=7", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, usageRepo.called)
	require.Equal(t, "day", usageRepo.granularity)
	require.Equal(t, int64(42), usageRepo.userID)
	require.Equal(t, int64(7), usageRepo.apiKeyID)
	require.True(t, usageRepo.startTime.Before(usageRepo.endTime))

	var got dailyUsageHandlerResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, 7, got.Data.Days)
	require.Len(t, got.Data.Items, 1)
	require.Equal(t, usage.APIKeyDailyUsagePoint{
		Date:             "2026-05-19",
		Requests:         3,
		InputTokens:      10,
		OutputTokens:     20,
		CacheReadTokens:  6,
		CacheWriteTokens: 4,
		TotalTokens:      40,
		Cost:             0.5,
		ActualCost:       0.4,
	}, got.Data.Items[0])
}

func (s *userUsageRepoCapture) ListWithFilters(ctx context.Context, params pagination.PaginationParams, filters usage.UsageLogFilters) ([]usage.UsageLog, *pagination.PaginationResult, error) {
	s.listParams = params
	s.listFilters = filters
	return s.listRows, &pagination.PaginationResult{
		Total:    int64(len(s.listRows)),
		Page:     params.Page,
		PageSize: params.PageSize,
		Pages:    0,
	}, nil
}

func (s *userUsageRepoCapture) GetStatsWithFilters(ctx context.Context, filters usage.UsageLogFilters) (*usage.UsageStats, error) {
	s.statsFilters = filters
	if s.stats != nil {
		return s.stats, nil
	}
	return &usage.UsageStats{}, nil
}

func (s *userUsageRepoCapture) GetUsageTrendWithFilters(ctx context.Context, startTime, endTime time.Time, granularity string, userID, apiKeyID, providerID, groupID int64, model string, requestType *int16, stream *bool, billingType *int8) ([]usage.TrendDataPoint, error) {
	s.trendFilters = usage.UsageLogFilters{
		UserID:      userID,
		APIKeyID:    apiKeyID,
		ProviderID:  providerID,
		GroupID:     groupID,
		Model:       model,
		RequestType: requestType,
		Stream:      stream,
		BillingType: billingType,
	}
	return []usage.TrendDataPoint{}, nil
}

func (s *userUsageRepoCapture) GetUsageTrendWithUsageFilters(_ context.Context, _, _ time.Time, _ string, filters usage.UsageLogFilters) ([]usage.TrendDataPoint, error) {
	s.trendFilters = filters
	return []usage.TrendDataPoint{}, nil
}

func (s *userUsageRepoCapture) GetModelStatsWithFilters(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, providerID, groupID int64, requestType *int16, stream *bool, billingType *int8) ([]usage.ModelStat, error) {
	return s.modelStats, nil
}

func (s *userUsageRepoCapture) GetGroupStatsWithFilters(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, providerID, groupID int64, requestType *int16, stream *bool, billingType *int8) ([]usage.GroupStat, error) {
	s.groupFilters = usage.UsageLogFilters{
		UserID:      userID,
		APIKeyID:    apiKeyID,
		ProviderID:  providerID,
		GroupID:     groupID,
		RequestType: requestType,
		Stream:      stream,
		BillingType: billingType,
	}
	return s.groupStats, nil
}

func (s *userUsageRepoCapture) GetGroupStatsWithUsageFilters(_ context.Context, _, _ time.Time, filters usage.UsageLogFilters) ([]usage.GroupStat, error) {
	s.groupFilters = filters
	return s.groupStats, nil
}

func newUserUsageRequestTypeTestRouter(repo *userUsageRepoCapture) *gin.Engine {
	usageSvc := usage.NewUsageService(repo)
	handler := NewUsageHandler(usageSvc, nil, nil, nil, timezone.NewCalendar(time.Local))
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 42})
		c.Next()
	})
	router.GET("/usage", handler.List)
	router.GET("/usage/stats", handler.Stats)
	router.GET("/usage/dashboard/models", handler.DashboardModels)
	router.GET("/usage/dashboard/snapshot-v2", handler.DashboardSnapshotV2)
	return router
}

func TestUserUsageListRequestTypePriority(t *testing.T) {
	repo := &userUsageRepoCapture{}
	router := newUserUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/usage?request_type=ws_v2&stream=bad", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(42), repo.listFilters.UserID)
	require.True(t, repo.listFilters.IncludeOwnedTeam)
	require.False(t, repo.listFilters.PersonalOnly)
	require.NotNil(t, repo.listFilters.RequestType)
	require.Equal(t, int16(usage.RequestTypeWSV2), *repo.listFilters.RequestType)
	require.Nil(t, repo.listFilters.Stream)
}

func TestUserUsageListInvalidRequestType(t *testing.T) {
	repo := &userUsageRepoCapture{}
	router := newUserUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/usage?request_type=invalid", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUserUsageListInvalidStream(t *testing.T) {
	repo := &userUsageRepoCapture{}
	router := newUserUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/usage?stream=invalid", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestParseUsageRankingTimeRangeDefaultsToToday(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/usage/ranking?timezone=Asia/Shanghai", nil)
	now := time.Date(2026, 5, 6, 15, 30, 0, 0, time.FixedZone("CST", 8*3600))

	start, end, err := parseUsageRankingTimeRange(c, now, "Asia/Shanghai", timezone.NewCalendar(time.Local))

	require.NoError(t, err)
	require.Equal(t, "2026-05-06 00:00:00 +0800 CST", start.String())
	require.Equal(t, "2026-05-07 00:00:00 +0800 CST", end.String())
}

func TestParseUsageRankingTimeRangeDateOnlyEndIsInclusive(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/usage/ranking?start_date=2026-05-01&end_date=2026-05-03&timezone=Asia/Shanghai", nil)
	now := time.Date(2026, 5, 6, 15, 30, 0, 0, time.UTC)

	start, end, err := parseUsageRankingTimeRange(c, now, "Asia/Shanghai", timezone.NewCalendar(time.Local))

	require.NoError(t, err)
	require.Equal(t, "2026-05-01 00:00:00 +0800 CST", start.String())
	require.Equal(t, "2026-05-04 00:00:00 +0800 CST", end.String())
	require.Equal(t, "2026-05-03", usageRankingDisplayEndDate(end))
}

func TestParseUsageRankingTimeRangeRejectsInvalidRange(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/usage/ranking?start_date=2026-05-03&end_date=2026-05-01&timezone=Asia/Shanghai", nil)
	now := time.Date(2026, 5, 6, 15, 30, 0, 0, time.UTC)

	_, _, err := parseUsageRankingTimeRange(c, now, "Asia/Shanghai", timezone.NewCalendar(time.Local))

	require.ErrorContains(t, err, "end_date must be later than start_date")
}

func TestUserUsageListAdvancedFilters(t *testing.T) {
	repo := &userUsageRepoCapture{}
	router := newUserUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/usage?group_id=7&model=gpt-5&billing_type=1&billing_mode=image&start_date=2026-03-01&end_date=2026-03-02", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(42), repo.listFilters.UserID)
	require.Equal(t, int64(7), repo.listFilters.GroupID)
	require.Equal(t, "gpt-5", repo.listFilters.Model)
	require.Equal(t, usage.ModelSourceRequested, repo.listFilters.ModelFilterSource)
	require.NotNil(t, repo.listFilters.BillingType)
	require.Equal(t, int8(1), *repo.listFilters.BillingType)
	require.Equal(t, "image", repo.listFilters.BillingMode)
	require.NotNil(t, repo.listFilters.StartTime)
	require.NotNil(t, repo.listFilters.EndTime)
}

func TestUserUsageListInvalidBillingMode(t *testing.T) {
	repo := &userUsageRepoCapture{}
	router := newUserUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/usage?billing_mode=bad", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUserUsageListAllowsVideoBillingMode(t *testing.T) {
	repo := &userUsageRepoCapture{}
	router := newUserUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/usage?billing_mode=video", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "video", repo.listFilters.BillingMode)
}

func TestUserUsageListKeepsUserBillingAndIPWithoutAdminCostFields(t *testing.T) {
	ipAddress := "203.0.113.10"
	upstreamModel := "upstream-private-model"
	billingTier := "internal-tier"
	pricingConfigID := int64(99)
	providerRateMultiplier := 1.7
	providerStatsCost := 0.12
	repo := &userUsageRepoCapture{
		listRows: []usage.UsageLog{{
			ID:                     1,
			UserID:                 42,
			APIKeyID:               7,
			ProviderID:             5,
			RequestID:              "req_user_billing",
			Model:                  "gpt-5",
			InputCost:              0.01,
			OutputCost:             0.02,
			CacheCreationCost:      0.03,
			CacheReadCost:          0.04,
			TotalCost:              0.10,
			ActualCost:             0.08,
			RateMultiplier:         0.8,
			IPAddress:              &ipAddress,
			UpstreamModel:          &upstreamModel,
			BillingTier:            &billingTier,
			PricingConfigID:        &pricingConfigID,
			ProviderRateMultiplier: &providerRateMultiplier,
			ProviderStatsCost:      &providerStatsCost,
		}},
	}
	router := newUserUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/usage", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, `"input_cost":0.01`)
	require.Contains(t, body, `"output_cost":0.02`)
	require.Contains(t, body, `"cache_creation_cost":0.03`)
	require.Contains(t, body, `"cache_read_cost":0.04`)
	require.Contains(t, body, `"total_cost":0.1`)
	require.Contains(t, body, `"actual_cost":0.08`)
	require.Contains(t, body, `"rate_multiplier":0.8`)
	require.Contains(t, body, `"ip_address":"203.0.113.10"`)
	require.NotContains(t, body, "upstream_endpoint")
	require.NotContains(t, body, "provider_rate_multiplier")
	require.NotContains(t, body, "provider_stats_cost")
	require.NotContains(t, body, "upstream_model")
	require.NotContains(t, body, "billing_tier")
	require.NotContains(t, body, "pricing_config_id")
	require.NotContains(t, body, `"provider":`)
}

func TestUserUsageStatsUsesScopedFilters(t *testing.T) {
	providerCost := 0.12
	repo := &userUsageRepoCapture{
		stats: &usage.UsageStats{
			TotalCost:         0.10,
			TotalActualCost:   0.08,
			TotalProviderCost: &providerCost,
			UpstreamEndpoints: []usage.EndpointStat{{
				Endpoint: "/v1/responses",
			}},
			EndpointPaths: []usage.EndpointStat{{
				Endpoint: "/v1/chat/completions -> /v1/responses",
			}},
		},
	}
	router := newUserUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/usage/stats?group_id=9&request_type=sync&billing_mode=token&start_date=2026-03-01&end_date=2026-03-02", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(42), repo.statsFilters.UserID)
	require.True(t, repo.statsFilters.IncludeOwnedTeam)
	require.False(t, repo.statsFilters.PersonalOnly)
	require.Equal(t, int64(9), repo.statsFilters.GroupID)
	require.Equal(t, usage.ModelSourceRequested, repo.statsFilters.ModelFilterSource)
	require.NotNil(t, repo.statsFilters.RequestType)
	require.Equal(t, int16(usage.RequestTypeSync), *repo.statsFilters.RequestType)
	require.Equal(t, "token", repo.statsFilters.BillingMode)
	require.Contains(t, rec.Body.String(), `"total_cost":0.1`)
	require.Contains(t, rec.Body.String(), `"total_actual_cost":0.08`)
	require.NotContains(t, rec.Body.String(), "total_provider_cost")
	require.NotContains(t, rec.Body.String(), "upstream_endpoints")
	require.NotContains(t, rec.Body.String(), "endpoint_paths")
}

func TestUserUsageDashboardModelsOmitsProviderCost(t *testing.T) {
	repo := &userUsageRepoCapture{
		modelStats: []usage.ModelStat{{
			Model:        "gpt-5",
			Requests:     2,
			TotalTokens:  30,
			Cost:         0.10,
			ActualCost:   0.08,
			ProviderCost: 0.07,
		}},
	}
	router := newUserUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/usage/dashboard/models?start_date=2026-03-01&end_date=2026-03-02", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, `"cost":0.1`)
	require.Contains(t, body, `"actual_cost":0.08`)
	require.NotContains(t, body, "provider_cost")
}

func TestUserUsageDashboardModelsRejectsAdminModelSources(t *testing.T) {
	repo := &userUsageRepoCapture{}
	router := newUserUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/usage/dashboard/models?model_source=upstream&start_date=2026-03-01&end_date=2026-03-02", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUserUsageSnapshotUsesScopedFilters(t *testing.T) {
	repo := &userUsageRepoCapture{
		modelStats: []usage.ModelStat{{Model: "gpt-5", ProviderCost: 0.07}},
		groupStats: []usage.GroupStat{{GroupID: 1, GroupName: "default", ProviderCost: 0.06}},
	}
	router := newUserUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/usage/dashboard/snapshot-v2?include_trend=true&include_model_stats=true&include_group_stats=true&group_id=11&request_type=stream&start_date=2026-03-01&end_date=2026-03-02", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(42), repo.trendFilters.UserID)
	require.True(t, repo.trendFilters.IncludeOwnedTeam)
	require.False(t, repo.trendFilters.PersonalOnly)
	require.Equal(t, int64(11), repo.trendFilters.GroupID)
	require.NotNil(t, repo.trendFilters.RequestType)
	require.Equal(t, int16(usage.RequestTypeStream), *repo.trendFilters.RequestType)
	require.Equal(t, int64(42), repo.groupFilters.UserID)
	require.True(t, repo.groupFilters.IncludeOwnedTeam)
	require.False(t, repo.groupFilters.PersonalOnly)
	require.Equal(t, int64(11), repo.groupFilters.GroupID)
	require.NotContains(t, rec.Body.String(), "provider_cost")
}

func TestUserUsageSnapshotRejectsInvalidIncludeFlags(t *testing.T) {
	repo := &userUsageRepoCapture{}
	router := newUserUsageRequestTypeTestRouter(repo)

	for _, query := range []string{
		"include_trend=bad",
		"include_model_stats=bad",
		"include_group_stats=bad",
	} {
		req := httptest.NewRequest(http.MethodGet, "/usage/dashboard/snapshot-v2?start_date=2026-03-01&end_date=2026-03-02&"+query, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		require.Equal(t, http.StatusBadRequest, rec.Code, query)
	}
}

func TestUserUsageListSortParams(t *testing.T) {
	repo := &userUsageRepoCapture{}
	router := newUserUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/usage?sort_by=model&sort_order=ASC", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "model", repo.listParams.SortBy)
	require.Equal(t, "ASC", repo.listParams.SortOrder)
}

func TestUserUsageListSortDefaults(t *testing.T) {
	repo := &userUsageRepoCapture{}
	router := newUserUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/usage", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "created_at", repo.listParams.SortBy)
	require.Equal(t, "desc", repo.listParams.SortOrder)
}

func (s *usageRankingSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}

func (r *usageRankingRepoCapture) GetUsageRanking(_ context.Context, _, _ time.Time, _ int, sortBy usage.UsageRankingSortBy) (*usage.UsageRankingResponse, error) {
	r.called = true
	r.sortBy = sortBy
	return &usage.UsageRankingResponse{
		Ranking: []usage.UsageRankingItem{{
			Rank:                1,
			UserID:              7,
			DisplayName:         "ranked-user",
			Requests:            8,
			InputTokens:         100,
			OutputTokens:        200,
			CacheCreationTokens: 30,
			CacheReadTokens:     40,
			TotalTokens:         370,
			ActualCost:          12.5,
		}},
		TotalRequests:   8,
		TotalTokens:     370,
		TotalActualCost: 12.5,
	}, nil
}

func newUsageRankingSettingsRouter(repo *usageRankingRepoCapture, values map[string]string) *gin.Engine {
	settingSvc := usage.NewRuntimeSettings(&usageRankingSettingRepoStub{values: values})
	h := NewUsageHandler(usage.NewUsageService(repo), nil, nil, settingSvc, timezone.NewCalendar(time.Local))
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 42})
		c.Next()
	})
	router.GET("/usage/ranking", h.Ranking)
	return router
}

func TestUsageRankingDisabledRejectsBeforeQuery(t *testing.T) {
	repo := &usageRankingRepoCapture{}
	router := newUsageRankingSettingsRouter(repo, map[string]string{
		usage.SettingKeyUsageRankingEnabled: "false",
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/usage/ranking", nil))

	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.False(t, repo.called)
}

func TestUsageRankingProjectsHiddenFieldsAndUsesConfiguredSort(t *testing.T) {
	repo := &usageRankingRepoCapture{}
	router := newUsageRankingSettingsRouter(repo, map[string]string{
		usage.SettingKeyUsageRankingEnabled:         "true",
		usage.SettingKeyUsageRankingSortBy:          string(usage.UsageRankingSortByRequests),
		usage.SettingKeyUsageRankingShowTotalTokens: "false",
		usage.SettingKeyUsageRankingShowRequests:    "false",
		usage.SettingKeyUsageRankingShowActualCost:  "false",
		usage.SettingKeyUsageRankingLimit:           "12",
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/usage/ranking", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	require.True(t, repo.called)
	require.Equal(t, usage.UsageRankingSortByRequests, repo.sortBy)

	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(envelope.Data, &payload))
	require.Contains(t, payload, "total_requests")
	require.NotContains(t, payload, "total_tokens")
	require.NotContains(t, payload, "total_actual_cost")
	require.Equal(t, json.RawMessage(`true`), payload["show_requests"])
	require.Equal(t, json.RawMessage(`false`), payload["show_total_tokens"])
	require.Equal(t, json.RawMessage(`false`), payload["show_actual_cost"])

	var rows []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(payload["ranking"], &rows))
	require.Len(t, rows, 1)
	require.Contains(t, rows[0], "requests")
	require.NotContains(t, rows[0], "total_tokens")
	require.NotContains(t, rows[0], "input_tokens")
	require.NotContains(t, rows[0], "output_tokens")
	require.NotContains(t, rows[0], "cache_creation_tokens")
	require.NotContains(t, rows[0], "cache_read_tokens")
	require.NotContains(t, rows[0], "actual_cost")
}

// TestRequestIDFiltersKeepScopeAndUseFullHistory checks that ID lookup retains user scope and skips default date limits.
func TestRequestIDFiltersKeepScopeAndUseFullHistory(t *testing.T) {
	repo := &userUsageRepoCapture{}
	router := newUserUsageRequestTypeTestRouter(repo)
	for _, path := range []string{"/usage", "/usage/stats"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path+"?request_id=%20client%3Ahistory%20&user_id=999", nil))
		require.Equal(t, http.StatusOK, response.Code)
	}
	require.Equal(t, "client:history", repo.listFilters.RequestID)
	require.Equal(t, "client:history", repo.statsFilters.RequestID)
	require.Equal(t, int64(42), repo.listFilters.UserID)
	require.Equal(t, int64(42), repo.statsFilters.UserID)
	require.Nil(t, repo.listFilters.StartTime)
	require.Nil(t, repo.statsFilters.StartTime)
	require.Nil(t, repo.statsFilters.EndTime)
}
