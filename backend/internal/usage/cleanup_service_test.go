package usage

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/timingwheel"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// Cleanup task execution can finish, fail or observe cancellation after
// deleting only part of its selected rows. These fixtures capture each
// delete, progress and terminal-state attempt, letting cases inspect the
// reported totals and whether aggregate repair was dispatched after the
// chosen exit.
//
// The blocking claim fixture separates context cancellation from repository
// return. Its channels let the shutdown case hold accepted manual work after
// cancellation and verify that Stop still waits. Other fixtures use mutexes
// or atomic counters when asynchronous repair and worker dispatch cross the
// test's read observations. Tests describe recorded method calls and injected
// outcomes; these fixtures perform no database deletion.

// partialCleanup supplies one full delete batch before a later status read
// reports cancellation. Its counters are used by one serial executeTask call.
type partialCleanup struct {
	UsageCleanupRepository       // unused cleanup repository method slots
	checks                 int   // serial cancellation status read count
	deleted                int64 // configured or recorded deletion count
}

// recomputeRecorder counts legacy repair invocations through an atomic
// counter for cancellation regression tests.
type recomputeRecorder struct {
	DashboardAggregationRepository              // unused aggregation repository method slots
	calls                          atomic.Int64 // atomic legacy repair invocation count
}

// cleanupDeleteResponse carries one queued deletion result supplied to the
// cleanup worker fixture.
type cleanupDeleteResponse struct {
	deleted int64 // configured or recorded deletion count
	err     error // injected deletion result failure
}

// cleanupDeleteCall records the selected filters and batch limit passed by
// cleanup execution.
type cleanupDeleteCall struct {
	filters UsageCleanupFilters // captured cleanup selection fields
	limit   int                 // captured deletion batch limit
}

// cleanupMarkCall records a task status or progress write attempted by
// cleanup execution.
type cleanupMarkCall struct {
	taskID      int64  // captured cleanup task identifier
	deletedRows int64  // captured reported deletion total
	errMsg      string // captured failed task diagnostic
}

// cleanupRepoStub supplies queued task outcomes and captures cleanup
// repository calls. Its methods guard fixture data with mu; configure results
// before dispatch and inspect captured values under that mutex.
type cleanupRepoStub struct {
	mu            sync.Mutex                   // fixture storage access mutex
	created       []*UsageCleanupTask          // captured shallow task creation copies
	createErr     error                        // injected task creation failure
	listTasks     []UsageCleanupTask           // configured shared task page
	listResult    *pagination.PaginationResult // configured shared pagination result
	listErr       error                        // injected task listing failure
	claimQueue    []*UsageCleanupTask          // queued task claim pointers
	claimErr      error                        // injected task claim failure
	deleteQueue   []cleanupDeleteResponse      // queued deletion operation results
	deleteCalls   []cleanupDeleteCall          // captured deletion query attempts
	markSucceeded []cleanupMarkCall            // captured successful terminal write attempts
	markFailed    []cleanupMarkCall            // captured failed terminal write attempts
	statusByID    map[int64]string             // configured mutable task status map
	statusErr     error                        // injected task status failure
	progressCalls []cleanupMarkCall            // captured progress write attempts
	updateErr     error                        // injected task progress failure
	cancelCalls   []int64                      // captured task cancellation identifiers
	cancelErr     error                        // injected cancellation operation failure
	cancelResult  *bool                        // optional conditional cancellation outcome override
	markFailedErr error                        // injected failed status publication error
}

// dashboardRepoStub counts legacy aggregate repair attempts and supplies an
// injected repair failure.
type dashboardRepoStub struct {
	recomputeErr   error        // injected legacy repair failure
	recomputeCalls atomic.Int64 // atomic legacy repair attempt count
}

// blockingCleanupClaimRepo holds one manual claim after cancellation until
// the test releases it.
type blockingCleanupClaimRepo struct {
	UsageCleanupRepository // unused cleanup repository method slots
	// entered closes at claim invocation and canceled closes after ctx ends.
	// The test closes release only after observing canceled, keeping repository
	// return separate from shutdown cancellation.
	entered, canceled, release chan struct{} // entered: accepted claim; canceled: observed context; release: allowed repository return
}

// GetTaskStatus returns running once and canceled on later reads in this
// serial fixture. The transition forces executeTask to observe cancellation
// only after the first full deletion batch.
func (r *partialCleanup) GetTaskStatus(context.Context, int64) (string, error) {
	r.checks++
	if r.checks > 1 {
		return UsageCleanupStatusCanceled, nil
	}
	return UsageCleanupStatusRunning, nil
}

// DeleteUsageLogsBatch records and returns a fixed five-thousand-row deletion
// in this fixture. That full default-size batch lets executeTask reach
// another persisted-status check before completing its scan.
func (r *partialCleanup) DeleteUsageLogsBatch(context.Context, UsageCleanupFilters, int) (int64, error) {
	r.deleted = 5000
	return 5000, nil
}

// UpdateTaskProgress acknowledges the unused progress write in the
// partial-cancellation fixture.
func (r *partialCleanup) UpdateTaskProgress(context.Context, int64, int64) error { return nil }

// RecomputeRange atomically records a legacy aggregate repair attempt and
// returns success.
func (r *recomputeRecorder) RecomputeRange(context.Context, time.Time, time.Time) error {
	r.calls.Add(1)
	return nil
}

// TestRegressionCanceledCleanupRepairsAggregates deletes one full fixture
// batch before cancellation. It requires the recorded five-thousand-row
// deletion and at least one legacy repair invocation after runtime work
// finishes.
func TestRegressionCanceledCleanupRepairsAggregates(t *testing.T) {
	r := &partialCleanup{}
	ar := &recomputeRecorder{}
	agg := NewDashboardAggregationService(ar, nil, nil)
	agg.runtimeStarted = true
	s := NewUsageCleanupService(r, nil, agg, nil)
	defer s.Stop()
	s.executeTask(context.Background(), &UsageCleanupTask{ID: 1, Filters: UsageCleanupFilters{StartTime: time.Now().Add(-24 * time.Hour), EndTime: time.Now().Add(-time.Hour)}})
	// Wait for dispatched repair before reading its atomic count and stopping the
	// runtime, so the cancellation regression observes an attempted legacy recomputation.
	agg.runtimeWG.Wait()
	agg.Stop()
	if r.deleted != 5000 {
		t.Fatalf("fixture failed to delete batch")
	}
	if ar.calls.Load() == 0 {
		t.Error("5000 rows were deleted before cancellation, but neither legacy nor analytics recompute was requested")
	}
}

// NewTimingWheelService constructs a timing-wheel fixture with a nil error
// result.
func NewTimingWheelService() (*timingwheel.Wheel, error) { return timingwheel.New(), nil }

// AggregateRange acknowledges unused live aggregation calls in cleanup task
// fixtures.
func (s *dashboardRepoStub) AggregateRange(ctx context.Context, start, end time.Time) error {
	return nil
}

// RecomputeRange atomically counts repair attempts before returning the
// configured repair error.
func (s *dashboardRepoStub) RecomputeRange(ctx context.Context, start, end time.Time) error {
	s.recomputeCalls.Add(1)
	return s.recomputeErr
}

// GetAggregationWatermark supplies absent progress for cleanup task
// aggregation fixtures.
func (s *dashboardRepoStub) GetAggregationWatermark(ctx context.Context) (time.Time, error) {
	return time.Time{}, nil
}

// UpdateAggregationWatermark acknowledges unused watermark writes in cleanup
// task fixtures.
func (s *dashboardRepoStub) UpdateAggregationWatermark(ctx context.Context, aggregatedAt time.Time) error {
	return nil
}

// CleanupAggregates acknowledges unused rollup retention calls in cleanup
// task fixtures.
func (s *dashboardRepoStub) CleanupAggregates(ctx context.Context, hourlyCutoff, dailyCutoff time.Time) error {
	return nil
}

// CleanupUsageLogs acknowledges unused usage retention calls in cleanup task
// fixtures.
func (s *dashboardRepoStub) CleanupUsageLogs(ctx context.Context, cutoff time.Time) error {
	return nil
}

// CleanupUsageBillingDedup acknowledges unused billing retention calls in
// cleanup task fixtures.
func (s *dashboardRepoStub) CleanupUsageBillingDedup(ctx context.Context, cutoff time.Time) error {
	return nil
}

// EnsureUsageLogsPartitions acknowledges unused partition preparation calls
// in cleanup task fixtures.
func (s *dashboardRepoStub) EnsureUsageLogsPartitions(ctx context.Context, now time.Time) error {
	return nil
}

// CreateTask assigns absent task metadata and captures a shallow task copy
// under mu. Nil tasks succeed immediately, configured creation errors return
// before capture, and filter pointers retain their supplied storage.
func (s *cleanupRepoStub) CreateTask(ctx context.Context, task *UsageCleanupTask) error {
	if task == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return s.createErr
	}
	if task.ID == 0 {
		task.ID = int64(len(s.created) + 1)
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now().UTC()
	}
	if task.UpdatedAt.IsZero() {
		task.UpdatedAt = task.CreatedAt
	}
	clone := *task
	s.created = append(s.created, &clone)
	return nil
}

// ListTasks returns the configured task page, pagination pointer and error
// under mu. Returned slice and pointer data remain shared, so tests configure
// them before calls and inspect them after completion.
func (s *cleanupRepoStub) ListTasks(ctx context.Context, params pagination.PaginationParams) ([]UsageCleanupTask, *pagination.PaginationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listTasks, s.listResult, s.listErr
}

// ClaimNextPendingTask returns the next queued task and records running
// status under mu. Configured errors and an empty queue end the claim, and
// returned task pointers retain fixture storage.
func (s *cleanupRepoStub) ClaimNextPendingTask(ctx context.Context, staleRunningAfterSeconds int64) (*UsageCleanupTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	if len(s.claimQueue) == 0 {
		return nil, nil
	}
	task := s.claimQueue[0]
	s.claimQueue = s.claimQueue[1:]
	if s.statusByID == nil {
		s.statusByID = map[int64]string{}
	}
	s.statusByID[task.ID] = UsageCleanupStatusRunning
	return task, nil
}

// GetTaskStatus reads configured status under mu or returns the injected
// status error. An absent map or missing ID produces ErrCleanupTaskNotFound
// for cancellation and worker-read cases.
func (s *cleanupRepoStub) GetTaskStatus(ctx context.Context, taskID int64) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statusErr != nil {
		return "", s.statusErr
	}
	if s.statusByID == nil {
		return "", ErrCleanupTaskNotFound
	}
	status, ok := s.statusByID[taskID]
	if !ok {
		return "", ErrCleanupTaskNotFound
	}
	return status, nil
}

// UpdateTaskProgress records an attempted progress write under mu before
// returning its configured error. Captured attempts remain visible even when
// the test injects failed progress persistence.
func (s *cleanupRepoStub) UpdateTaskProgress(ctx context.Context, taskID int64, deletedRows int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.progressCalls = append(s.progressCalls, cleanupMarkCall{taskID: taskID, deletedRows: deletedRows})
	if s.updateErr != nil {
		return s.updateErr
	}
	return nil
}

// CancelTask records the requested ID and selects its configured or
// status-dependent outcome under mu. Successful outcomes store canceled
// status; injected errors or false outcomes let the service exercise
// rejection and reread paths.
func (s *cleanupRepoStub) CancelTask(ctx context.Context, taskID int64, canceledBy int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelCalls = append(s.cancelCalls, taskID)
	if s.cancelErr != nil {
		return false, s.cancelErr
	}
	if s.cancelResult != nil {
		ok := *s.cancelResult
		if ok {
			if s.statusByID == nil {
				s.statusByID = map[int64]string{}
			}
			s.statusByID[taskID] = UsageCleanupStatusCanceled
		}
		return ok, nil
	}
	if s.statusByID == nil {
		s.statusByID = map[int64]string{}
	}
	status := s.statusByID[taskID]
	if status != UsageCleanupStatusPending && status != UsageCleanupStatusRunning {
		return false, nil
	}
	s.statusByID[taskID] = UsageCleanupStatusCanceled
	return true, nil
}

// MarkTaskSucceeded records task identity and deletion total, then stores
// succeeded status under mu.
func (s *cleanupRepoStub) MarkTaskSucceeded(ctx context.Context, taskID int64, deletedRows int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markSucceeded = append(s.markSucceeded, cleanupMarkCall{taskID: taskID, deletedRows: deletedRows})
	if s.statusByID == nil {
		s.statusByID = map[int64]string{}
	}
	s.statusByID[taskID] = UsageCleanupStatusSucceeded
	return nil
}

// MarkTaskFailed captures the failure write and stores failed status under mu
// before returning an optional injected error. Tests can therefore observe
// the attempted terminal publication even when its result fails.
func (s *cleanupRepoStub) MarkTaskFailed(ctx context.Context, taskID int64, deletedRows int64, errorMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markFailed = append(s.markFailed, cleanupMarkCall{taskID: taskID, deletedRows: deletedRows, errMsg: errorMsg})
	if s.statusByID == nil {
		s.statusByID = map[int64]string{}
	}
	s.statusByID[taskID] = UsageCleanupStatusFailed
	if s.markFailedErr != nil {
		return s.markFailedErr
	}
	return nil
}

// DeleteUsageLogsBatch records filters and limit before consuming one
// configured outcome under mu. An empty queue returns zero successfully,
// while queued errors let execution cases select their failure or
// cancellation path.
func (s *cleanupRepoStub) DeleteUsageLogsBatch(ctx context.Context, filters UsageCleanupFilters, limit int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteCalls = append(s.deleteCalls, cleanupDeleteCall{filters: filters, limit: limit})
	if len(s.deleteQueue) == 0 {
		return 0, nil
	}
	resp := s.deleteQueue[0]
	s.deleteQueue = s.deleteQueue[1:]
	return resp.deleted, resp.err
}

// TestUsageCleanupServiceCreateTaskSanitizeFilters submits invalid user and
// billing selectors with a spaced model. It checks pending task attribution,
// cleared selectors and the retained key and trimmed model.
func TestUsageCleanupServiceCreateTaskSanitizeFilters(t *testing.T) {
	repo := &cleanupRepoStub{}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true, MaxRangeDays: 31}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)

	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	userID := int64(-1)
	apiKeyID := int64(10)
	model := "  gpt-4  "
	billingType := int8(-2)
	filters := UsageCleanupFilters{
		StartTime:   start,
		EndTime:     end,
		UserID:      &userID,
		APIKeyID:    &apiKeyID,
		Model:       &model,
		BillingType: &billingType,
	}

	task, err := svc.CreateTask(context.Background(), filters, 9)
	require.NoError(t, err)
	require.Equal(t, UsageCleanupStatusPending, task.Status)
	require.Nil(t, task.Filters.UserID)
	require.NotNil(t, task.Filters.APIKeyID)
	require.Equal(t, apiKeyID, *task.Filters.APIKeyID)
	require.NotNil(t, task.Filters.Model)
	require.Equal(t, "gpt-4", *task.Filters.Model)
	require.Nil(t, task.Filters.BillingType)
	require.Equal(t, int64(9), task.CreatedBy)
}

// TestSanitizeUsageCleanupFiltersRequestTypePriority supplies WS v2 beside a
// false stream selector and spaced model. It requires normalized category,
// absent stream and trimmed model.
func TestSanitizeUsageCleanupFiltersRequestTypePriority(t *testing.T) {
	requestType := int16(RequestTypeWSV2)
	stream := false
	model := "  gpt-5  "
	filters := UsageCleanupFilters{
		Model:       &model,
		RequestType: &requestType,
		Stream:      &stream,
	}

	sanitizeUsageCleanupFilters(&filters)

	require.NotNil(t, filters.RequestType)
	require.Equal(t, int16(RequestTypeWSV2), *filters.RequestType)
	require.Nil(t, filters.Stream)
	require.NotNil(t, filters.Model)
	require.Equal(t, "gpt-5", *filters.Model)
}

// TestSanitizeUsageCleanupFiltersInvalidRequestType supplies an unsupported
// category beside true stream. It requires category removal while the legacy
// stream selector remains present and true.
func TestSanitizeUsageCleanupFiltersInvalidRequestType(t *testing.T) {
	requestType := int16(99)
	stream := true
	filters := UsageCleanupFilters{
		RequestType: &requestType,
		Stream:      &stream,
	}

	sanitizeUsageCleanupFilters(&filters)

	require.Nil(t, filters.RequestType)
	require.NotNil(t, filters.Stream)
	require.True(t, *filters.Stream)
}

// TestDescribeUsageCleanupFiltersIncludesRequestType formats a cleanup filter
// carrying WS v2. It requires the diagnostic text to contain the normalized
// request_type label for that category.
func TestDescribeUsageCleanupFiltersIncludesRequestType(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	requestType := int16(RequestTypeWSV2)
	desc := describeUsageCleanupFilters(UsageCleanupFilters{
		StartTime:   start,
		EndTime:     end,
		RequestType: &requestType,
	})

	require.Contains(t, desc, "request_type=ws_v2")
}

// TestUsageCleanupServiceCreateTaskInvalidCreator supplies valid bounds with
// creator zero. It requires a service error carrying the invalid-creator
// reason used by cleanup command callers.
func TestUsageCleanupServiceCreateTaskInvalidCreator(t *testing.T) {
	repo := &cleanupRepoStub{}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)

	filters := UsageCleanupFilters{
		StartTime: time.Now(),
		EndTime:   time.Now().Add(24 * time.Hour),
	}
	_, err := svc.CreateTask(context.Background(), filters, 0)
	require.Error(t, err)
	require.Equal(t, "USAGE_CLEANUP_INVALID_CREATOR", infraerrors.Reason(err))
}

// TestUsageCleanupServiceCreateTaskDisabled supplies a disabled cleanup
// configuration and valid bounds. It requires service-unavailable category
// and the cleanup-disabled reason from task creation.
func TestUsageCleanupServiceCreateTaskDisabled(t *testing.T) {
	repo := &cleanupRepoStub{}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: false}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)

	filters := UsageCleanupFilters{
		StartTime: time.Now(),
		EndTime:   time.Now().Add(24 * time.Hour),
	}
	_, err := svc.CreateTask(context.Background(), filters, 1)
	require.Error(t, err)
	require.Equal(t, http.StatusServiceUnavailable, int(infraerrors.CategoryOf(err)))
	require.Equal(t, "USAGE_CLEANUP_DISABLED", infraerrors.Reason(err))
}

// TestUsageCleanupServiceCreateTaskRangeTooLarge supplies a positive creator
// and forty-eight elapsed hours under a configured one-day limit. It requires
// the exact cleanup range-too-large service reason.
func TestUsageCleanupServiceCreateTaskRangeTooLarge(t *testing.T) {
	repo := &cleanupRepoStub{}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true, MaxRangeDays: 1}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)

	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(48 * time.Hour)
	filters := UsageCleanupFilters{StartTime: start, EndTime: end}

	_, err := svc.CreateTask(context.Background(), filters, 1)
	require.Error(t, err)
	require.Equal(t, "USAGE_CLEANUP_RANGE_TOO_LARGE", infraerrors.Reason(err))
}

// TestUsageCleanupServiceCreateTaskMissingRange submits zero-valued filters
// with a valid creator. It requires the missing-range reason before a task
// can be created by the service.
func TestUsageCleanupServiceCreateTaskMissingRange(t *testing.T) {
	repo := &cleanupRepoStub{}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)

	_, err := svc.CreateTask(context.Background(), UsageCleanupFilters{}, 1)
	require.Error(t, err)
	require.Equal(t, "USAGE_CLEANUP_MISSING_RANGE", infraerrors.Reason(err))
}

// TestUsageCleanupServiceCreateTaskRepoError injects a storage failure during
// task creation. It requires an error whose text includes the service's
// create-task operation context.
func TestUsageCleanupServiceCreateTaskRepoError(t *testing.T) {
	repo := &cleanupRepoStub{createErr: errors.New("db down")}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)

	filters := UsageCleanupFilters{
		StartTime: time.Now(),
		EndTime:   time.Now().Add(24 * time.Hour),
	}
	_, err := svc.CreateTask(context.Background(), filters, 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "create cleanup task")
}

// TestUsageCleanupServiceRunOnceSuccess queues two full batches and a short
// final batch. It checks captured bounds and limit plus one succeeded write
// reporting five removed rows.
func TestUsageCleanupServiceRunOnceSuccess(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	repo := &cleanupRepoStub{
		claimQueue: []*UsageCleanupTask{
			{ID: 5, Filters: UsageCleanupFilters{StartTime: start, EndTime: end}},
		},
		deleteQueue: []cleanupDeleteResponse{
			{deleted: 2},
			{deleted: 2},
			{deleted: 1},
		},
	}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true, BatchSize: 2, TaskTimeoutSeconds: 30}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)

	svc.runOnce()

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.deleteCalls, 3)
	require.Equal(t, 2, repo.deleteCalls[0].limit)
	require.True(t, repo.deleteCalls[0].filters.StartTime.Equal(start))
	require.True(t, repo.deleteCalls[0].filters.EndTime.Equal(end))
	require.Len(t, repo.markSucceeded, 1)
	require.Empty(t, repo.markFailed)
	require.Equal(t, int64(5), repo.markSucceeded[0].taskID)
	require.Equal(t, int64(5), repo.markSucceeded[0].deletedRows)
	require.Equal(t, 2, repo.deleteCalls[0].limit)
	require.Equal(t, start, repo.deleteCalls[0].filters.StartTime)
	require.Equal(t, end, repo.deleteCalls[0].filters.EndTime)
}

// TestUsageCleanupServiceRunOnceClaimError injects a claim failure before any
// task is selected. It requires no succeeded or failed terminal writes in the
// fixture.
func TestUsageCleanupServiceRunOnceClaimError(t *testing.T) {
	repo := &cleanupRepoStub{claimErr: errors.New("claim failed")}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)
	svc.runOnce()

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Empty(t, repo.markSucceeded)
	require.Empty(t, repo.markFailed)
}

// TestUsageCleanupServiceRunOnceAlreadyRunning invokes a service whose local
// running flag is already set. Returning without panic is its smoke
// observation; it contains no call or state assertions.
func TestUsageCleanupServiceRunOnceAlreadyRunning(t *testing.T) {
	repo := &cleanupRepoStub{}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)
	svc.running = 1
	svc.runOnce()
}

// TestUsageCleanupServiceExecuteTaskFailed injects a six-hundred-byte
// deletion error. It requires one failed-state attempt for the task and a
// persisted diagnostic length of five hundred bytes.
func TestUsageCleanupServiceExecuteTaskFailed(t *testing.T) {
	longMsg := strings.Repeat("x", 600)
	repo := &cleanupRepoStub{
		deleteQueue: []cleanupDeleteResponse{
			{err: errors.New(longMsg)},
		},
	}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true, BatchSize: 3}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)
	task := &UsageCleanupTask{
		ID: 11,
		Filters: UsageCleanupFilters{
			StartTime: time.Now(),
			EndTime:   time.Now().Add(24 * time.Hour),
		},
	}

	svc.executeTask(context.Background(), task)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.markFailed, 1)
	require.Equal(t, int64(11), repo.markFailed[0].taskID)
	require.Equal(t, 500, len(repo.markFailed[0].errMsg))
}

// TestUsageCleanupServiceExecuteTaskProgressError fails the progress save
// after a full batch and then returns zero deletion. It requires succeeded
// status, one progress attempt and no failure write.
func TestUsageCleanupServiceExecuteTaskProgressError(t *testing.T) {
	repo := &cleanupRepoStub{
		deleteQueue: []cleanupDeleteResponse{
			{deleted: 2},
			{deleted: 0},
		},
		updateErr: errors.New("update failed"),
	}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true, BatchSize: 2}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)
	task := &UsageCleanupTask{
		ID: 8,
		Filters: UsageCleanupFilters{
			StartTime: time.Now().UTC(),
			EndTime:   time.Now().UTC().Add(time.Hour),
		},
	}

	svc.executeTask(context.Background(), task)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.markSucceeded, 1)
	require.Empty(t, repo.markFailed)
	require.Len(t, repo.progressCalls, 1)
}

// TestUsageCleanupServiceExecuteTaskDeleteCanceled injects context
// cancellation from the delete operation. It requires neither succeeded nor
// failed terminal writes after that interrupted batch call.
func TestUsageCleanupServiceExecuteTaskDeleteCanceled(t *testing.T) {
	repo := &cleanupRepoStub{
		deleteQueue: []cleanupDeleteResponse{
			{err: context.Canceled},
		},
	}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true, BatchSize: 2}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)
	task := &UsageCleanupTask{
		ID: 12,
		Filters: UsageCleanupFilters{
			StartTime: time.Now().UTC(),
			EndTime:   time.Now().UTC().Add(time.Hour),
		},
	}

	svc.executeTask(context.Background(), task)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Empty(t, repo.markSucceeded)
	require.Empty(t, repo.markFailed)
}

// TestUsageCleanupServiceExecuteTaskContextCanceled cancels the execution
// context before entry. It requires no delete calls and no succeeded or
// failed terminal writes in the fixture.
func TestUsageCleanupServiceExecuteTaskContextCanceled(t *testing.T) {
	repo := &cleanupRepoStub{}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true, BatchSize: 2}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)
	task := &UsageCleanupTask{
		ID: 9,
		Filters: UsageCleanupFilters{
			StartTime: time.Now().UTC(),
			EndTime:   time.Now().UTC().Add(time.Hour),
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	svc.executeTask(ctx, task)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Empty(t, repo.markSucceeded)
	require.Empty(t, repo.markFailed)
	require.Empty(t, repo.deleteCalls)
}

// TestUsageCleanupServiceExecuteTaskMarkFailedUpdateError injects deletion
// failure and failed terminal publication. It still requires one recorded
// failed-state attempt carrying the supplied task ID.
func TestUsageCleanupServiceExecuteTaskMarkFailedUpdateError(t *testing.T) {
	repo := &cleanupRepoStub{
		deleteQueue: []cleanupDeleteResponse{
			{err: errors.New("boom")},
		},
		markFailedErr: errors.New("update failed"),
	}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true, BatchSize: 2}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)
	task := &UsageCleanupTask{
		ID: 13,
		Filters: UsageCleanupFilters{
			StartTime: time.Now().UTC(),
			EndTime:   time.Now().UTC().Add(time.Hour),
		},
	}

	svc.executeTask(context.Background(), task)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.markFailed, 1)
	require.Equal(t, int64(13), repo.markFailed[0].taskID)
}

// TestUsageCleanupServiceExecuteTaskDashboardRecomputeError completes an
// empty scan and injects repair failure. It requires a succeeded-task attempt
// and eventually one legacy repair invocation.
func TestUsageCleanupServiceExecuteTaskDashboardRecomputeError(t *testing.T) {
	dashboardRepo := &dashboardRepoStub{recomputeErr: errors.New("recompute failed")}
	repo := &cleanupRepoStub{
		deleteQueue: []cleanupDeleteResponse{
			{deleted: 0},
		},
	}
	dashboard := NewDashboardAggregationService(dashboardRepo, nil, &Options{
		DashboardAgg: DashboardAggregationConfig{Enabled: true},
	})
	dashboard.Start()
	t.Cleanup(dashboard.Stop)

	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true, BatchSize: 2}}
	svc := NewUsageCleanupService(repo, nil, dashboard, cfg)
	task := &UsageCleanupTask{
		ID: 14,
		Filters: UsageCleanupFilters{
			StartTime: time.Now().UTC(),
			EndTime:   time.Now().UTC().Add(time.Hour),
		},
	}

	svc.executeTask(context.Background(), task)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.markSucceeded, 1)
	require.Eventually(t, func() bool { return dashboardRepo.recomputeCalls.Load() == 1 }, time.Second, 10*time.Millisecond)
}

// TestUsageCleanupServiceExecuteTaskDashboardRecomputeSuccess completes an
// empty scan with successful repair storage. It requires a succeeded-task
// attempt and eventually one legacy repair invocation.
func TestUsageCleanupServiceExecuteTaskDashboardRecomputeSuccess(t *testing.T) {
	dashboardRepo := &dashboardRepoStub{}
	repo := &cleanupRepoStub{
		deleteQueue: []cleanupDeleteResponse{
			{deleted: 0},
		},
	}
	dashboard := NewDashboardAggregationService(dashboardRepo, nil, &Options{
		DashboardAgg: DashboardAggregationConfig{Enabled: true},
	})
	dashboard.Start()
	t.Cleanup(dashboard.Stop)

	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true, BatchSize: 2}}
	svc := NewUsageCleanupService(repo, nil, dashboard, cfg)
	task := &UsageCleanupTask{
		ID: 15,
		Filters: UsageCleanupFilters{
			StartTime: time.Now().UTC(),
			EndTime:   time.Now().UTC().Add(time.Hour),
		},
	}

	svc.executeTask(context.Background(), task)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.markSucceeded, 1)
	require.Eventually(t, func() bool { return dashboardRepo.recomputeCalls.Load() == 1 }, time.Second, 10*time.Millisecond)
}

// TestUsageCleanupServiceExecuteTaskCanceled supplies persisted canceled
// status before execution. It requires no delete calls and no succeeded or
// failed terminal writes for that task.
func TestUsageCleanupServiceExecuteTaskCanceled(t *testing.T) {
	repo := &cleanupRepoStub{
		statusByID: map[int64]string{
			3: UsageCleanupStatusCanceled,
		},
	}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true, BatchSize: 2}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)
	task := &UsageCleanupTask{
		ID: 3,
		Filters: UsageCleanupFilters{
			StartTime: time.Now().UTC(),
			EndTime:   time.Now().UTC().Add(time.Hour),
		},
	}

	svc.executeTask(context.Background(), task)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Empty(t, repo.deleteCalls)
	require.Empty(t, repo.markSucceeded)
	require.Empty(t, repo.markFailed)
}

// TestUsageCleanupServiceCancelTaskSuccess supplies a pending task and
// positive operator. It requires canceled fixture status and one repository
// cancellation attempt after a successful service return.
func TestUsageCleanupServiceCancelTaskSuccess(t *testing.T) {
	repo := &cleanupRepoStub{
		statusByID: map[int64]string{
			5: UsageCleanupStatusPending,
		},
	}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)

	err := svc.CancelTask(context.Background(), 5, 9)
	require.NoError(t, err)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Equal(t, UsageCleanupStatusCanceled, repo.statusByID[5])
	require.Len(t, repo.cancelCalls, 1)
}

// TestUsageCleanupServiceCancelTaskDisabled calls cancellation under disabled
// cleanup configuration. It requires the service-unavailable category and
// exact cleanup-disabled reason from the resulting application error.
func TestUsageCleanupServiceCancelTaskDisabled(t *testing.T) {
	repo := &cleanupRepoStub{}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: false}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)

	err := svc.CancelTask(context.Background(), 1, 2)
	require.Error(t, err)
	require.Equal(t, http.StatusServiceUnavailable, int(infraerrors.CategoryOf(err)))
	require.Equal(t, "USAGE_CLEANUP_DISABLED", infraerrors.Reason(err))
}

// TestUsageCleanupServiceCancelTaskNotFound requests an absent task ID from
// the fixture. It requires not-found category and the exact
// cleanup-task-not-found reason exposed by cancellation.
func TestUsageCleanupServiceCancelTaskNotFound(t *testing.T) {
	repo := &cleanupRepoStub{}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)

	err := svc.CancelTask(context.Background(), 999, 1)
	require.Error(t, err)
	require.Equal(t, http.StatusNotFound, int(infraerrors.CategoryOf(err)))
	require.Equal(t, "USAGE_CLEANUP_TASK_NOT_FOUND", infraerrors.Reason(err))
}

// TestUsageCleanupServiceCancelTaskStatusError injects a persisted-status
// lookup failure for a pending cancellation request. It requires the
// resulting service error to contain that supplied diagnostic text.
func TestUsageCleanupServiceCancelTaskStatusError(t *testing.T) {
	repo := &cleanupRepoStub{statusErr: errors.New("status broken")}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)

	err := svc.CancelTask(context.Background(), 7, 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "status broken")
}

// TestUsageCleanupServiceCancelTaskConflict supplies succeeded task status
// before cancellation is requested. It requires conflict category and the
// exact cleanup-cancel-conflict reason for that terminal task.
func TestUsageCleanupServiceCancelTaskConflict(t *testing.T) {
	repo := &cleanupRepoStub{
		statusByID: map[int64]string{
			7: UsageCleanupStatusSucceeded,
		},
	}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)

	err := svc.CancelTask(context.Background(), 7, 1)
	require.Error(t, err)
	require.Equal(t, http.StatusConflict, int(infraerrors.CategoryOf(err)))
	require.Equal(t, "USAGE_CLEANUP_CANCEL_CONFLICT", infraerrors.Reason(err))
}

// TestUsageCleanupServiceCancelTaskAlreadyCanceledIsIdempotent supplies an
// already-canceled task. It requires successful service return and no
// additional repository cancellation write in the captured calls.
func TestUsageCleanupServiceCancelTaskAlreadyCanceledIsIdempotent(t *testing.T) {
	repo := &cleanupRepoStub{
		statusByID: map[int64]string{
			7: UsageCleanupStatusCanceled,
		},
	}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)

	err := svc.CancelTask(context.Background(), 7, 1)
	require.NoError(t, err)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Empty(t, repo.cancelCalls, "already canceled should return success without extra cancel write")
}

// TestUsageCleanupServiceCancelTaskRepoConflict supplies pending status but a
// false conditional update result. It requires conflict category and the
// cleanup cancellation conflict reason after reread.
func TestUsageCleanupServiceCancelTaskRepoConflict(t *testing.T) {
	shouldCancel := false
	repo := &cleanupRepoStub{
		statusByID: map[int64]string{
			7: UsageCleanupStatusPending,
		},
		cancelResult: &shouldCancel,
	}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)

	err := svc.CancelTask(context.Background(), 7, 1)
	require.Error(t, err)
	require.Equal(t, http.StatusConflict, int(infraerrors.CategoryOf(err)))
	require.Equal(t, "USAGE_CLEANUP_CANCEL_CONFLICT", infraerrors.Reason(err))
}

// TestUsageCleanupServiceCancelTaskRepoError injects failure in the
// repository cancellation operation. It requires the service to return an
// error containing the supplied cancellation diagnostic.
func TestUsageCleanupServiceCancelTaskRepoError(t *testing.T) {
	repo := &cleanupRepoStub{
		statusByID: map[int64]string{
			7: UsageCleanupStatusPending,
		},
		cancelErr: errors.New("cancel failed"),
	}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)

	err := svc.CancelTask(context.Background(), 7, 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cancel failed")
}

// TestUsageCleanupServiceCancelTaskInvalidCanceller passes a zero operator
// against a running task. It requires the invalid-canceller reason before a
// repository cancellation can succeed.
func TestUsageCleanupServiceCancelTaskInvalidCanceller(t *testing.T) {
	repo := &cleanupRepoStub{
		statusByID: map[int64]string{
			7: UsageCleanupStatusRunning,
		},
	}
	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true}}
	svc := NewUsageCleanupService(repo, nil, nil, cfg)

	err := svc.CancelTask(context.Background(), 7, 0)
	require.Error(t, err)
	require.Equal(t, "USAGE_CLEANUP_INVALID_CANCELLER", infraerrors.Reason(err))
}

// TestUsageCleanupServiceListTasks configures a two-task page and pagination
// total. It requires successful listing, two returned task rows and the
// configured total count.
func TestUsageCleanupServiceListTasks(t *testing.T) {
	repo := &cleanupRepoStub{
		listTasks: []UsageCleanupTask{{ID: 1}, {ID: 2}},
		listResult: &pagination.PaginationResult{
			Total:    2,
			Page:     1,
			PageSize: 20,
			Pages:    1,
		},
	}
	svc := NewUsageCleanupService(repo, nil, nil, &Options{UsageCleanup: UsageCleanupConfig{Enabled: true}})

	tasks, result, err := svc.ListTasks(context.Background(), pagination.PaginationParams{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	require.Equal(t, int64(2), result.Total)
}

// TestUsageCleanupServiceListTasksNotReady calls listing on a nil service and
// a service lacking storage. It requires an error from both incomplete
// constructions.
func TestUsageCleanupServiceListTasksNotReady(t *testing.T) {
	var nilSvc *UsageCleanupService
	_, _, err := nilSvc.ListTasks(context.Background(), pagination.PaginationParams{Page: 1, PageSize: 20})
	require.Error(t, err)

	svc := NewUsageCleanupService(nil, nil, nil, &Options{UsageCleanup: UsageCleanupConfig{Enabled: true}})
	_, _, err = svc.ListTasks(context.Background(), pagination.PaginationParams{Page: 1, PageSize: 20})
	require.Error(t, err)
}

// TestUsageCleanupServiceDefaultsAndLifecycle checks nil and configured
// default helpers, then invokes lifecycle paths. It asserts range, batch,
// interval and timeout defaults plus a positive interval override.
func TestUsageCleanupServiceDefaultsAndLifecycle(t *testing.T) {
	var nilSvc *UsageCleanupService
	require.Equal(t, 31, nilSvc.maxRangeDays())
	require.Equal(t, 5000, nilSvc.batchSize())
	require.Equal(t, 10*time.Second, nilSvc.workerInterval())
	require.Equal(t, 30*time.Minute, nilSvc.taskTimeout())
	nilSvc.Start()
	nilSvc.Stop()

	repo := &cleanupRepoStub{}
	cfgDisabled := &Options{UsageCleanup: UsageCleanupConfig{Enabled: false}}
	svcDisabled := NewUsageCleanupService(repo, nil, nil, cfgDisabled)
	svcDisabled.Start()
	svcDisabled.Stop()

	timingWheel, err := NewTimingWheelService()
	require.NoError(t, err)

	cfg := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true, WorkerIntervalSeconds: 5}}
	svc := NewUsageCleanupService(repo, timingWheel, nil, cfg)
	require.Equal(t, 5*time.Second, svc.workerInterval())
	svc.Start()
	svc.Stop()

	cfgFallback := &Options{UsageCleanup: UsageCleanupConfig{Enabled: true}}
	svcFallback := NewUsageCleanupService(repo, timingWheel, nil, cfgFallback)
	require.Equal(t, 31, svcFallback.maxRangeDays())
	require.Equal(t, 5000, svcFallback.batchSize())
	require.Equal(t, 10*time.Second, svcFallback.workerInterval())

	svcMissingDeps := NewUsageCleanupService(nil, nil, nil, cfgFallback)
	svcMissingDeps.Start()
}

// TestSanitizeUsageCleanupFiltersModelEmpty supplies blank model text and
// negative identity selectors. It requires removal of the tested user, key,
// provider, group and model filters.
func TestSanitizeUsageCleanupFiltersModelEmpty(t *testing.T) {
	model := "   "
	apiKeyID := int64(-5)
	providerID := int64(-1)
	groupID := int64(-2)
	filters := UsageCleanupFilters{
		UserID:     &apiKeyID,
		APIKeyID:   &apiKeyID,
		ProviderID: &providerID,
		GroupID:    &groupID,
		Model:      &model,
	}

	sanitizeUsageCleanupFilters(&filters)
	require.Nil(t, filters.UserID)
	require.Nil(t, filters.APIKeyID)
	require.Nil(t, filters.ProviderID)
	require.Nil(t, filters.GroupID)
	require.Nil(t, filters.Model)
}

// TestDescribeUsageCleanupFiltersAllFields supplies the displayed bounds and
// tested optional selectors. It requires the complete formatted diagnostic
// string, including trimmed model and legacy stream values.
func TestDescribeUsageCleanupFiltersAllFields(t *testing.T) {
	start := time.Date(2024, 2, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	userID := int64(1)
	apiKeyID := int64(2)
	providerID := int64(3)
	groupID := int64(4)
	model := " gpt-4 "
	stream := true
	billingType := int8(2)
	filters := UsageCleanupFilters{
		StartTime:   start,
		EndTime:     end,
		UserID:      &userID,
		APIKeyID:    &apiKeyID,
		ProviderID:  &providerID,
		GroupID:     &groupID,
		Model:       &model,
		Stream:      &stream,
		BillingType: &billingType,
	}

	desc := describeUsageCleanupFilters(filters)
	require.Equal(t, "start=2024-02-01T10:00:00Z end=2024-02-01T12:00:00Z user_id=1 api_key_id=2 provider_id=3 group_id=4 model=gpt-4 stream=true billing_type=2", desc)
}

// TestUsageCleanupServiceIsTaskCanceledNotFound queries an absent fixture
// status row. It requires successful return with false cancellation,
// distinguishing missing state from a stored canceled task.
func TestUsageCleanupServiceIsTaskCanceledNotFound(t *testing.T) {
	repo := &cleanupRepoStub{}
	svc := NewUsageCleanupService(repo, nil, nil, &Options{UsageCleanup: UsageCleanupConfig{Enabled: true}})

	canceled, err := svc.isTaskCanceled(context.Background(), 9)
	require.NoError(t, err)
	require.False(t, canceled)
}

// TestUsageCleanupServiceIsTaskCanceledError injects a fixture status-read
// failure for the worker's cancellation check. It requires an error
// containing the supplied diagnostic text from that lookup.
func TestUsageCleanupServiceIsTaskCanceledError(t *testing.T) {
	repo := &cleanupRepoStub{statusErr: errors.New("status err")}
	svc := NewUsageCleanupService(repo, nil, nil, &Options{UsageCleanup: UsageCleanupConfig{Enabled: true}})

	_, err := svc.isTaskCanceled(context.Background(), 9)
	require.Error(t, err)
	require.Contains(t, err.Error(), "status err")
}

// ClaimNextPendingTask announces one accepted claim, waits for context
// cancellation and then waits for explicit release. Configure all three
// channels before its single invocation and close release after observing
// cancellation.
//
// This ordering makes canceled signal that the worker saw shutdown while the
// repository call is still held. Stop must keep waiting until release allows
// the accepted round to return. The fixture returns the canceled context
// error afterward; repeated invocation would close its publication channels
// again.
func (r *blockingCleanupClaimRepo) ClaimNextPendingTask(ctx context.Context, _ int64) (*UsageCleanupTask, error) {
	// Announce entry before waiting for cancellation, then hold return until
	// release. The test can inspect Stop while this accepted claim still occupies
	// its worker registration.
	close(r.entered)
	<-ctx.Done()
	close(r.canceled)
	<-r.release
	return nil, ctx.Err()
}

// TestUsageCleanupStopWaitsForManualDispatch holds a claim after it observes
// shutdown cancellation. It requires Stop to keep waiting until repository
// release, then exercises repeated stopped calls.
func TestUsageCleanupStopWaitsForManualDispatch(t *testing.T) {
	repo := &blockingCleanupClaimRepo{entered: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	svc := NewUsageCleanupService(repo, nil, nil, nil)
	done := make(chan struct{})
	// Admit manual work before starting shutdown. The claim fixture publishes entry
	// so Stop's context cancellation applies to an already registered worker round.
	go func() { svc.runOnce(); close(done) }()
	<-repo.entered
	stopped := make(chan struct{})
	go func() { svc.Stop(); close(stopped) }()
	// Hold repository return after it observes cancellation. Stop must continue
	// waiting for registered work until explicit release ends this accepted worker round.
	<-repo.canceled
	select {
	case <-stopped:
		t.Fatal("手动任务尚未返回")
	case <-time.After(20 * time.Millisecond):
	}
	close(repo.release)
	<-done
	<-stopped
	svc.runOnce()
	svc.Stop()
}
