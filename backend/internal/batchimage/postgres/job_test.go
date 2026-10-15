package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/batchimage"
)

// Multi-step metadata operations can own a transaction or borrow one from their
// caller. Ordered SQL expectations exercise each public operation through real
// database/sql transactions, including statement failures and the caller's
// ability to finish a borrowed transaction after the repository returns.

// batchImageTransactionCase binds a public repository operation to its expected
// ordered SQL statements, with an optional failure after an earlier write.
type batchImageTransactionCase struct {
	name   string                       // public operation name
	expect func(sqlmock.Sqlmock, error) // ordered statement expectations
	run    func(*Repository) error      // public repository invocation
}

// TestBatchImageRepository_OwnedTransactionLifecycle exercises successful writes
// and failures at transaction creation, SQL execution, and commit. Each operation
// commits once on success and keeps the first error when rollback also fails.
func TestBatchImageRepository_OwnedTransactionLifecycle(t *testing.T) {
	for _, operation := range batchImageTransactionCases() {
		t.Run(operation.name, func(t *testing.T) {
			for _, outcome := range []string{"commit", "statement_error", "begin_error", "commit_error", "rollback_error"} {
				t.Run(outcome, func(t *testing.T) {
					db, mock := newBatchImageSQLMock(t)
					repo, ok := NewBatchImageRepository(db).(*Repository)
					require.True(t, ok)
					firstErr := errors.New("transaction lifecycle failure")
					var wantErr error

					begin := mock.ExpectBegin()
					if outcome == "begin_error" {
						begin.WillReturnError(firstErr)
						wantErr = firstErr
					} else {
						var statementErr error
						if outcome == "statement_error" || outcome == "rollback_error" {
							statementErr = firstErr
							wantErr = firstErr
						}
						operation.expect(mock, statementErr)
						if statementErr != nil {
							rollback := mock.ExpectRollback()
							if outcome == "rollback_error" {
								rollback.WillReturnError(errors.New("rollback failure"))
							}
						} else {
							commit := mock.ExpectCommit()
							if outcome == "commit_error" {
								commit.WillReturnError(firstErr)
								wantErr = firstErr
							}
						}
					}

					err := operation.run(repo)
					if wantErr == nil {
						require.NoError(t, err)
					} else {
						require.ErrorIs(t, err, wantErr)
					}
					require.NoError(t, mock.ExpectationsWereMet())
				})
			}
		})
	}
}

// TestBatchImageRepository_BorrowedTransactionLifecycle calls every multi-step
// operation through a caller-created transaction on success and SQL failure.
// The caller can commit or roll back afterward, proving the repository leaves
// the transaction open for its owner to finish.
func TestBatchImageRepository_BorrowedTransactionLifecycle(t *testing.T) {
	for _, operation := range batchImageTransactionCases() {
		t.Run(operation.name, func(t *testing.T) {
			for _, outcome := range []string{"commit", "rollback"} {
				t.Run(outcome, func(t *testing.T) {
					db, mock := newBatchImageSQLMock(t)
					mock.ExpectBegin()
					tx, err := db.BeginTx(context.Background(), nil)
					require.NoError(t, err)
					repo := NewRepositoryWithSQL(tx)
					var statementErr error
					if outcome == "rollback" {
						statementErr = errors.New("statement failure")
					}
					operation.expect(mock, statementErr)

					err = operation.run(repo)
					if statementErr == nil {
						require.NoError(t, err)
						// The owner publishes successful metadata after the repository
						// returns. An earlier repository commit would make this fail.
						mock.ExpectCommit()
						require.NoError(t, tx.Commit())
					} else {
						require.ErrorIs(t, err, statementErr)
						// The owner discards writes after the repository reports an
						// error. An earlier repository rollback would finish this tx.
						mock.ExpectRollback()
						require.NoError(t, tx.Rollback())
					}
					require.NoError(t, mock.ExpectationsWereMet())
				})
			}
		})
	}
}

// TestBatchImageRepository_BulkCreateEmpty supplies nil and empty item slices
// through pool-backed and injected repositories. Both inputs return success
// before beginning a transaction or executing any SQL statement.
func TestBatchImageRepository_BulkCreateEmpty(t *testing.T) {
	for _, owned := range []bool{true, false} {
		for _, items := range [][]batchimage.CreateBatchImageItemParams{nil, {}} {
			db, mock := newBatchImageSQLMock(t)
			repo := NewRepositoryWithSQL(db)
			if owned {
				var ok bool
				repo, ok = NewBatchImageRepository(db).(*Repository)
				require.True(t, ok)
			}
			require.NoError(t, repo.BulkCreateBatchImageItems(context.Background(), items))
			require.NoError(t, mock.ExpectationsWereMet())
		}
	}
}

// batchImageTransactionCases supplies SQL sequences for all operations that
// choose transaction ownership. Errors occur after an earlier write so each
// case exercises rollback of partial metadata work, and successful bulk inserts
// verify the supplied item order through distinct custom IDs.
func batchImageTransactionCases() []batchImageTransactionCase {
	ctx := context.Background()
	batchID := "imgbatch_transaction"
	now := time.Date(2026, 7, 3, 8, 0, 0, 0, time.UTC)
	return []batchImageTransactionCase{
		{
			name: "transition",
			expect: func(mock sqlmock.Sqlmock, err error) {
				mock.ExpectQuery("SELECT status FROM batch_image_jobs WHERE batch_id = \\$1 FOR UPDATE").
					WithArgs(batchID).
					WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(batchimage.BatchImageJobStatusCreated))
				mock.ExpectExec("UPDATE batch_image_jobs SET status = \\$2::varchar").
					WithArgs(batchID, batchimage.BatchImageJobStatusUploading, now, nil, nil).
					WillReturnResult(sqlmock.NewResult(0, 1))
				expectBatchImageEvent(mock, batchID, "status_changed", err)
			},
			run: func(repo *Repository) error {
				return repo.TransitionBatchImageJobStatus(ctx, batchID, batchimage.BatchImageJobStatusUploading, batchimage.BatchImageTransitionOptions{
					EventType: "status_changed",
					Now:       &now,
				})
			},
		},
		{
			name: "provider_submit",
			expect: func(mock sqlmock.Sqlmock, err error) {
				mock.ExpectQuery("SELECT status FROM batch_image_jobs WHERE batch_id = \\$1 FOR UPDATE").
					WithArgs(batchID).
					WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(batchimage.BatchImageJobStatusUploading))
				mock.ExpectExec("UPDATE batch_image_jobs SET status = 'submitted'").
					WithArgs(batchID, "provider-job", "input-ref", "output-ref", "", "", sqlmock.AnyArg()).
					WillReturnResult(sqlmock.NewResult(0, 1))
				expectBatchImageEvent(mock, batchID, "provider_submitted", err)
			},
			run: func(repo *Repository) error {
				return repo.UpdateBatchImageJobProviderSubmit(ctx, batchimage.UpdateBatchImageJobProviderSubmitParams{
					BatchID:           batchID,
					ProviderJobName:   "provider-job",
					ProviderInputRef:  "input-ref",
					ProviderOutputRef: "output-ref",
				})
			},
		},
		{
			name: "settlement",
			expect: func(mock sqlmock.Sqlmock, err error) {
				mock.ExpectExec("UPDATE batch_image_jobs SET status = 'completed'").
					WithArgs(batchID, 0.5, "manifest-hash", now, nil).
					WillReturnResult(sqlmock.NewResult(0, 1))
				expectBatchImageEvent(mock, batchID, "settlement_completed", err)
			},
			run: func(repo *Repository) error {
				return repo.MarkBatchImageJobSettled(ctx, batchimage.MarkBatchImageJobSettledParams{
					BatchID:      batchID,
					ActualCost:   0.5,
					ManifestHash: "manifest-hash",
					Now:          &now,
				})
			},
		},
		{
			name: "bulk_items",
			expect: func(mock sqlmock.Sqlmock, err error) {
				expectBatchImageItem(mock, batchID, "image-1", nil, nil)
				expectBatchImageItem(mock, batchID, "image-2", nil, err)
				if err == nil {
					expectBatchImageItem(mock, batchID, "image-3", nil, nil)
				}
			},
			run: func(repo *Repository) error {
				return repo.BulkCreateBatchImageItems(ctx, []batchimage.CreateBatchImageItemParams{
					{JobID: batchID, CustomID: "image-1", Status: batchimage.BatchImageItemStatusSuccess, ImageCount: 1},
					{JobID: batchID, CustomID: "image-2", Status: batchimage.BatchImageItemStatusSuccess, ImageCount: 1},
					{JobID: batchID, CustomID: "image-3", Status: batchimage.BatchImageItemStatusSuccess, ImageCount: 1},
				})
			},
		},
		{
			name: "replace_items",
			expect: func(mock sqlmock.Sqlmock, err error) {
				mock.ExpectQuery("SELECT id, status FROM batch_image_jobs WHERE batch_id = \\$1 FOR UPDATE").
					WithArgs(batchID).
					WillReturnRows(sqlmock.NewRows([]string{"id", "status"}).AddRow(17, batchimage.BatchImageJobStatusIndexing))
				mock.ExpectQuery("SELECT custom_id, prompt_preview FROM batch_image_items").
					WithArgs(batchID).
					WillReturnRows(sqlmock.NewRows([]string{"custom_id", "prompt_preview"}).AddRow("image-1", "submission preview"))
				mock.ExpectExec("DELETE FROM batch_image_items WHERE job_id = \\$1").
					WithArgs(batchID).
					WillReturnResult(sqlmock.NewResult(0, 1))
				preview := "submission preview"
				expectBatchImageItem(mock, batchID, "image-1", &preview, nil)
				update := mock.ExpectExec("UPDATE batch_image_jobs SET success_count = \\$2").
					WithArgs(batchID, 1, 0, sqlmock.AnyArg())
				if err != nil {
					update.WillReturnError(err)
				} else {
					update.WillReturnResult(sqlmock.NewResult(0, 1))
				}
			},
			run: func(repo *Repository) error {
				return repo.ReplaceBatchImageItemsForJob(ctx, batchID, []batchimage.CreateBatchImageItemParams{
					{JobID: "ignored-input-job", CustomID: "image-1", Status: batchimage.BatchImageItemStatusSuccess, ImageCount: 1},
				}, batchimage.BatchImageCounts{SuccessCount: 1})
			},
		},
	}
}

// expectBatchImageEvent records an audit insertion after its metadata write.
// A supplied error models a failure late enough to require discarding the
// earlier state update in the pool-owned transaction.
func expectBatchImageEvent(mock sqlmock.Sqlmock, batchID, eventType string, err error) {
	expectation := mock.ExpectExec("INSERT INTO batch_image_events").WithArgs(batchID, eventType, nil)
	if err != nil {
		expectation.WillReturnError(err)
	} else {
		expectation.WillReturnResult(sqlmock.NewResult(0, 1))
	}
}

// expectBatchImageItem records one indexed insertion with its identity and
// optional prompt preview. Returned rows exercise the production item scanner;
// errors let bulk tests observe termination before a later item is attempted.
func expectBatchImageItem(mock sqlmock.Sqlmock, batchID, customID string, preview *string, err error) {
	expectation := mock.ExpectQuery("INSERT INTO batch_image_items").
		WithArgs(batchID, customID, batchimage.BatchImageItemStatusSuccess, nil, preview, nil,
			nil, nil, nil, nil, nil, 1, nil, nil, nil, nil)
	if err != nil {
		expectation.WillReturnError(err)
		return
	}
	expectation.WillReturnRows(sqlmock.NewRows([]string{
		"id", "job_id", "custom_id", "status", "request_hash", "prompt_preview", "provider_source_object",
		"source_line_number", "source_byte_offset", "source_byte_length", "mime_type", "file_extension", "image_count",
		"error_code", "error_message", "billed_amount", "created_at", "indexed_at",
	}).AddRow(17, batchID, customID, batchimage.BatchImageItemStatusSuccess, nil, preview, nil,
		nil, nil, nil, nil, nil, 1, nil, nil, nil, time.Date(2026, 7, 3, 8, 0, 0, 0, time.UTC), nil))
}

// newBatchImageSQLMock opens an ordered SQL mock for metadata lifecycle tests.
// Cleanup checks closure and remaining expectations so an omitted statement or
// transaction completion remains observable even after a failed assertion.
func newBatchImageSQLMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Closing after lifecycle assertions releases the fixture connection. Register
	// its expectation here so cleanup does not appear as an unexpected SQL call.
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	return db, mock
}
