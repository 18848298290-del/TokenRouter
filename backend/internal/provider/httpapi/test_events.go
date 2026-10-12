package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// TestStreamWriter writes and flushes provider test events to an HTTP response.
type TestStreamWriter interface {
	http.ResponseWriter
	http.Flusher
}
type TestEventSink struct{ writer TestStreamWriter }

func NewTestEventSink(writer TestStreamWriter) *TestEventSink { return &TestEventSink{writer: writer} }

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

func (s *TestEventSink) Emit(_ context.Context, event provider.TestEvent) error {
	// Encoding failures emit an empty data line; write failures return an error so the use case can cancel execution.
	raw, _ := json.Marshal(event)
	if _, err := fmt.Fprintf(s.writer, "data: %s\n\n", raw); err != nil {
		return err
	}
	s.writer.Flush()
	return nil
}
