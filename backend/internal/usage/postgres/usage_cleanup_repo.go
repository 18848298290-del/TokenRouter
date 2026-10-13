package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	dbusagecleanuptask "github.com/TokenFlux/TokenRouter/ent/usagecleanuptask"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Usage cleanup persists filters and progress separately from the raw rows each
// batch removes. Task creation and status operations prefer an available Ent
// client, including a transaction client from context. Claiming and batch
// deletion use the SQL executor, so assembly supplies both access paths when
// Ent-backed task operations are selected. Filters include both time endpoints,
// and cleanup repair must account for the end instant's bucket.
//
// Claiming selects a pending or stale running task and updates it in one SKIP
// LOCKED statement. Cancellation records a conditional pending/running
// transition; progress and terminal outcomes instead match task ID directly.
// The service coordinates those calls between batches. Deletion, progress
// publication, and aggregate repair are separate operations whose transaction
// and cancellation rules come from their owning callers.

// CleanupStore persists cleanup task state and deletes filtered raw usage
// batches.
type CleanupStore struct {
	// client selects Ent for task CRUD when present. A context transaction
	// client takes precedence over this default; claim and deletion still use sql.
	client *dbent.Client // default cleanup Ent client
	// sql executes claims and raw deletion, plus task CRUD when client is nil.
	// Its owner controls the connection, transaction completion, and shutdown.
	sql sqlExecutor // caller-selected cleanup SQL executor
}

// NewUsageCleanupRepository binds the default Ent client and SQL database for
// cleanup operations. An available client selects Ent task CRUD, while SQL
// remains required for claiming and deleting usage batches.
func NewUsageCleanupRepository(client *dbent.Client, sqlDB *sql.DB) usage.UsageCleanupRepository {
	return NewCleanupStoreWithSQL(client, sqlDB)
}

// NewCleanupStoreWithSQL binds task persistence and batch deletion to
// caller-owned dependencies. A nonnil client selects Ent task CRUD, and the
// supplied SQL executor retains its own connection and transaction lifecycle.
func NewCleanupStoreWithSQL(client *dbent.Client, sqlq sqlExecutor) *CleanupStore {
	return &CleanupStore{client: client, sql: sqlq}
}

// CreateTask inserts task filters, initial status, creator, and deletion count,
// then fills its ID and timestamps. Nil performs no work; an available Ent
// client takes precedence over SQL for this operation.
func (r *CleanupStore) CreateTask(ctx context.Context, task *usage.UsageCleanupTask) error {
	if task == nil {
		return nil
	}
	if r.client != nil {
		return r.createTaskWithEnt(ctx, task)
	}
	return r.createTaskWithSQL(ctx, task)
}

// ListTasks returns cleanup metadata newest by created_at then ID, with
// separately queried pagination totals. Ent takes precedence when present;
// malformed stored filter JSON or query errors abort the page, and an empty
// task table returns an empty slice.
func (r *CleanupStore) ListTasks(ctx context.Context, params pagination.PaginationParams) ([]usage.UsageCleanupTask, *pagination.PaginationResult, error) {
	if r.client != nil {
		return r.listTasksWithEnt(ctx, params)
	}
	var total int64
	if err := scanSingleRow(ctx, r.sql, "SELECT COUNT(*) FROM usage_cleanup_tasks", nil, &total); err != nil {
		return nil, nil, err
	}
	if total == 0 {
		return []usage.UsageCleanupTask{}, paginationResultFromTotal(0, params), nil
	}

	query := `
		SELECT id, status, filters, created_by, deleted_rows, error_message,
			canceled_by, canceled_at,
			started_at, finished_at, created_at, updated_at
		FROM usage_cleanup_tasks
		ORDER BY created_at DESC, id DESC
		LIMIT $1 OFFSET $2
	`
	rows, err := r.sql.QueryContext(ctx, query, params.Limit(), params.Offset())
	if err != nil {
		return nil, nil, err
	}
	// Release the task page on every return, including malformed filter rows;
	// deferred closing is best effort and keeps the selected query or decoding
	// error.
	defer func() { _ = rows.Close() }()

	tasks := make([]usage.UsageCleanupTask, 0)
	for rows.Next() {
		var task usage.UsageCleanupTask
		var filtersJSON []byte
		var errMsg sql.NullString
		var canceledBy sql.NullInt64
		var canceledAt sql.NullTime
		var startedAt sql.NullTime
		var finishedAt sql.NullTime
		if err := rows.Scan(
			&task.ID,
			&task.Status,
			&filtersJSON,
			&task.CreatedBy,
			&task.DeletedRows,
			&errMsg,
			&canceledBy,
			&canceledAt,
			&startedAt,
			&finishedAt,
			&task.CreatedAt,
			&task.UpdatedAt,
		); err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal(filtersJSON, &task.Filters); err != nil {
			return nil, nil, fmt.Errorf("parse cleanup filters: %w", err)
		}
		if errMsg.Valid {
			task.ErrorMsg = &errMsg.String
		}
		if canceledBy.Valid {
			v := canceledBy.Int64
			task.CanceledBy = &v
		}
		if canceledAt.Valid {
			task.CanceledAt = &canceledAt.Time
		}
		if startedAt.Valid {
			task.StartedAt = &startedAt.Time
		}
		if finishedAt.Valid {
			task.FinishedAt = &finishedAt.Time
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return tasks, paginationResultFromTotal(total, params), nil
}

// ClaimNextPendingTask selects the oldest eligible task and marks it running in
// one SQL statement. Pending tasks qualify immediately; running tasks qualify
// when their nonnull started_at precedes the stale threshold. Nonpositive
// staleRunningAfterSeconds selects 1800 seconds, and no eligible row returns
// nil.
//
// SKIP LOCKED lets another claimant skip a currently locked candidate. The
// update retains deleted_rows while resetting start, finish, and error
// metadata. Filter JSON is decoded after the update; decoding failure does not
// roll back an autocommitted claim, and stale reclamation does not establish
// exclusive ownership for the entire task lifetime.
func (r *CleanupStore) ClaimNextPendingTask(ctx context.Context, staleRunningAfterSeconds int64) (*usage.UsageCleanupTask, error) {
	if staleRunningAfterSeconds <= 0 {
		staleRunningAfterSeconds = 1800
	}
	// Select and update within one statement, keeping the chosen task row locked
	// during its transition while other claimants can skip locked candidates.
	query := `
		WITH next AS (
			SELECT id
			FROM usage_cleanup_tasks
			WHERE status = $1
				OR (
					status = $2
					AND started_at IS NOT NULL
					AND started_at < NOW() - ($3 * interval '1 second')
				)
			ORDER BY created_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE usage_cleanup_tasks AS tasks
		SET status = $4,
			started_at = NOW(),
			finished_at = NULL,
			error_message = NULL,
			updated_at = NOW()
		FROM next
		WHERE tasks.id = next.id
		RETURNING tasks.id, tasks.status, tasks.filters, tasks.created_by, tasks.deleted_rows, tasks.error_message,
			tasks.started_at, tasks.finished_at, tasks.created_at, tasks.updated_at
	`
	var task usage.UsageCleanupTask
	var filtersJSON []byte
	var errMsg sql.NullString
	var startedAt sql.NullTime
	var finishedAt sql.NullTime
	if err := scanSingleRow(
		ctx,
		r.sql,
		query,
		[]any{
			usage.UsageCleanupStatusPending,
			usage.UsageCleanupStatusRunning,
			staleRunningAfterSeconds,
			usage.UsageCleanupStatusRunning,
		},
		&task.ID,
		&task.Status,
		&filtersJSON,
		&task.CreatedBy,
		&task.DeletedRows,
		&errMsg,
		&startedAt,
		&finishedAt,
		&task.CreatedAt,
		&task.UpdatedAt,
	); err != nil {
		// No returned candidate means this poll found no eligible task. Convert that
		// row sentinel to an idle worker result instead of failure.
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	// The running transition has already executed. A filter decoding error returns
	// to the worker but cannot undo a claim committed by this executor.
	if err := json.Unmarshal(filtersJSON, &task.Filters); err != nil {
		return nil, fmt.Errorf("parse cleanup filters: %w", err)
	}
	if errMsg.Valid {
		task.ErrorMsg = &errMsg.String
	}
	if startedAt.Valid {
		task.StartedAt = &startedAt.Time
	}
	if finishedAt.Valid {
		task.FinishedAt = &finishedAt.Time
	}
	return &task, nil
}

// GetTaskStatus reads the persisted state for taskID, preferring Ent when
// available. Missing tasks return an error matching both ErrCleanupTaskNotFound
// and sql.ErrNoRows; other database failures propagate.
func (r *CleanupStore) GetTaskStatus(ctx context.Context, taskID int64) (string, error) {
	if r.client != nil {
		return r.getTaskStatusWithEnt(ctx, taskID)
	}
	var status string
	if err := scanSingleRow(ctx, r.sql, "SELECT status FROM usage_cleanup_tasks WHERE id = $1", []any{taskID}, &status); err != nil {
		// Attach the cleanup sentinel while retaining the SQL cause so cancellation
		// polling and storage callers can identify the same missing task.
		if errors.Is(err, sql.ErrNoRows) {
			return "", errors.Join(usage.ErrCleanupTaskNotFound, err)
		}
		return "", err
	}
	return status, nil
}

// UpdateTaskProgress stores the cumulative deletedRows count and refreshes
// updated_at for taskID. It matches any stored status and leaves started_at
// unchanged, so callers handle cancellation and stale-task timing separately.
// An ID with no matching task also returns success.
func (r *CleanupStore) UpdateTaskProgress(ctx context.Context, taskID int64, deletedRows int64) error {
	if r.client != nil {
		return r.updateTaskProgressWithEnt(ctx, taskID, deletedRows)
	}
	query := `
		UPDATE usage_cleanup_tasks
		SET deleted_rows = $1,
			updated_at = NOW()
		WHERE id = $2
	`
	_, err := r.sql.ExecContext(ctx, query, deletedRows, taskID)
	return err
}

// CancelTask marks a pending or running task canceled, records its operator and
// timestamps, and clears its error. False means no eligible row changed,
// including missing or completed tasks; workers observe this persisted status
// between batches.
func (r *CleanupStore) CancelTask(ctx context.Context, taskID int64, canceledBy int64) (bool, error) {
	if r.client != nil {
		return r.cancelTaskWithEnt(ctx, taskID, canceledBy)
	}
	query := `
		UPDATE usage_cleanup_tasks
		SET status = $1,
			canceled_by = $3,
			canceled_at = NOW(),
			finished_at = NOW(),
			error_message = NULL,
			updated_at = NOW()
		WHERE id = $2
			AND status IN ($4, $5)
		RETURNING id
	`
	var id int64
	err := scanSingleRow(ctx, r.sql, query, []any{
		usage.UsageCleanupStatusCanceled,
		taskID,
		canceledBy,
		usage.UsageCleanupStatusPending,
		usage.UsageCleanupStatusRunning,
	}, &id)
	// An absent RETURNING row covers both missing tasks and states outside
	// pending/running; report an unchanged result so the service resolves that
	// condition.
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// MarkTaskSucceeded stores success, cumulative deletion count, and
// finish/update timestamps for taskID. The update matches ID regardless of the
// current status, so the service must coordinate outcome publication with
// cancellation; a missing ID is not reported.
func (r *CleanupStore) MarkTaskSucceeded(ctx context.Context, taskID int64, deletedRows int64) error {
	if r.client != nil {
		return r.markTaskSucceededWithEnt(ctx, taskID, deletedRows)
	}
	query := `
		UPDATE usage_cleanup_tasks
		SET status = $1,
			deleted_rows = $2,
			finished_at = NOW(),
			updated_at = NOW()
		WHERE id = $3
	`
	_, err := r.sql.ExecContext(ctx, query, usage.UsageCleanupStatusSucceeded, deletedRows, taskID)
	return err
}

// MarkTaskFailed stores failure, cumulative deletion count, error text, and
// finish/update timestamps for taskID. It matches ID regardless of current
// status, so callers coordinate it with cancellation and supply the error text
// to persist. A missing task also returns success.
func (r *CleanupStore) MarkTaskFailed(ctx context.Context, taskID int64, deletedRows int64, errorMsg string) error {
	if r.client != nil {
		return r.markTaskFailedWithEnt(ctx, taskID, deletedRows, errorMsg)
	}
	query := `
		UPDATE usage_cleanup_tasks
		SET status = $1,
			deleted_rows = $2,
			error_message = $3,
			finished_at = NOW(),
			updated_at = NOW()
		WHERE id = $4
	`
	_, err := r.sql.ExecContext(ctx, query, usage.UsageCleanupStatusFailed, deletedRows, errorMsg, taskID)
	return err
}

// DeleteUsageLogsBatch selects the oldest matching raw IDs by created_at then
// ID and counts deleted rows returned by SQL. Both time endpoints must be
// nonzero, and callers supply a valid batch limit. EndTime is inclusive, while
// other conditions come from buildUsageCleanupWhere.
//
// The single SQL statement chooses IDs and deletes them on the current
// executor; RETURNING row order is not specified. A query or iteration error
// returns zero with that error, and transaction ownership determines whether
// attempted deletion commits. Task progress and aggregate repair are separate
// calls, so the returned count alone does not publish either outcome.
func (r *CleanupStore) DeleteUsageLogsBatch(ctx context.Context, filters usage.UsageCleanupFilters, limit int) (int64, error) {
	if filters.StartTime.IsZero() || filters.EndTime.IsZero() {
		return 0, fmt.Errorf("cleanup filters missing time range")
	}
	whereClause, args := buildUsageCleanupWhere(filters)
	if whereClause == "" {
		return 0, fmt.Errorf("cleanup filters missing time range")
	}
	args = append(args, limit)
	query := fmt.Sprintf(`
		WITH target AS (
			SELECT id
			FROM usage_logs
			WHERE %s
			ORDER BY created_at ASC, id ASC
			LIMIT $%d
		)
		DELETE FROM usage_logs
		WHERE id IN (SELECT id FROM target)
		RETURNING id
	`, whereClause, len(args))

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	// Release RETURNING rows on every exit. The count comes from completed
	// iteration, while this deferred cleanup does not replace a query or iteration
	// result.
	defer func() { _ = rows.Close() }()

	var deleted int64
	for rows.Next() {
		deleted++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return deleted, nil
}

// buildUsageCleanupWhere returns bound conditions for the supplied cleanup
// filters, including either nonzero time endpoint. DeleteUsageLogsBatch
// separately requires both endpoints. ID pointers add equality conditions,
// while trimmed blank models add none.
//
// RequestType takes precedence over Stream. Sync, stream, and websocket
// selectors include compatible legacy zero-type rows through the shared
// predicate. Other filters retain their argument order so the LIMIT binding
// follows their values. EndTime is inclusive and can delete a record exactly
// at a bucket's start, requiring repair of that bucket.
func buildUsageCleanupWhere(filters usage.UsageCleanupFilters) (string, []any) {
	conditions := make([]string, 0, 8)
	args := make([]any, 0, 8)
	idx := 1
	if !filters.StartTime.IsZero() {
		conditions = append(conditions, fmt.Sprintf("created_at >= $%d", idx))
		args = append(args, filters.StartTime)
		idx++
	}
	// Cleanup includes a record at EndTime, so aggregate repair must also rebuild
	// the hour or date beginning at an exact endpoint.
	if !filters.EndTime.IsZero() {
		conditions = append(conditions, fmt.Sprintf("created_at <= $%d", idx))
		args = append(args, filters.EndTime)
		idx++
	}
	if filters.UserID != nil {
		conditions = append(conditions, fmt.Sprintf("user_id = $%d", idx))
		args = append(args, *filters.UserID)
		idx++
	}
	if filters.APIKeyID != nil {
		conditions = append(conditions, fmt.Sprintf("api_key_id = $%d", idx))
		args = append(args, *filters.APIKeyID)
		idx++
	}
	if filters.ProviderID != nil {
		conditions = append(conditions, fmt.Sprintf("provider_id = $%d", idx))
		args = append(args, *filters.ProviderID)
		idx++
	}
	if filters.GroupID != nil {
		conditions = append(conditions, fmt.Sprintf("group_id = $%d", idx))
		args = append(args, *filters.GroupID)
		idx++
	}
	if filters.TeamID != nil {
		conditions = append(conditions, fmt.Sprintf("team_id = $%d", idx))
		args = append(args, *filters.TeamID)
		idx++
	}
	if filters.Model != nil {
		model := strings.TrimSpace(*filters.Model)
		if model != "" {
			conditions = append(conditions, fmt.Sprintf("model = $%d", idx))
			args = append(args, model)
			idx++
		}
	}
	// With RequestType supplied, the shared predicate decides which stored and
	// legacy values qualify. Adding a separate Stream condition would narrow
	// that chosen selection despite the declared precedence.
	if filters.RequestType != nil {
		condition, conditionArgs := buildRequestTypeFilterCondition(idx, *filters.RequestType)
		conditions = append(conditions, condition)
		args = append(args, conditionArgs...)
		idx += len(conditionArgs)
	} else if filters.Stream != nil {
		conditions = append(conditions, fmt.Sprintf("stream = $%d", idx))
		args = append(args, *filters.Stream)
		idx++
	}
	if filters.BillingType != nil {
		conditions = append(conditions, fmt.Sprintf("billing_type = $%d", idx))
		args = append(args, *filters.BillingType)
	}
	return strings.Join(conditions, " AND "), args
}

// createTaskWithEnt inserts a nonnil task using the context's Ent transaction
// client or the default client. It JSON-encodes filters and fills task ID and
// creation/update timestamps after save succeeds.
func (r *CleanupStore) createTaskWithEnt(ctx context.Context, task *usage.UsageCleanupTask) error {
	client := clientFromContext(ctx, r.client)
	filtersJSON, err := json.Marshal(task.Filters)
	if err != nil {
		return fmt.Errorf("marshal cleanup filters: %w", err)
	}
	created, err := client.UsageCleanupTask.
		Create().
		SetStatus(task.Status).
		SetFilters(json.RawMessage(filtersJSON)).
		SetCreatedBy(task.CreatedBy).
		SetDeletedRows(task.DeletedRows).
		Save(ctx)
	if err != nil {
		return err
	}
	task.ID = created.ID
	task.CreatedAt = created.CreatedAt
	task.UpdatedAt = created.UpdatedAt
	return nil
}

// createTaskWithSQL JSON-encodes a nonnil task's filters and inserts initial
// task metadata on the SQL executor. RETURNING fills the caller's ID and
// timestamps; transaction completion remains with the executor owner.
func (r *CleanupStore) createTaskWithSQL(ctx context.Context, task *usage.UsageCleanupTask) error {
	filtersJSON, err := json.Marshal(task.Filters)
	if err != nil {
		return fmt.Errorf("marshal cleanup filters: %w", err)
	}
	query := `
		INSERT INTO usage_cleanup_tasks (
			status,
			filters,
			created_by,
			deleted_rows
		) VALUES ($1, $2, $3, $4)
		RETURNING id, created_at, updated_at
	`
	return scanSingleRow(ctx, r.sql, query, []any{task.Status, filtersJSON, task.CreatedBy, task.DeletedRows}, &task.ID, &task.CreatedAt, &task.UpdatedAt)
}

// listTasksWithEnt returns task rows ordered by creation time and ID descending
// through the context-selected Ent client. Count and page reads are separate;
// an empty count returns an empty slice, and decoding any row's filters aborts
// the page.
func (r *CleanupStore) listTasksWithEnt(ctx context.Context, params pagination.PaginationParams) ([]usage.UsageCleanupTask, *pagination.PaginationResult, error) {
	client := clientFromContext(ctx, r.client)
	query := client.UsageCleanupTask.Query()
	total, err := query.Clone().Count(ctx)
	if err != nil {
		return nil, nil, err
	}
	if total == 0 {
		return []usage.UsageCleanupTask{}, paginationResultFromTotal(0, params), nil
	}
	rows, err := query.
		Order(dbent.Desc(dbusagecleanuptask.FieldCreatedAt), dbent.Desc(dbusagecleanuptask.FieldID)).
		Offset(params.Offset()).
		Limit(params.Limit()).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}
	tasks := make([]usage.UsageCleanupTask, 0, len(rows))
	for _, row := range rows {
		task, err := usageCleanupTaskFromEnt(row)
		if err != nil {
			return nil, nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, paginationResultFromTotal(int64(total), params), nil
}

// getTaskStatusWithEnt reads taskID through the context-selected Ent client. A
// missing Ent row becomes a joined ErrCleanupTaskNotFound and sql.ErrNoRows
// error so cleanup callers can recognize either sentinel.
func (r *CleanupStore) getTaskStatusWithEnt(ctx context.Context, taskID int64) (string, error) {
	client := clientFromContext(ctx, r.client)
	task, err := client.UsageCleanupTask.Query().
		Where(dbusagecleanuptask.IDEQ(taskID)).
		Only(ctx)
	if err != nil {
		// Translate Ent absence into cleanup and SQL sentinels, giving cancellation
		// polling the same missing-task result as direct SQL storage.
		if dbent.IsNotFound(err) {
			return "", errors.Join(usage.ErrCleanupTaskNotFound, sql.ErrNoRows)
		}
		return "", err
	}
	return task.Status, nil
}

// updateTaskProgressWithEnt replaces deleted_rows and updated_at through the
// context-selected Ent client. It matches task ID independently of status and
// leaves the running task's started_at reclamation timestamp unchanged.
func (r *CleanupStore) updateTaskProgressWithEnt(ctx context.Context, taskID int64, deletedRows int64) error {
	client := clientFromContext(ctx, r.client)
	now := time.Now()
	_, err := client.UsageCleanupTask.Update().
		Where(dbusagecleanuptask.IDEQ(taskID)).
		SetDeletedRows(deletedRows).
		SetUpdatedAt(now).
		Save(ctx)
	return err
}

// cancelTaskWithEnt conditionally cancels a pending or running task through the
// context-selected Ent client. Cancellation, finish, and update times share one
// captured timestamp; false reports no eligible row changed.
func (r *CleanupStore) cancelTaskWithEnt(ctx context.Context, taskID int64, canceledBy int64) (bool, error) {
	client := clientFromContext(ctx, r.client)
	now := time.Now()
	affected, err := client.UsageCleanupTask.Update().
		Where(
			dbusagecleanuptask.IDEQ(taskID),
			dbusagecleanuptask.StatusIn(usage.UsageCleanupStatusPending, usage.UsageCleanupStatusRunning),
		).
		SetStatus(usage.UsageCleanupStatusCanceled).
		SetCanceledBy(canceledBy).
		SetCanceledAt(now).
		SetFinishedAt(now).
		ClearErrorMessage().
		SetUpdatedAt(now).
		Save(ctx)
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// markTaskSucceededWithEnt stores success and cumulative deletion count through
// the context-selected Ent client. Finish and update times share one
// observation, and task ID matches independently of the stored status.
func (r *CleanupStore) markTaskSucceededWithEnt(ctx context.Context, taskID int64, deletedRows int64) error {
	client := clientFromContext(ctx, r.client)
	now := time.Now()
	_, err := client.UsageCleanupTask.Update().
		Where(dbusagecleanuptask.IDEQ(taskID)).
		SetStatus(usage.UsageCleanupStatusSucceeded).
		SetDeletedRows(deletedRows).
		SetFinishedAt(now).
		SetUpdatedAt(now).
		Save(ctx)
	return err
}

// markTaskFailedWithEnt stores failure, deletion count, and supplied error text
// through the context-selected Ent client. Finish and update times share one
// observation; callers coordinate this ID-based outcome with cancellation.
func (r *CleanupStore) markTaskFailedWithEnt(ctx context.Context, taskID int64, deletedRows int64, errorMsg string) error {
	client := clientFromContext(ctx, r.client)
	now := time.Now()
	_, err := client.UsageCleanupTask.Update().
		Where(dbusagecleanuptask.IDEQ(taskID)).
		SetStatus(usage.UsageCleanupStatusFailed).
		SetDeletedRows(deletedRows).
		SetErrorMessage(errorMsg).
		SetFinishedAt(now).
		SetUpdatedAt(now).
		Save(ctx)
	return err
}

// usageCleanupTaskFromEnt converts a nonnil Ent row into task metadata and
// decoded filters. Empty filter bytes leave zero filters, malformed JSON
// returns an empty task with an error, and optional metadata pointers remain
// shared with the Ent row.
func usageCleanupTaskFromEnt(row *dbent.UsageCleanupTask) (usage.UsageCleanupTask, error) {
	task := usage.UsageCleanupTask{
		ID:          row.ID,
		Status:      row.Status,
		CreatedBy:   row.CreatedBy,
		DeletedRows: row.DeletedRows,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
	if len(row.Filters) > 0 {
		if err := json.Unmarshal(row.Filters, &task.Filters); err != nil {
			return usage.UsageCleanupTask{}, fmt.Errorf("parse cleanup filters: %w", err)
		}
	}
	// Optional metadata already has the task representation's pointer types.
	// Retain those entity pointers, so callers treat the pointed-to metadata as
	// shared.
	if row.ErrorMessage != nil {
		task.ErrorMsg = row.ErrorMessage
	}
	if row.CanceledBy != nil {
		task.CanceledBy = row.CanceledBy
	}
	if row.CanceledAt != nil {
		task.CanceledAt = row.CanceledAt
	}
	if row.StartedAt != nil {
		task.StartedAt = row.StartedAt
	}
	if row.FinishedAt != nil {
		task.FinishedAt = row.FinishedAt
	}
	return task, nil
}
