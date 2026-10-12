package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Provider tests emit one JSON payload per SSE data frame and flush each
// completed event. Stream preparation can defer the first flush so local
// validation still controls HTTP response commitment, while event-write errors
// return to the use case for execution cancellation.

// TestStreamWriter supplies HTTP response headers, event writes and flush for
// provider-test streaming.
type TestStreamWriter interface {
	http.ResponseWriter // HTTP headers and event writes
	http.Flusher        // synchronous event delivery flush
}

// TestEventSink borrows an HTTP stream writer for synchronous provider-test
// event output.
type TestEventSink struct {
	// writer is retained for synchronous output. Its owner serializes Begin and
	// Emit and keeps the response writer usable until the test finishes.
	writer TestStreamWriter // borrowed synchronous response writer
}

// NewTestEventSink borrows the supplied HTTP writer for provider-test events.
// The caller serializes sink operations and keeps the writer valid through
// execution; the sink retains no independently closeable response resource.
func NewTestEventSink(writer TestStreamWriter) *TestEventSink { return &TestEventSink{writer: writer} }

// Begin installs SSE content and cache headers, flushing only when commit is
// true. Committed preparation also sets connection and buffering headers; the
// supplied context is unused and Flush supplies no error result.
func (s *TestEventSink) Begin(_ context.Context, commit bool) error {
	s.writer.Header().Set("Content-Type", "text/event-stream")
	s.writer.Header().Set("Cache-Control", "no-cache")
	if commit {
		s.writer.Header().Set("Connection", "keep-alive")
		s.writer.Header().Set("X-Accel-Buffering", "no")
		s.writer.Flush()
	}
	return nil
}

// Emit writes and flushes one JSON SSE frame through the borrowed response
// writer. Marshal errors produce an empty data payload, write errors are
// returned before flushing, and the supplied context is unused.
func (s *TestEventSink) Emit(_ context.Context, event provider.TestEvent) error {
	// A failed marshal retains the compatibility empty-data frame. Return a
	// write failure to TestRun before flushing so execution can stop after lost output.
	raw, _ := json.Marshal(event)
	if _, err := fmt.Fprintf(s.writer, "data: %s\n\n", raw); err != nil {
		return err
	}
	s.writer.Flush()
	return nil
}
