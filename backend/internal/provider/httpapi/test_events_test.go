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

type failTestEventWriter struct {
	TestStreamWriter
	err error
}

// TestProviderTestPropagatesEventWriteFailure checks that lost stream output is reported as a test failure.
func TestProviderTestPropagatesEventWriteFailure(t *testing.T) {
	failed := errors.New("forced test event write failure")
	writer := failTestEventWriter{TestStreamWriter: httptest.NewRecorder(), err: failed}
	run := provideradapter.NewTestRun(t.Context(), make(http.Header), NewTestEventSink(writer))
	defer run.Cancel()
	err := (provideradapter.TestStreamOutput{}).Responses(run, strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"fixture\"}\n\ndata: {\"type\":\"response.completed\"}\n\n"))
	err = run.Result(err)
	require.ErrorIs(t, err, failed)
}

// TestTestEventSinkPreservesPreludeAndFrames checks deferred flushing during preparation and full SSE headers on commit.
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

func TestProcessGeminiStream_EmitsImageEvent(t *testing.T) {
	t.Parallel()
	recorder := httptest.NewRecorder()
	run := provideradapter.NewTestRun(t.Context(), make(http.Header), NewTestEventSink(recorder))
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

func (w failTestEventWriter) Write([]byte) (int, error) { return 0, w.err }
