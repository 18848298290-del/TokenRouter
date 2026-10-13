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

// Administrator cleanup HTTP commands parse dates and operator identity
// before calling the persisted task service. These capture fixtures inspect
// authentication rejection, date validation, request-category precedence,
// list envelopes and cancellation status mapping.
//
// Usage-read cases capture selectors and sort input passed through the
// handler, then decode optional access-log timing and deleted-account
// suggestions. The fixtures supply in-memory outcomes rather than SQL
// results, so filter forwarding and response visibility are the observations
// protected here.

// The compile-time assertion keeps the cleanup fixture compatible with the
// repository methods required by administrator task routes.
var _ usage.UsageCleanupRepository = (*cleanupRepoStub)(nil)

// cleanupRepoStub stores shallow creation records and cancellation state for
// administrator HTTP tests. Configure its page inputs before use; methods
// protect mutable state and captured task records with mu.
type cleanupRepoStub struct {
	mu         sync.Mutex                   // fixture task state access mutex
	created    []*usage.UsageCleanupTask    // captured shallow task creation copies
	listTasks  []usage.UsageCleanupTask     // configured shared cleanup task page
	listResult *pagination.PaginationResult // configured shared pagination result
	listErr    error                        // injected task page failure
	statusByID map[int64]string             // configured mutable cleanup status map
}

// adminUsageRepoCapture captures list and statistics input and supplies
// configured usage rows.
type adminUsageRepoCapture struct {
	usage.UsageLogRepository                             // unused usage repository method slots
	listParams               pagination.PaginationParams // captured list pagination and sort
	listFilters              usage.UsageLogFilters       // captured usage list selectors
	statsFilters             usage.UsageLogFilters       // captured usage statistics selectors
	logs                     []usage.UsageLog            // configured shared usage record rows
}

// adminUsageTimingOpsRepo supplies persisted timing observations for
// administrator response enrichment tests.
type adminUsageTimingOpsRepo struct {
	ops.OpsRepository                                  // unused Ops repository method slots
	timings           map[string]*ops.OpsRequestTiming // configured shared request stage map
}

// searchUsersAdminStub captures account suggestion filters and supplies
// active and deleted identities.
type searchUsersAdminStub struct {
	gotFilters ports.UserListFilters // captured account suggestion filters
}

// CreateTask assigns missing identity and creation time, then captures a
// shallow task copy under mu. Nil input succeeds, and the fixture's stored
// filters retain optional pointers from the supplied task.
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

// ListTasks returns the configured task page, pagination pointer and error
// under mu. Its slice and pointer results remain shared with fixture
// configuration, which tests initialize before their request runs.
func (s *cleanupRepoStub) ListTasks(ctx context.Context, params pagination.PaginationParams) ([]usage.UsageCleanupTask, *pagination.PaginationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listTasks, s.listResult, s.listErr
}

// ClaimNextPendingTask supplies an empty queue for background dispatch in
// administrator command tests.
func (s *cleanupRepoStub) ClaimNextPendingTask(ctx context.Context, staleRunningAfterSeconds int64) (*usage.UsageCleanupTask, error) {
	return nil, nil
}

// GetTaskStatus reads a configured status under mu or reports a missing task.
// Missing results join the domain sentinel with sql.ErrNoRows, exercising the
// service's error-chain handling for HTTP cancellation.
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

// UpdateTaskProgress acknowledges unused worker progress in the administrator
// command fixture.
func (s *cleanupRepoStub) UpdateTaskProgress(ctx context.Context, taskID int64, deletedRows int64) error {
	return nil
}

// CancelTask records canceled status only for pending or running fixture
// tasks under mu. Other statuses return false, allowing the handler's service
// to select its conflict response.
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

// MarkTaskSucceeded acknowledges unused successful completion in the
// administrator command fixture.
func (s *cleanupRepoStub) MarkTaskSucceeded(ctx context.Context, taskID int64, deletedRows int64) error {
	return nil
}

// MarkTaskFailed acknowledges unused failed completion in the administrator
// command fixture.
func (s *cleanupRepoStub) MarkTaskFailed(ctx context.Context, taskID int64, deletedRows int64, errorMsg string) error {
	return nil
}

// DeleteUsageLogsBatch supplies zero removed rows for unused administrator
// worker execution.
func (s *cleanupRepoStub) DeleteUsageLogsBatch(ctx context.Context, filters usage.UsageCleanupFilters, limit int) (int64, error) {
	return 0, nil
}

// setupCleanupRouter registers cleanup routes and optionally installs a
// positive supplied user subject. Zero IDs leave authentication absent,
// allowing HTTP cases to exercise the command handler's own operator checks.
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

// TestUsageHandlerCreateCleanupTaskUnauthorized invokes creation without an
// installed user subject. It requires unauthorized status before the cleanup
// command can accept its empty request body.
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

// TestUsageHandlerCreateCleanupTaskUnavailable installs a user subject but no
// cleanup service. It requires service-unavailable status from the creation
// endpoint for that configuration.
func TestUsageHandlerCreateCleanupTaskUnavailable(t *testing.T) {
	router := setupCleanupRouter(nil, 1)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/usage/cleanup-tasks", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

// TestUsageHandlerCreateCleanupTaskBindError submits malformed JSON under an
// authenticated subject. It requires a bad-request response from body binding
// before cleanup task creation.
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

// TestUsageHandlerCreateCleanupTaskMissingRange supplies a start date and
// timezone with no end date. It requires the creation handler to return a
// bad-request response.
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

// TestUsageHandlerCreateCleanupTaskInvalidDate submits a start date with an
// invalid month. It requires the creation handler to reject that date with
// bad-request status.
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

// TestUsageHandlerCreateCleanupTaskInvalidEndDate submits an end date with an
// invalid day. It requires the creation handler to reject that date with
// bad-request status.
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

// TestUsageHandlerCreateCleanupTaskInvalidRequestType submits valid UTC dates
// with an unsupported category label. It requires the creation handler to
// reject that filter with bad-request status.
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

// TestUsageHandlerCreateCleanupTaskRequestTypePriority submits WS v2 beside
// false stream. It requires one accepted task carrying the WS v2 integer and
// an absent stream selector.
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

// TestUsageHandlerCreateCleanupTaskWithLegacyStream submits true stream
// without a category. It requires one accepted task with absent request type
// and the retained true stream filter.
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

// TestUsageHandlerCreateCleanupTaskSuccess supplies spaced start text, UTC
// dates and model. It checks accepted envelope, operator, selected model and
// the fixed elapsed-day inclusive end instant.
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

// TestUsageHandlerListCleanupTasksUnavailable registers listing without a
// cleanup service. It requires service-unavailable status from the route when
// its task page cannot be queried.
func TestUsageHandlerListCleanupTasksUnavailable(t *testing.T) {
	router := setupCleanupRouter(nil, 0)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/usage/cleanup-tasks", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

// TestUsageHandlerListCleanupTasksSuccess supplies a one-task page and
// pagination data. It checks successful envelope, task ID, total and page
// number in decoded JSON.
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
		Code int `json:"code"` // decoded envelope result code
		Data struct {
			Items []dto.UsageCleanupTask `json:"items"` // decoded cleanup task rows
			Total int64                  `json:"total"` // decoded matching task count
			Page  int                    `json:"page"`  // decoded current page number
		} `json:"data"` // decoded administrator response payload
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.Len(t, resp.Data.Items, 1)
	require.Equal(t, int64(7), resp.Data.Items[0].ID)
	require.Equal(t, int64(1), resp.Data.Total)
	require.Equal(t, 1, resp.Data.Page)
}

// TestUsageHandlerListCleanupTasksError injects a listing failure in cleanup
// storage. It requires an internal-error response from the administrator task
// page endpoint for that query.
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

// TestUsageHandlerCancelCleanupTaskUnauthorized submits cancellation without
// an installed user subject. It requires unauthorized status before the
// service can process the supplied task ID.
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

// TestUsageHandlerCancelCleanupTaskNotFound requests an absent task with an
// authenticated subject. It requires not-found status after the fixture's
// joined domain and SQL status error.
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

// TestUsageHandlerCancelCleanupTaskConflict supplies a task already marked
// succeeded. It requires conflict status when an authenticated operator
// requests cancellation of that terminal task.
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

// TestUsageHandlerCancelCleanupTaskSuccess supplies a pending task and
// authenticated operator. It requires the endpoint to return success after
// the service's conditional cancellation handling.
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

// ListWithFilters captures query selectors and pagination before returning
// configured shared logs and a derived page. Tests read the capture after
// synchronous request completion and leave configured records stable during
// conversion.
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

// ListRequestTimings returns the configured shared timing map for
// administrator enrichment fixtures.
func (r *adminUsageTimingOpsRepo) ListRequestTimings(_ context.Context, ids []string) (map[string]*ops.OpsRequestTiming, error) {
	return r.timings, nil
}

// TestAdminUsageListIncludesDetailedTiming supplies a historical
// client-prefixed request and a matching timing observation. It requires the
// decoded row's optional timing and exact first-byte offset.
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
					UpstreamFirstResponseByteMs *int64 `json:"upstream_first_response_byte_ms,omitempty"` // optional first byte offset milliseconds
				} `json:"detailed_timing,omitempty"` // decoded optional request stage observations
			} `json:"items"` // decoded cleanup or usage rows
		} `json:"data"` // decoded administrator response payload
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.Len(t, response.Data.Items, 1)
	require.NotNil(t, response.Data.Items[0].DetailedTiming)
	require.Equal(t, int64(120000), *response.Data.Items[0].DetailedTiming.UpstreamFirstResponseByteMs)
}

// GetStatsWithFilters captures supplied administrator selectors and returns
// an empty successful summary.
func (s *adminUsageRepoCapture) GetStatsWithFilters(ctx context.Context, filters usage.UsageLogFilters) (*usage.UsageStats, error) {
	s.statsFilters = filters
	return &usage.UsageStats{}, nil
}

// newAdminUsageRequestTypeTestRouter constructs administrator usage list and
// statistics routes with a capture fixture.
func newAdminUsageRequestTypeTestRouter(repo *adminUsageRepoCapture) *gin.Engine {
	usageSvc := usage.NewUsageService(repo)
	handler := NewUsageHandler(usageSvc, nil, nil, nil, nil, timezone.NewCalendar(time.Local))
	router := gin.New()
	router.GET("/admin/usage", handler.List)
	router.GET("/admin/usage/stats", handler.Stats)
	return router
}

// TestAdminUsageListRequestTypePriority sends WS v2 beside false legacy
// stream. It checks successful response and the captured category pointer
// with absent stream selection.
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

// TestAdminUsageListUsesRequestedModelForDisplayModelFilter submits a model
// name to listing. It checks success and the captured model text with the
// requested reporting-source selector.
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

// TestAdminUsageListTeamFilter submits a team identifier to the usage list.
// It requires a successful response and that identifier in the captured
// repository selectors.
func TestAdminUsageListTeamFilter(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage?team_id=27", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(27), repo.listFilters.TeamID)
}

// TestAdminUsageListInvalidRequestType sends unsupported category text to
// listing. It requires the handler to return a bad-request response for that
// request-type input.
func TestAdminUsageListInvalidRequestType(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage?request_type=bad", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestAdminUsageListInvalidStream sends malformed legacy stream text without
// a category. It requires the handler to return a bad-request response for
// that boolean input.
func TestAdminUsageListInvalidStream(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage?stream=bad", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestAdminUsageListExactTotalTrue requests an exact pagination total. It
// requires a successful HTTP response and true ExactTotal in the selectors
// captured by the repository fixture.
func TestAdminUsageListExactTotalTrue(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage?exact_total=true", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, repo.listFilters.ExactTotal)
}

// TestAdminUsageListRequestIDFilter supplies request identity surrounded by
// encoded spaces. It requires HTTP success and trimmed request text in the
// captured usage-list filters.
func TestAdminUsageListRequestIDFilter(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage?request_id=%20req-0123%20", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "req-0123", repo.listFilters.RequestID)
}

// TestAdminUsageListInvalidExactTotal submits an unsupported exact_total
// value. It requires a bad-request response from pagination-total boolean
// validation in the administrator list handler.
func TestAdminUsageListInvalidExactTotal(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage?exact_total=oops", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestAdminUsageStatsRequestTypePriority sends stream category beside
// malformed stream text. It requires success, the stored stream category
// pointer and absent legacy stream selection in captured statistics filters.
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

// TestAdminUsageStatsUsesRequestedModelForDisplayModelFilter supplies a
// specific model name to summary statistics. It checks successful handling
// and the exact model text plus requested source in captured filters.
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

// TestAdminUsageStatsTeamFilter supplies a team identifier to summary
// statistics. It requires a successful response and that identifier in the
// captured repository filter set.
func TestAdminUsageStatsTeamFilter(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage/stats?team_id=31", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(31), repo.statsFilters.TeamID)
}

// TestAdminUsageStatsInvalidRequestType submits unsupported category text to
// statistics. It requires the handler to return a bad-request response for
// the supplied category input.
func TestAdminUsageStatsInvalidRequestType(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage/stats?request_type=oops", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestAdminUsageStatsInvalidStream submits malformed legacy stream text with
// no category. It requires the statistics handler to reject that input with
// bad-request status.
func TestAdminUsageStatsInvalidStream(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/usage/stats?stream=oops", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestAdminUsageEndpointSource supplies empty, all, supported and invalid
// chart sources. It checks all-to-empty normalization, supported selector
// forwarding and bad-request rejection of the unknown source.
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

// ListUsers captures supplied search filters and returns two fixed account
// references. One has a deletion instant, letting the HTTP test distinguish
// active and deleted suggestion flags.
func (s *searchUsersAdminStub) ListUsers(ctx context.Context, page, pageSize int, filters ports.UserListFilters, sortBy, sortOrder string) ([]ports.UserReference, int64, error) {
	s.gotFilters = filters
	ts := time.Date(2026, 5, 28, 0, 0, 0, 0, time.UTC)
	return []ports.UserReference{
		{ID: 1, Email: "active@test.com"},
		{ID: 2, Email: "deleted@test.com", DeletedAt: &ts},
	}, 2, nil
}

// TestAdminUsageSearchUsers_IncludesDeletedAndFlags supplies active and
// deleted suggestion fixtures. It checks the IncludeDeleted query flag, two
// returned rows and their decoded deletion flags.
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
			ID      int64  `json:"id"`      // decoded suggested account identifier
			Email   string `json:"email"`   // decoded suggested account email
			Deleted bool   `json:"deleted"` // decoded soft deletion flag
		} `json:"data"` // decoded administrator response payload
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 2)
	require.False(t, resp.Data[0].Deleted)
	require.True(t, resp.Data[1].Deleted, "已删用户必须标记 deleted=true")
}

// TestAdminUsageListSortParams supplies model sorting with uppercase
// direction text. It requires successful HTTP handling and both strings
// captured as supplied by the repository fixture.
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

// TestAdminUsageListSortDefaults omits list sorting parameters. It requires
// successful HTTP handling and the created_at and descending defaults
// captured in the repository pagination input.
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
