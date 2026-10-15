package batchimage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// processor.go polls bound providers and reconciles their result rows into
// persisted batch-image items.
// Provider polling resumes from the stored batch stage and the originally
// bound backend. A successful observation first records its output reference,
// then enters indexing; already-indexing jobs reopen their saved output
// directly. Failed, expired and cancelled provider outcomes save terminal
// state before releasing the original funding reservation.
//
// The indexer reconciles provider custom IDs with submitted placeholders
// before replacing result rows and counts. When submitted IDs exist, unknown
// IDs are skipped and missing IDs become visible failures. Duplicate accepted
// IDs abort replacement.
// Repository indexing guards prevent a stale worker from replacing already
// progressed results. Parsing supports the nested and top-level candidate
// shapes used by Gemini batch outputs, retains result metadata, and keeps
// image payloads out of persisted item fields. After a successful replacement
// the processor advances to settling for a later funding visit.

// Parsed outcome labels and polling defaults govern provider retry scheduling
// and the metadata retained when result rows are rebuilt.
const (
	BatchImageParsedStatusSucceeded = "succeeded" // parsed row contains image parts
	BatchImageParsedStatusFailed    = "failed"    // parsed row contains no images

	DefaultBatchImageProcessorRequeue = 30 * time.Second
	BatchImageProviderErrorRequeue    = time.Minute
	BatchImageMaxErrorMessageLength   = 1000
)

// BoundProvider performs upstream operations using the execution provider
// already bound to a batch.
type BoundProvider interface {
	// Submit creates an upstream batch from its expanded requests and persisted
	// local job identity. Callers reserve funding before invoking it and save
	// its returned job and resource references before publishing a queue visit.
	Submit(context.Context, *BatchImageJob, BatchImageInput) (*BatchProviderJob, error)
	// Get observes the upstream job already bound to the local task. It returns
	// normalized provider status and optional result references; callers honor
	// cancellation and decide how the stored lifecycle should progress.
	Get(context.Context, *BatchImageJob) (*BatchProviderStatus, error)
	// Cancel requests termination of the upstream job already bound to this
	// local task. Public cancellation retains the stored stage and reservation;
	// submitted and running jobs confirm the outcome through polling, while
	// indexing visits continue reading saved results.
	Cancel(context.Context, *BatchImageJob) error
	// OpenResult opens the provider's retained output stream and returns its
	// content type. The caller closes that stream after indexing or scanning,
	// including failures and cancellation after an opened result is returned.
	OpenResult(context.Context, *BatchImageJob) (io.ReadCloser, string, error)
	// Cleanup removes the selected provider resources recorded on the stored
	// task. Callers pass server-generated references, preserve deletion markers
	// until success, and apply their own missing-resource handling for cleanup
	// retries.
	Cleanup(context.Context, *BatchImageJob, CleanupTarget) error
}

// ProviderProcessor advances polling and indexing while preserving the stored
// task and funding identities.
type ProviderProcessor struct {
	// Now is retained on the processor value. Current progression obtains
	// timestamps through repository transitions and ResultIndexer, so this
	// callback is not read by Process.
	Now             func() time.Time                                             // retained processor clock callback
	Repo            BatchImageRepository                                         // persistent task and item store
	ResolveProvider func(context.Context, *BatchImageJob) (BoundProvider, error) // job bound operation resolver
	// Indexer is reused when supplied, and IndexAndSettle fills a missing
	// repository before indexing. Configure a shared indexer before concurrent
	// workers can invoke this method.
	Indexer        *ResultIndexer               // optional configured result indexer
	Funding        Funding                      // terminal task refund adapter
	InvalidateAuth func(context.Context, int64) // optional payer cache invalidation
	DefaultRequeue time.Duration                // unspecified provider polling delay
	Observe        func(string, ...any)         // optional progression diagnostic callback
}

// ResultIndexer rebuilds stored outcomes by matching provider rows to
// submitted custom IDs.
type ResultIndexer struct {
	Now     func() time.Time     // item indexing clock callback
	Repo    BatchImageRepository // persistent task and item store
	Observe func(string, ...any) // optional progression diagnostic callback
}

// BatchImageIndexResult counts accepted custom IDs after provider result
// reconciliation.
type BatchImageIndexResult struct {
	SuccessCount int // successful item count
	FailCount    int // failed item count
	TotalCount   int // indexed custom ID count
}

// ParsedBatchImageResult records provider line metadata used to rebuild one
// persisted item.
type ParsedBatchImageResult struct {
	CustomID      string // submitted item identity
	Status        string // parsed provider row outcome
	MimeType      string // image MIME type
	FileExtension string // image filename extension
	ImageCount    int    // returned image parts

	ErrorCode    string // stable failure category
	ErrorMessage string // failure diagnostic text

	SourceLineNumber int // one based result line
}

// warn sends a provider progression diagnostic through the configured
// observation callback.
func (p *ProviderProcessor) warn(event string, values ...any) {
	if p.Observe != nil {
		p.Observe(event, values...)
	}
}

// Process advances the reserved batch through its stored provider and
// indexing stages. Processor-done jobs retry terminal hold release where
// applicable and return a terminal queue outcome. Other jobs require a
// resolved bound provider and saved upstream name before polling.
//
// Polling errors schedule a fixed provider retry while cancellation returns
// its context error. Output references are saved before status progression.
// Running observations record running state, successful observations enter
// indexing, and failure or cancellation records terminal status before
// funding release. Already-indexing jobs resume IndexAndSettle without
// another provider status call, keeping saved results available across worker
// restarts.
func (p *ProviderProcessor) Process(ctx context.Context, batchID string) (BatchImageProcessResult, error) {
	if p == nil || p.Repo == nil || p.ResolveProvider == nil {
		return BatchImageProcessResult{}, infraerrors.New(infraerrors.CategoryInternalServer, "BATCH_IMAGE_PROCESSOR_NOT_CONFIGURED", "batch image processor is not configured")
	}

	job, err := p.Repo.GetBatchImageJobByBatchID(ctx, batchID)
	if err != nil {
		return BatchImageProcessResult{}, err
	}
	if IsBatchImageProcessorDoneStatus(job.Status) {
		if err := p.ReleaseTerminalHold(ctx, job); err != nil {
			return BatchImageProcessResult{}, err
		}
		return BatchImageProcessResult{Terminal: true}, nil
	}

	platform, err := p.ResolveProvider(ctx, job)
	if err != nil {
		return BatchImageProcessResult{}, err
	}
	if strings.TrimSpace(BatchImageDerefString(job.ProviderJobName)) == "" {
		return BatchImageProcessResult{}, ErrBatchImageMissingProviderJobName
	}

	if job.Status == BatchImageJobStatusIndexing {
		return p.IndexAndSettle(ctx, job, platform)
	}

	status, err := platform.Get(ctx, job)
	if ctx.Err() != nil {
		return BatchImageProcessResult{}, ctx.Err()
	}
	if err != nil {
		p.warn("batch_image.provider_status_check_failed",
			"batch_id", job.BatchID,
			"platform", job.Platform,
			"provider_job_name", BatchImageDerefString(job.ProviderJobName),
			"error", err,
		)
		// An upstream observation error leaves stored progress unchanged; schedule
		// another provider check so transient failures do not terminate a submitted
		// batch.
		return BatchImageProcessResult{RequeueAfter: BatchImageProviderErrorRequeue}, nil
	}
	// An empty observation carries no confirmed progress; retain the stored
	// stage and ask the worker to poll again using its normal delay.
	if status == nil {
		return BatchImageProcessResult{RequeueAfter: p.RequeueDelay(0)}, nil
	}
	// Save a newly reported output reference before entering indexing so another
	// queue visit can reopen the same result after interruption.
	if err := p.PersistProviderOutputRef(ctx, job, status.ProviderOutputRef); err != nil {
		return BatchImageProcessResult{}, err
	}

	switch status.InternalState {
	case BatchProviderStateQueued:
		return BatchImageProcessResult{RequeueAfter: p.RequeueDelay(status.SuggestedRequeueAfter)}, nil
	case BatchProviderStateRunning:
		if job.Status != BatchImageJobStatusRunning {
			if err := p.Repo.TransitionBatchImageJobStatus(ctx, job.BatchID, BatchImageJobStatusRunning, BatchImageTransitionOptions{
				EventType:    "provider_status_checked",
				EventPayload: map[string]any{"provider_state": status.RawState},
			}); err != nil {
				return BatchImageProcessResult{}, err
			}
			job.Status = BatchImageJobStatusRunning
		}
		return BatchImageProcessResult{RequeueAfter: p.RequeueDelay(status.SuggestedRequeueAfter)}, nil
	case BatchProviderStateSucceeded:
		if job.Status != BatchImageJobStatusIndexing {
			if err := p.Repo.TransitionBatchImageJobStatus(ctx, job.BatchID, BatchImageJobStatusIndexing, BatchImageTransitionOptions{
				EventType:    "indexing_started",
				EventPayload: map[string]any{"provider_state": status.RawState},
			}); err != nil {
				return BatchImageProcessResult{}, err
			}
			job.Status = BatchImageJobStatusIndexing
		}
		return p.IndexAndSettle(ctx, job, platform)
	// A confirmed provider failure ends upstream execution; persist the terminal
	// diagnostic before returning its reservation so later queue retries can
	// finish funding cleanup.
	case BatchProviderStateFailed, BatchProviderStateExpired:
		code := strings.TrimSpace(status.ErrorCode)
		if code == "" && status.InternalState == BatchProviderStateExpired {
			code = "PROVIDER_BATCH_EXPIRED"
		}
		if code == "" {
			code = "PROVIDER_BATCH_FAILED"
		}
		msg := TruncateBatchImageMessage(status.ErrorMessage, BatchImageMaxErrorMessageLength)
		if err := p.Repo.TransitionBatchImageJobStatus(ctx, job.BatchID, BatchImageJobStatusFailed, BatchImageTransitionOptions{
			EventType:    "job_failed",
			EventPayload: map[string]any{"provider_state": status.RawState, "error_code": code},
			ErrorCode:    BatchImageStringPtr(code),
			ErrorMessage: BatchImageOptionalStringPtr(msg),
		}); err != nil {
			return BatchImageProcessResult{}, err
		}
		job.Status = BatchImageJobStatusFailed
		if err := p.ReleaseTerminalHold(ctx, job); err != nil {
			return BatchImageProcessResult{}, err
		}
		return BatchImageProcessResult{Terminal: true}, nil
	// Provider cancellation is confirmed by this observation; record that
	// terminal outcome before releasing the hold retained during the earlier
	// cancellation request.
	case BatchProviderStateCancelled:
		if err := p.Repo.TransitionBatchImageJobStatus(ctx, job.BatchID, BatchImageJobStatusCancelled, BatchImageTransitionOptions{
			EventType:    "job_failed",
			EventPayload: map[string]any{"provider_state": status.RawState, "error_code": "PROVIDER_BATCH_CANCELLED"},
		}); err != nil {
			return BatchImageProcessResult{}, err
		}
		job.Status = BatchImageJobStatusCancelled
		if err := p.ReleaseTerminalHold(ctx, job); err != nil {
			return BatchImageProcessResult{}, err
		}
		return BatchImageProcessResult{Terminal: true}, nil
	default:
		return BatchImageProcessResult{RequeueAfter: p.RequeueDelay(status.SuggestedRequeueAfter)}, nil
	}
}

// IndexAndSettle rebuilds result metadata and advances the job into settling
// for a later queue visit. An absent indexer is constructed from processor
// dependencies; an injected indexer with no repository is filled from the
// processor, so configure shared indexers before concurrent use.
//
// Missing output returns an error for worker retry. An indexing-state
// conflict schedules a brief reread because another worker has progressed the
// task. Other indexing failures save a failed outcome and then release its
// hold. Successful replacement persists counts before the settling
// transition, so later settlement reads the reconciled custom-ID outcomes.
func (p *ProviderProcessor) IndexAndSettle(ctx context.Context, job *BatchImageJob, platform BoundProvider) (BatchImageProcessResult, error) {
	indexer := p.Indexer
	if indexer == nil {
		indexer = &ResultIndexer{Repo: p.Repo, Observe: p.Observe}
	}
	if indexer.Repo == nil {
		indexer.Repo = p.Repo
	}

	result, err := indexer.Index(ctx, job, platform)
	if ctx.Err() != nil {
		return BatchImageProcessResult{}, ctx.Err()
	}
	if err != nil {
		if errors.Is(err, ErrBatchImageIndexOutputMissing) {
			return BatchImageProcessResult{}, err
		}
		// Another worker has advanced the job, possibly to settlement or a terminal state.
		// Requeue briefly to read that state again instead of reporting an indexing failure.
		if errors.Is(err, ErrBatchImageIndexStateConflict) {
			return BatchImageProcessResult{RequeueAfter: time.Millisecond}, nil
		}
		code := "INDEX_PARSE_FAILED"
		if errors.Is(err, ErrBatchImageDuplicateCustomID) {
			code = "DUPLICATE_CUSTOM_ID_IN_OUTPUT"
		}
		msg := TruncateBatchImageMessage(err.Error(), BatchImageMaxErrorMessageLength)
		// Unusable output cannot establish chargeable counts. Save terminal failure
		// before refund so a later visit can finish release if this attempt stops.
		transitionErr := p.Repo.TransitionBatchImageJobStatus(ctx, job.BatchID, BatchImageJobStatusFailed, BatchImageTransitionOptions{
			EventType:    "indexing_failed",
			EventPayload: map[string]any{"error_code": code},
			ErrorCode:    BatchImageStringPtr(code),
			ErrorMessage: BatchImageOptionalStringPtr(msg),
		})
		if transitionErr != nil {
			return BatchImageProcessResult{}, transitionErr
		}
		job.Status = BatchImageJobStatusFailed
		if err := p.ReleaseTerminalHold(ctx, job); err != nil {
			return BatchImageProcessResult{}, err
		}
		return BatchImageProcessResult{Terminal: true}, nil
	}

	// The item replacement already saved reconciled outcomes and counts; publish
	// settling next so a later queue visit can capture that exact workload.
	if err := p.Repo.TransitionBatchImageJobStatus(ctx, job.BatchID, BatchImageJobStatusSettling, BatchImageTransitionOptions{
		EventType: "indexing_completed",
		EventPayload: map[string]any{
			"success_count": result.SuccessCount,
			"fail_count":    result.FailCount,
			"total_count":   result.TotalCount,
		},
	}); err != nil {
		return BatchImageProcessResult{}, err
	}
	return BatchImageProcessResult{RequeueAfter: time.Millisecond}, nil
}

// ReleaseTerminalHold returns the original reservation for failed and
// cancelled jobs, then invalidates the payer cache. Nil inputs and other
// statuses return immediately; funding failures remain errors so the worker
// can retry the terminal visit.
func (p *ProviderProcessor) ReleaseTerminalHold(ctx context.Context, job *BatchImageJob) error {
	if p == nil || job == nil {
		return nil
	}
	if job.Status != BatchImageJobStatusFailed && job.Status != BatchImageJobStatusCancelled {
		return nil
	}
	if err := p.Funding.Release(ctx, job, BatchImageDerefString(job.RequestHash)); err != nil {
		return err
	}
	// Returned reservation funds can change subsequent admission decisions.
	// Refresh payer authentication data after the successful terminal funding call.
	if billingUserID := BillingUserID(job); p.InvalidateAuth != nil && billingUserID > 0 {
		p.InvalidateAuth(ctx, billingUserID)
	}
	return nil
}

// PersistProviderOutputRef saves a changed nonblank output reference before
// updating the local job pointer. Empty input, nil jobs and equal trimmed
// references return immediately, leaving existing result access unchanged.
func (p *ProviderProcessor) PersistProviderOutputRef(ctx context.Context, job *BatchImageJob, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" || job == nil || BatchImageDerefString(job.ProviderOutputRef) == ref {
		return nil
	}
	// Keep the local reference at its persisted value until the write succeeds;
	// indexing must not reopen an output that a later visit cannot recover.
	if err := p.Repo.UpdateBatchImageJobProviderOutputRef(ctx, job.BatchID, ref); err != nil {
		return err
	}
	job.ProviderOutputRef = &ref
	return nil
}

// RequeueDelay prefers a positive provider-suggested interval, then the
// processor's positive default. Absent or nonpositive suggestions and
// settings use thirty seconds for subsequent queued or running status checks.
func (p *ProviderProcessor) RequeueDelay(suggested time.Duration) time.Duration {
	if suggested > 0 {
		return suggested
	}
	if p != nil && p.DefaultRequeue > 0 {
		return p.DefaultRequeue
	}
	return DefaultBatchImageProcessorRequeue
}

// IsBatchImageProcessorDoneStatus reports whether provider processing can
// stop at this stage. Settling is done for provider work in addition to
// terminal statuses, allowing PipelineProcessor to choose funding progression
// separately.
func IsBatchImageProcessorDoneStatus(status string) bool {
	if status == BatchImageJobStatusSettling {
		return true
	}
	return IsTerminalBatchImageJobStatus(status)
}

// Index reads provider output and replaces result metadata after reconciling
// it with all stored custom IDs. The caller supplies a job in indexing state
// and a bound result provider. The result stream is closed after parsing, and
// cancellation discards the pending replacement.
//
// When expected IDs exist, unknown provider rows are skipped and absent IDs
// become PROVIDER_RESULT_MISSING failures. Duplicate accepted IDs abort
// indexing. Successful counts measure custom IDs containing at least one
// image, while each item retains its image-part count for downloads. All
// metadata and counts are handed to the guarded repository replacement only
// after the full stream succeeds; audit append failures are observed
// independently.
func (i *ResultIndexer) Index(ctx context.Context, job *BatchImageJob, platform BoundProvider) (*BatchImageIndexResult, error) {
	if i == nil || i.Repo == nil || job == nil || platform == nil {
		return nil, ErrBatchImageIndexOutputMissing
	}
	expected, err := i.ListExpectedCustomIDs(ctx, job.BatchID)
	if err != nil {
		return nil, err
	}

	r, _, err := platform.OpenResult(ctx, job)
	if ctx.Err() != nil {
		// Cancellation can arrive after the provider has opened a stream; close
		// that returned resource before discarding the pending indexing attempt.
		if r != nil {
			_ = r.Close()
		}
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, ErrBatchImageIndexOutputMissing.WithCause(err)
	}
	// The result stream belongs to this indexing attempt; close it after every
	// parsing outcome before any later queue visit reopens provider output.
	defer func() { _ = r.Close() }()

	scanner := bufio.NewScanner(r)
	// Inline image data can enlarge one JSONL record; the sixteen-MiB scan cap
	// rejects oversized lines before partial metadata replacement.
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	seen := make(map[string]int)
	unknownCount := 0
	var items []CreateBatchImageItemParams
	result := &BatchImageIndexResult{}
	lineNumber := 0
	now := i.now()
	sourceObject := BatchImageDerefString(job.ProviderOutputRef)
	// Some provider results lack an output reference; retain the upstream job
	// name as source metadata so indexed failures can still identify their
	// provider origin.
	if sourceObject == "" {
		sourceObject = BatchImageDerefString(job.ProviderJobName)
	}

	for scanner.Scan() {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parsed, err := ParseBatchImageResultLine([]byte(line), lineNumber)
		if err != nil {
			return nil, err
		}
		// Reconcile against submitted custom IDs. Extra provider rows would make
		// success+fail exceed item_count and cause settlement validation to keep failing.
		if len(expected) > 0 {
			if _, ok := expected[parsed.CustomID]; !ok {
				unknownCount++
				continue
			}
		}
		// Repeated accepted identities leave the row-to-request association ambiguous.
		// Reject the full scan before any replacement can publish conflicting counts.
		if firstLine, ok := seen[parsed.CustomID]; ok {
			return nil, ErrBatchImageDuplicateCustomID.WithCause(fmt.Errorf("custom id %q duplicated at lines %d and %d", parsed.CustomID, firstLine, lineNumber))
		}
		seen[parsed.CustomID] = lineNumber

		lineNo := parsed.SourceLineNumber
		item := CreateBatchImageItemParams{
			JobID:                job.BatchID,
			CustomID:             parsed.CustomID,
			Status:               BatchImageItemStatusFailed,
			ProviderSourceObject: BatchImageOptionalStringPtr(sourceObject),
			SourceLineNumber:     &lineNo,
			ImageCount:           parsed.ImageCount,
			IndexedAt:            &now,
		}
		if parsed.Status == BatchImageParsedStatusSucceeded {
			item.Status = BatchImageItemStatusSuccess
			item.MimeType = BatchImageOptionalStringPtr(parsed.MimeType)
			item.FileExtension = BatchImageOptionalStringPtr(parsed.FileExtension)
			result.SuccessCount++
		} else {
			item.ErrorCode = BatchImageOptionalStringPtr(parsed.ErrorCode)
			item.ErrorMessage = BatchImageOptionalStringPtr(parsed.ErrorMessage)
			result.FailCount++
		}
		items = append(items, item)
		result.TotalCount++
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, ErrBatchImageIndexParseFailed.WithCause(err)
		}
		return nil, err
	}
	// Record submitted items missing from provider output as failures so users can see
	// the missing results while billing counts successful custom IDs independently.
	missingCount := 0
	if len(expected) > 0 {
		missingIDs := make([]string, 0)
		for customID := range expected {
			if _, ok := seen[customID]; !ok {
				missingIDs = append(missingIDs, customID)
			}
		}
		sort.Strings(missingIDs)
		for _, customID := range missingIDs {
			items = append(items, CreateBatchImageItemParams{
				JobID:                job.BatchID,
				CustomID:             customID,
				Status:               BatchImageItemStatusFailed,
				ProviderSourceObject: BatchImageOptionalStringPtr(sourceObject),
				ErrorCode:            BatchImageStringPtr("PROVIDER_RESULT_MISSING"),
				ErrorMessage:         BatchImageStringPtr("platform output did not include a result for this item"),
				IndexedAt:            &now,
			})
			result.FailCount++
			result.TotalCount++
		}
		missingCount = len(missingIDs)
	}
	if result.TotalCount == 0 {
		return nil, ErrBatchImageIndexNoResultLines
	}
	if unknownCount > 0 || missingCount > 0 {
		i.warn("batch_image.index_reconciled",
			"batch_id", job.BatchID,
			"unknown_custom_ids", unknownCount,
			"missing_custom_ids", missingCount,
		)
		// Reconciliation metadata remains usable when its audit write fails. Observe
		// that failure and continue to the guarded result replacement below.
		if err := i.Repo.AppendBatchImageEvent(ctx, job.BatchID, "index_reconciled", map[string]any{
			"batch_id":           job.BatchID,
			"unknown_custom_ids": unknownCount,
			"missing_custom_ids": missingCount,
		}); err != nil {
			i.warn("batch_image.index_reconcile_event_failed",
				"batch_id", job.BatchID,
				"error", err,
			)
		}
	}
	// Publish items and counts after the entire scan and custom-ID
	// reconciliation succeed; the repository checks indexing state before
	// replacing persisted outcomes.
	if err := i.Repo.ReplaceBatchImageItemsForJob(ctx, job.BatchID, items, BatchImageCounts{
		SuccessCount: result.SuccessCount,
		FailCount:    result.FailCount,
	}); err != nil {
		return nil, err
	}
	return result, nil
}

// ListExpectedCustomIDs collects every stored custom ID across pages of five
// hundred items. Submission placeholders or earlier rebuilt rows establish
// the set; a repository error aborts reconciliation before provider output
// can replace those results.
func (i *ResultIndexer) ListExpectedCustomIDs(ctx context.Context, batchID string) (map[string]struct{}, error) {
	// Use the repository's supported maximum item page to collect every expected
	// custom ID before reconciliation.
	const pageSize = 500
	expected := make(map[string]struct{})
	offset := 0
	for {
		page, err := i.Repo.ListBatchImageItems(ctx, batchID, BatchImageItemFilter{Limit: pageSize, Offset: offset})
		if err != nil {
			return nil, err
		}
		for _, item := range page {
			if item != nil {
				expected[item.CustomID] = struct{}{}
			}
		}
		if len(page) < pageSize {
			return expected, nil
		}
		offset += len(page)
	}
}

// ParseBatchImageResultLine reads one provider JSON object and returns
// metadata for its custom ID and physical line number. Identity is chosen
// from key, custom_id, customId or request.key in that order. Invalid JSON
// and absent identity return the indexing parse category.
//
// Images in response.candidates take precedence when present; top-level
// candidates are inspected when that nested shape yields none. Any accepted
// image marks the custom ID successful, even when provider error fields also
// exist. Otherwise status precedes error diagnostics, and response-shaped
// text-only output becomes EMPTY_IMAGE_OUTPUT. The metadata keeps counts and
// MIME information while payload bytes remain in the provider result.
func ParseBatchImageResultLine(line []byte, lineNumber int) (*ParsedBatchImageResult, error) {
	var obj map[string]any
	if err := json.Unmarshal(line, &obj); err != nil {
		return nil, ErrBatchImageIndexParseFailed.WithCause(fmt.Errorf("line %d: %w", lineNumber, err))
	}

	customID := BatchImageFirstNonEmptyString(
		BatchImageMapString(obj, "key"),
		BatchImageMapString(obj, "custom_id"),
		BatchImageMapString(obj, "customId"),
		BatchImageNestedString(obj, "request", "key"),
	)
	if customID == "" {
		return nil, ErrBatchImageIndexParseFailed.WithCause(fmt.Errorf("line %d: missing custom id", lineNumber))
	}

	parsed := &ParsedBatchImageResult{
		CustomID:         customID,
		SourceLineNumber: lineNumber,
	}
	// A row can carry images alongside diagnostic fields. Nonempty encoded parts
	// establish custom-ID success before failure envelopes are considered for billing.
	imageCount, mimeType := BatchImageFindImageParts(obj)
	if imageCount > 0 {
		parsed.Status = BatchImageParsedStatusSucceeded
		parsed.ImageCount = imageCount
		parsed.MimeType = mimeType
		parsed.FileExtension = BatchImageFileExtension(mimeType)
		return parsed, nil
	}

	if code, message, ok := BatchImageFailureFromProviderFields(obj); ok {
		parsed.Status = BatchImageParsedStatusFailed
		parsed.ErrorCode = code
		parsed.ErrorMessage = TruncateBatchImageMessage(message, BatchImageMaxErrorMessageLength)
		return parsed, nil
	}

	if _, hasResponse := obj["response"]; hasResponse || BatchImageHasCandidates(obj) {
		parsed.Status = BatchImageParsedStatusFailed
		parsed.ErrorCode = "EMPTY_IMAGE_OUTPUT"
		parsed.ErrorMessage = "platform response contained no image output"
		return parsed, nil
	}

	parsed.Status = BatchImageParsedStatusFailed
	parsed.ErrorCode = "PROVIDER_ITEM_FAILED"
	parsed.ErrorMessage = "platform result line contained no image output"
	return parsed, nil
}

// BatchImageFindImageParts counts accepted images in response.candidates
// first and returns its first MIME type. When that shape contains no images
// it falls back to top-level candidates, establishing indexing precedence for
// mixed envelopes.
func BatchImageFindImageParts(obj map[string]any) (int, string) {
	count, mimeType := BatchImageFindImagePartsInCandidates(BatchImageNestedAny(obj, "response", "candidates"))
	if count > 0 {
		return count, mimeType
	}
	// A response envelope with no eligible images can accompany top-level
	// candidates. Try that representation so such rows retain their delivered output.
	return BatchImageFindImagePartsInCandidates(obj["candidates"])
}

// BatchImageFindImagePartsInCandidates counts nonempty inline image parts and
// returns the first accepted MIME spelling. Candidate and part shapes are
// screened by visitBatchImageInlineImages; encoded data is counted before
// download decoding validates it.
func BatchImageFindImagePartsInCandidates(raw any) (int, string) {
	count := 0
	firstMime := ""
	visitBatchImageInlineImages(raw, func(mime, _ string) {
		count++
		if firstMime == "" {
			firstMime = mime
		}
	})
	return count, firstMime
}

// visitBatchImageInlineImages visits nonempty image payloads inside candidate
// content parts in their stored order. Malformed candidate or part shapes are
// skipped. The caller supplies a callback that consumes each trimmed MIME and
// encoded data value.
//
// Camel-case inlineData is selected before inline_data when either is a map,
// including an empty first map. MIME text similarly prefers a nonblank
// mimeType before mime_type. A case-insensitive image/ prefix and nonblank
// data make a part eligible; base64 validity is deferred to the download
// consumer. This shared traversal lets indexing count metadata while
// downloads retain encoded values.
func visitBatchImageInlineImages(raw any, visit func(mime, data string)) {
	candidates, ok := raw.([]any)
	if !ok {
		return
	}
	for _, candidateRaw := range candidates {
		candidate, ok := candidateRaw.(map[string]any)
		if !ok {
			continue
		}
		partsRaw := BatchImageNestedAny(candidate, "content", "parts")
		parts, ok := partsRaw.([]any)
		if !ok {
			continue
		}
		for _, partRaw := range parts {
			part, ok := partRaw.(map[string]any)
			if !ok {
				continue
			}
			inline, ok := FirstMap(part["inlineData"], part["inline_data"])
			if !ok {
				continue
			}
			data := strings.TrimSpace(BatchImageMapString(inline, "data"))
			mime := strings.TrimSpace(BatchImageFirstNonEmptyString(BatchImageMapString(inline, "mimeType"), BatchImageMapString(inline, "mime_type")))
			if data == "" || !strings.HasPrefix(strings.ToLower(mime), "image/") {
				continue
			}
			visit(mime, data)
		}
	}
}

// BatchImageFailureFromProviderFields reads status diagnostics before error
// diagnostics and normalizes their category. A present map reports a failure
// even with blank fields; absent maps return false for the parser's
// empty-output handling.
func BatchImageFailureFromProviderFields(obj map[string]any) (string, string, bool) {
	// One present diagnostic envelope supplies the whole failure. Even a blank
	// status map takes precedence so fields from competing envelopes are not merged.
	if status, ok := obj["status"].(map[string]any); ok {
		message := BatchImageFirstNonEmptyString(BatchImageMapString(status, "message"), BatchImageMapString(status, "details"))
		code := BatchImageFirstNonEmptyString(BatchImageMapString(status, "code"), BatchImageMapString(status, "status"))
		return BatchImageMapFailureCode(code, message), message, true
	}
	if errObj, ok := obj["error"].(map[string]any); ok {
		message := BatchImageFirstNonEmptyString(BatchImageMapString(errObj, "message"), BatchImageMapString(errObj, "details"))
		code := BatchImageFirstNonEmptyString(BatchImageMapString(errObj, "code"), BatchImageMapString(errObj, "status"))
		return BatchImageMapFailureCode(code, message), message, true
	}
	return "", "", false
}

// BatchImageMapFailureCode classifies provider code and message text into a
// public item category. Both values participate after trimming and lowercasing,
// so a code naming a rate limit and a message naming safety can match different
// groups at once.
//
// Safety keywords win first, invalid-argument keywords second, and rate-limit
// keywords third. Unmatched text receives PROVIDER_ITEM_FAILED. This ordering
// determines the single category shown in item listings when a provider's
// diagnostics describe more than one condition.
func BatchImageMapFailureCode(code, message string) string {
	text := strings.ToLower(strings.TrimSpace(code + " " + message))
	switch {
	case strings.Contains(text, "safety"), strings.Contains(text, "policy"), strings.Contains(text, "blocked"), strings.Contains(text, "prohibited"):
		return "SAFETY_BLOCKED"
	case strings.Contains(text, "invalid_argument"), strings.Contains(text, "invalid argument"), strings.Contains(text, "bad request"):
		return "INVALID_ARGUMENT"
	case strings.Contains(text, "quota"), strings.Contains(text, "rate"), strings.Contains(text, "resource_exhausted"), strings.Contains(text, "too many requests"):
		return "PROVIDER_RATE_LIMITED"
	default:
		return "PROVIDER_ITEM_FAILED"
	}
}

// BatchImageFileExtension returns the supported filename suffix for a
// trimmed, case-normalized image MIME type. JPEG aliases produce jpg, PNG and
// WebP retain their extensions, and unknown types return empty text.
func BatchImageFileExtension(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png":
		return "png"
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/webp":
		return "webp"
	default:
		return ""
	}
}

// BatchImageHasCandidates recognizes the presence of a top-level candidates
// key or a nested candidate slice. An empty or malformed present top-level
// value still distinguishes response-shaped output for the parser's failure
// category.
func BatchImageHasCandidates(obj map[string]any) bool {
	if _, ok := obj["candidates"]; ok {
		return true
	}
	_, ok := BatchImageNestedAny(obj, "response", "candidates").([]any)
	return ok
}

// BatchImageMapString returns trimmed string fields or decimal text for JSON
// numeric values. Float64 values are converted to integers before formatting,
// and unsupported or absent values produce empty text for parser selection.
func BatchImageMapString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	switch v := m[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	case float64:
		// Provider numeric diagnostic codes are rendered as decimal integers,
		// truncating any fractional value decoded into float64.
		return strconv.FormatInt(int64(v), 10)
	default:
		return ""
	}
}

// BatchImageNestedString reads a trimmed string at the supplied nested map
// keys. A missing path or value of another type returns empty text, letting
// provider identity and diagnostic selection try another field.
func BatchImageNestedString(m map[string]any, keys ...string) string {
	if nested, ok := BatchImageNestedAny(m, keys...).(string); ok {
		return strings.TrimSpace(nested)
	}
	return ""
}

// BatchImageNestedAny follows the supplied keys through map[string]any values
// and returns the reached value. An absent path or incompatible intermediate
// shape returns nil, while an empty key sequence returns the original map.
func BatchImageNestedAny(m map[string]any, keys ...string) any {
	var current any = m
	for _, key := range keys {
		cm, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = cm[key]
	}
	return current
}

// FirstMap returns the first candidate whose dynamic value is map[string]any,
// including an empty or nil map. This shape precedence selects inlineData
// before inline_data even when the first accepted map contains no image data.
func FirstMap(values ...any) (map[string]any, bool) {
	// Selecting one envelope keeps its spelling authoritative. An empty camel-case
	// map therefore cannot be filled from a competing snake-case image payload.
	for _, value := range values {
		if m, ok := value.(map[string]any); ok {
			return m, true
		}
	}
	return nil, false
}

// BatchImageFirstNonEmptyString returns the first trimmed value containing
// text. Whitespace-only values are skipped, so custom ID and MIME
// compatibility fields can supply later alternatives when earlier spellings
// are empty.
func BatchImageFirstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// warn sends an indexing diagnostic through the configured observation
// callback.
func (i *ResultIndexer) warn(event string, values ...any) {
	if i.Observe != nil {
		i.Observe(event, values...)
	}
}

// now reads the indexer clock when a callback was injected at construction.
// Otherwise it uses time.Now, producing one indexing timestamp for the result
// metadata collected during this replacement attempt.
func (i *ResultIndexer) now() time.Time {
	if i.Now != nil {
		return i.Now()
	}
	return time.Now()
}
