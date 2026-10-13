package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/enttest"
	dbusagecleanuptask "github.com/TokenFlux/TokenRouter/ent/usagecleanuptask"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Cleanup metadata can be stored through Ent or direct SQL, while batch filters
// share one inclusive time-range builder. SQLite fixtures exercise Ent
// persistence and state changes; SQL mocks exercise returned metadata, idle
// claims, failures, and bound deletion arguments. The two paths retain the same
// task-facing result shapes.

// newUsageCleanupEntRepo creates an Ent cleanup fixture on a named
// shared-memory SQLite database with foreign keys enabled. It registers
// database and client cleanup with the test and returns both handles for
// persistence assertions.
func newUsageCleanupEntRepo(t *testing.T) (*CleanupStore, *dbent.Client) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:usage_cleanup?mode=memory&cache=shared")
	require.NoError(t, err)
	// Register database cleanup immediately, with later client cleanup running
	// first. Each constructed handle is released if setup ends at an assertion.
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	repo := &CleanupStore{client: client, sql: db}
	return repo, client
}

// TestUsageCleanupRepositoryEntCreateAndList persists pending and running tasks
// through Ent. Listing reports both rows, places the newer task first, and
// restores the earlier task's time filters.
func TestUsageCleanupRepositoryEntCreateAndList(t *testing.T) {
	repo, _ := newUsageCleanupEntRepo(t)

	start := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	task := &usage.UsageCleanupTask{
		Status:    usage.UsageCleanupStatusPending,
		Filters:   usage.UsageCleanupFilters{StartTime: start, EndTime: end},
		CreatedBy: 9,
	}
	require.NoError(t, repo.CreateTask(context.Background(), task))
	require.NotZero(t, task.ID)

	task2 := &usage.UsageCleanupTask{
		Status:    usage.UsageCleanupStatusRunning,
		Filters:   usage.UsageCleanupFilters{StartTime: start.Add(-24 * time.Hour), EndTime: end.Add(-24 * time.Hour)},
		CreatedBy: 10,
	}
	require.NoError(t, repo.CreateTask(context.Background(), task2))

	tasks, result, err := repo.ListTasks(context.Background(), pagination.PaginationParams{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	require.Equal(t, int64(2), result.Total)
	require.Greater(t, tasks[0].ID, tasks[1].ID)
	require.Equal(t, start, tasks[1].Filters.StartTime)
	require.Equal(t, end, tasks[1].Filters.EndTime)
}

// TestUsageCleanupRepositoryEntListEmpty queries a freshly created Ent cleanup
// fixture. An empty task table returns no task items and a pagination total of
// zero.
func TestUsageCleanupRepositoryEntListEmpty(t *testing.T) {
	repo, _ := newUsageCleanupEntRepo(t)

	tasks, result, err := repo.ListTasks(context.Background(), pagination.PaginationParams{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Empty(t, tasks)
	require.Equal(t, int64(0), result.Total)
}

// TestUsageCleanupRepositoryEntGetStatusAndProgress reads a persisted pending
// task and updates its deletion count. A missing task matches both sentinels,
// while the stored existing task retains the supplied progress.
func TestUsageCleanupRepositoryEntGetStatusAndProgress(t *testing.T) {
	repo, client := newUsageCleanupEntRepo(t)

	task := &usage.UsageCleanupTask{
		Status:    usage.UsageCleanupStatusPending,
		Filters:   usage.UsageCleanupFilters{StartTime: time.Now().UTC(), EndTime: time.Now().UTC().Add(time.Hour)},
		CreatedBy: 3,
	}
	require.NoError(t, repo.CreateTask(context.Background(), task))

	status, err := repo.GetTaskStatus(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, usage.UsageCleanupStatusPending, status)

	_, err = repo.GetTaskStatus(context.Background(), task.ID+99)
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.ErrorIs(t, err, usage.ErrCleanupTaskNotFound)

	require.NoError(t, repo.UpdateTaskProgress(context.Background(), task.ID, 42))
	loaded, err := client.UsageCleanupTask.Get(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, int64(42), loaded.DeletedRows)
}

// TestUsageCleanupRepositoryEntCancelAndFinish cancels a pending task and
// inspects cancellation metadata. Changing the stored state to succeeded makes
// a later cancellation report that no eligible task changed.
func TestUsageCleanupRepositoryEntCancelAndFinish(t *testing.T) {
	repo, client := newUsageCleanupEntRepo(t)

	task := &usage.UsageCleanupTask{
		Status:    usage.UsageCleanupStatusPending,
		Filters:   usage.UsageCleanupFilters{StartTime: time.Now().UTC(), EndTime: time.Now().UTC().Add(time.Hour)},
		CreatedBy: 5,
	}
	require.NoError(t, repo.CreateTask(context.Background(), task))

	ok, err := repo.CancelTask(context.Background(), task.ID, 7)
	require.NoError(t, err)
	require.True(t, ok)

	loaded, err := client.UsageCleanupTask.Get(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, usage.UsageCleanupStatusCanceled, loaded.Status)
	require.NotNil(t, loaded.CanceledBy)
	require.NotNil(t, loaded.CanceledAt)
	require.NotNil(t, loaded.FinishedAt)

	loaded.Status = usage.UsageCleanupStatusSucceeded
	// Put the canceled fixture into a terminal state before the second call,
	// isolating the cancellation predicate from the first call's metadata checks.
	_, err = client.UsageCleanupTask.Update().Where(dbusagecleanuptask.IDEQ(task.ID)).SetStatus(loaded.Status).Save(context.Background())
	require.NoError(t, err)

	ok, err = repo.CancelTask(context.Background(), task.ID, 7)
	require.NoError(t, err)
	require.False(t, ok)
}

// TestUsageCleanupRepositoryEntCancelError closes the Ent client before
// cancellation. The repository reports the persistence failure instead of
// treating the attempted transition as a successful cancellation.
func TestUsageCleanupRepositoryEntCancelError(t *testing.T) {
	repo, client := newUsageCleanupEntRepo(t)

	task := &usage.UsageCleanupTask{
		Status:    usage.UsageCleanupStatusPending,
		Filters:   usage.UsageCleanupFilters{StartTime: time.Now().UTC(), EndTime: time.Now().UTC().Add(time.Hour)},
		CreatedBy: 5,
	}
	require.NoError(t, repo.CreateTask(context.Background(), task))

	// Close the shared persistence handle to make cancellation exercise the
	// storage-error path after task creation has already succeeded.
	require.NoError(t, client.Close())
	_, err := repo.CancelTask(context.Background(), task.ID, 7)
	require.Error(t, err)
}

// TestUsageCleanupRepositoryEntMarkResults finishes two running Ent tasks with
// different outcomes. Success stores its count and finish time, while failure
// stores the supplied error message.
func TestUsageCleanupRepositoryEntMarkResults(t *testing.T) {
	repo, client := newUsageCleanupEntRepo(t)

	task := &usage.UsageCleanupTask{
		Status:    usage.UsageCleanupStatusRunning,
		Filters:   usage.UsageCleanupFilters{StartTime: time.Now().UTC(), EndTime: time.Now().UTC().Add(time.Hour)},
		CreatedBy: 12,
	}
	require.NoError(t, repo.CreateTask(context.Background(), task))

	require.NoError(t, repo.MarkTaskSucceeded(context.Background(), task.ID, 6))
	loaded, err := client.UsageCleanupTask.Get(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, usage.UsageCleanupStatusSucceeded, loaded.Status)
	require.Equal(t, int64(6), loaded.DeletedRows)
	require.NotNil(t, loaded.FinishedAt)

	task2 := &usage.UsageCleanupTask{
		Status:    usage.UsageCleanupStatusRunning,
		Filters:   usage.UsageCleanupFilters{StartTime: time.Now().UTC(), EndTime: time.Now().UTC().Add(time.Hour)},
		CreatedBy: 12,
	}
	require.NoError(t, repo.CreateTask(context.Background(), task2))

	require.NoError(t, repo.MarkTaskFailed(context.Background(), task2.ID, 4, "boom"))
	loaded2, err := client.UsageCleanupTask.Get(context.Background(), task2.ID)
	require.NoError(t, err)
	require.Equal(t, usage.UsageCleanupStatusFailed, loaded2.Status)
	require.Equal(t, "boom", *loaded2.ErrorMessage)
}

// TestUsageCleanupRepositoryEntInvalidStatus creates a task with a status
// outside the Ent enum. The schema-backed save rejects the task and returns an
// error.
func TestUsageCleanupRepositoryEntInvalidStatus(t *testing.T) {
	repo, _ := newUsageCleanupEntRepo(t)

	task := &usage.UsageCleanupTask{
		Status:    "invalid",
		Filters:   usage.UsageCleanupFilters{StartTime: time.Now().UTC(), EndTime: time.Now().UTC().Add(time.Hour)},
		CreatedBy: 1,
	}
	require.Error(t, repo.CreateTask(context.Background(), task))
}

// TestUsageCleanupRepositoryEntListInvalidFilters inserts invalid filter bytes
// through the underlying SQLite driver. Listing through Ent returns an error
// when the stored row cannot be decoded.
func TestUsageCleanupRepositoryEntListInvalidFilters(t *testing.T) {
	repo, client := newUsageCleanupEntRepo(t)

	now := time.Now().UTC()
	driver, ok := client.Driver().(*entsql.Driver)
	require.True(t, ok)
	// Write malformed filters below Ent so the fixture can represent invalid
	// stored bytes that normal task creation would JSON-encode first.
	_, err := driver.DB().ExecContext(
		context.Background(),
		`INSERT INTO usage_cleanup_tasks (status, filters, created_by, deleted_rows, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		usage.UsageCleanupStatusPending,
		[]byte("invalid-json"),
		int64(1),
		int64(0),
		now,
		now,
	)
	require.NoError(t, err)

	_, _, err = repo.ListTasks(context.Background(), pagination.PaginationParams{Page: 1, PageSize: 10})
	require.Error(t, err)
}

// TestUsageCleanupTaskFromEntFull converts an Ent row carrying all optional
// metadata pointers. The result retains task identity and status, with error,
// cancellation, start, and finish fields present.
func TestUsageCleanupTaskFromEntFull(t *testing.T) {
	start := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	errMsg := "failed"
	canceledBy := int64(2)
	canceledAt := start.Add(time.Minute)
	startedAt := start.Add(2 * time.Minute)
	finishedAt := start.Add(3 * time.Minute)
	filters := usage.UsageCleanupFilters{StartTime: start, EndTime: end}
	filtersJSON, err := json.Marshal(filters)
	require.NoError(t, err)

	task, err := usageCleanupTaskFromEnt(&dbent.UsageCleanupTask{
		ID:           10,
		Status:       usage.UsageCleanupStatusFailed,
		Filters:      filtersJSON,
		CreatedBy:    11,
		DeletedRows:  7,
		ErrorMessage: &errMsg,
		CanceledBy:   &canceledBy,
		CanceledAt:   &canceledAt,
		StartedAt:    &startedAt,
		FinishedAt:   &finishedAt,
		CreatedAt:    start,
		UpdatedAt:    end,
	})
	require.NoError(t, err)
	require.Equal(t, int64(10), task.ID)
	require.Equal(t, usage.UsageCleanupStatusFailed, task.Status)
	require.NotNil(t, task.ErrorMsg)
	require.NotNil(t, task.CanceledBy)
	require.NotNil(t, task.CanceledAt)
	require.NotNil(t, task.StartedAt)
	require.NotNil(t, task.FinishedAt)
}

// TestUsageCleanupTaskFromEntInvalidFilters converts an Ent row with malformed
// filter JSON. Conversion reports an error and returns an empty task rather
// than partially decoded metadata.
func TestUsageCleanupTaskFromEntInvalidFilters(t *testing.T) {
	task, err := usageCleanupTaskFromEnt(&dbent.UsageCleanupTask{
		Filters: json.RawMessage("invalid-json"),
	})
	require.Error(t, err)
	require.Empty(t, task)
}

// TestNewUsageCleanupRepository constructs cleanup persistence with a nil Ent
// client and mocked SQL database. The constructor returns a nonnil repository
// with those supplied persistence dependencies bound.
func TestNewUsageCleanupRepository(t *testing.T) {
	db, _ := newSQLMock(t)
	repo := NewUsageCleanupRepository(nil, db)
	require.NotNil(t, repo)
}

// TestUsageCleanupRepositoryCreateTask mocks a successful task INSERT with
// returned identity and timestamps. CreateTask writes those returned values
// into the supplied task and satisfies the expected arguments.
func TestUsageCleanupRepositoryCreateTask(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &CleanupStore{sql: db}

	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	task := &usage.UsageCleanupTask{
		Status:    usage.UsageCleanupStatusPending,
		Filters:   usage.UsageCleanupFilters{StartTime: start, EndTime: end},
		CreatedBy: 12,
	}
	now := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery("INSERT INTO usage_cleanup_tasks").
		WithArgs(task.Status, sqlmock.AnyArg(), task.CreatedBy, task.DeletedRows).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(1), now, now))

	err := repo.CreateTask(context.Background(), task)
	require.NoError(t, err)
	require.Equal(t, int64(1), task.ID)
	require.Equal(t, now, task.CreatedAt)
	require.Equal(t, now, task.UpdatedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageCleanupRepositoryCreateTaskNil passes no task to a SQL-backed
// cleanup repository. The call succeeds and satisfies an empty mock expectation
// set without inserting task metadata.
func TestUsageCleanupRepositoryCreateTaskNil(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &CleanupStore{sql: db}

	err := repo.CreateTask(context.Background(), nil)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageCleanupRepositoryCreateTaskQueryError injects a connection failure
// into the task INSERT. CreateTask returns an error and consumes the expected
// query with its initial task arguments.
func TestUsageCleanupRepositoryCreateTaskQueryError(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &CleanupStore{sql: db}

	task := &usage.UsageCleanupTask{
		Status:    usage.UsageCleanupStatusPending,
		Filters:   usage.UsageCleanupFilters{StartTime: time.Now(), EndTime: time.Now().Add(time.Hour)},
		CreatedBy: 1,
	}

	mock.ExpectQuery("INSERT INTO usage_cleanup_tasks").
		WithArgs(task.Status, sqlmock.AnyArg(), task.CreatedBy, task.DeletedRows).
		WillReturnError(sql.ErrConnDone)

	err := repo.CreateTask(context.Background(), task)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageCleanupRepositoryListTasksEmpty makes the SQL count query report
// zero tasks. Listing returns an empty result and zero pagination total with no
// page-row expectation.
func TestUsageCleanupRepositoryListTasksEmpty(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &CleanupStore{sql: db}

	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM usage_cleanup_tasks").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))

	tasks, result, err := repo.ListTasks(context.Background(), pagination.PaginationParams{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Empty(t, tasks)
	require.Equal(t, int64(0), result.Total)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageCleanupRepositoryListTasks supplies one SQL task row with encoded
// filters and optional metadata. Listing decodes its identity, status, creator,
// progress, error and lifecycle fields alongside the counted total.
func TestUsageCleanupRepositoryListTasks(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &CleanupStore{sql: db}

	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	filters := usage.UsageCleanupFilters{StartTime: start, EndTime: end}
	filtersJSON, err := json.Marshal(filters)
	require.NoError(t, err)

	createdAt := time.Date(2024, 1, 2, 12, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Minute)
	rows := sqlmock.NewRows([]string{
		"id", "status", "filters", "created_by", "deleted_rows", "error_message",
		"canceled_by", "canceled_at",
		"started_at", "finished_at", "created_at", "updated_at",
	}).AddRow(
		int64(1),
		usage.UsageCleanupStatusSucceeded,
		filtersJSON,
		int64(2),
		int64(9),
		"error",
		nil,
		nil,
		start,
		end,
		createdAt,
		updatedAt,
	)

	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM usage_cleanup_tasks").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
	mock.ExpectQuery("SELECT id, status, filters, created_by, deleted_rows, error_message").
		WithArgs(20, 0).
		WillReturnRows(rows)

	tasks, result, err := repo.ListTasks(context.Background(), pagination.PaginationParams{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, int64(1), tasks[0].ID)
	require.Equal(t, usage.UsageCleanupStatusSucceeded, tasks[0].Status)
	require.Equal(t, int64(2), tasks[0].CreatedBy)
	require.Equal(t, int64(9), tasks[0].DeletedRows)
	require.NotNil(t, tasks[0].ErrorMsg)
	require.Equal(t, "error", *tasks[0].ErrorMsg)
	require.NotNil(t, tasks[0].StartedAt)
	require.NotNil(t, tasks[0].FinishedAt)
	require.Equal(t, int64(1), result.Total)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageCleanupRepositoryListTasksQueryError returns a positive count
// followed by a page-query failure. Listing reports the error and consumes the
// expected pagination arguments.
func TestUsageCleanupRepositoryListTasksQueryError(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &CleanupStore{sql: db}

	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM usage_cleanup_tasks").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(2)))
	mock.ExpectQuery("SELECT id, status, filters, created_by, deleted_rows, error_message").
		WithArgs(20, 0).
		WillReturnError(sql.ErrConnDone)

	_, _, err := repo.ListTasks(context.Background(), pagination.PaginationParams{Page: 1, PageSize: 20})
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageCleanupRepositoryListTasksInvalidFilters returns a SQL row
// containing malformed filter JSON. Listing fails during decoding and satisfies
// both count and page-query expectations.
func TestUsageCleanupRepositoryListTasksInvalidFilters(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &CleanupStore{sql: db}

	rows := sqlmock.NewRows([]string{
		"id", "status", "filters", "created_by", "deleted_rows", "error_message",
		"canceled_by", "canceled_at",
		"started_at", "finished_at", "created_at", "updated_at",
	}).AddRow(
		int64(1),
		usage.UsageCleanupStatusSucceeded,
		[]byte("not-json"),
		int64(2),
		int64(9),
		nil,
		nil,
		nil,
		nil,
		nil,
		time.Now().UTC(),
		time.Now().UTC(),
	)

	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM usage_cleanup_tasks").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
	mock.ExpectQuery("SELECT id, status, filters, created_by, deleted_rows, error_message").
		WithArgs(20, 0).
		WillReturnRows(rows)

	_, _, err := repo.ListTasks(context.Background(), pagination.PaginationParams{Page: 1, PageSize: 20})
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageCleanupRepositoryClaimNextPendingTaskNone returns no row from the
// claim statement with a thirty-minute stale threshold. The repository returns
// a nil task and no error for that idle poll.
func TestUsageCleanupRepositoryClaimNextPendingTaskNone(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &CleanupStore{sql: db}

	mock.ExpectQuery("UPDATE usage_cleanup_tasks").
		WithArgs(usage.UsageCleanupStatusPending, usage.UsageCleanupStatusRunning, int64(1800), usage.UsageCleanupStatusRunning).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "status", "filters", "created_by", "deleted_rows", "error_message",
			"started_at", "finished_at", "created_at", "updated_at",
		}))

	task, err := repo.ClaimNextPendingTask(context.Background(), 1800)
	require.NoError(t, err)
	require.Nil(t, task)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageCleanupRepositoryClaimNextPendingTask supplies a running task
// returned by the claim statement. The repository exposes its ID, creator and
// start time while retaining a missing error message.
func TestUsageCleanupRepositoryClaimNextPendingTask(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &CleanupStore{sql: db}

	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	filters := usage.UsageCleanupFilters{StartTime: start, EndTime: end}
	filtersJSON, err := json.Marshal(filters)
	require.NoError(t, err)

	rows := sqlmock.NewRows([]string{
		"id", "status", "filters", "created_by", "deleted_rows", "error_message",
		"started_at", "finished_at", "created_at", "updated_at",
	}).AddRow(
		int64(4),
		usage.UsageCleanupStatusRunning,
		filtersJSON,
		int64(7),
		int64(0),
		nil,
		start,
		nil,
		start,
		start,
	)

	mock.ExpectQuery("UPDATE usage_cleanup_tasks").
		WithArgs(usage.UsageCleanupStatusPending, usage.UsageCleanupStatusRunning, int64(1800), usage.UsageCleanupStatusRunning).
		WillReturnRows(rows)

	task, err := repo.ClaimNextPendingTask(context.Background(), 1800)
	require.NoError(t, err)
	require.NotNil(t, task)
	require.Equal(t, int64(4), task.ID)
	require.Equal(t, usage.UsageCleanupStatusRunning, task.Status)
	require.Equal(t, int64(7), task.CreatedBy)
	require.NotNil(t, task.StartedAt)
	require.Nil(t, task.ErrorMsg)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageCleanupRepositoryClaimNextPendingTaskError injects a connection
// failure into the claim statement. Claiming returns an error after using the
// expected pending, running and stale-time arguments.
func TestUsageCleanupRepositoryClaimNextPendingTaskError(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &CleanupStore{sql: db}

	mock.ExpectQuery("UPDATE usage_cleanup_tasks").
		WithArgs(usage.UsageCleanupStatusPending, usage.UsageCleanupStatusRunning, int64(1800), usage.UsageCleanupStatusRunning).
		WillReturnError(sql.ErrConnDone)

	_, err := repo.ClaimNextPendingTask(context.Background(), 1800)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageCleanupRepositoryClaimNextPendingTaskInvalidFilters returns a
// claimed row with malformed filter JSON. The call reports a decoding error;
// the mock does not establish database rollback of the earlier claim.
func TestUsageCleanupRepositoryClaimNextPendingTaskInvalidFilters(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &CleanupStore{sql: db}

	rows := sqlmock.NewRows([]string{
		"id", "status", "filters", "created_by", "deleted_rows", "error_message",
		"started_at", "finished_at", "created_at", "updated_at",
	}).AddRow(
		int64(4),
		usage.UsageCleanupStatusRunning,
		[]byte("invalid"),
		int64(7),
		int64(0),
		nil,
		nil,
		nil,
		time.Now().UTC(),
		time.Now().UTC(),
	)

	mock.ExpectQuery("UPDATE usage_cleanup_tasks").
		WithArgs(usage.UsageCleanupStatusPending, usage.UsageCleanupStatusRunning, int64(1800), usage.UsageCleanupStatusRunning).
		WillReturnRows(rows)

	_, err := repo.ClaimNextPendingTask(context.Background(), 1800)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageCleanupRepositoryMarkTaskSucceeded mocks a successful terminal
// metadata update. The repository forwards success status, cumulative
// deleted-row count and task ID in the expected SQL argument order.
func TestUsageCleanupRepositoryMarkTaskSucceeded(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &CleanupStore{sql: db}

	mock.ExpectExec("UPDATE usage_cleanup_tasks").
		WithArgs(usage.UsageCleanupStatusSucceeded, int64(12), int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := repo.MarkTaskSucceeded(context.Background(), 9, 12)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageCleanupRepositoryMarkTaskFailed mocks a failed-outcome metadata
// update. The repository forwards failure status, cumulative count, supplied
// error text and task ID in SQL argument order.
func TestUsageCleanupRepositoryMarkTaskFailed(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &CleanupStore{sql: db}

	mock.ExpectExec("UPDATE usage_cleanup_tasks").
		WithArgs(usage.UsageCleanupStatusFailed, int64(4), "boom", int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := repo.MarkTaskFailed(context.Background(), 2, 4, "boom")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageCleanupRepositoryGetTaskStatus returns a pending value from the
// mocked status query. The repository exposes that returned string and uses
// the requested task ID.
func TestUsageCleanupRepositoryGetTaskStatus(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &CleanupStore{sql: db}

	mock.ExpectQuery("SELECT status FROM usage_cleanup_tasks").
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(usage.UsageCleanupStatusPending))

	status, err := repo.GetTaskStatus(context.Background(), 9)
	require.NoError(t, err)
	require.Equal(t, usage.UsageCleanupStatusPending, status)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageCleanupRepositoryGetTaskStatusQueryError fails the SQL status query
// for a requested task ID. The repository returns an error and satisfies that
// single query expectation.
func TestUsageCleanupRepositoryGetTaskStatusQueryError(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &CleanupStore{sql: db}

	mock.ExpectQuery("SELECT status FROM usage_cleanup_tasks").
		WithArgs(int64(9)).
		WillReturnError(sql.ErrConnDone)

	_, err := repo.GetTaskStatus(context.Background(), 9)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageCleanupRepositoryUpdateTaskProgress mocks a successful
// cumulative-count update. The repository forwards the supplied deletion count
// and task ID in the expected SQL argument order.
func TestUsageCleanupRepositoryUpdateTaskProgress(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &CleanupStore{sql: db}

	mock.ExpectExec("UPDATE usage_cleanup_tasks").
		WithArgs(int64(123), int64(8)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := repo.UpdateTaskProgress(context.Background(), 8, 123)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageCleanupRepositoryCancelTask returns an ID from the conditional
// cancellation statement. The result reports a changed task after sending the
// operator and eligible pending/running states.
func TestUsageCleanupRepositoryCancelTask(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &CleanupStore{sql: db}

	mock.ExpectQuery("UPDATE usage_cleanup_tasks").
		WithArgs(usage.UsageCleanupStatusCanceled, int64(6), int64(9), usage.UsageCleanupStatusPending, usage.UsageCleanupStatusRunning).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(6)))

	ok, err := repo.CancelTask(context.Background(), 6, 9)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageCleanupRepositoryCancelTaskNoRows returns no ID from conditional
// cancellation. The repository reports false with no error when that statement
// changes no eligible task.
func TestUsageCleanupRepositoryCancelTaskNoRows(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &CleanupStore{sql: db}

	mock.ExpectQuery("UPDATE usage_cleanup_tasks").
		WithArgs(usage.UsageCleanupStatusCanceled, int64(6), int64(9), usage.UsageCleanupStatusPending, usage.UsageCleanupStatusRunning).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	ok, err := repo.CancelTask(context.Background(), 6, 9)
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageCleanupRepositoryDeleteUsageLogsBatchMissingRange passes zero-valued
// cleanup filters to a SQL-backed repository. The assertion checks the
// returned error, leaving its category and any attempted query outside the
// asserted behavior.
func TestUsageCleanupRepositoryDeleteUsageLogsBatchMissingRange(t *testing.T) {
	db, _ := newSQLMock(t)
	repo := &CleanupStore{sql: db}

	_, err := repo.DeleteUsageLogsBatch(context.Background(), usage.UsageCleanupFilters{}, 10)
	require.Error(t, err)
}

// TestUsageCleanupRepositoryDeleteUsageLogsBatch supplies two deleted IDs for a
// filtered batch. The count is two, and bound SQL arguments include the trimmed
// model plus the requested limit.
func TestUsageCleanupRepositoryDeleteUsageLogsBatch(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &CleanupStore{sql: db}

	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	userID := int64(3)
	model := " gpt-4 "
	filters := usage.UsageCleanupFilters{
		StartTime: start,
		EndTime:   end,
		UserID:    &userID,
		Model:     &model,
	}

	mock.ExpectQuery("DELETE FROM usage_logs").
		WithArgs(start, end, userID, "gpt-4", 2).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(1)).AddRow(int64(2)))

	deleted, err := repo.DeleteUsageLogsBatch(context.Background(), filters, 2)
	require.NoError(t, err)
	require.Equal(t, int64(2), deleted)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageCleanupRepositoryDeleteUsageLogsBatchQueryError injects a connection
// error for a bounded deletion range. The repository reports failure after
// forwarding the requested timestamps and batch limit.
func TestUsageCleanupRepositoryDeleteUsageLogsBatchQueryError(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &CleanupStore{sql: db}

	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	filters := usage.UsageCleanupFilters{StartTime: start, EndTime: end}

	mock.ExpectQuery("DELETE FROM usage_logs").
		WithArgs(start, end, 5).
		WillReturnError(sql.ErrConnDone)

	_, err := repo.DeleteUsageLogsBatch(context.Background(), filters, 5)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestBuildUsageCleanupWhere supplies time, identity, model, stream and billing
// filters together. The builder emits inclusive endpoints and ordered
// placeholders, with the trimmed model in the matching argument position.
func TestBuildUsageCleanupWhere(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	userID := int64(1)
	apiKeyID := int64(2)
	providerID := int64(3)
	groupID := int64(4)
	teamID := int64(5)
	model := " gpt-4 "
	stream := true
	billingType := int8(2)

	where, args := buildUsageCleanupWhere(usage.UsageCleanupFilters{
		StartTime:   start,
		EndTime:     end,
		UserID:      &userID,
		APIKeyID:    &apiKeyID,
		ProviderID:  &providerID,
		GroupID:     &groupID,
		TeamID:      &teamID,
		Model:       &model,
		Stream:      &stream,
		BillingType: &billingType,
	})

	require.Equal(t, "created_at >= $1 AND created_at <= $2 AND user_id = $3 AND api_key_id = $4 AND provider_id = $5 AND group_id = $6 AND team_id = $7 AND model = $8 AND stream = $9 AND billing_type = $10", where)
	require.Equal(t, []any{start, end, userID, apiKeyID, providerID, groupID, teamID, "gpt-4", stream, billingType}, args)
}

// TestBuildUsageCleanupWhereRequestTypePriority combines websocket request type
// with a false Stream selector. The request-type predicate wins and includes
// legacy websocket records instead of adding an incompatible Stream condition.
func TestBuildUsageCleanupWhereRequestTypePriority(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	requestType := int16(usage.RequestTypeWSV2)
	stream := false

	where, args := buildUsageCleanupWhere(usage.UsageCleanupFilters{
		StartTime:   start,
		EndTime:     end,
		RequestType: &requestType,
		Stream:      &stream,
	})

	require.Equal(t, "created_at >= $1 AND created_at <= $2 AND (request_type = $3 OR (request_type = 0 AND openai_ws_mode = TRUE))", where)
	require.Equal(t, []any{start, end, requestType}, args)
}

// TestBuildUsageCleanupWhereRequestTypeLegacyFallback selects streaming
// requests from explicit and legacy representations. The predicate accepts
// zero-type streamed records when their websocket flag is false.
func TestBuildUsageCleanupWhereRequestTypeLegacyFallback(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	requestType := int16(usage.RequestTypeStream)

	where, args := buildUsageCleanupWhere(usage.UsageCleanupFilters{
		StartTime:   start,
		EndTime:     end,
		RequestType: &requestType,
	})

	require.Equal(t, "created_at >= $1 AND created_at <= $2 AND (request_type = $3 OR (request_type = 0 AND stream = TRUE AND openai_ws_mode = FALSE))", where)
	require.Equal(t, []any{start, end, requestType}, args)
}

// TestBuildUsageCleanupWhereModelEmpty supplies whitespace as the model filter.
// The builder omits that equality and leaves the two inclusive timestamp
// predicates and their arguments.
func TestBuildUsageCleanupWhereModelEmpty(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	model := "   "

	where, args := buildUsageCleanupWhere(usage.UsageCleanupFilters{
		StartTime: start,
		EndTime:   end,
		Model:     &model,
	})

	require.Equal(t, "created_at >= $1 AND created_at <= $2", where)
	require.Equal(t, []any{start, end}, args)
}
