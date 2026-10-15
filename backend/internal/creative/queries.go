package creative

import (
	"context"
	"errors"
	"strings"
	"time"
)

// queries.go reads creative tasks and delivers or acknowledges their outputs
// within the caller's workspace.
// Creative task queries combine authenticated ownership with the browser
// workspace UUID before reading public metadata or image bytes. The feature
// callback gates these operations independently from worker recovery, and
// every task lookup uses the supplied user/workspace scope before output
// metadata is accessed. History includes output metadata so the client can
// match each row to its locally saved media.
//
// Content retrieval requires a terminal task and a succeeded output whose
// metadata lifetime remains valid. Confirmed expiry or non-outage read failure
// can downgrade a succeeded task to result_lost. Acknowledgment instead
// persists receipt before deleting transient bytes, allowing cleanup to retry
// deletion while the database already records that the client received the
// output.

// Queries serves scoped creative metadata and temporary output retrieval for
// authenticated users.
type Queries struct {
	// Now supplies the query time for expiry and receipt checks. A nil callback
	// uses system time, and callers keep its value stable while requests run.
	Now            func() time.Time       // optional query clock callback
	Repo           CreativeRunRepository  // task and output metadata reader
	TransientStore CreativeTransientStore // temporary image byte storage
	// Enabled is evaluated for each query after scope normalization. A nil
	// callback or false result returns ErrCreativeDisabled before resource lookup.
	Enabled func(context.Context) bool // required public feature gate callback
	// Observe receives deletion failures after durable receipt succeeds. A nil
	// callback leaves those diagnostics unreported while acknowledgment
	// still completes.
	Observe func(string, ...any) // optional query diagnostic callback
}

// CreativeOutputContent contains image bytes and their response MIME type.
type CreativeOutputContent struct {
	// Content retains the bytes returned by transient storage. Copy that storage
	// before modifying it when the bound store shares data with another consumer.
	Content     []byte // transient image response bytes
	ContentType string // selected output response MIME
}

// queries creates a query facade sharing Public stores, clock, and callbacks.
// Enabled retains the Public receiver method, so each operation evaluates its
// current feature gate rather than a copied enable result.
func (s *Public) queries() *Queries {
	return &Queries{Now: s.Now, Repo: s.Repo, TransientStore: s.TransientStore, Enabled: s.Enabled, Observe: s.Observe}
}

// GetRun delegates task details through the Public query facade using the
// supplied scope. Scope normalization, feature gating, and owner/workspace
// lookup occur before public output metadata is returned.
func (s *Public) GetRun(ctx context.Context, scope CreativeRunScope, id string) (*CreativeRunPublic, error) {
	return s.queries().GetRun(ctx, scope, id)
}

// ListRuns delegates scoped task history and output metadata through the
// Public query facade. Filter limits and offsets follow Queries normalization,
// and HasMore reports whether the returned page filled its limit.
func (s *Public) ListRuns(ctx context.Context, scope CreativeRunScope, filter CreativeRunFilter) (*CreativeListRunsResponse, error) {
	return s.queries().ListRuns(ctx, scope, filter)
}

// GetOutputContent delegates temporary image retrieval through the scoped
// Public query facade. The query applies terminal/output state checks and can
// mark a succeeded run lost after expiry or missing bytes.
func (s *Public) GetOutputContent(ctx context.Context, scope CreativeRunScope, id string, index int) (*CreativeOutputContent, error) {
	return s.queries().GetOutputContent(ctx, scope, id, index)
}

// AckOutput delegates scoped client receipt through the Public query facade.
// The query writes acknowledgment before deleting temporary bytes and treats
// an already acknowledged output as a successful repeat.
func (s *Public) AckOutput(ctx context.Context, scope CreativeRunScope, id string, index int) error {
	return s.queries().AckOutput(ctx, scope, id, index)
}

// warn sends a query event and attributes to Observe synchronously. A nil
// callback drops the diagnostic; supplied values pass through unchanged when
// reporting failed deletion after acknowledgment.
func (s *Queries) warn(event string, values ...any) {
	if s.Observe != nil {
		s.Observe(event, values...)
	}
}

// GetRun returns run and output metadata for the normalized user/workspace
// scope. A missing or false Enabled callback rejects the query; owner lookup
// errors stop output access before conversion to public metadata.
func (s *Queries) GetRun(ctx context.Context, scope CreativeRunScope, runID string) (*CreativeRunPublic, error) {
	normalizedScope, err := NormalizeCreativeRunScope(scope)
	if err != nil {
		return nil, err
	}
	scope = normalizedScope
	if s.Enabled == nil || !s.Enabled(ctx) {
		return nil, ErrCreativeDisabled
	}
	run, err := s.Repo.GetCreativeRunByRunIDForOwner(ctx, scope, runID)
	if err != nil {
		return nil, err
	}
	outputs, err := s.Repo.ListCreativeRunOutputs(ctx, run.RunID)
	if err != nil {
		return nil, err
	}
	return CreativeRunToPublic(run, outputs), nil
}

// ListRuns returns normalized-scope history with associated output metadata
// and a possible-next-page flag. Invalid limits use twenty, negative offsets
// become zero, and a missing or false feature gate rejects the query.
//
// A repository batch reader replaces individual output lookups for the whole
// page. Missing keys in its returned map supply an empty output list rather
// than triggering individual reads. Without that optional reader, every run
// output list is loaded separately. HasMore is true when the page fills the
// chosen limit, so it does not prove that another row exists.
func (s *Queries) ListRuns(ctx context.Context, scope CreativeRunScope, filter CreativeRunFilter) (*CreativeListRunsResponse, error) {
	normalizedScope, err := NormalizeCreativeRunScope(scope)
	if err != nil {
		return nil, err
	}
	scope = normalizedScope
	if s.Enabled == nil || !s.Enabled(ctx) {
		return nil, ErrCreativeDisabled
	}
	if filter.Limit <= 0 || filter.Limit > 100 {
		filter.Limit = 20
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	runs, err := s.Repo.ListCreativeRunsForOwner(ctx, scope, filter)
	if err != nil {
		return nil, err
	}
	data := make([]*CreativeRunPublic, 0, len(runs))
	outputByRun := make(map[string][]*CreativeRunOutput, len(runs))
	batchOutputs := false
	// The optional batch reader supplies output metadata for the complete page.
	// Use that result even for absent run keys, avoiding individual queries that
	// could observe different metadata during this history response.
	if batchReader, ok := s.Repo.(CreativeRunOutputBatchReader); ok {
		batchOutputs = true
		runIDs := make([]string, 0, len(runs))
		for _, run := range runs {
			if run != nil {
				runIDs = append(runIDs, run.RunID)
			}
		}
		outputByRun, err = batchReader.ListCreativeRunOutputsForRuns(ctx, runIDs)
		if err != nil {
			return nil, err
		}
	}
	for _, run := range runs {
		// Output metadata lets the frontend associate history entries
		// with local media or missing-media
		// placeholders.
		outputs := outputByRun[run.RunID]
		if !batchOutputs {
			outputs, err = s.Repo.ListCreativeRunOutputs(ctx, run.RunID)
			if err != nil {
				return nil, err
			}
		}
		data = append(data, CreativeRunToPublic(run, outputs))
	}
	return &CreativeListRunsResponse{Data: data, HasMore: len(data) == filter.Limit}, nil
}

// GetOutputContent returns image bytes after scope ownership, terminal task,
// and succeeded output checks. Acknowledged, lost, failed, and pending output
// states produce their respective unavailable results before storage is read.
//
// An elapsed metadata deadline marks a succeeded run result_lost before
// returning expiry. A transient-unavailable error returns a service failure
// without that downgrade; other read errors request the downgrade and return
// result loss. Other terminal task states remain unchanged by
// MarkRunResultLost. Content uses the trimmed stored MIME when present,
// otherwise application/octet-stream, and retains the bytes returned by
// transient storage.
func (s *Queries) GetOutputContent(ctx context.Context, scope CreativeRunScope, runID string, outputIndex int) (*CreativeOutputContent, error) {
	normalizedScope, err := NormalizeCreativeRunScope(scope)
	if err != nil {
		return nil, err
	}
	scope = normalizedScope
	if s.Enabled == nil || !s.Enabled(ctx) {
		return nil, ErrCreativeDisabled
	}
	run, err := s.Repo.GetCreativeRunByRunIDForOwner(ctx, scope, runID)
	if err != nil {
		return nil, err
	}
	if !IsTerminalCreativeRunStatus(run.Status) {
		return nil, ErrCreativeOutputNotReady
	}
	output, err := s.Repo.GetCreativeRunOutput(ctx, runID, outputIndex)
	if err != nil {
		return nil, err
	}
	switch output.Status {
	case CreativeRunOutputStatusSucceeded:
	case CreativeRunOutputStatusAcked:
		return nil, ErrCreativeOutputExpired
	case CreativeRunOutputStatusLost:
		return nil, ErrCreativeResultLost
	case CreativeRunOutputStatusFailed:
		return nil, ErrCreativeOutputNotReady
	default:
		return nil, ErrCreativeOutputNotReady
	}
	now := s.now()
	if output.TransientExpiresAt != nil && now.After(*output.TransientExpiresAt) {
		if err := s.MarkRunResultLost(ctx, run); err != nil {
			return nil, err
		}
		return nil, ErrCreativeOutputExpired
	}
	if s.TransientStore == nil {
		return nil, ErrCreativeTransientFailed
	}
	data, err := s.TransientStore.LoadOutput(ctx, runID, outputIndex)
	if err != nil {
		if errors.Is(err, ErrCreativeTransientUnavailable) {
			return nil, ErrCreativeTransientFailed
		}
		// Availability errors above keep the task status. Other failed reads mark
		// only succeeded runs lost, so an unreadable temporary result
		// cannot remain
		// advertised as deliverable success in later history responses.
		if markErr := s.MarkRunResultLost(ctx, run); markErr != nil {
			return nil, markErr
		}
		return nil, ErrCreativeResultLost
	}
	contentType := "application/octet-stream"
	if output.MimeType != nil && strings.TrimSpace(*output.MimeType) != "" {
		contentType = strings.TrimSpace(*output.MimeType)
	}
	return &CreativeOutputContent{Content: data, ContentType: contentType}, nil
}

// MarkRunResultLost transitions a supplied succeeded task to result_lost with
// RESULT_EXPIRED details. Nil or other-status tasks succeed unchanged, and
// repository transition errors remain available for the query caller to retry.
func (s *Queries) MarkRunResultLost(ctx context.Context, run *CreativeRun) error {
	if run == nil || run.Status != CreativeRunStatusSucceeded {
		return nil
	}
	return s.Repo.TransitionCreativeRunStatus(ctx, run.RunID, CreativeRunStatusResultLost, CreativeRunTransitionOptions{
		ErrorCode:    CreativeStringPtr("RESULT_EXPIRED"),
		ErrorMessage: CreativeStringPtr("transient output expired before client acknowledgment"),
	})
}

// AckOutput records client receipt before requesting deletion of the scoped
// temporary image. It requires normalized ownership and a terminal task, with
// a succeeded or already acknowledged output.
//
// New acknowledgments persist in the repository before transient deletion.
// Deletion failures are logged while the durable receipt still succeeds, and
// cleanup can delete remaining bytes later. Repeated acknowledgments retry
// deletion when storage is configured and ignore that deletion error. The
// acknowledgment does not check the metadata expiry timestamp or load the
// image bytes.
func (s *Queries) AckOutput(ctx context.Context, scope CreativeRunScope, runID string, outputIndex int) error {
	normalizedScope, err := NormalizeCreativeRunScope(scope)
	if err != nil {
		return err
	}
	scope = normalizedScope
	if s.Enabled == nil || !s.Enabled(ctx) {
		return ErrCreativeDisabled
	}
	run, err := s.Repo.GetCreativeRunByRunIDForOwner(ctx, scope, runID)
	if err != nil {
		return err
	}
	if !IsTerminalCreativeRunStatus(run.Status) {
		return ErrCreativeOutputNotReady
	}
	output, err := s.Repo.GetCreativeRunOutput(ctx, runID, outputIndex)
	if err != nil {
		return err
	}
	if output.Status == CreativeRunOutputStatusAcked {
		// The database already records client receipt, so another
		// acknowledgment can
		// retry leftover byte deletion without changing its success
		// result if that
		// transient cleanup still fails.
		if s.TransientStore != nil {
			_ = s.TransientStore.DeleteOutput(ctx, runID, outputIndex)
		}
		return nil
	}
	if output.Status != CreativeRunOutputStatusSucceeded {
		return ErrCreativeOutputNotReady
	}
	// Persist client receipt before deleting bytes. A failed metadata write leaves
	// the image available, while a later deletion failure can be retried using
	// the acknowledged row or terminal transient cleanup.
	if err := s.Repo.MarkCreativeRunOutputAcked(ctx, runID, outputIndex, s.now()); err != nil {
		return err
	}
	if s.TransientStore != nil {
		if err := s.TransientStore.DeleteOutput(ctx, runID, outputIndex); err != nil {
			s.warn("creative.ack_delete_output_failed",
				"run_id", runID,
				"output_index", outputIndex,
				"error", err,
			)
			// Terminal cleanup will retry remaining bytes using the
			// durable run metadata,
			// so returning success here reflects recorded receipt
			// rather than successful
			// completion of this particular Redis deletion.
		}
	}
	return nil
}

// now returns the optional query clock value or the current system time.
// Callers configure Now before concurrent query use and leave it stable while
// requests are running.
func (s *Queries) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
