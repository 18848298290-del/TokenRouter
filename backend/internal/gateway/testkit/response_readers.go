package testkit

import (
	"io"
	"sync"
)

// response_readers.go provides response bodies that block until closure or
// record cleanup attempts. The reader holds caller-provided bytes and a close
// channel, so the test can observe completion after the production executor
// releases the body.
//
// Read offset advances serially and is not synchronized, while Close uses
// sync.Once to signal waiting readers exactly once. Closing does not erase a
// remaining prefix: a subsequent Read still drains those bytes before returning
// EOF. A separate wrapper records that the executor attempted body closure.
// Tests serialize its flag inspection after the close path returns and provide
// any other io.Reader behavior themselves.

// BlockingReadCloser emits the supplied bytes, then waits for Close to test
// cancellation and cleanup.
type BlockingReadCloser struct {
	// data retains the constructor's byte slice; callers leave it unchanged during
	// reading. offset is advanced by a single serial reader until that prefix ends.
	data   []byte // caller-provided response prefix bytes
	offset int    // next unread response byte index
	// closed and closeOnce jointly unblock waits after the prefix is exhausted.
	// Close signals once even when normal and cancellation cleanup both invoke it.
	closed    chan struct{} // reader cleanup completion signal
	closeOnce sync.Once     // one-time close signal guard
}

// CloseTrackingReader records whether the response body was closed.
type CloseTrackingReader struct {
	io.Reader // wrapped response body reader
	// Closed records a Close call and is unsynchronized. Tests inspect it after
	// the executing close path returns, or provide their own ordering.
	Closed bool
}

// NewBlockingReadCloser retains data as the prefix returned before waiting for
// closure. Callers keep that slice stable and read serially; Close can be
// called repeatedly to unblock a later exhausted read.
func NewBlockingReadCloser(data []byte) *BlockingReadCloser {
	return &BlockingReadCloser{
		data:   data,
		closed: make(chan struct{}),
	}
}

// Read copies remaining prefix bytes and advances the serial offset before
// waiting on closure. Callers keep the retained byte slice stable and use one
// reader; a zero-length destination can return zero without advancing that
// prefix.
//
// Once the prefix is exhausted, Read waits for Close and then returns EOF.
// Close can occur before or during that wait, but it does not discard
// remaining prefix bytes. This ordering lets cancellation tests distinguish
// body closure from ordinary prefix delivery.
func (r *BlockingReadCloser) Read(p []byte) (int, error) {
	if r.offset < len(r.data) {
		n := copy(p, r.data[r.offset:])
		r.offset += n
		return n, nil
	}
	// Hold the exhausted read until resource cleanup closes the body, letting
	// cancellation tests detect whether the executor actually releases its
	// blocked reader.
	<-r.closed
	return 0, io.EOF
}

// Close signals exhausted readers through a channel guarded by sync.Once.
// Repeated calls succeed, and remaining prefix bytes stay available to serial
// Read calls before they reach the closed wait.
func (r *BlockingReadCloser) Close() error {
	// Signal at most once because cancellation and normal body cleanup can converge;
	// another Close must not panic while releasing the same blocked read.
	r.closeOnce.Do(func() {
		close(r.closed)
	})
	return nil
}

// Close marks that the wrapper's response body cleanup was invoked.
func (r *CloseTrackingReader) Close() error {
	r.Closed = true
	return nil
}
