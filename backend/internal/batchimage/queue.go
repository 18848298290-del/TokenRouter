package batchimage

import (
	"context"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

var (
	ErrBatchImageLeaseLost     = infraerrors.Conflict("BATCH_IMAGE_LEASE_LOST", "batch image job lease is no longer owned")
	ErrBatchImageQueueEmpty    = infraerrors.New(infraerrors.CategoryNotFound, "BATCH_IMAGE_QUEUE_EMPTY", "batch image queue is empty")
	ErrBatchImageAlreadyQueued = infraerrors.New(infraerrors.CategoryConflict, "BATCH_IMAGE_ALREADY_QUEUED", "batch image job is already queued")

	ErrInvalidBatchImageQueuePayload = infraerrors.New(infraerrors.CategoryBadRequest, "BATCH_IMAGE_QUEUE_INVALID_PAYLOAD", "invalid batch image queue payload")
)

type ReservedBatchImageJob struct {
	BatchID string
}

type BatchImageJobLock interface {
	Release(ctx context.Context) error
	Heartbeat(context.Context) error
	Ack(context.Context) error
	RequeueAfter(context.Context, time.Duration) error
}

type BatchImageQueue interface {
	Enqueue(ctx context.Context, batchID string) error
	Reserve(ctx context.Context, blockTimeout time.Duration) (ReservedBatchImageJob, error)
	RequeueAfter(ctx context.Context, batchID string, delay time.Duration) error
	Ack(ctx context.Context, batchID string) error
	Heartbeat(ctx context.Context, batchID string) error
	MoveDueDelayedToReady(ctx context.Context, limit int) (int, error)
	RecoverStaleActive(ctx context.Context, staleAfter time.Duration, limit int) (int, error)
	TryAcquireJobLock(ctx context.Context, batchID string, ttl time.Duration) (BatchImageJobLock, bool, error)
}

// BatchImageJobLockRefresher reports ownership loss when renewing a lock.
type BatchImageJobLockRefresher interface {
	Refresh(context.Context, time.Duration) error
}

func IsValidBatchImageID(batchID string) bool {
	return strings.HasPrefix(batchID, "imgbatch_") && len(batchID) > len("imgbatch_")
}
