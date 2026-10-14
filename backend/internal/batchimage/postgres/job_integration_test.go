//go:build integration

package postgres

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/batchimage"
	"github.com/TokenFlux/TokenRouter/internal/testutil/postgrescontainer"
)

// Repository integration cases persist job identities, result rows, transitions,
// and events under pool-owned and caller-owned transactions. A per-test payer
// satisfies the billing foreign key. Reads outside a borrowed transaction show
// when its owner publishes metadata, while failures after writes verify that
// rollback restores the prior job and item set.

// TestBatchImageRepository_CreateJobAndDuplicates persists distinct requested,
// internal, and upstream model identities. It checks created-state and currency
// defaults, then verifies a repeated batch ID returns the job-exists error.
func TestBatchImageRepository_CreateJobAndDuplicates(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	repo, userID := newBatchImageRepositoryWithSQL(t, tx)
	batchID := batchImageTestID(t, "create")

	job, err := repo.CreateBatchImageJob(ctx, batchimage.CreateBatchImageJobParams{
		BatchID:        batchID,
		UserID:         userID,
		BillingUserID:  userID,
		Platform:       batchimage.BatchImageProviderGeminiAPI,
		Model:          "upstream-image-model",
		RequestedModel: "Gemini/image-alias",
		InternalModel:  "gemini-2.5-flash-image",
		ItemCount:      2,
		EstimatedCost:  0.02,
	})
	require.NoError(t, err)
	require.Equal(t, batchID, job.BatchID)
	require.Equal(t, batchimage.BatchImageJobStatusCreated, job.Status)
	require.Equal(t, "USD", job.Currency)
	require.Equal(t, "upstream-image-model", job.Model)
	require.Equal(t, "Gemini/image-alias", job.RequestedModel)
	require.Equal(t, "gemini-2.5-flash-image", job.InternalModel)

	_, err = repo.CreateBatchImageJob(ctx, batchimage.CreateBatchImageJobParams{
		BatchID:       batchID,
		UserID:        userID,
		BillingUserID: userID,
		Platform:      batchimage.BatchImageProviderGeminiAPI,
		Model:         "gemini-2.5-flash-image",
		ItemCount:     1,
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, batchimage.ErrBatchImageJobExists))
}

// TestBatchImageRepository_InvalidProvider supplies an unknown platform to job
// creation. The repository returns the invalid-provider error before attempting
// to persist that unsupported execution identity.
func TestBatchImageRepository_InvalidProvider(t *testing.T) {
	tx := testTx(t)
	repo, userID := newBatchImageRepositoryWithSQL(t, tx)

	_, err := repo.CreateBatchImageJob(context.Background(), batchimage.CreateBatchImageJobParams{
		BatchID:       batchImageTestID(t, "platform"),
		UserID:        userID,
		BillingUserID: userID,
		Platform:      "unknown",
		Model:         "gemini-2.5-flash-image",
		ItemCount:     1,
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, batchimage.ErrBatchImageInvalidProvider))
}

// TestBatchImageRepository_TransitionIncrementsVersionAndEvents moves a
// created job to uploading with an event. It checks the stored status, one
// version increment, and one matching event in the caller-owned transaction.
func TestBatchImageRepository_TransitionIncrementsVersionAndEvents(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	repo, userID := newBatchImageRepositoryWithSQL(t, tx)
	batchID := batchImageTestID(t, "transition")
	now := time.Date(2026, 7, 3, 8, 0, 0, 0, time.UTC)

	_, err := repo.CreateBatchImageJob(ctx, batchimage.CreateBatchImageJobParams{
		BatchID:       batchID,
		UserID:        userID,
		BillingUserID: userID,
		Platform:      batchimage.BatchImageProviderVertex,
		Model:         "gemini-2.5-flash-image",
		ItemCount:     1,
	})
	require.NoError(t, err)

	err = repo.TransitionBatchImageJobStatus(ctx, batchID, batchimage.BatchImageJobStatusUploading, batchimage.BatchImageTransitionOptions{
		EventType:    "status_changed",
		EventPayload: map[string]any{"to": batchimage.BatchImageJobStatusUploading},
		Now:          &now,
	})
	require.NoError(t, err)

	job, err := repo.GetBatchImageJobByBatchID(ctx, batchID)
	require.NoError(t, err)
	require.Equal(t, batchimage.BatchImageJobStatusUploading, job.Status)
	require.Equal(t, 1, job.Version)

	var eventCount int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM batch_image_events WHERE job_id = $1 AND event_type = 'status_changed'`, batchID).Scan(&eventCount)
	require.NoError(t, err)
	require.Equal(t, 1, eventCount)
}

// TestBatchImageRepository_InvalidTransition asks a created job to move
// directly to running. The repository returns the invalid-transition error,
// protecting the lifecycle rule that requires submission before execution.
func TestBatchImageRepository_InvalidTransition(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	repo, userID := newBatchImageRepositoryWithSQL(t, tx)
	batchID := batchImageTestID(t, "invalid-transition")

	_, err := repo.CreateBatchImageJob(ctx, batchimage.CreateBatchImageJobParams{
		BatchID:       batchID,
		UserID:        userID,
		BillingUserID: userID,
		Platform:      batchimage.BatchImageProviderGeminiAPI,
		Model:         "gemini-2.5-flash-image",
		ItemCount:     1,
	})
	require.NoError(t, err)

	err = repo.TransitionBatchImageJobStatus(ctx, batchID, batchimage.BatchImageJobStatusRunning, batchimage.BatchImageTransitionOptions{})
	require.Error(t, err)
	require.True(t, errors.Is(err, batchimage.ErrBatchImageInvalidTransition))
}

// TestBatchImageRepository_TerminalStatusCannotMoveBack starts with a completed
// job and requests running. The repository rejects that move with the lifecycle
// error, protecting completed work from renewed execution.
func TestBatchImageRepository_TerminalStatusCannotMoveBack(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	repo, userID := newBatchImageRepositoryWithSQL(t, tx)
	batchID := batchImageTestID(t, "terminal")

	_, err := repo.CreateBatchImageJob(ctx, batchimage.CreateBatchImageJobParams{
		BatchID:       batchID,
		UserID:        userID,
		BillingUserID: userID,
		Platform:      batchimage.BatchImageProviderGeminiAPI,
		Model:         "gemini-2.5-flash-image",
		Status:        batchimage.BatchImageJobStatusCompleted,
		ItemCount:     1,
	})
	require.NoError(t, err)

	err = repo.TransitionBatchImageJobStatus(ctx, batchID, batchimage.BatchImageJobStatusRunning, batchimage.BatchImageTransitionOptions{})
	require.Error(t, err)
	require.True(t, errors.Is(err, batchimage.ErrBatchImageInvalidTransition))
}

// TestBatchImageRepository_ItemCustomIDUniqueness inserts the same custom ID
// within one job and across two jobs. The same-job duplicate fails, while the
// second job succeeds and the first retains one indexed item.
func TestBatchImageRepository_ItemCustomIDUniqueness(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	repo, userID := newBatchImageRepositoryWithSQL(t, tx)
	firstBatchID := batchImageTestID(t, "items-a")
	secondBatchID := batchImageTestID(t, "items-b")

	for _, batchID := range []string{firstBatchID, secondBatchID} {
		_, err := repo.CreateBatchImageJob(ctx, batchimage.CreateBatchImageJobParams{
			BatchID:       batchID,
			UserID:        userID,
			BillingUserID: userID,
			Platform:      batchimage.BatchImageProviderGeminiAPI,
			Model:         "gemini-2.5-flash-image",
			ItemCount:     1,
		})
		require.NoError(t, err)
	}

	_, err := repo.CreateBatchImageItem(ctx, batchimage.CreateBatchImageItemParams{
		JobID:      firstBatchID,
		CustomID:   "line-1",
		Status:     batchimage.BatchImageItemStatusSuccess,
		ImageCount: 1,
	})
	require.NoError(t, err)

	// A duplicate insert leaves a PostgreSQL transaction in its failed state.
	// Create a savepoint first so the test can recover that expected failure and
	// continue checking a different job's custom ID.
	_, err = tx.ExecContext(ctx, `SAVEPOINT batch_image_duplicate_item`)
	require.NoError(t, err)
	_, err = repo.CreateBatchImageItem(ctx, batchimage.CreateBatchImageItemParams{
		JobID:    firstBatchID,
		CustomID: "line-1",
		Status:   batchimage.BatchImageItemStatusFailed,
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, batchimage.ErrBatchImageItemExists))
	// Roll back the expected duplicate failure to its savepoint before inserting
	// the same custom ID for another job. This restores the transaction's ability
	// to execute the remaining assertions.
	_, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT batch_image_duplicate_item`)
	require.NoError(t, rollbackErr)

	_, err = repo.CreateBatchImageItem(ctx, batchimage.CreateBatchImageItemParams{
		JobID:      secondBatchID,
		CustomID:   "line-1",
		Status:     batchimage.BatchImageItemStatusSuccess,
		ImageCount: 1,
	})
	require.NoError(t, err)

	items, err := repo.ListBatchImageItems(ctx, firstBatchID, batchimage.BatchImageItemFilter{})
	require.NoError(t, err)
	require.Len(t, items, 1)
}

// TestBatchImageRepository_ReplaceBatchImageItemsForJob attempts replacement
// before indexing, then rebuilds results during indexing. It checks the initial
// state conflict, removal of older custom IDs, replacement order, and updated
// success and failure counts.
func TestBatchImageRepository_ReplaceBatchImageItemsForJob(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	repo, userID := newBatchImageRepositoryWithSQL(t, tx)
	batchID := batchImageTestID(t, "replace-items")
	lineOne := 1
	lineTwo := 2

	_, err := repo.CreateBatchImageJob(ctx, batchimage.CreateBatchImageJobParams{
		BatchID:       batchID,
		UserID:        userID,
		BillingUserID: userID,
		Platform:      batchimage.BatchImageProviderGeminiAPI,
		Model:         "gemini-2.5-flash-image",
		ItemCount:     2,
	})
	require.NoError(t, err)

	// Attempt replacement before the job enters indexing to exercise its
	// state guard. This case checks an early rejection, protecting later settled
	// results from late-worker replacement attempts.
	err = repo.ReplaceBatchImageItemsForJob(ctx, batchID, []batchimage.CreateBatchImageItemParams{
		{CustomID: "old", Status: batchimage.BatchImageItemStatusSuccess, SourceLineNumber: &lineOne, ImageCount: 1},
	}, batchimage.BatchImageCounts{SuccessCount: 1})
	require.ErrorIs(t, err, batchimage.ErrBatchImageIndexStateConflict)

	require.NoError(t, repo.TransitionBatchImageJobStatus(ctx, batchID, batchimage.BatchImageJobStatusSubmitted, batchimage.BatchImageTransitionOptions{}))
	require.NoError(t, repo.TransitionBatchImageJobStatus(ctx, batchID, batchimage.BatchImageJobStatusIndexing, batchimage.BatchImageTransitionOptions{}))

	err = repo.ReplaceBatchImageItemsForJob(ctx, batchID, []batchimage.CreateBatchImageItemParams{
		{CustomID: "old", Status: batchimage.BatchImageItemStatusSuccess, SourceLineNumber: &lineOne, ImageCount: 1},
	}, batchimage.BatchImageCounts{SuccessCount: 1})
	require.NoError(t, err)

	err = repo.ReplaceBatchImageItemsForJob(ctx, batchID, []batchimage.CreateBatchImageItemParams{
		{CustomID: "new-ok", Status: batchimage.BatchImageItemStatusSuccess, SourceLineNumber: &lineOne, ImageCount: 1},
		{CustomID: "new-fail", Status: batchimage.BatchImageItemStatusFailed, SourceLineNumber: &lineTwo, ErrorCode: batchImageTestStringPtr("SAFETY_BLOCKED")},
	}, batchimage.BatchImageCounts{SuccessCount: 1, FailCount: 1})
	require.NoError(t, err)

	items, err := repo.ListBatchImageItems(ctx, batchID, batchimage.BatchImageItemFilter{})
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, "new-ok", items[0].CustomID)
	require.Equal(t, "new-fail", items[1].CustomID)

	job, err := repo.GetBatchImageJobByBatchID(ctx, batchID)
	require.NoError(t, err)
	require.Equal(t, 1, job.SuccessCount)
	require.Equal(t, 1, job.FailCount)
}

// TestBatchImageRepository_MarkBatchImageJobSettled completes a settling job
// using a chosen time and manifest. It checks actual cost, manifest identity,
// settlement time, completed status, and one completion event.
func TestBatchImageRepository_MarkBatchImageJobSettled(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	repo, userID := newBatchImageRepositoryWithSQL(t, tx)
	batchID := batchImageTestID(t, "settled")
	apiKeyID := int64(2001)
	providerID := int64(3001)
	providerJob := "providers/job"
	outputRef := "files/output"
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)

	_, err := repo.CreateBatchImageJob(ctx, batchimage.CreateBatchImageJobParams{
		BatchID:           batchID,
		UserID:            userID,
		BillingUserID:     userID,
		APIKeyID:          &apiKeyID,
		ProviderID:        &providerID,
		Platform:          batchimage.BatchImageProviderGeminiAPI,
		Model:             "gemini-image",
		Status:            batchimage.BatchImageJobStatusSettling,
		ProviderJobName:   &providerJob,
		ProviderOutputRef: &outputRef,
		ItemCount:         3,
		SuccessCount:      2,
		FailCount:         1,
	})
	require.NoError(t, err)

	err = repo.MarkBatchImageJobSettled(ctx, batchimage.MarkBatchImageJobSettledParams{
		BatchID:      batchID,
		ActualCost:   0.5,
		ManifestHash: "manifest-hash",
		EventPayload: map[string]any{"request_id": "batch_image_settlement:" + batchID},
		Now:          &now,
	})
	require.NoError(t, err)

	job, err := repo.GetBatchImageJobByBatchID(ctx, batchID)
	require.NoError(t, err)
	require.Equal(t, batchimage.BatchImageJobStatusCompleted, job.Status)
	require.NotNil(t, job.ActualCost)
	require.Equal(t, 0.5, *job.ActualCost)
	require.Equal(t, "manifest-hash", batchImageDerefTest(job.ManifestHash))
	require.NotNil(t, job.SettledAt)
	require.Equal(t, now, *job.SettledAt)

	var eventCount int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM batch_image_events WHERE job_id = $1 AND event_type = 'settlement_completed'`, batchID).Scan(&eventCount)
	require.NoError(t, err)
	require.Equal(t, 1, eventCount)
}

// TestBatchImageRepository_SetBatchImageJobSettlementFailed records a temporary
// billing failure for a settling job. The returned retry count, stored error,
// and persisted count increase while its status remains settling.
func TestBatchImageRepository_SetBatchImageJobSettlementFailed(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	repo, userID := newBatchImageRepositoryWithSQL(t, tx)
	batchID := batchImageTestID(t, "settlement-failed")

	_, err := repo.CreateBatchImageJob(ctx, batchimage.CreateBatchImageJobParams{
		BatchID:       batchID,
		UserID:        userID,
		BillingUserID: userID,
		Platform:      batchimage.BatchImageProviderGeminiAPI,
		Model:         "gemini-image",
		Status:        batchimage.BatchImageJobStatusSettling,
		ItemCount:     1,
		SuccessCount:  1,
	})
	require.NoError(t, err)

	retryCount, err := repo.SetBatchImageJobSettlementFailed(ctx, batchID, "SETTLEMENT_BILLING_FAILED", "temporary")
	require.NoError(t, err)
	require.Equal(t, 1, retryCount)

	job, err := repo.GetBatchImageJobByBatchID(ctx, batchID)
	require.NoError(t, err)
	require.Equal(t, batchimage.BatchImageJobStatusSettling, job.Status)
	require.Equal(t, "SETTLEMENT_BILLING_FAILED", batchImageDerefTest(job.LastErrorCode))
	require.Equal(t, "temporary", batchImageDerefTest(job.LastErrorMessage))
	require.Equal(t, 1, job.RetryCount)
}

// TestBatchImageRepository_AppendEvent appends a job-created event with batch
// identity in its payload. Reading the stored JSON text finds that identity,
// checking event persistence through the caller-owned transaction.
func TestBatchImageRepository_AppendEvent(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	repo, userID := newBatchImageRepositoryWithSQL(t, tx)
	batchID := batchImageTestID(t, "event")

	_, err := repo.CreateBatchImageJob(ctx, batchimage.CreateBatchImageJobParams{
		BatchID:       batchID,
		UserID:        userID,
		BillingUserID: userID,
		Platform:      batchimage.BatchImageProviderVertex,
		Model:         "gemini-2.5-flash-image",
		ItemCount:     1,
	})
	require.NoError(t, err)

	err = repo.AppendBatchImageEvent(ctx, batchID, "job_created", map[string]any{"batch_id": batchID})
	require.NoError(t, err)

	var payload string
	err = tx.QueryRowContext(ctx, `SELECT payload::text FROM batch_image_events WHERE job_id = $1 AND event_type = 'job_created'`, batchID).Scan(&payload)
	require.NoError(t, err)
	require.Contains(t, payload, batchID)
}

// TestBatchImageRepository_OwnedTransactionOutcomes persists all five multi-step
// operations through a SQL pool. Success publishes their complete metadata,
// while a later event or item failure restores the prior job and item rows.
func TestBatchImageRepository_OwnedTransactionOutcomes(t *testing.T) {
	ctx := context.Background()
	db := postgrescontainer.New(t)
	_, userID := newBatchImageRepositoryWithSQL(t, db)
	repo := NewBatchImageRepository(db).(*Repository)
	operations := []struct {
		name          string                  // metadata operation name
		initialStatus string                  // starting persisted status
		wantStatus    string                  // successful resulting status
		run           func(string, any) error // public metadata invocation
	}{
		{
			name:          "transition",
			initialStatus: batchimage.BatchImageJobStatusCreated,
			wantStatus:    batchimage.BatchImageJobStatusUploading,
			run: func(batchID string, payload any) error {
				return repo.TransitionBatchImageJobStatus(ctx, batchID, batchimage.BatchImageJobStatusUploading, batchimage.BatchImageTransitionOptions{
					EventType:    "status_changed",
					EventPayload: payload,
				})
			},
		},
		{
			name:          "provider_submit",
			initialStatus: batchimage.BatchImageJobStatusUploading,
			wantStatus:    batchimage.BatchImageJobStatusSubmitted,
			run: func(batchID string, payload any) error {
				return repo.UpdateBatchImageJobProviderSubmit(ctx, batchimage.UpdateBatchImageJobProviderSubmitParams{
					BatchID:         batchID,
					ProviderJobName: "provider-job",
					EventPayload:    payload,
				})
			},
		},
		{
			name:          "settlement",
			initialStatus: batchimage.BatchImageJobStatusSettling,
			wantStatus:    batchimage.BatchImageJobStatusCompleted,
			run: func(batchID string, payload any) error {
				return repo.MarkBatchImageJobSettled(ctx, batchimage.MarkBatchImageJobSettledParams{
					BatchID:      batchID,
					ActualCost:   0.5,
					ManifestHash: "manifest-hash",
					EventPayload: payload,
				})
			},
		},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			for _, outcome := range []string{"commit", "rollback"} {
				t.Run(outcome, func(t *testing.T) {
					batchID := batchImageTestID(t, "owned-event")
					before, err := repo.CreateBatchImageJob(ctx, batchimage.CreateBatchImageJobParams{
						BatchID:       batchID,
						UserID:        userID,
						BillingUserID: userID,
						Platform:      batchimage.BatchImageProviderGeminiAPI,
						Model:         "gemini-image",
						Status:        operation.initialStatus,
						ItemCount:     1,
					})
					require.NoError(t, err)
					var payload any
					if outcome == "rollback" {
						// Event serialization runs after the metadata update. Reject a
						// channel payload to test rollback after that write has executed.
						payload = make(chan int)
					}
					err = operation.run(batchID, payload)
					if outcome == "rollback" {
						require.Error(t, err)
					} else {
						require.NoError(t, err)
					}
					after, err := repo.GetBatchImageJobByBatchID(ctx, batchID)
					require.NoError(t, err)
					var eventCount int
					require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM batch_image_events WHERE job_id = $1`, batchID).Scan(&eventCount))
					if outcome == "rollback" {
						require.Equal(t, before, after)
						require.Zero(t, eventCount)
					} else {
						require.Equal(t, operation.wantStatus, after.Status)
						require.Equal(t, before.Version+1, after.Version)
						require.Equal(t, 1, eventCount)
					}
				})
			}
		})
	}
	for _, operation := range []string{"bulk_items", "replace_items"} {
		t.Run(operation, func(t *testing.T) {
			for _, outcome := range []string{"commit", "rollback"} {
				t.Run(outcome, func(t *testing.T) {
					batchID := batchImageTestID(t, "owned-items")
					beforeJob, err := repo.CreateBatchImageJob(ctx, batchimage.CreateBatchImageJobParams{
						BatchID:       batchID,
						UserID:        userID,
						BillingUserID: userID,
						Platform:      batchimage.BatchImageProviderGeminiAPI,
						Model:         "gemini-image",
						Status:        batchimage.BatchImageJobStatusIndexing,
						ItemCount:     2,
						SuccessCount:  1,
					})
					require.NoError(t, err)
					if operation == "replace_items" {
						_, err = repo.CreateBatchImageItem(ctx, batchimage.CreateBatchImageItemParams{
							JobID:         batchID,
							CustomID:      "image-1",
							Status:        batchimage.BatchImageItemStatusPending,
							PromptPreview: batchImageTestStringPtr("submission preview"),
						})
						require.NoError(t, err)
					}
					beforeItems, err := repo.ListBatchImageItems(ctx, batchID, batchimage.BatchImageItemFilter{})
					require.NoError(t, err)
					items := []batchimage.CreateBatchImageItemParams{
						{JobID: batchID, CustomID: "image-1", Status: batchimage.BatchImageItemStatusSuccess, ImageCount: 1},
						{JobID: batchID, CustomID: "image-2", Status: batchimage.BatchImageItemStatusSuccess, ImageCount: 1},
					}
					if outcome == "rollback" {
						items[1].CustomID = items[0].CustomID
					}
					if operation == "bulk_items" {
						err = repo.BulkCreateBatchImageItems(ctx, items)
					} else {
						err = repo.ReplaceBatchImageItemsForJob(ctx, batchID, items, batchimage.BatchImageCounts{SuccessCount: 2})
					}
					if outcome == "rollback" {
						require.ErrorIs(t, err, batchimage.ErrBatchImageItemExists)
					} else {
						require.NoError(t, err)
					}
					afterItems, err := repo.ListBatchImageItems(ctx, batchID, batchimage.BatchImageItemFilter{})
					require.NoError(t, err)
					afterJob, err := repo.GetBatchImageJobByBatchID(ctx, batchID)
					require.NoError(t, err)
					if outcome == "rollback" {
						require.Equal(t, beforeItems, afterItems)
						require.Equal(t, beforeJob, afterJob)
					} else {
						require.Len(t, afterItems, 2)
						require.Equal(t, "image-1", afterItems[0].CustomID)
						require.Equal(t, "image-2", afterItems[1].CustomID)
						if operation == "replace_items" {
							require.Equal(t, "submission preview", batchImageDerefTest(afterItems[0].PromptPreview))
							require.Equal(t, 2, afterJob.SuccessCount)
						} else {
							require.Equal(t, beforeJob, afterJob)
						}
					}
				})
			}
		})
	}
}

// TestBatchImageRepository_BorrowedTransactionOutcomes observes a successful or
// failed transition through a caller-owned PostgreSQL transaction. Pool reads
// see the previous job until the caller commits, and caller rollback discards
// both successful writes and writes preceding an event error.
func TestBatchImageRepository_BorrowedTransactionOutcomes(t *testing.T) {
	ctx := context.Background()
	db := postgrescontainer.New(t)
	poolRepo, userID := newBatchImageRepositoryWithSQL(t, db)
	for _, outcome := range []string{"commit", "rollback", "event_error"} {
		t.Run(outcome, func(t *testing.T) {
			batchID := batchImageTestID(t, "borrowed")
			before, err := poolRepo.CreateBatchImageJob(ctx, batchimage.CreateBatchImageJobParams{
				BatchID:       batchID,
				UserID:        userID,
				BillingUserID: userID,
				Platform:      batchimage.BatchImageProviderGeminiAPI,
				Model:         "gemini-image",
				ItemCount:     1,
			})
			require.NoError(t, err)
			tx, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)
			// Release the transaction after a failed assertion as well as normal
			// owner completion. A completed transaction returns ErrTxDone during cleanup.
			t.Cleanup(func() { _ = tx.Rollback() })
			repo := NewRepositoryWithSQL(tx)
			var payload any
			if outcome == "event_error" {
				// Serialization fails after the transition update. The caller then
				// decides to discard that partial sequence with its own rollback.
				payload = make(chan int)
			}
			err = repo.TransitionBatchImageJobStatus(ctx, batchID, batchimage.BatchImageJobStatusUploading, batchimage.BatchImageTransitionOptions{
				EventType:    "status_changed",
				EventPayload: payload,
			})
			if outcome == "event_error" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			pending, err := repo.GetBatchImageJobByBatchID(ctx, batchID)
			require.NoError(t, err)
			require.Equal(t, batchimage.BatchImageJobStatusUploading, pending.Status)
			outside, err := poolRepo.GetBatchImageJobByBatchID(ctx, batchID)
			require.NoError(t, err)
			require.Equal(t, before, outside)
			if outcome == "commit" {
				// Publish the transition after outside reads observe the previous job.
				// This checks that the repository left transaction completion to its owner.
				require.NoError(t, tx.Commit())
			} else {
				// Discard both successful and partially failed sequences at the owner's
				// request. The transaction remains usable until this rollback is called.
				require.NoError(t, tx.Rollback())
			}
			after, err := poolRepo.GetBatchImageJobByBatchID(ctx, batchID)
			require.NoError(t, err)
			var eventCount int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM batch_image_events WHERE job_id = $1`, batchID).Scan(&eventCount))
			if outcome == "commit" {
				require.Equal(t, batchimage.BatchImageJobStatusUploading, after.Status)
				require.Equal(t, 1, eventCount)
			} else {
				require.Equal(t, before, after)
				require.Zero(t, eventCount)
			}
		})
	}
}

// newBatchImageRepositoryWithSQL inserts a test payer and binds the supplied
// executor to a repository. Assertions stop on insertion failure; the executor
// owner controls the payer and job transaction's lifetime.
func newBatchImageRepositoryWithSQL(t *testing.T, sqlq SQLExecutor) (*Repository, int64) {
	t.Helper()
	// Create a payer referenced by this fixture's jobs. Its surrounding transaction
	// or isolated database fixture then removes all related rows together after
	// the test completes.
	var userID int64
	err := sqlq.QueryRowContext(context.Background(), `
		INSERT INTO users (email, password_hash, role, status)
		VALUES ($1, 'test-password-hash', 'user', 'active')
		RETURNING id
	`, batchImageTestID(t, "user")+"@example.com").Scan(&userID)
	require.NoError(t, err)
	return NewRepositoryWithSQL(sqlq), userID
}

// batchImageTestID combines a bounded normalized prefix with a digest of the
// test name. Repeated calls with the same test and prefix reproduce one
// batch-shaped identifier for duplicate and lookup assertions.
func batchImageTestID(t *testing.T, prefix string) string {
	t.Helper()
	safePrefix := batchImageSafeTestIDSegment(prefix, 20)
	sum := sha1.Sum([]byte(t.Name()))
	// Use sixteen digest characters to bound fixture IDs.
	return "imgbatch_" + safePrefix + "_" + hex.EncodeToString(sum[:])[:16]
}

// batchImageSafeTestIDSegment normalizes a test-ID prefix and truncates it
// when it exceeds the supplied nonnegative byte limit. Empty normalized or
// trimmed values return job, including after truncation to zero; negative limits
// are invalid for slicing.
func batchImageSafeTestIDSegment(v string, maxLen int) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = regexp.MustCompile(`[^a-z0-9_-]+`).ReplaceAllString(v, "-")
	v = strings.Trim(v, "-_")
	if v == "" {
		v = "job"
	}
	if len(v) > maxLen {
		// Bound the ASCII normalized prefix before composing fixture IDs.
		v = v[:maxLen]
		v = strings.Trim(v, "-_")
	}
	if v == "" {
		return "job"
	}
	return v
}

// batchImageTestStringPtr returns a pointer to the supplied fixture string.
func batchImageTestStringPtr(v string) *string {
	return &v
}

// batchImageDerefTest returns a pointed-to fixture string or empty for nil.
// It lets assertions compare optional metadata values directly when the
// absence case should share the empty-string expectation.
func batchImageDerefTest(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// testTx opens a test database transaction and registers its rollback after
// database setup. Testing cleanup runs in reverse order, so the transaction
// finishes before the database fixture closes.
func testTx(t *testing.T) *sql.Tx {
	t.Helper()
	db := postgrescontainer.New(t)
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	// Register rollback after the database fixture cleanup so reverse cleanup
	// order ends the transaction first. Discard rollback errors because test
	// assertions have already reported the repository result under examination.
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}
