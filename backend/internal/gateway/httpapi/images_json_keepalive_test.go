package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// TestOpenAIImagesJSONKeepalive_PreservesValidJSONResponse waits for padding
// before writing an image JSON document. The response stays parseable,
// adjusted size tracks real output, and cleanup restores the original writer.
func TestOpenAIImagesJSONKeepalive_PreservesValidJSONResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	originalWriter := c.Writer

	stop := StartOpenAIImagesJSONKeepalive(c, 5*time.Millisecond)
	waitForOpenAIImagesJSONKeepalive(t, c)
	require.Equal(t, -1, OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c))

	c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"b64_json": "aW1hZ2U="}}})
	stop()

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Equal(t, "no", rec.Header().Get("X-Accel-Buffering"))
	require.True(t, rec.Flushed)
	require.True(t, json.Valid(rec.Body.Bytes()), rec.Body.String())
	require.Equal(t, "aW1hZ2U=", gjson.Get(rec.Body.String(), "data.0.b64_json").String())
	require.Greater(t, OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c), 0)
	require.Same(t, originalWriter, c.Writer)
}

// TestOpenAIImagesJSONKeepalive_DisabledIsNoop uses a zero heartbeat interval
// before writing a request error. Cleanup leaves the original writer
// installed and the error retains its requested status.
func TestOpenAIImagesJSONKeepalive_DisabledIsNoop(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	originalWriter := c.Writer

	stop := StartOpenAIImagesJSONKeepalive(c, 0)
	stop()
	c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "invalid request"}})

	require.Same(t, originalWriter, c.Writer)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid request", gjson.Get(rec.Body.String(), "error.message").String())
}

// TestOpenAIImagesJSONKeepalive_FastErrorPreservesStatus writes an image
// error before the first scheduled heartbeat. The error keeps status 400 and
// reaches the client without preceding heartbeat whitespace.
func TestOpenAIImagesJSONKeepalive_FastErrorPreservesStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	stop := StartOpenAIImagesJSONKeepalive(c, time.Second)
	wrote := writeKeepaliveImageError(c, &ImageErrorResponse{
		Status:  http.StatusBadRequest,
		Type:    "invalid_request_error",
		Message: "invalid size",
	})
	stop()

	require.True(t, wrote)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.False(t, strings.HasPrefix(rec.Body.String(), " \n"))
	require.Equal(t, "invalid size", gjson.Get(rec.Body.String(), "error.message").String())
}

// TestOpenAIImagesJSONKeepalive_LateErrorRemainsJSON waits until padding has
// committed status 200 before writing an image error. The error retains its
// code and message in a valid JSON document.
func TestOpenAIImagesJSONKeepalive_LateErrorRemainsJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	stop := StartOpenAIImagesJSONKeepalive(c, 5*time.Millisecond)
	defer stop()
	waitForOpenAIImagesJSONKeepalive(t, c)

	wrote := writeKeepaliveImageError(c, &ImageErrorResponse{
		Status:  http.StatusBadRequest,
		Type:    "image_generation_user_error",
		Code:    "moderation_blocked",
		Message: "request rejected",
	})

	require.True(t, wrote)
	require.Equal(t, http.StatusOK, rec.Code, "heartbeat already committed the status")
	require.True(t, json.Valid(rec.Body.Bytes()), rec.Body.String())
	require.Equal(t, "moderation_blocked", gjson.Get(rec.Body.String(), "error.code").String())
	require.Equal(t, "request rejected", gjson.Get(rec.Body.String(), "error.message").String())
}

// TestOpenAIImagesJSONKeepalive_DoesNotBlockFailoverDetection observes a
// response containing heartbeat whitespace alone. HTTP reports commitment
// while the adjusted byte count stays -1, keeping actual-output detection
// unchanged.
func TestOpenAIImagesJSONKeepalive_DoesNotBlockFailoverDetection(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	stop := StartOpenAIImagesJSONKeepalive(c, 5*time.Millisecond)
	waitForOpenAIImagesJSONKeepalive(t, c)

	before := OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c)
	require.Equal(t, -1, before)
	require.True(t, c.Writer.Written())
	require.Equal(t, before, OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c))
	stop()
	require.True(t, strings.TrimSpace(rec.Body.String()) == "")
}

// TestOpenAIImagesJSONKeepaliveWriter_NilGuards exercises a wrapper lacking
// heartbeat state and an underlying writer. Its response methods remain
// callable, hijacking fails, and close notification is already closed.
func TestOpenAIImagesJSONKeepaliveWriter_NilGuards(t *testing.T) {
	w := &openAIImagesJSONKeepaliveWriter{}
	require.NotPanics(t, func() {
		require.NotNil(t, w.Header())
		_, _ = w.Write([]byte("test"))
		_, _ = w.WriteString("test")
		w.WriteHeader(http.StatusOK)
		w.WriteHeaderNow()
		w.Flush()
		require.Equal(t, 0, w.Status())
		require.Equal(t, 0, w.Size())
		require.False(t, w.Written())
		require.Nil(t, w.Pusher())
	})

	conn, _, err := w.Hijack()
	require.Error(t, err)
	require.Nil(t, conn)
	select {
	case <-w.CloseNotify():
	default:
		t.Fatal("nil writer CloseNotify channel should be closed")
	}
}

// waitForOpenAIImagesJSONKeepalive waits up to one second for retained
// heartbeat state to report a started response. It checks that state under
// the heartbeat mutex, failing the test if installation or startup is
// missing.
func waitForOpenAIImagesJSONKeepalive(t *testing.T, c *gin.Context) {
	t.Helper()
	k := openAIImagesJSONKeepaliveFromContext(c)
	require.NotNil(t, k)
	require.Eventually(t, func() bool {
		k.mu.Lock()
		defer k.mu.Unlock()
		return k.started
	}, time.Second, time.Millisecond)
}

// writeKeepaliveImageError supplies heartbeat-adjusted size and stopping
// callbacks to the image error writer. Provider error conversion is exercised
// elsewhere; this helper presents the already sanitized error supplied by the
// test.
func writeKeepaliveImageError(c *gin.Context, in *ImageErrorResponse) bool {
	return WriteImageError(c, in, func() int { return OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c) }, func() { StopOpenAIImagesJSONKeepaliveCommitted(c) })
}

// TestOpenAIImagesJSONKeepalive_KeepsOAuthNonStreamResponseValid delays an
// OAuth SSE image result while JSON heartbeats run. The nonstreaming reader
// returns one image and leaves a valid JSON response after the padding.
func TestOpenAIImagesJSONKeepalive_KeepsOAuthNonStreamResponseValid(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	// Delay the pipe producer so the heartbeat commits padding before the OAuth
	// terminal event arrives; closing the pipe lets the reader finish its stream.
	reader, writer := io.Pipe()
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = io.WriteString(writer,
			"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000000,\"output\":[{\"type\":\"image_generation_call\",\"result\":\"aW1hZ2U=\",\"output_format\":\"png\"}]}}\n\n"+
				"data: [DONE]\n\n",
		)
		_ = writer.Close()
	}()

	stop := StartOpenAIImagesJSONKeepalive(c, 5*time.Millisecond)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       reader,
	}
	svc := newImagesFixture(imagesFixtureInputs{})
	_, imageCount, _, err := openai.ReadImagesOAuthNonStreaming(resp, ResponseSink{Writer: c.Writer}, svc.Output.ImageOptions(c), "b64_json", "gpt-image-2")
	stop()

	require.NoError(t, err)
	require.Equal(t, 1, imageCount)
	require.True(t, rec.Flushed)
	require.True(t, strings.HasPrefix(rec.Body.String(), " \n"), rec.Body.String())
	require.True(t, json.Valid(rec.Body.Bytes()), rec.Body.String())
	require.Equal(t, "aW1hZ2U=", gjson.Get(rec.Body.String(), "data.0.b64_json").String())
}
