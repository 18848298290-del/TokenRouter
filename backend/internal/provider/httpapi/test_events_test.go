package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

// Provider-test SSE preparation defers flushing until commitment, while each
// emitted frame flushes immediately. Tests in test_events.go's scope exercise
// write-error propagation through TestRun, exact newline escaping and Gemini
// inline image presentation with controlled stream text.

// failTestEventWriter retains header and flush behavior while rejecting every
// event body write.
type failTestEventWriter struct {
	TestStreamWriter       // borrowed header and flush writer
	err              error // configured event write failure
}

// TestProviderTestPropagatesEventWriteFailure injects a writer failure while
// processing Responses text events. TestRun returns the same error after stream
// output fails, protecting propagation of the sink failure through the
// output-aware result even when stream processing has also returned an error.
func TestProviderTestPropagatesEventWriteFailure(t *testing.T) {
	failed := errors.New("forced test event write failure")
	writer := failTestEventWriter{TestStreamWriter: httptest.NewRecorder(), err: failed}
	run := provideradapter.NewTestRun(t.Context(), make(http.Header), NewTestEventSink(writer))
	// Release the derived test-run context after stream assertions so its
	// cancellation ownership does not outlive the fixture's output writer.
	defer run.Cancel()
	err := (provideradapter.TestStreamOutput{}).Responses(run, strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"fixture\"}\n\ndata: {\"type\":\"response.completed\"}\n\n"))
	err = run.Result(err)
	require.ErrorIs(t, err, failed)
}

// TestTestEventSinkPreservesPreludeAndFrames prepares a stream before and after
// commitment and emits text containing a newline. Headers and flush state
// follow commit selection, and the body contains one correctly escaped SSE
// frame.
func TestTestEventSinkPreservesPreludeAndFrames(t *testing.T) {
	recorder := httptest.NewRecorder()
	sink := NewTestEventSink(recorder)
	require.NoError(t, sink.Begin(context.Background(), false))
	require.False(t, recorder.Flushed)
	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	require.Empty(t, recorder.Header().Get("Connection"))
	require.NoError(t, sink.Begin(context.Background(), true))
	require.True(t, recorder.Flushed)
	require.Equal(t, "keep-alive", recorder.Header().Get("Connection"))
	require.Equal(t, "no", recorder.Header().Get("X-Accel-Buffering"))
	require.NoError(t, sink.Emit(context.Background(), provider.TestEvent{Type: "content", Text: "line\n"}))
	require.Equal(t, "data: {\"type\":\"content\",\"text\":\"line\\n\"}\n\n", recorder.Body.String())
}

// TestProcessGeminiStream_EmitsImageEvent supplies Gemini text and inline image
// data in one stream. Output contains content and image events with the
// expected base64 data URL and MIME type.
func TestProcessGeminiStream_EmitsImageEvent(t *testing.T) {
	t.Parallel()
	recorder := httptest.NewRecorder()
	run := provideradapter.NewTestRun(t.Context(), make(http.Header), NewTestEventSink(recorder))
	// Release the derived test-run context after stream assertions so its
	// cancellation ownership does not outlive the fixture's output writer.
	defer run.Cancel()

	stream := strings.NewReader("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"},{\"inlineData\":{\"mimeType\":\"image/png\",\"data\":\"QUJD\"}}]}}]}\n\ndata: [DONE]\n\n")

	err := (provideradapter.TestStreamOutput{}).Gemini(run, stream)
	require.NoError(t, err)

	body := recorder.Body.String()
	require.Contains(t, body, "\"type\":\"content\"")
	require.Contains(t, body, "\"text\":\"ok\"")
	require.Contains(t, body, "\"type\":\"image\"")
	require.Contains(t, body, "\"image_url\":\"data:image/png;base64,QUJD\"")
	require.Contains(t, body, "\"mime_type\":\"image/png\"")
}

// Write returns the configured fixture error and reports zero bytes written.
func (w failTestEventWriter) Write([]byte) (int, error) { return 0, w.err }
