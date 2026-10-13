package usage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// Administrator cleanup persists a deletion task before a worker claims it
// and removes matching usage rows in batches. A recurring callback and
// immediate dispatch from CreateTask can arrive together, so lifecycle
// registration and an atomic running flag keep one local round active while
// repository claims handle task selection.
//
// Task cancellation is read between delete batches. Committed deletes and
// progress writes are separate operations, so stored progress can trail
// removed rows after an update failure. Task execution requests aggregate
// repair on completion or observed partial deletion, using the aggregation
// service's runtime context. Stop cancels the worker context and waits for
// registered manual dispatch as well as timing-wheel callbacks.

// The cleanup worker name identifies the recurring callback that Start
// registers and Stop cancels.
const (
	usageCleanupWorkerName = "usage_cleanup_worker" // recurring cleanup callback identifier
)

// UsageCleanupService manages persisted deletion tasks and the lifecycle of
// their local worker.
type UsageCleanupService struct {
	repo        UsageCleanupRepository // cleanup task and deletion store
	timingWheel TimingWheel            // recurring cleanup callback scheduler
	// dashboard requests repair after a completed scan or observed deletion.
	// Nil skips that request while repository task-state handling still runs.
	dashboard *DashboardAggregationService // optional deleted range repair service
	cfg       *Options                     // cleanup configuration and reporting options

	// running admits one local claim and execution round through
	// compare-and-swap. Competing immediate dispatch and recurring callbacks
	// leave the active round in control.
	running   int32     // local execution admission flag
	startOnce sync.Once // recurring callback registration guard
	stopOnce  sync.Once // worker stop invocation guard
	// lifecycleMu protects stopped and workWG registration. runOnce registers
	// before releasing this lock, so Stop can prevent future registration before
	// waiting for accepted work.
	lifecycleMu sync.Mutex     // worker lifecycle registration lock
	stopped     bool           // permanent worker shutdown flag
	workWG      sync.WaitGroup // registered worker round completion count

	// workerCtx and workerCancel are created together by NewUsageCleanupService.
	// Each round derives its timeout from this context, and Stop cancels it
	// before waiting for completion.
	workerCtx    context.Context    // worker runtime cancellation context
	workerCancel context.CancelFunc // worker runtime cancellation function
}

// NewUsageCleanupService constructs an idle cleanup service with its own
// cancellable worker context. The caller binds task storage and scheduling
// before Start, and calls Stop to cancel and wait for accepted worker rounds.
func NewUsageCleanupService(repo UsageCleanupRepository, timingWheel TimingWheel, dashboard *DashboardAggregationService, cfg *Options) *UsageCleanupService {
	workerCtx, workerCancel := context.WithCancel(context.Background())
	return &UsageCleanupService{
		repo:         repo,
		timingWheel:  timingWheel,
		dashboard:    dashboard,
		cfg:          cfg,
		workerCtx:    workerCtx,
		workerCancel: workerCancel,
	}
}

// describeUsageCleanupFilters formats selected cleanup bounds and filters for
// task diagnostics.
func describeUsageCleanupFilters(filters UsageCleanupFilters) string {
	var parts []string
	parts = append(parts, "start="+filters.StartTime.UTC().Format(time.RFC3339))
	parts = append(parts, "end="+filters.EndTime.UTC().Format(time.RFC3339))
	if filters.UserID != nil {
		parts = append(parts, fmt.Sprintf("user_id=%d", *filters.UserID))
	}
	if filters.APIKeyID != nil {
		parts = append(parts, fmt.Sprintf("api_key_id=%d", *filters.APIKeyID))
	}
	if filters.ProviderID != nil {
		parts = append(parts, fmt.Sprintf("provider_id=%d", *filters.ProviderID))
	}
	if filters.GroupID != nil {
		parts = append(parts, fmt.Sprintf("group_id=%d", *filters.GroupID))
	}
	if filters.TeamID != nil {
		parts = append(parts, fmt.Sprintf("team_id=%d", *filters.TeamID))
	}
	if filters.Model != nil {
		parts = append(parts, "model="+strings.TrimSpace(*filters.Model))
	}
	if filters.RequestType != nil {
		parts = append(parts, "request_type="+RequestTypeFromInt16(*filters.RequestType).String())
	}
	if filters.Stream != nil {
		parts = append(parts, fmt.Sprintf("stream=%t", *filters.Stream))
	}
	if filters.BillingType != nil {
		parts = append(parts, fmt.Sprintf("billing_type=%d", *filters.BillingType))
	}
	return strings.Join(parts, " ")
}

// Start registers one recurring cleanup callback when dependencies and
// deployment settings permit it. Nil services return immediately; a stopped
// service stays stopped, and repeated calls share the startOnce registration
// guard.
func (s *UsageCleanupService) Start() {
	if s == nil {
		return
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopped {
		return
	}
	if s.cfg != nil && !s.cfg.UsageCleanup.Enabled {
		s.logMessage("service.usage_cleanup", "[UsageCleanup] not started (disabled)")
		return
	}
	if s.repo == nil || s.timingWheel == nil {
		s.logMessage("service.usage_cleanup", "[UsageCleanup] not started (missing deps)")
		return
	}

	interval := s.workerInterval()
	s.startOnce.Do(func() {
		s.timingWheel.ScheduleRecurring(usageCleanupWorkerName, interval, s.runOnce)
		s.logMessage("service.usage_cleanup", "[UsageCleanup] started (interval=%s max_range_days=%d batch_size=%d task_timeout=%s)", interval, s.maxRangeDays(), s.batchSize(), s.taskTimeout())
	})
}

// Stop closes worker admission, cancels its runtime context and waits for
// accepted execution to return. Nil services return immediately; concurrent
// or repeated calls share the stopOnce completion path.
//
// Lifecycle locking prevents runOnce from registering new work after stopped
// is set. Timing-wheel cancellation waits for its callbacks, then workWG
// covers the immediate rounds launched by task creation. Repository
// operations need to observe their contexts for cancellation to shorten this
// otherwise unbounded wait.
func (s *UsageCleanupService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		// Close registration and cancel active rounds under lifecycleMu before
		// waiting. runOnce registers under this lock, preventing Stop from missing
		// accepted work during WaitGroup accounting.
		s.lifecycleMu.Lock()
		s.stopped = true
		if s.workerCancel != nil {
			s.workerCancel()
		}
		s.lifecycleMu.Unlock()
		if s.timingWheel != nil {
			s.timingWheel.CancelAndWait(usageCleanupWorkerName)
		}
		// CancelAndWait covers scheduled callbacks. Immediate dispatch from CreateTask
		// registers in workWG, so waiting here also covers accepted manual worker rounds.
		s.workWG.Wait()
		s.logMessage("service.usage_cleanup", "[UsageCleanup] stopped")
	})
}

// ListTasks returns the repository's cleanup page and pagination result for
// params. A nil service or repository returns a readiness error; task listing
// delegates storage errors and pagination policy to the repository.
func (s *UsageCleanupService) ListTasks(ctx context.Context, params pagination.PaginationParams) ([]UsageCleanupTask, *pagination.PaginationResult, error) {
	if s == nil || s.repo == nil {
		return nil, nil, fmt.Errorf("cleanup service not ready")
	}
	return s.repo.ListTasks(ctx, params)
}

// CreateTask sanitizes filters and persists a pending task attributed to a
// positive creator ID. Disabled cleanup and invalid ranges return errors;
// persistence launches a worker attempt whose admission is closed after Stop.
func (s *UsageCleanupService) CreateTask(ctx context.Context, filters UsageCleanupFilters, createdBy int64) (*UsageCleanupTask, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("cleanup service not ready")
	}
	if s.cfg != nil && !s.cfg.UsageCleanup.Enabled {
		return nil, infraerrors.New(infraerrors.CategoryServiceUnavailable, "USAGE_CLEANUP_DISABLED", "usage cleanup is disabled")
	}
	if createdBy <= 0 {
		return nil, infraerrors.BadRequest("USAGE_CLEANUP_INVALID_CREATOR", "invalid creator")
	}

	s.logMessage("service.usage_cleanup", "[UsageCleanup] create_task requested: operator=%d %s", createdBy, describeUsageCleanupFilters(filters))
	sanitizeUsageCleanupFilters(&filters)
	if err := s.validateFilters(filters); err != nil {
		s.logMessage("service.usage_cleanup", "[UsageCleanup] create_task rejected: operator=%d err=%v %s", createdBy, err, describeUsageCleanupFilters(filters))
		return nil, err
	}

	task := &UsageCleanupTask{
		Status:    UsageCleanupStatusPending,
		Filters:   filters,
		CreatedBy: createdBy,
	}
	if err := s.repo.CreateTask(ctx, task); err != nil {
		s.logMessage("service.usage_cleanup", "[UsageCleanup] create_task persist failed: operator=%d err=%v %s", createdBy, err, describeUsageCleanupFilters(filters))
		return nil, fmt.Errorf("create cleanup task: %w", err)
	}
	s.logMessage("service.usage_cleanup", "[UsageCleanup] create_task persisted: task=%d operator=%d status=%s deleted_rows=%d %s", task.ID, createdBy, task.Status, task.DeletedRows, describeUsageCleanupFilters(filters))
	// Dispatch after persistence succeeds so the worker can claim the stored
	// task through its normal lifecycle registration and local admission checks.
	go s.runOnce()
	return task, nil
}

// runOnce registers and executes one local task-claim attempt unless shutdown
// has closed admission. Registration occurs under lifecycleMu so Stop can
// wait for an accepted immediate dispatch as well as recurring callbacks.
//
// The running compare-and-swap permits one local round at a time. A round
// derives its timeout from workerCtx and supplies that timeout in seconds for
// stale-running reclamation. Claim errors or an empty queue end the round; a
// claimed task proceeds to executeTask before registration is released.
func (s *UsageCleanupService) runOnce() {
	if s == nil {
		return
	}
	s.lifecycleMu.Lock()
	if s.stopped {
		s.lifecycleMu.Unlock()
		return
	}
	// Register before releasing lifecycleMu so Stop can close admission and then
	// wait for every accepted manual round as well as the recurring worker callback.
	s.workWG.Add(1)
	s.lifecycleMu.Unlock()
	defer s.workWG.Done()
	svc := s
	if svc == nil {
		return
	}
	// Immediate dispatch and the timing wheel can reach this round together. Keep
	// the existing local claimant active while competing callbacks finish their registration.
	if !atomic.CompareAndSwapInt32(&svc.running, 0, 1) {
		s.logMessage("service.usage_cleanup", "[UsageCleanup] run_once skipped: already_running=true")
		return
	}
	defer atomic.StoreInt32(&svc.running, 0)

	parent := context.Background()
	if svc.workerCtx != nil {
		parent = svc.workerCtx
	}
	ctx, cancel := context.WithTimeout(parent, svc.taskTimeout())
	defer cancel()

	// Use the worker-round timeout for stale-running reclamation as well, so a
	// replacement claimant can recover tasks whose earlier round exhausted that
	// same allowance.
	task, err := svc.repo.ClaimNextPendingTask(ctx, int64(svc.taskTimeout().Seconds()))
	if err != nil {
		s.logMessage("service.usage_cleanup", "[UsageCleanup] claim pending task failed: %v", err)
		return
	}
	if task == nil {
		slog.Debug("[UsageCleanup] run_once done: no_task=true")
		return
	}

	s.logMessage("service.usage_cleanup", "[UsageCleanup] task claimed: task=%d status=%s created_by=%d deleted_rows=%d %s", task.ID, task.Status, task.CreatedBy, task.DeletedRows, describeUsageCleanupFilters(task.Filters))
	svc.executeTask(ctx, task)
}

// executeTask deletes a claimed task's matching rows until a batch is short,
// cancellation is observed, or an error ends execution. Nil tasks return
// immediately, and a reclaimed task begins from its reported DeletedRows.
//
// Delete batches commit independently of progress updates. Successful
// deletions increase the local total even if a background progress write
// fails; context interruption leaves the task available for later
// stale-running reclamation, while ordinary errors attempt failed status.
// Completion attempts succeeded status. Deferred repair runs after a
// completed scan or observed deleted rows, including partial work before
// cancellation or failure.
func (s *UsageCleanupService) executeTask(ctx context.Context, task *UsageCleanupTask) {
	if task == nil {
		return
	}

	batchSize := s.batchSize()
	deletedTotal := task.DeletedRows
	completedScan := false
	// Reclaimed progress and newly committed deletions both leave aggregates stale.
	// Request repair after any observed partial work, and after a completed empty scan.
	defer func() {
		if completedScan || deletedTotal > 0 {
			s.recomputeDeletedRange(task)
		}
	}()
	start := time.Now()
	s.logMessage("service.usage_cleanup", "[UsageCleanup] task started: task=%d batch_size=%d deleted_rows=%d %s", task.ID, batchSize, deletedTotal, describeUsageCleanupFilters(task.Filters))
	var batchNum int

	for {
		if ctx != nil && ctx.Err() != nil {
			s.logMessage("service.usage_cleanup", "[UsageCleanup] task interrupted: task=%d err=%v", task.ID, ctx.Err())
			return
		}
		canceled, err := s.isTaskCanceled(ctx, task.ID)
		if err != nil {
			s.markTaskFailed(task.ID, deletedTotal, err)
			return
		}
		if canceled {
			s.logMessage("service.usage_cleanup", "[UsageCleanup] task canceled: task=%d deleted_rows=%d duration=%s", task.ID, deletedTotal, time.Since(start))
			return
		}

		batchNum++
		// DeleteUsageLogsBatch commits before UpdateTaskProgress records the cumulative
		// count. Keep the local total for completion and repair if progress persistence fails.
		deleted, err := s.repo.DeleteUsageLogsBatch(ctx, task.Filters, batchSize)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				// Context interruption can end this round during shutdown or timeout. Leave
				// its running status available for stale-task reclamation to resume later.
				s.logMessage("service.usage_cleanup", "[UsageCleanup] task interrupted: task=%d err=%v", task.ID, err)
				return
			}
			s.markTaskFailed(task.ID, deletedTotal, err)
			return
		}
		deletedTotal += deleted
		if deleted > 0 {
			updateCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			// A failed progress write cannot restore committed deletes. Continue scanning
			// and retain deletedTotal so completion or deferred repair still reflects local work.
			if err := s.repo.UpdateTaskProgress(updateCtx, task.ID, deletedTotal); err != nil {
				s.logMessage("service.usage_cleanup", "[UsageCleanup] task progress update failed: task=%d deleted_rows=%d err=%v", task.ID, deletedTotal, err)
			}
			cancel()
		}
		// Log early batches, periodic progress and the short final batch to keep
		// long cleanup rounds observable after their initial progress messages.
		if batchNum <= 3 || batchNum%20 == 0 || deleted < int64(batchSize) {
			s.logMessage("service.usage_cleanup", "[UsageCleanup] task batch done: task=%d batch=%d deleted=%d deleted_total=%d", task.ID, batchNum, deleted, deletedTotal)
		}
		if deleted == 0 || deleted < int64(batchSize) {
			break
		}
	}

	completedScan = true
	updateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The completed scan and repair request survive an unsuccessful terminal write.
	// Log that failure so stored running status can be diagnosed or reclaimed later.
	if err := s.repo.MarkTaskSucceeded(updateCtx, task.ID, deletedTotal); err != nil {
		s.logMessage("service.usage_cleanup", "[UsageCleanup] update task succeeded failed: task=%d err=%v", task.ID, err)
	} else {
		s.logMessage("service.usage_cleanup", "[UsageCleanup] task succeeded: task=%d deleted_rows=%d duration=%s", task.ID, deletedTotal, time.Since(start))
	}
}

// markTaskFailed attempts a failed-state write with a trimmed diagnostic and
// deleted-row total. Callers supply a nonnil error for its message, which is
// limited to five hundred bytes. The status write uses a separate five-second
// background context, and write errors are logged.
func (s *UsageCleanupService) markTaskFailed(taskID int64, deletedRows int64, err error) {
	msg := strings.TrimSpace(err.Error())
	// Bound the worker diagnostic to five hundred bytes. Byte slicing can split
	// a multibyte UTF-8 character at this limit.
	if len(msg) > 500 {
		msg = msg[:500]
	}
	s.logMessage("service.usage_cleanup", "[UsageCleanup] task failed: task=%d deleted_rows=%d err=%s", taskID, deletedRows, msg)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Failure recording cannot restore completed deletes. Log its publication error
	// and leave persisted status for later inspection or stale-task reclamation.
	if updateErr := s.repo.MarkTaskFailed(ctx, taskID, deletedRows, msg); updateErr != nil {
		s.logMessage("service.usage_cleanup", "[UsageCleanup] update task failed failed: task=%d err=%v", taskID, updateErr)
	}
}

// isTaskCanceled reports whether persisted status is canceled, treating a
// missing task as uncanceled. It uses a separate two-second background read,
// so the supplied caller context does not control the status check.
func (s *UsageCleanupService) isTaskCanceled(ctx context.Context, taskID int64) (bool, error) {
	if s == nil || s.repo == nil {
		return false, fmt.Errorf("cleanup service not ready")
	}
	checkCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	status, err := s.repo.GetTaskStatus(checkCtx, taskID)
	if err != nil {
		// A missing status row supplies no persisted cancellation request. Keep the
		// deletion round eligible, allowing its next repository operation to determine progress.
		if errors.Is(err, ErrCleanupTaskNotFound) {
			return false, nil
		}
		return false, err
	}
	if status == UsageCleanupStatusCanceled {
		s.logMessage("service.usage_cleanup", "[UsageCleanup] task cancel detected: task=%d", taskID)
	}
	return status == UsageCleanupStatusCanceled, nil
}

// validateFilters checks that both inclusive cleanup bounds exist and the end
// does not precede the start. Equal bounds are accepted; the configured
// maximum measures elapsed twenty-four-hour periods rather than local
// calendar dates.
func (s *UsageCleanupService) validateFilters(filters UsageCleanupFilters) error {
	if filters.StartTime.IsZero() || filters.EndTime.IsZero() {
		return infraerrors.BadRequest("USAGE_CLEANUP_MISSING_RANGE", "start_date and end_date are required")
	}
	if filters.EndTime.Before(filters.StartTime) {
		return infraerrors.BadRequest("USAGE_CLEANUP_INVALID_RANGE", "end_date must be after start_date")
	}
	maxDays := s.maxRangeDays()
	if maxDays > 0 {
		delta := filters.EndTime.Sub(filters.StartTime)
		// A calendar-date span can consume a different elapsed duration on DST days.
		if delta > time.Duration(maxDays)*24*time.Hour {
			return infraerrors.BadRequest("USAGE_CLEANUP_RANGE_TOO_LARGE", fmt.Sprintf("date range exceeds %d days", maxDays))
		}
	}
	return nil
}

// CancelTask records cancellation for a pending or running task attributed to
// a positive operator ID. Disabled cleanup, missing tasks and
// already-terminal noncanceled statuses return their corresponding errors; an
// already-canceled task succeeds without another write.
//
// The repository conditionally performs the state change after the initial
// status read. If another transition wins, a second read accepts an
// already-canceled result and otherwise reports conflict. A worker observes
// persisted cancellation between batches, so this operation does not
// establish that a current delete has finished.
func (s *UsageCleanupService) CancelTask(ctx context.Context, taskID int64, canceledBy int64) error {
	if s == nil || s.repo == nil {
		return fmt.Errorf("cleanup service not ready")
	}
	if s.cfg != nil && !s.cfg.UsageCleanup.Enabled {
		return infraerrors.New(infraerrors.CategoryServiceUnavailable, "USAGE_CLEANUP_DISABLED", "usage cleanup is disabled")
	}
	if canceledBy <= 0 {
		return infraerrors.BadRequest("USAGE_CLEANUP_INVALID_CANCELLER", "invalid canceller")
	}
	status, err := s.repo.GetTaskStatus(ctx, taskID)
	if err != nil {
		if errors.Is(err, ErrCleanupTaskNotFound) {
			return infraerrors.New(infraerrors.CategoryNotFound, "USAGE_CLEANUP_TASK_NOT_FOUND", "cleanup task not found")
		}
		return err
	}
	s.logMessage("service.usage_cleanup", "[UsageCleanup] cancel_task requested: task=%d operator=%d status=%s", taskID, canceledBy, status)
	if status == UsageCleanupStatusCanceled {
		s.logMessage("service.usage_cleanup", "[UsageCleanup] cancel_task idempotent hit: task=%d operator=%d", taskID, canceledBy)
		return nil
	}
	if status != UsageCleanupStatusPending && status != UsageCleanupStatusRunning {
		return infraerrors.New(infraerrors.CategoryConflict, "USAGE_CLEANUP_CANCEL_CONFLICT", "cleanup task cannot be canceled in current status")
	}
	ok, err := s.repo.CancelTask(ctx, taskID, canceledBy)
	if err != nil {
		return err
	}
	if !ok {
		// Conditional cancellation can lose to another transition after the first read.
		// Recheck canceled status so overlapping cancellation requests can still return success.
		currentStatus, getErr := s.repo.GetTaskStatus(ctx, taskID)
		if getErr == nil && currentStatus == UsageCleanupStatusCanceled {
			s.logMessage("service.usage_cleanup", "[UsageCleanup] cancel_task idempotent race hit: task=%d operator=%d", taskID, canceledBy)
			return nil
		}
		return infraerrors.New(infraerrors.CategoryConflict, "USAGE_CLEANUP_CANCEL_CONFLICT", "cleanup task cannot be canceled in current status")
	}
	s.logMessage("service.usage_cleanup", "[UsageCleanup] cancel_task done: task=%d operator=%d", taskID, canceledBy)
	return nil
}

// sanitizeUsageCleanupFilters clears unusable optional selectors and trims
// model text in place. Nil filters return immediately, and supported values
// remain available for task persistence and later batch deletion.
//
// Nonpositive identity selectors, blank model text and negative billing
// categories become absent. A valid request category is normalized and takes
// precedence by clearing Stream. An invalid category becomes absent while
// Stream remains available, allowing the legacy selector to keep its
// filtering role.
func sanitizeUsageCleanupFilters(filters *UsageCleanupFilters) {
	if filters == nil {
		return
	}
	// Nonpositive UserID, APIKeyID, ProviderID, GroupID and TeamID values represent
	// unusable identity selectors; discard them before the task reaches persisted batch filters.
	if filters.UserID != nil && *filters.UserID <= 0 {
		filters.UserID = nil
	}
	if filters.APIKeyID != nil && *filters.APIKeyID <= 0 {
		filters.APIKeyID = nil
	}
	if filters.ProviderID != nil && *filters.ProviderID <= 0 {
		filters.ProviderID = nil
	}
	if filters.GroupID != nil && *filters.GroupID <= 0 {
		filters.GroupID = nil
	}
	if filters.TeamID != nil && *filters.TeamID <= 0 {
		filters.TeamID = nil
	}
	// Blank model input carries no usable equality selector. Remove it after trimming
	// so persistence does not retain a filter that batch deletion later ignores.
	if filters.Model != nil {
		model := strings.TrimSpace(*filters.Model)
		if model == "" {
			filters.Model = nil
		} else {
			filters.Model = &model
		}
	}
	// Supported categories take precedence over legacy stream filtering. An invalid
	// category is discarded while its supplied stream selector remains available for deletion.
	if filters.RequestType != nil {
		requestType := RequestType(*filters.RequestType)
		if !requestType.IsValid() {
			filters.RequestType = nil
		} else {
			value := int16(requestType.Normalize())
			filters.RequestType = &value
			filters.Stream = nil
		}
	}
	// Negative billing categories have no usable stored funding label. Remove them
	// so a cleanup task can use its remaining filters rather than an invalid equality.
	if filters.BillingType != nil && *filters.BillingType < 0 {
		filters.BillingType = nil
	}
}

// maxRangeDays returns the positive configured cleanup range limit or
// thirty-one elapsed days. Nil services, missing options and nonpositive
// values select the default used by inclusive-range duration validation.
func (s *UsageCleanupService) maxRangeDays() int {
	if s == nil || s.cfg == nil {
		return 31
	}
	if s.cfg.UsageCleanup.MaxRangeDays > 0 {
		return s.cfg.UsageCleanup.MaxRangeDays
	}
	return 31
}

// batchSize returns the positive configured deletion batch limit or five
// thousand rows. Nil services, missing options and nonpositive values select
// that default for each repository batch.
func (s *UsageCleanupService) batchSize() int {
	if s == nil || s.cfg == nil {
		return 5000
	}
	if s.cfg.UsageCleanup.BatchSize > 0 {
		return s.cfg.UsageCleanup.BatchSize
	}
	return 5000
}

// workerInterval returns the positive configured callback interval as seconds
// or ten seconds. Nil services, missing options and nonpositive values select
// that default when Start registers recurring work.
func (s *UsageCleanupService) workerInterval() time.Duration {
	if s == nil || s.cfg == nil {
		return 10 * time.Second
	}
	if s.cfg.UsageCleanup.WorkerIntervalSeconds > 0 {
		return time.Duration(s.cfg.UsageCleanup.WorkerIntervalSeconds) * time.Second
	}
	return 10 * time.Second
}

// taskTimeout returns the positive configured worker-round timeout as seconds
// or thirty minutes. Nil services, missing options and nonpositive values
// select the default used for execution and stale-running reclamation.
func (s *UsageCleanupService) taskTimeout() time.Duration {
	if s == nil || s.cfg == nil {
		return 30 * time.Minute
	}
	if s.cfg.UsageCleanup.TaskTimeoutSeconds > 0 {
		return time.Duration(s.cfg.UsageCleanup.TaskTimeoutSeconds) * time.Second
	}
	return 30 * time.Minute
}

// recomputeDeletedRange requests aggregate repair when an aggregator is
// bound. Repair uses that runtime's context; dispatch can be ignored before
// Start or after shutdown, and range-validation errors are logged separately
// from the cleanup outcome.
// @project-doc docs/operations/pre_aggregation.md#usage_state_ownership
func (s *UsageCleanupService) recomputeDeletedRange(task *UsageCleanupTask) {
	if s.dashboard != nil {
		// The trigger validates the range synchronously and dispatches repair
		// separately. Log validation failure while retaining the task outcome and
		// its already completed deletions.
		if err := s.dashboard.TriggerRecomputeRange(task.Filters.StartTime, task.Filters.EndTime); err != nil {
			s.logMessage("service.usage_cleanup", "[UsageCleanup] trigger dashboard recompute failed: task=%d err=%v", task.ID, err)
		} else {
			s.logMessage("service.usage_cleanup", "[UsageCleanup] trigger dashboard recompute: task=%d start=%s end=%s", task.ID, task.Filters.StartTime.UTC().Format(time.RFC3339), task.Filters.EndTime.UTC().Format(time.RFC3339))
		}
	}
}

// logMessage reports cleanup diagnostics through the configured reporter when
// present. Nil services or missing reporters use logUsage, keeping worker and
// task diagnostics available through the package's reporting helper.
func (s *UsageCleanupService) logMessage(component, format string, args ...any) {
	if s != nil && s.cfg != nil && s.cfg.Logf != nil {
		s.cfg.Logf(component, format, args...)
		return
	}
	// An absent injected reporter uses the package reporter so lifecycle and failure
	// paths still publish their diagnostics for services built directly or in tests.
	logUsage(component, format, args...)
}
