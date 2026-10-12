package admin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
	"github.com/TokenFlux/TokenRouter/internal/usage"
	"github.com/TokenFlux/TokenRouter/internal/usage/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/usage/httpapi/ports"
)

var _ usage.UsageCleanupRepository = (*cleanupRepoStub)(nil)

type cleanupRepoStub struct {
	mu         sync.Mutex
	created    []*usage.UsageCleanupTask
	listTasks  []usage.UsageCleanupTask
	listResult *pagination.PaginationResult
	listErr    error
	statusByID map[int64]string
}

type adminUsageRepoCapture struct {
	usage.UsageLogRepository
	listParams   pagination.PaginationParams
	listFilters  usage.UsageLogFilters
	statsFilters usage.UsageLogFilters
	logs         []usage.UsageLog
}

type adminUsageTimingOpsRepo struct {
	ops.OpsRepository
	timings map[string]*ops.OpsRequestTiming
}

// searchUsersAdminStub captures query filters and includes deleted users in its results.
type searchUsersAdminStub struct {
	gotFilters ports.UserListFilters
}

func (s *cleanupRepoStub) CreateTask(ctx context.Context, task *usage.UsageCleanupTask) error {
	if task == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if task.ID == 0 {
		task.ID = int64(len(s.created) + 1)
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now().UTC()
	}
	task.UpdatedAt = task.CreatedAt
	clone := *task
	s.created = append(s.created, &clone)
	return nil
}

func (s *cleanupRepoStub) ListTasks(ctx context.Context, params pagination.PaginationParams) ([]usage.UsageCleanupTask, *pagination.PaginationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listTasks, s.listResult, s.listErr
}

func (s *cleanupRepoStub) ClaimNextPendingTask(ctx context.Context, staleRunningAfterSeconds int64) (*usage.UsageCleanupTask, error) {
	return nil, nil
}

func (s *cleanupRepoStub) GetTaskStatus(ctx context.Context, taskID int64) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statusByID == nil {
		return "", errors.Join(usage.ErrCleanupTaskNotFound, sql.ErrNoRows)
	}
	status, ok := s.statusByID[taskID]
	if !ok {
		return "", errors.Join(usage.ErrCleanupTaskNotFound, sql.ErrNoRows)
	}
	return status, nil
}

func (s *cleanupRepoStub) UpdateTaskProgress(ctx context.Context, taskID int64, deletedRows int64) error {
	return nil
}

func (s *cleanupRepoStub) CancelTask(ctx context.Context, taskID int64, canceledBy int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statusByID == nil {
		s.statusByID = map[int64]string{}
	}
	status := s.statusByID[taskID]
	if status != usage.UsageCleanupStatusPending && status != usage.UsageCleanupStatusRunning {
		return false, nil
	}
	s.statusByID[taskID] = usage.UsageCleanupStatusCanceled
	return true, nil
}

func (s *cleanupRepoStub) MarkTaskSucceeded(ctx context.Context, taskID int64, deletedRows int64) error {
	return nil
}

func (s *cleanupRepoStub) MarkTaskFailed(ctx context.Context, taskID int64, deletedRows int64, errorMsg string) error {
	return nil
}

func (s *cleanupRepoStub) DeleteUsageLogsBatch(ctx context.Context, filters usage.UsageCleanupFilters, limit int) (int64, error) {
	return 0, nil
}

func setupCleanupRouter(cleanupService *usage.UsageCleanupService, userID int64) *gin.Engine {
	router := gin.New()
	if userID > 0 {
		router.Use(func(c *gin.Context) {
			c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: userID})
			c.Next()
		})
	}

	handler := NewUsageHandler(nil, nil, nil, cleanupService, nil, timezone.NewCalendar(time.Local))
	router.POST("/api/v1/admin/usage/cleanup-tasks", handler.CreateCleanupTask)
	router.GET("/api/v1/admin/usage/cleanup-tasks", handler.ListCleanupTasks)
	router.POST("/api/v1/admin/usage/cleanup-tasks/:id/cancel", handler.CancelCleanupTask)
	return router
}

func TestUsageHandlerCreateCleanupTaskUnauthorized(t *testing.T) {
	repo := &cleanupRepoStub{}
	cfg := &usage.Options{UsageCleanup: usage.UsageCleanupConfig{Enabled: true, MaxRangeDays: 31}}
	cleanupService := usage.NewUsageCleanupService(repo, nil, nil, cfg)
	router := setupCleanupRouter(cleanupService, 0)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/usage/cleanup-tasks", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestUsageHandlerCreateCleanupTaskUnavailable(t *testing.T) {
	router := setupCleanupRouter(nil, 1)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/usage/cleanup-tasks", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestUsageHandlerCreateCleanupTaskBindError(t *testing.T) {
	repo := &cleanupRepoStub{}
	cfg := &usage.Options{UsageCleanup: usage.UsageCleanupConfig{Enabled: true, MaxRangeDays: 31}}
	cleanupService := usage.NewUsageCleanupService(repo, nil, nil, cfg)
	router := setupCleanupRouter(cleanupService, 88)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/usage/cleanup-tasks", bytes.NewBufferString("{bad-json"))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestUsageHandlerCreateCleanupTaskMissingRange(t *testing.T) {
	repo := &cleanupRepoStub{}
	cfg := &usage.Options{UsageCleanup: usage.UsageCleanupConfig{Enabled: true, MaxRangeDays: 31}}
	cleanupService := usage.NewUsageCleanupService(repo, nil, nil, cfg)
	router := setupCleanupRouter(cleanupService, 88)

	payload := map[string]any{
		"start_date": "2024-01-01",
		"timezone":   "UTC",
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/usage/cleanup-tasks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestUsageHandlerCreateCleanupTaskInvalidDate(t *testing.T) {
	repo := &cleanupRepoStub{}
	cfg := &usage.Options{UsageCleanup: usage.UsageCleanupConfig{Enabled: true, MaxRangeDays: 31}}
	cleanupService := usage.NewUsageCleanupService(repo, nil, nil, cfg)
	router := setupCleanupRouter(cleanupService, 88)

	payload := map[string]any{
		"start_date": "2024-13-01",
		"end_date":   "2024-01-02",
		"timezone":   "UTC",
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/usage/cleanup-tasks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestUsageHandlerCreateCleanupTaskInvalidEndDate(t *testing.T) {
	repo := &cleanupRepoStub{}
	cfg := &usage.Options{UsageCleanup: usage.UsageCleanupConfig{Enabled: true, MaxRangeDays: 31}}
	cleanupService := usage.NewUsageCleanupService(repo, nil, nil, cfg)
	router := setupCleanupRouter(cleanupService, 88)

	payload := map[string]any{
		"start_date": "2024-01-01",
		"end_date":   "2024-02-40",
		"timezone":   "UTC",
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/usage/cleanup-tasks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestUsageHandlerCreateCleanupTaskInvalidRequestType(t *testing.T) {
	repo := &cleanupRepoStub{}
	cfg := &usage.Options{UsageCleanup: usage.UsageCleanupConfig{Enabled: true, MaxRangeDays: 31}}
	cleanupService := usage.NewUsageCleanupService(repo, nil, nil, cfg)
	router := setupCleanupRouter(cleanupService, 88)

	payload := map[string]any{
		"start_date":   "2024-01-01",
		"end_date":     "2024-01-02",
		"timezone":     "UTC",
		"request_type": "invalid",
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/usage/cleanup-tasks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestUsageHandlerCreateCleanupTaskRequestTypePriority(t *testing.T) {
	repo := &cleanupRepoStub{}
	cfg := &usage.Options{UsageCleanup: usage.UsageCleanupConfig{Enabled: true, MaxRangeDays: 31}}
	cleanupService := usage.NewUsageCleanupService(repo, nil, nil, cfg)
	router := setupCleanupRouter(cleanupService, 99)

	payload := map[string]any{
		"start_date":   "2024-01-01",
		"end_date":     "2024-01-02",
		"timezone":     "UTC",
		"request_type": "ws_v2",
		"stream":       false,
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/usage/cleanup-tasks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.created, 1)
	created := repo.created[0]
	require.NotNil(t, created.Filters.RequestType)
	require.Equal(t, int16(usage.RequestTypeWSV2), *created.Filters.RequestType)
	require.Nil(t, created.Filters.Stream)
}

func TestUsageHandlerCreateCleanupTaskWithLegacyStream(t *testing.T) {
	repo := &cleanupRepoStub{}
	cfg := &usage.Options{UsageCleanup: usage.UsageCleanupConfig{Enabled: true, MaxRangeDays: 31}}
	cleanupService := usage.NewUsageCleanupService(repo, nil, nil, cfg)
	router := setupCleanupRouter(cleanupService, 99)

	payload := map[string]any{
		"start_date": "2024-01-01",
		"end_date":   "2024-01-02",
		"timezone":   "UTC",
		"stream":     true,
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/usage/cleanup-tasks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.created, 1)
	created := repo.created[0]
	require.Nil(t, created.Filters.RequestType)
	require.NotNil(t, created.Filters.Stream)
	require.True(t, *created.Filters.Stream)
}

func TestUsageHandlerCreateCleanupTaskSuccess(t *testing.T) {
	repo := &cleanupRepoStub{}
	cfg := &usage.Options{UsageCleanup: usage.UsageCleanupConfig{Enabled: true, MaxRangeDays: 31}}
	cleanupService := usage.NewUsageCleanupService(repo, nil, nil, cfg)
	router := setupCleanupRouter(cleanupService, 99)

	payload := map[string]any{
		"start_date": " 2024-01-01 ",
		"end_date":   "2024-01-02",
		"timezone":   "UTC",
		"model":      "gpt-4",
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/usage/cleanup-tasks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)

	var resp response.Response
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.created, 1)
	created := repo.created[0]
	require.Equal(t, int64(99), created.CreatedBy)
	require.NotNil(t, created.Filters.Model)
	require.Equal(t, "gpt-4", *created.Filters.Model)

	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC).Add(24*time.Hour - time.Nanosecond)
	require.True(t, created.Filters.StartTime.Equal(start))
	require.True(t, created.Filters.EndTime.Equal(end))
}

func TestUsageHandlerListCleanupTasksUnavailable(t *testing.T) {
	router := setupCleanupRouter(nil, 0)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/usage/cleanup-tasks", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestUsageHandlerListCleanupTasksSuccess(t *testing.T) {
	repo := &cleanupRepoStub{}
	repo.listTasks = []usage.UsageCleanupTask{
		{
			ID:        7,
			Status:    usage.UsageCleanupStatusSucceeded,
			CreatedBy: 4,
		},
	}
	repo.listResult = &pagination.PaginationResult{Total: 1, Page: 1, PageSize: 20, Pages: 1}
	cfg := &usage.Options{UsageCleanup: usage.UsageCleanupConfig{Enabled: true, MaxRangeDays: 31}}
	cleanupService := usage.NewUsageCleanupService(repo, nil, nil, cfg)
	router := setupCleanupRouter(cleanupService, 1)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/usage/cleanup-tasks", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)

	var resp struct {
		Code int `json:"code"`
		Data struct {
			Items []dto.UsageCleanupTask `json:"items"`
			Total int64                  `json:"total"`
			Page  int                    `json:"page"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.Len(t, resp.Data.Items, 1)
	require.Equal(t, int64(7), resp.Data.Items[0].ID)
	require.Equal(t, int64(1), resp.Data.Total)
	require.Equal(t, 1, resp.Data.Page)
}

func TestUsageHandlerListCleanupTasksError(t *testing.T) {
	repo := &cleanupRepoStub{listErr: errors.New("boom")}
	cfg := &usage.Options{UsageCleanup: usage.UsageCleanupConfig{Enabled: true, MaxRangeDays: 31}}
	cleanupService := usage.NewUsageCleanupService(repo, nil, nil, cfg)
	router := setupCleanupRouter(cleanupService, 1)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/usage/cleanup-tasks", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
}

func TestUsageHandlerCancelCleanupTaskUnauthorized(t *testing.T) {
	repo := &cleanupRepoStub{}
	cfg := &usage.Options{UsageCleanup: usage.UsageCleanupConfig{Enabled: true}}
	cleanupService := usage.NewUsageCleanupService(repo, nil, nil, cfg)
	router := setupCleanupRouter(cleanupService, 0)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/usage/cleanup-tasks/1/cancel", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestUsageHandlerCancelCleanupTaskNotFound(t *testing.T) {
	repo := &cleanupRepoStub{}
	cfg := &usage.Options{UsageCleanup: usage.UsageCleanupConfig{Enabled: true}}
	cleanupService := usage.NewUsageCleanupService(repo, nil, nil, cfg)
	router := setupCleanupRouter(cleanupService, 1)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/usage/cleanup-tasks/999/cancel", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUsageHandlerCancelCleanupTaskConflict(t *testing.T) {
	repo := &cleanupRepoStub{statusByID: map[int64]string{2: usage.UsageCleanupStatusSucceeded}}
	cfg := &usage.Options{UsageCleanup: usage.UsageCleanupConfig{Enabled: true}}
	cleanupService := usage.NewUsageCleanupService(repo, nil, nil, cfg)
	router := setupCleanupRouter(cleanupService, 1)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/usage/cleanup-tasks/2/cancel", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code)
}

func TestUsageHandlerCancelCleanupTaskSuccess(t *testing.T) {
	repo := &cleanupRepoStub{statusByID: map[int64]string{3: usage.UsageCleanupStatusPending}}
	cfg := &usage.Options{UsageCleanup: usage.UsageCleanupConfig{Enabled: true}}
	cleanupService := usage.NewUsageCleanupService(repo, nil, nil, cfg)
	router := setupCleanupRouter(cleanupService, 1)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/usage/cleanup-tasks/3/cancel", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

func (s *adminUsageRepoCapture) ListWithFilters(ctx context.Context, params pagination.PaginationParams, filters usage.UsageLogFilters) ([]usage.UsageLog, *pagination.PaginationResult, error) {
	s.listParams = params
	s.listFilters = filters
	return s.logs, &pagination.PaginationResult{
		Total:    int64(len(s.logs)),
		Page:     params.Page,
		PageSize: params.PageSize,
		Pages:    0,
	}, nil
}

func (r *adminUsageTimingOpsRepo) ListRequestTimings(_ context.Context, ids []string) (map[string]*ops.OpsRequestTiming, error) {
	return r.timings, nil
}

func TestAdminUsageListIncludesDetailedTiming(t *testing.T) {
	firstByte := int64(120000)
	usageRepo := &adminUsageRepoCapture{logs: []usage.UsageLog{{
		RequestID: "client:req-internal-1",
		Model:     "gpt-5",
	}}}
	opsRepo := &adminUsageTimingOpsRepo{timings: map[string]*ops.OpsRequestTiming{
		"req-internal-1": {UpstreamFirstResponseByteMs: &firstByte},
	}}
	usageSvc := usage.NewUsageService(usageRepo)
	opsSvc := ops.NewOpsService(opsRepo, nil, nil, nil, nil, nil, nil, nil)
	handler := NewUsageHandler(usageSvc, nil, nil, nil, opsSvc, timezone.NewCalendar(time.Local))
	router := gin.New()
	router.GET("/admin/usage", handler.List)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var response struct {
		Data struct {
			Items []struct {
				DetailedTiming *struct {
					UpstreamFirstResponseByteMs *int64 `json:"upstream_first_response_byte_ms,omitempty"`
				} `json:"detailed_timing,omitempty"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.Len(t, response.Data.Items, 1)
	require.NotNil(t, response.Data.Items[0].DetailedTiming)
	require.Equal(t, int64(120000), *response.Data.Items[0].DetailedTiming.UpstreamFirstResponseByteMs)
}

func (s *adminUsageRepoCapture) GetStatsWithFilters(ctx context.Context, filters usage.UsageLogFilters) (*usage.UsageStats, error) {
	s.statsFilters = filters
	return &usage.UsageStats{}, nil
}

func newAdminUsageRequestTypeTestRouter(repo *adminUsageRepoCapture) *gin.Engine {
	usageSvc := usage.NewUsageService(repo)
	handler := NewUsageHandler(usageSvc, nil, nil, nil, nil, timezone.NewCalendar(time.Local))
	router := gin.New()
	router.GET("/admin/usage", handler.List)
	router.GET("/admin/usage/stats", handler.Stats)
	return router
}

func TestAdminUsageListRequestTypePriority(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage?request_type=ws_v2&stream=false", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, repo.listFilters.RequestType)
	require.Equal(t, int16(usage.RequestTypeWSV2), *repo.listFilters.RequestType)
	require.Nil(t, repo.listFilters.Stream)
}

func TestAdminUsageListUsesRequestedModelForDisplayModelFilter(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage?model=grok-imagine-video-1.5", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "grok-imagine-video-1.5", repo.listFilters.Model)
	require.Equal(t, usage.ModelSourceRequested, repo.listFilters.ModelFilterSource)
}

// TestAdminUsageListTeamFilter checks that team scope reaches the list repository.
func TestAdminUsageListTeamFilter(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage?team_id=27", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(27), repo.listFilters.TeamID)
}

func TestAdminUsageListInvalidRequestType(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage?request_type=bad", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAdminUsageListInvalidStream(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage?stream=bad", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAdminUsageListExactTotalTrue(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage?exact_total=true", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, repo.listFilters.ExactTotal)
}

// TestAdminUsageListRequestIDFilter checks whitespace trimming and request-ID forwarding.
func TestAdminUsageListRequestIDFilter(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage?request_id=%20req-0123%20", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "req-0123", repo.listFilters.RequestID)
}

func TestAdminUsageListInvalidExactTotal(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage?exact_total=oops", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAdminUsageStatsRequestTypePriority(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage/stats?request_type=stream&stream=bad", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, repo.statsFilters.RequestType)
	require.Equal(t, int16(usage.RequestTypeStream), *repo.statsFilters.RequestType)
	require.Nil(t, repo.statsFilters.Stream)
}

func TestAdminUsageStatsUsesRequestedModelForDisplayModelFilter(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage/stats?model=grok-imagine-video-1.5", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "grok-imagine-video-1.5", repo.statsFilters.Model)
	require.Equal(t, usage.ModelSourceRequested, repo.statsFilters.ModelFilterSource)
}

// TestAdminUsageStatsTeamFilter checks that summary statistics receive the team filter.
func TestAdminUsageStatsTeamFilter(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage/stats?team_id=31", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(31), repo.statsFilters.TeamID)
}

func TestAdminUsageStatsInvalidRequestType(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage/stats?request_type=oops", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAdminUsageStatsInvalidStream(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage/stats?stream=oops", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestAdminUsageEndpointSource checks the all-chart default and rejection of unknown sources.
func TestAdminUsageEndpointSource(t *testing.T) {
	for _, source := range []string{"", "all", "inbound", "upstream", "path", "invalid"} {
		repo := &adminUsageRepoCapture{}
		router := newAdminUsageRequestTypeTestRouter(repo)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/usage/stats?endpoint_source="+source, nil))
		if source == "invalid" {
			require.Equal(t, http.StatusBadRequest, rec.Code)
			continue
		}
		require.Equal(t, http.StatusOK, rec.Code)
		if source == "all" {
			source = ""
		}
		require.Equal(t, source, repo.statsFilters.EndpointSource)
	}
}

func (s *searchUsersAdminStub) ListUsers(ctx context.Context, page, pageSize int, filters ports.UserListFilters, sortBy, sortOrder string) ([]ports.UserReference, int64, error) {
	s.gotFilters = filters
	ts := time.Date(2026, 5, 28, 0, 0, 0, 0, time.UTC)
	return []ports.UserReference{
		{ID: 1, Email: "active@test.com"},
		{ID: 2, Email: "deleted@test.com", DeletedAt: &ts},
	}, 2, nil
}

func TestAdminUsageSearchUsers_IncludesDeletedAndFlags(t *testing.T) {
	stub := &searchUsersAdminStub{}
	handler := NewUsageHandler(nil, nil, stub, nil, nil, timezone.NewCalendar(time.Local))
	router := gin.New()
	router.GET("/admin/usage/search-users", handler.SearchUsers)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage/search-users?q=test", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, stub.gotFilters.IncludeDeleted, "SearchUsers 必须请求 IncludeDeleted")

	var resp struct {
		Data []struct {
			ID      int64  `json:"id"`
			Email   string `json:"email"`
			Deleted bool   `json:"deleted"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 2)
	require.False(t, resp.Data[0].Deleted)
	require.True(t, resp.Data[1].Deleted, "已删用户必须标记 deleted=true")
}

func TestAdminUsageListSortParams(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage?sort_by=model&sort_order=ASC", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "model", repo.listParams.SortBy)
	require.Equal(t, "ASC", repo.listParams.SortOrder)
}

func TestAdminUsageListSortDefaults(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "created_at", repo.listParams.SortBy)
	require.Equal(t, "desc", repo.listParams.SortOrder)
}
