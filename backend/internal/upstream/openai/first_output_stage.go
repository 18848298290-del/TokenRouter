package openai

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync/atomic"
)

// An attempt can receive upstream events before downstream output is safe to
// commit. First-output staging bounds that uncommitted data separately from the
// post-commit scanner budget, and guarded streams use a smaller event queue so
// a fast producer cannot build a large backlog.
//
// Small staged bodies remain in memory; a larger write can move existing bytes
// to a temporary descriptor. On Unix the path is unlinked before request data
// is written, allowing descriptor access while reducing named plaintext left
// after a crash. A failed unlink selects memory for future writes and retains
// cleanup evidence for Close. Commit copies buffered bytes before closing
// storage, so cleanup failure after delivery is recorded instead of turning
// already-delivered output into an upstream failure.

// Staging constants bound retained pre-output bytes, scanner framing overhead,
// and event queues so guarded attempts cannot accumulate an unbounded producer backlog.
const (
	OpenAIFirstOutputStageMemoryLimit        = 64 * 1024
	OpenAIFirstOutputStageMaxBytes           = 8 * 1024 * 1024
	OpenAIFirstOutputScannerFramingAllowance = 64
	OpenAIFirstOutputGuardQueueSize          = 1
	OpenAIDefaultStreamQueueSize             = 16
)

// Staging sentinel errors identify byte-buffer overflow and pre-output line overflow
// for response readers deciding whether an uncommitted attempt can be abandoned.
var (
	ErrOpenAIFirstOutputStageLimit   = errors.New("openai first-output staging limit exceeded")
	ErrOpenAIFirstOutputScannerLimit = errors.New("openai pre-output scanner token limit exceeded")
)

// OpenAIFirstOutputStage owns bounded pre-commit bytes for one serialized
// attempt.
type OpenAIFirstOutputStage struct {
	// size records bytes in memory or tempFile under the same limit. Callers
	// serialize writes, commit, and cleanup so storage movement and accounting
	// remain consistent throughout the stage lifetime.
	limit    int64        // staged content byte budget
	size     int64        // accepted staged byte count
	memory   bytes.Buffer // in-memory staged response bytes
	tempFile *os.File     // owned unlinked spool descriptor
	// tempPath retains the temporary name until removal succeeds or reports absent;
	// failed unlink cleanup can therefore be retried by Close.
	tempPath   string
	createTemp func() (*os.File, error) // temporary spool creation callback
	removeFile func(string) error       // temporary path removal callback
	// memoryOnly selects buffer storage on Windows or after unlink failure; future
	// writes then avoid creating another spool in this stage.
	memoryOnly bool
	// cleanupErr stores deferred cleanup evidence, including failures after
	// successful delivery; Close drains it into the next returned cleanup error.
	cleanupErr error
	closed     bool // ended staging lifetime flag
}

// NewOpenAIFirstOutputStage creates an empty stage with a positive byte budget.
// Nonpositive limits become one byte, Windows selects memory storage, and
// callers serialize access and close the stage on every exit.
func NewOpenAIFirstOutputStage(limit int64) *OpenAIFirstOutputStage {
	if limit < 1 {
		limit = 1
	}
	return &OpenAIFirstOutputStage{
		limit:      limit,
		createTemp: func() (*os.File, error) { return os.CreateTemp("", "tokenrouter-openai-first-output-*") },
		removeFile: os.Remove,
		memoryOnly: runtime.GOOS == "windows",
	}
}

// NewDefaultOpenAIFirstOutputStage creates staging with the standard eight-MiB
// byte budget. Callers own the stage, serialize writes and commit, and close it
// when the attempt completes or is abandoned.
func NewDefaultOpenAIFirstOutputStage() *OpenAIFirstOutputStage {
	return NewOpenAIFirstOutputStage(OpenAIFirstOutputStageMaxBytes)
}

// OpenAIFirstOutputEventQueueSize selects the queue capacity for guarded or
// ordinary streams. Guarded attempts use one slot to apply producer
// backpressure while output remains undecided; ordinary streams use the
// standard capacity.
func OpenAIFirstOutputEventQueueSize(guardFirstOutput bool) int {
	if guardFirstOutput {
		return OpenAIFirstOutputGuardQueueSize
	}
	return OpenAIDefaultStreamQueueSize
}

// OpenAIFirstOutputDynamicScanLines returns a ScanLines splitter with a
// pre-output token budget. A nil or false guardActive value delegates line
// splitting unchanged. Callers update the atomic flag when the attempt leaves
// guarded staging.
//
// While active, the splitter rejects complete oversized lines and undelimited
// data that has reached the guarded limit, before Scanner grows toward its
// larger maximum. The framing allowance keeps line framing separate from the
// staged-byte cap. Deactivating the guard permits later response lines under
// the owning Scanner budget without rebuilding the splitter.
func OpenAIFirstOutputDynamicScanLines(guardActive *atomic.Bool) bufio.SplitFunc {
	return func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		advance, token, err = bufio.ScanLines(data, atEOF)
		if err != nil || guardActive == nil || !guardActive.Load() {
			return advance, token, err
		}
		limit := OpenAIFirstOutputStageMaxBytes + OpenAIFirstOutputScannerFramingAllowance
		if token != nil {
			// Bound complete tokens before the owning Scanner can accept them.
			if len(token) > limit {
				return 0, nil, ErrOpenAIFirstOutputScannerLimit
			}
			return advance, token, nil
		}
		// With no delimiter at the budget limit, further scanner growth would exceed
		// the guarded token budget. Fail before Scanner grows toward MaxLineSize.
		if len(data) >= limit {
			return 0, nil, ErrOpenAIFirstOutputScannerLimit
		}
		return advance, token, nil
	}
}

// Buffered returns the currently staged byte count or zero for nil stages.
// Close resets that count, so callers read it before cleanup when diagnosing a
// staging limit failure.
func (s *OpenAIFirstOutputStage) Buffered() int64 {
	if s == nil {
		return 0
	}
	return s.size
}

// WriteString appends value to the active stage and returns bytes written.
// Budget overflow rejects the complete write before storage changes; storage
// failures can report partial progress, and callers serialize use with commit
// and cleanup.
func (s *OpenAIFirstOutputStage) WriteString(value string) (int, error) {
	if err := s.prepareWrite(len(value)); err != nil {
		return 0, err
	}
	var n int
	var err error
	if s.tempFile == nil {
		n, err = s.memory.WriteString(value)
	} else {
		n, err = io.WriteString(s.tempFile, value)
	}
	s.size += int64(n)
	if err != nil {
		return n, fmt.Errorf("write first-output stage: %w", err)
	}
	return n, nil
}

// Write appends p to the active stage and returns bytes written. Budget
// overflow rejects the complete write before storage changes; storage failures
// can report partial progress, and callers serialize use with commit and
// cleanup.
func (s *OpenAIFirstOutputStage) Write(p []byte) (int, error) {
	if err := s.prepareWrite(len(p)); err != nil {
		return 0, err
	}
	var n int
	var err error
	if s.tempFile == nil {
		n, err = s.memory.Write(p)
	} else {
		n, err = s.tempFile.Write(p)
	}
	s.size += int64(n)
	if err != nil {
		return n, fmt.Errorf("write first-output stage: %w", err)
	}
	return n, nil
}

// prepareWrite checks capacity and prepares storage before an incoming append.
// Nil or closed stages return os.ErrClosed, and an over-budget append returns
// the stage-limit error before its data is written. Existing spooled or
// memory-only stages keep their storage choice.
//
// A spill first creates and unlinks the empty temporary file, then copies the
// memory buffer before publishing the descriptor. Unlink failure closes that
// file, retries removal, records cleanup errors, and selects memory for later
// writes so named request plaintext is not written. A failed initial copy
// closes the descriptor and leaves existing memory available for the caller to
// handle the write failure.
func (s *OpenAIFirstOutputStage) prepareWrite(incoming int) error {
	if s == nil || s.closed {
		return os.ErrClosed
	}
	// Reject the entire append before accepting any incoming bytes.
	if int64(incoming) > s.limit-s.size {
		return fmt.Errorf("%w: buffered=%d incoming=%d limit=%d", ErrOpenAIFirstOutputStageLimit, s.size, incoming, s.limit)
	}
	// Spill only when accepted and incoming bytes exceed memory storage.
	if s.tempFile != nil || s.memoryOnly || s.size+int64(incoming) <= OpenAIFirstOutputStageMemoryLimit {
		return nil
	}
	file, err := s.createTemp()
	if err != nil {
		return fmt.Errorf("create first-output spool: %w", err)
	}
	path := file.Name()
	// Unlink before writing request data. Unix keeps the descriptor readable while
	// a crash or SIGKILL cannot leave a named plaintext spool file.
	if unlinkErr := s.removeFile(path); unlinkErr != nil {
		closeErr := file.Close()
		removeErr := s.removeFile(path)
		// A path removed by another cleanup attempt is already resolved; normalize
		// that absence while retaining the original unlink failure for diagnostic reporting.
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		// The path could remain named after unlink failure. Keep request data in
		// memory for this stage and retain the empty path for deferred cleanup retries.
		s.memoryOnly = true
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			s.tempPath = path
		}
		s.cleanupErr = errors.Join(
			s.cleanupErr,
			fmt.Errorf("unlink first-output spool before use: %w", unlinkErr),
			closeErr,
			removeErr,
		)
		return nil
	}
	if _, err := file.Write(s.memory.Bytes()); err != nil {
		// The new descriptor is not published yet. Close it on failed copying
		// while the memory buffer retains the data already accepted by this attempt.
		_ = file.Close()
		return fmt.Errorf("initialize first-output spool: %w", err)
	}
	s.tempFile = file
	s.tempPath = path
	s.memory.Reset()
	return nil
}

// CommitTo copies all staged bytes to dst and closes staging after successful
// delivery. Callers serialize this operation with writes and cleanup; nil or
// closed stages return os.ErrClosed. A write, seek, or copy failure leaves
// cleanup to the caller and can follow partial downstream output.
//
// Spool delivery seeks to the beginning and copies the recorded byte count,
// matching memory delivery from its current buffer. Once copying succeeds,
// cleanup errors are retained in the stage and the method reports success
// because the bytes have already been delivered. Callers can inspect a later
// Close error for cleanup diagnostics without treating those delivered bytes as
// an upstream stream failure.
func (s *OpenAIFirstOutputStage) CommitTo(dst io.Writer) error {
	if s == nil || s.closed {
		return os.ErrClosed
	}
	if s.tempFile == nil {
		if _, err := io.Copy(dst, bytes.NewReader(s.memory.Bytes())); err != nil {
			return err
		}
	} else {
		if _, err := s.tempFile.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("seek first-output spool: %w", err)
		}
		if _, err := io.CopyN(dst, s.tempFile, s.size); err != nil {
			return err
		}
	}
	if err := s.Close(); err != nil {
		// Delivery succeeded. Retain cleanup errors for deferred cleanup or logging
		// instead of reporting already-delivered bytes as a stream failure.
		s.cleanupErr = errors.Join(s.cleanupErr, err)
	}
	return nil
}

// Close ends staging, resets buffered bytes, and attempts descriptor and path
// cleanup. Nil stages return nil, and callers serialize access with writes or
// commit. A closed stage can be closed again when a previous removal failure
// left a path to retry.
//
// Cleanup combines recorded failures with the descriptor close and path removal
// results. Successful removal or an absent path clears the pending path; other
// removal errors retain it for the next call. After all storage and errors are
// cleared, repeated Close returns nil. This supports deferred cleanup after
// commit and retry of a failed unlink without reopening the stage.
func (s *OpenAIFirstOutputStage) Close() error {
	if s == nil {
		return nil
	}
	if s.closed && s.tempFile == nil && s.tempPath == "" && s.cleanupErr == nil {
		return nil
	}
	s.closed = true
	s.size = 0
	s.memory.Reset()
	closeErr := s.cleanupErr
	s.cleanupErr = nil
	// End descriptor ownership before removing any retained path, and clear the
	// published descriptor so a later cleanup retry cannot close the same handle again.
	if s.tempFile != nil {
		closeErr = errors.Join(closeErr, s.tempFile.Close())
		s.tempFile = nil
	}
	if s.tempPath != "" {
		removeErr := s.removeFile(s.tempPath)
		// Unix spools were unlinked before request bytes were accepted; an absent
		// path therefore completes cleanup just like successful removal of a retained name.
		if removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
			s.tempPath = ""
		} else {
			closeErr = errors.Join(closeErr, removeErr)
		}
	}
	return closeErr
}

// Closed reports whether staging has ended for this attempt.
func (s *OpenAIFirstOutputStage) Closed() bool { return s.closed }

// Limit returns the byte budget configured for this staging object.
func (s *OpenAIFirstOutputStage) Limit() int64 { return s.limit }
