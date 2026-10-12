package batchimage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/batchimage"
	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// recoveryFundsFixture records release requests, injects failures, and deduplicates request IDs.
type recoveryFundsFixture struct {
	releases   []*billing.TaskFundsCommand
	releaseErr error
	seen       map[string]struct{}
}

type recordingBatchImageQueue struct {
	*fakeBatchImageQueue
	enqueued []string
}

func TestBatchImageBillingRecoveryService_ReleasesStaleUnsubmittedHold(t *testing.T) {
	repo := newFakeBatchImageRepository()
	apiKeyID := int64(22)
	holdAmount := 0.5
	stale := &batchimage.BatchImageJob{
		BatchID:       "imgbatch_stale_created",
		UserID:        11,
		APIKeyID:      &apiKeyID,
		Status:        batchimage.BatchImageJobStatusCreated,
		EstimatedCost: holdAmount,
		HoldAmount:    &holdAmount,
		CreatedAt:     time.Now().Add(-time.Hour),
		UpdatedAt:     time.Now().Add(-time.Hour),
	}
	activeProviderName := "providers/job"
	active := &batchimage.BatchImageJob{
		BatchID:         "imgbatch_has_provider",
		UserID:          11,
		APIKeyID:        &apiKeyID,
		Status:          batchimage.BatchImageJobStatusSubmitted,
		ProviderJobName: &activeProviderName,
		EstimatedCost:   holdAmount,
		HoldAmount:      &holdAmount,
		CreatedAt:       time.Now().Add(-time.Hour),
		UpdatedAt:       time.Now().Add(-time.Hour),
	}
	repo.jobs[stale.BatchID] = stale
	repo.jobs[active.BatchID] = active
	billing := &recoveryFundsFixture{}
	svc := &batchimage.BillingRecovery{Repo: repo, Funding: batchimage.Funding{Store: billing}, StaleAfter: time.Minute, Limit: 10}

	released, err := svc.ReleaseStaleUnsubmittedOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, released)
	require.Equal(t, batchimage.BatchImageJobStatusFailed, repo.jobs[stale.BatchID].Status)
	require.Equal(t, "SUBMIT_STALE_BEFORE_PROVIDER", batchimage.BatchImageDerefString(repo.jobs[stale.BatchID].LastErrorCode))
	require.Len(t, billing.releases, 1)
	require.Equal(t, batchimage.BatchImageReleaseRequestID(stale.BatchID), billing.releases[0].RequestID)
	require.Equal(t, batchimage.BatchImageJobStatusSubmitted, repo.jobs[active.BatchID].Status)
}

func TestBatchImageBillingRecoveryService_SkipsJobRefreshedByHeartbeat(t *testing.T) {
	repo := newFakeBatchImageRepository()
	apiKeyID := int64(22)
	holdAmount := 0.5
	// A heartbeat after cutoff keeps the slow submission alive and its funds reserved.
	fresh := &batchimage.BatchImageJob{
		BatchID:       "imgbatch_fresh_uploading",
		UserID:        11,
		APIKeyID:      &apiKeyID,
		Status:        batchimage.BatchImageJobStatusUploading,
		EstimatedCost: holdAmount,
		HoldAmount:    &holdAmount,
		CreatedAt:     time.Now().Add(-time.Hour),
		UpdatedAt:     time.Now(),
	}
	repo.jobs[fresh.BatchID] = fresh
	billing := &recoveryFundsFixture{}
	svc := &batchimage.BillingRecovery{Repo: repo, Funding: batchimage.Funding{Store: billing}, StaleAfter: time.Minute, Limit: 10}

	released, err := svc.ReleaseStaleUnsubmittedOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, released)
	require.Equal(t, batchimage.BatchImageJobStatusUploading, repo.jobs[fresh.BatchID].Status)
	require.Empty(t, billing.releases)
}

func TestBatchImageBillingRecoveryService_EnqueuesRetryWhenReleaseFails(t *testing.T) {
	repo := newFakeBatchImageRepository()
	apiKeyID := int64(22)
	holdAmount := 0.5
	stale := &batchimage.BatchImageJob{
		BatchID:       "imgbatch_stale_release_fail",
		UserID:        11,
		APIKeyID:      &apiKeyID,
		Status:        batchimage.BatchImageJobStatusCreated,
		EstimatedCost: holdAmount,
		HoldAmount:    &holdAmount,
		CreatedAt:     time.Now().Add(-time.Hour),
		UpdatedAt:     time.Now().Add(-time.Hour),
	}
	repo.jobs[stale.BatchID] = stale
	billing := &recoveryFundsFixture{releaseErr: errors.New("billing db down")}
	queue := &recordingBatchImageQueue{fakeBatchImageQueue: newFakeBatchImageQueue("")}
	svc := &batchimage.BillingRecovery{Repo: repo, Funding: batchimage.Funding{Store: billing}, Queue: queue, StaleAfter: time.Minute, Limit: 10}

	released, err := svc.ReleaseStaleUnsubmittedOnce(context.Background())
	// A failed job disappears from the stale list; enqueue a failed release so
	// ReleaseTerminalHold can retry it and return the reserved funds.
	require.Error(t, err)
	require.Equal(t, 0, released)
	require.Equal(t, batchimage.BatchImageJobStatusFailed, repo.jobs[stale.BatchID].Status)
	require.Equal(t, []string{stale.BatchID}, queue.enqueued)
}

func (r *recoveryFundsFixture) Reserve(context.Context, *billing.TaskFundsCommand) (*billing.TaskFundsResult, error) {
	panic("unexpected Reserve")
}

func (r *recoveryFundsFixture) Capture(context.Context, *billing.TaskFundsCommand) (*billing.TaskFundsResult, error) {
	panic("unexpected Capture")
}

func (r *recoveryFundsFixture) Release(_ context.Context, cmd *billing.TaskFundsCommand) (*billing.TaskFundsResult, error) {
	if r.releaseErr != nil {
		r.releases = append(r.releases, cmd)
		return nil, r.releaseErr
	}
	if r.seen == nil {
		r.seen = make(map[string]struct{})
	}
	if cmd != nil {
		cmd.Normalize()
		if _, ok := r.seen[cmd.RequestID]; ok {
			r.releases = append(r.releases, cmd)
			return &billing.TaskFundsResult{Applied: false}, nil
		}
		r.seen[cmd.RequestID] = struct{}{}
	}
	r.releases = append(r.releases, cmd)
	return &billing.TaskFundsResult{Applied: true}, nil
}

func (q *recordingBatchImageQueue) Enqueue(_ context.Context, batchID string) error {
	q.enqueued = append(q.enqueued, batchID)
	return nil
}
