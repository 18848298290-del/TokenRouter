package batchimage

import (
	"context"
	"errors"
	"time"
)

// A settling task has already finished provider polling and indexing, so its
// next queue visit is a funding operation. PipelineProcessor reads persisted
// status before selecting settlement or provider progression, allowing the
// same queue entry to resume whichever stage a prior visit reached.
//
// Settlement failures can leave the job settling for another attempt or fail
// it after releasing the reservation. The processor rereads status when the
// billing-failed category is returned so a failed terminal job is
// acknowledged, while a still-active settlement receives its configured retry
// delay. Other errors return to the worker for its error retry policy. A
// missing settlement service treats the settling visit as terminal.

// PipelineProcessor chooses provider progression or settlement for each
// queued batch visit.
type PipelineProcessor struct {
	ProviderProcessor *ProviderProcessor // provider polling and indexing service
	SettlementService *Settlement        // optional task settlement service
	RetryDelay        time.Duration      // billing failure retry delay
}

// Process advances one queued batch according to its stored stage. Settling
// jobs invoke the configured settlement service; all other stages delegate to
// ProviderProcessor. A missing provider processor is a configuration error,
// and a missing settlement service returns a terminal result for a settling
// job.
//
// A billing failure triggers a status reread because retry exhaustion can
// already have failed the task. Terminal status acknowledges completion; an
// unreadable or still-settling record receives the configured RetryDelay,
// defaulting to the settlement retry interval.
func (p *PipelineProcessor) Process(ctx context.Context, batchID string) (BatchImageProcessResult, error) {
	if p == nil || p.ProviderProcessor == nil {
		return BatchImageProcessResult{}, errors.New("batch image pipeline processor is not configured")
	}
	job, err := p.ProviderProcessor.Repo.GetBatchImageJobByBatchID(ctx, batchID)
	if err != nil {
		return BatchImageProcessResult{}, err
	}
	if job.Status == BatchImageJobStatusSettling {
		if p.SettlementService == nil {
			return BatchImageProcessResult{Terminal: true}, nil
		}
		_, err := p.SettlementService.Settle(ctx, batchID)
		if err != nil {
			if errors.Is(err, ErrBatchImageSettlementBillingFailed) {
				// Settlement can return a billing error after releasing and failing the
				// task; reread persisted status before scheduling another funding
				// attempt.
				updated, getErr := p.ProviderProcessor.Repo.GetBatchImageJobByBatchID(ctx, batchID)
				if getErr == nil && IsTerminalBatchImageJobStatus(updated.Status) {
					return BatchImageProcessResult{Terminal: true}, nil
				}
				delay := p.RetryDelay
				if delay <= 0 {
					delay = BatchImageSettlementRetryDelay
				}
				return BatchImageProcessResult{RequeueAfter: delay}, nil
			}
			return BatchImageProcessResult{}, err
		}
		return BatchImageProcessResult{Terminal: true}, nil
	}
	return p.ProviderProcessor.Process(ctx, batchID)
}
