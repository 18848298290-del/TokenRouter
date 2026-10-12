package creative

import (
	"context"
	"errors"
	"strings"
	"time"
)

type Queries struct {
	Now            func() time.Time
	Repo           CreativeRunRepository
	TransientStore CreativeTransientStore
	Enabled        func(context.Context) bool
	Observe        func(string, ...any)
}

// CreativeOutputContent contains image bytes and their response MIME type.
type CreativeOutputContent struct {
	Content     []byte
	ContentType string
}

func (s *Public) queries() *Queries {
	return &Queries{Now: s.Now, Repo: s.Repo, TransientStore: s.TransientStore, Enabled: s.Enabled, Observe: s.Observe}
}

func (s *Public) GetRun(ctx context.Context, scope CreativeRunScope, id string) (*CreativeRunPublic, error) {
	return s.queries().GetRun(ctx, scope, id)
}

func (s *Public) ListRuns(ctx context.Context, scope CreativeRunScope, filter CreativeRunFilter) (*CreativeListRunsResponse, error) {
	return s.queries().ListRuns(ctx, scope, filter)
}

func (s *Public) GetOutputContent(ctx context.Context, scope CreativeRunScope, id string, index int) (*CreativeOutputContent, error) {
	return s.queries().GetOutputContent(ctx, scope, id, index)
}

func (s *Public) AckOutput(ctx context.Context, scope CreativeRunScope, id string, index int) error {
	return s.queries().AckOutput(ctx, scope, id, index)
}

func (s *Queries) warn(event string, values ...any) {
	if s.Observe != nil {
		s.Observe(event, values...)
	}
}

// GetRun returns task and output metadata after checking user and workspace ownership.
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

// ListRuns returns the user's workspace tasks ordered by descending creation time.
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
		// Output metadata lets the frontend associate history entries with local media or missing-media
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

// GetOutputContent checks ownership and output status before reading temporary image bytes.
// Expired or missing output downgrades a succeeded task to result_lost and returns an error.
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
		// Expired, missing, or corrupt output downgrades a succeeded task to result_lost.
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

// MarkRunResultLost downgrades a succeeded task to result_lost and returns database errors for retry.
func (s *Queries) MarkRunResultLost(ctx context.Context, run *CreativeRun) error {
	if run == nil || run.Status != CreativeRunStatusSucceeded {
		return nil
	}
	return s.Repo.TransitionCreativeRunStatus(ctx, run.RunID, CreativeRunStatusResultLost, CreativeRunTransitionOptions{
		ErrorCode:    CreativeStringPtr("RESULT_EXPIRED"),
		ErrorMessage: CreativeStringPtr("transient output expired before client acknowledgment"),
	})
}

// AckOutput records client acknowledgment before deleting temporary output; repeated calls are safe.
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
		// Repeated acknowledgments succeed and retry any outstanding deletion.
		if s.TransientStore != nil {
			_ = s.TransientStore.DeleteOutput(ctx, runID, outputIndex)
		}
		return nil
	}
	if output.Status != CreativeRunOutputStatusSucceeded {
		return ErrCreativeOutputNotReady
	}
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
			// The acknowledgment is durable; transient cleanup retries failed deletion.
		}
	}
	return nil
}

// now returns the injected clock's time or the system time when no clock is configured.
func (s *Queries) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
