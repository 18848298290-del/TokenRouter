package batchimage

import (
	"context"
	"errors"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// cleanup.go removes retained provider resources and records input or output
// deletion after cleanup succeeds.
// Provider input and output references can outlive generation, settlement and
// the process that started the job. Cleanup selects records whose input
// retention or output expiry has elapsed, resolves the provider bound to each
// record, and removes those resources before persisting deletion markers. The
// public manual output operation requires completed state.
//
// Internal input cleanup accepts terminal outcomes, while internal output
// cleanup also accepts failed and cancelled jobs. A completed output becomes
// output_deleted after successful cleanup; failed and cancelled records
// retain their outcome. Provider not-found diagnostics are accepted as
// already deleted, enabling retry after remote deletion succeeded but a local
// marker write failed. Other provider errors save a sanitized diagnostic and
// leave resources eligible for a later attempt.

// Cleanup defaults set terminal input retention, scan frequency and
// per-resource candidate limits when options do not supply positive values.
const (
	DefaultBatchImageInputRetentionAfterTerminal = 24 * time.Hour

	DefaultBatchImageCleanupInterval  = 30 * time.Minute
	DefaultBatchImageCleanupBatchSize = 100
)

// CleanupOptions configures terminal input retention, periodic scanning and
// candidate batch sizes.
type CleanupOptions struct {
	InputRetention time.Duration // terminal input retention duration
	Interval       time.Duration // periodic cleanup scan interval
	BatchSize      int           // candidates per resource scan
}

// Cleanup deletes job-bound provider resources and persists successful input
// or output removal.
type Cleanup struct {
	Now             func() time.Time                                             // injected current time
	Repo            BatchImageRepository                                         // persistent cleanup metadata store
	ResolveProvider func(context.Context, *BatchImageJob) (BoundProvider, error) // job bound cleanup provider resolver
	Options         CleanupOptions                                               // cleanup timing and scan limits
	Observe         func(string, ...any)                                         // optional cleanup diagnostic callback
}

// BatchImageCleanupRunResult counts successful resource cleanups and
// individual failures during one scan.
type BatchImageCleanupRunResult struct {
	InputCleaned  int // successfully handled input jobs
	OutputCleaned int // successfully handled output jobs
	Failures      int // individual resource cleanup failures
}

// warn sends a cleanup diagnostic through the configured observation
// callback.
func (s *Cleanup) warn(event string, values ...any) {
	if s.Observe != nil {
		s.Observe(event, values...)
	}
}

// Run performs an immediate cleanup scan and repeats it at CleanupInterval
// until cancellation. Per-scan errors are discarded for the next scheduled
// attempt, while RunOnce records counts and individual cleanup failures.
func (s *Cleanup) Run(ctx context.Context) {
	ticker := time.NewTicker(s.CleanupInterval())
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		// Resource failures remain recorded for later scans; a scan error ends this
		// attempt while the periodic loop stays available until cancellation.
		_, _ = s.RunOnce(ctx, s.now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// AppendCleanupEvent writes an audit event and reports its failure through
// Observe. Its return carries no error, so cleanup callers can continue their
// main deletion or marker operation after an audit failure.
func (s *Cleanup) AppendCleanupEvent(ctx context.Context, batchID, eventType string, payload any) {
	// Audit failure cannot undo a remote deletion or prevent its local marker.
	// Report the failed event separately while callers finish that resource operation.
	if err := s.Repo.AppendBatchImageEvent(ctx, batchID, eventType, payload); err != nil {
		s.warn("batch_image.cleanup_event_failed",
			"batch_id", batchID,
			"event_type", eventType,
			"error", err,
		)
	}
}

// DeleteOutputsForOwner authorizes the user and API key before manually
// deleting a completed batch's output. An existing deletion marker or
// output_deleted status returns the current public batch immediately. Other
// unfinished or failed states reject this public operation.
//
// The requested event is followed by CleanupJob, which removes provider
// resources before storing deletion state. The method then rereads the
// owner-scoped record for its public response. A later repository error can
// therefore occur after remote deletion has already succeeded, and a retry
// uses the recorded marker or provider not-found handling.
func (s *Cleanup) DeleteOutputsForOwner(ctx context.Context, owner BatchImageOwner, batchID string) (*BatchImagePublicBatch, error) {
	job, err := s.Repo.GetBatchImageJobByBatchIDForOwner(ctx, owner.UserID, owner.APIKeyID, batchID)
	if err != nil {
		return nil, err
	}
	if job.Status == BatchImageJobStatusOutputDeleted || job.OutputDeletedAt != nil {
		return BatchImageJobToPublic(job), nil
	}
	if job.Status != BatchImageJobStatusCompleted {
		return nil, ErrBatchImageOutputDeleteNotReady
	}
	s.AppendCleanupEvent(ctx, job.BatchID, "manual_output_delete_requested", map[string]any{
		"batch_id":       job.BatchID,
		"cleanup_target": "output",
		"reason":         "manual",
	})
	if err := s.CleanupJob(ctx, job, CleanupTargetOutput, "manual"); err != nil {
		return nil, err
	}
	updated, err := s.Repo.GetBatchImageJobByBatchIDForOwner(ctx, owner.UserID, owner.APIKeyID, batchID)
	if err != nil {
		return nil, err
	}
	return BatchImageJobToPublic(updated), nil
}

// CleanupInput loads an internal job and applies input cleanup with the TTL
// reason. The caller supplies an authorized internal batch identity;
// CleanupJob checks terminal eligibility and existing input deletion.
func (s *Cleanup) CleanupInput(ctx context.Context, batchID string) error {
	job, err := s.Repo.GetBatchImageJobByBatchID(ctx, batchID)
	if err != nil {
		return err
	}
	return s.CleanupJob(ctx, job, CleanupTargetInput, "ttl")
}

// CleanupOutput loads an internal job and applies output cleanup with the
// supplied reason. Callers choose a server-side batch identity; CleanupJob
// checks its state and reads the stored provider references.
func (s *Cleanup) CleanupOutput(ctx context.Context, batchID string, reason string) error {
	job, err := s.Repo.GetBatchImageJobByBatchID(ctx, batchID)
	if err != nil {
		return err
	}
	return s.CleanupJob(ctx, job, CleanupTargetOutput, reason)
}

// RunOnce scans bounded input and output cleanup candidates at the supplied
// time, using the service clock for a zero time. Individual deletion failures
// increase Failures and allow later candidates to continue; list errors end
// the scan.
func (s *Cleanup) RunOnce(ctx context.Context, now time.Time) (BatchImageCleanupRunResult, error) {
	if s == nil || s.Repo == nil {
		return BatchImageCleanupRunResult{}, ErrBatchImageCleanupFailed
	}
	if now.IsZero() {
		now = s.now()
	}
	limit := s.CleanupBatchSize()
	result := BatchImageCleanupRunResult{}
	inputCutoff := now.Add(-s.InputRetentionAfterTerminal())
	inputJobs, err := s.Repo.ListBatchImageJobsDueForInputCleanup(ctx, inputCutoff, limit)
	if err != nil {
		return result, err
	}
	for _, job := range inputJobs {
		if job == nil {
			continue
		}
		// An input cleanup failure affects this candidate only; continue the
		// bounded scan so other eligible jobs can release their provider storage.
		if err := s.CleanupJob(ctx, job, CleanupTargetInput, "ttl"); err != nil {
			result.Failures++
			continue
		}
		result.InputCleaned++
	}
	outputJobs, err := s.Repo.ListBatchImageJobsDueForOutputCleanup(ctx, now, limit)
	if err != nil {
		return result, err
	}
	for _, job := range outputJobs {
		if job == nil {
			continue
		}
		// An expired-output failure retains its record for another attempt;
		// continue processing other expired outputs in the same cleanup batch.
		if err := s.CleanupJob(ctx, job, CleanupTargetOutput, "expired"); err != nil {
			result.Failures++
			continue
		}
		result.OutputCleaned++
	}
	return result, nil
}

// CleanupJob removes the selected stored provider resources and records the
// deletion timestamp after success. Input requires a terminal job; output
// accepts completed, failed or cancelled state. Existing deletion markers
// return successfully. CleanupTargetAll and other targets are rejected here;
// provider adapters handle their own all-resource operation separately.
//
// Audit events describe the attempt but do not gate deletion. A provider
// failure records a scrubbed diagnostic, maps unsafe paths distinctly and
// leaves deletion markers absent. After successful remote cleanup, the
// repository marks input or output; a marker error can leave already removed
// resources for the next idempotent provider attempt.
func (s *Cleanup) CleanupJob(ctx context.Context, job *BatchImageJob, target CleanupTarget, reason string) error {
	if job == nil {
		return ErrBatchImageJobNotFound
	}
	switch target {
	case CleanupTargetInput:
		if job.InputDeletedAt != nil {
			return nil
		}
		if !IsTerminalBatchImageJobStatus(job.Status) {
			return ErrBatchImageCleanupFailed
		}
		s.AppendCleanupEvent(ctx, job.BatchID, "input_cleanup_started", CleanupEventPayload(job.BatchID, target, reason, nil))
	case CleanupTargetOutput:
		if job.OutputDeletedAt != nil || job.Status == BatchImageJobStatusOutputDeleted {
			return nil
		}
		if job.Status != BatchImageJobStatusCompleted && job.Status != BatchImageJobStatusFailed && job.Status != BatchImageJobStatusCancelled {
			return ErrBatchImageOutputDeleteNotReady
		}
		s.AppendCleanupEvent(ctx, job.BatchID, "output_cleanup_started", CleanupEventPayload(job.BatchID, target, reason, nil))
	default:
		return ErrUnsupportedCleanupTarget
	}

	if err := s.CallProviderCleanup(ctx, job, target); err != nil {
		code := CleanupFailureCode(err)
		msg := SanitizeBatchImagePublicMessage(err.Error())
		// A diagnostic write can fail independently of provider deletion. Keep the
		// cleanup error as the caller's result and leave deletion markers absent.
		if recordErr := s.Repo.RecordBatchImageCleanupFailure(ctx, job.BatchID, code, msg); recordErr != nil {
			s.warn("batch_image.cleanup_failure_record_failed",
				"batch_id", job.BatchID,
				"error", recordErr,
			)
		}
		event := string(target) + "_cleanup_failed"
		s.AppendCleanupEvent(ctx, job.BatchID, event, map[string]any{"batch_id": job.BatchID, "cleanup_target": string(target), "reason": reason, "error_code": code})
		if errors.Is(err, ErrBatchImageProviderUnsafeCleanupPath) {
			return ErrBatchImageCleanupUnsafePath
		}
		return ErrBatchImageProviderCleanupFailed
	}

	// Provider cleanup has succeeded before the deletion marker is persisted;
	// record that outcome so later scans can skip this resource target.
	deletedAt := s.now()
	if target == CleanupTargetInput {
		return s.Repo.MarkBatchImageInputDeleted(ctx, job.BatchID, deletedAt)
	}
	return s.Repo.MarkBatchImageOutputDeleted(ctx, job.BatchID, deletedAt)
}

// CallProviderCleanup resolves the backend bound to this job and invokes its
// cleanup operation. Diagnostics recognized by CleanupErrorIsNotFound are
// accepted as already deleted; other lookup or cleanup errors propagate to
// CleanupJob.
func (s *Cleanup) CallProviderCleanup(ctx context.Context, job *BatchImageJob, target CleanupTarget) error {
	if s == nil || s.ResolveProvider == nil {
		return ErrBatchImageCleanupFailed
	}
	platform, err := s.ResolveProvider(ctx, job)
	if err != nil {
		return err
	}
	if err := platform.Cleanup(ctx, job, target); err != nil {
		// A retry can encounter a resource removed before an earlier marker write
		// failed; accept recognizable absence and proceed to persist deletion.
		if CleanupErrorIsNotFound(err) {
			return nil
		}
		return err
	}
	return nil
}

// InputRetentionAfterTerminal returns the configured positive input retention
// duration. Nil services and nonpositive settings use twenty-four hours,
// which RunOnce subtracts from its scan time to select old terminal input.
func (s *Cleanup) InputRetentionAfterTerminal() time.Duration {
	if s != nil && s.Options.InputRetention > 0 {
		return s.Options.InputRetention
	}
	return DefaultBatchImageInputRetentionAfterTerminal
}

// CleanupInterval returns the configured positive duration between cleanup
// scans. Nil services and nonpositive settings use thirty minutes, allowing
// Run to construct a valid periodic ticker before its first scan.
func (s *Cleanup) CleanupInterval() time.Duration {
	if s != nil && s.Options.Interval > 0 {
		return s.Options.Interval
	}
	return DefaultBatchImageCleanupInterval
}

// CleanupBatchSize returns the configured positive candidate limit for each
// input or output scan. Nil services and nonpositive settings use one
// hundred, bounding the work requested from each repository listing.
func (s *Cleanup) CleanupBatchSize() int {
	if s != nil && s.Options.BatchSize > 0 {
		return s.Options.BatchSize
	}
	return DefaultBatchImageCleanupBatchSize
}

// CleanupEventPayload records the batch, resource target and cleanup reason
// for audit writing. A supplied deletion time adds UTC RFC3339 text, while an
// absent time leaves that field out.
func CleanupEventPayload(batchID string, target CleanupTarget, reason string, deletedAt *time.Time) map[string]any {
	payload := map[string]any{
		"batch_id":       batchID,
		"cleanup_target": string(target),
		"reason":         reason,
	}
	if deletedAt != nil {
		payload["deleted_at"] = deletedAt.UTC().Format(time.RFC3339)
	}
	return payload
}

// CleanupErrorIsNotFound recognizes missing-resource diagnostics through
// their reason and message text. It matches NOT_FOUND, NOT FOUND or 404 text,
// so cleanup uses this heuristic to accept already removed resources.
func CleanupErrorIsNotFound(err error) bool {
	if err == nil {
		return false
	}
	reason := strings.ToUpper(infraerrors.Reason(err))
	msg := strings.ToUpper(err.Error())
	return strings.Contains(reason, "NOT_FOUND") || strings.Contains(msg, "NOT FOUND") || strings.Contains(msg, "404")
}

// CleanupFailureCode chooses the unsafe-path category before considering an
// application reason. Other errors retain a nonblank reason, and unclassified
// provider failures receive the generic cleanup diagnostic code.
func CleanupFailureCode(err error) string {
	if errors.Is(err, ErrBatchImageProviderUnsafeCleanupPath) {
		return "BATCH_IMAGE_CLEANUP_UNSAFE_PATH"
	}
	reason := strings.TrimSpace(infraerrors.Reason(err))
	if reason != "" {
		return reason
	}
	return "BATCH_IMAGE_PROVIDER_CLEANUP_FAILED"
}

// now reads the cleanup clock when a callback was injected at construction.
// Otherwise it uses time.Now, so controlled scans can fix their timing while
// production records current deletion timestamps.
func (s *Cleanup) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
