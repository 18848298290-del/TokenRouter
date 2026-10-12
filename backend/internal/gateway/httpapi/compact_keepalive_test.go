package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartOpenAICompactSSEKeepalive_NoopWhenUnmarkedOrDisabled(t *testing.T) {
	// No client-stream marker disables compact heartbeats.
	c, rec := newCompactBridgeTestContext(t, false)
	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	waitForKeepaliveBeats()
	stop()
	require.Zero(t, rec.Body.Len())
	require.False(t, StopOpenAICompactSSEKeepaliveCommitted(c))

	// A zero interval disables heartbeats.
	c, rec = newCompactBridgeTestContext(t, true)
	stop = StartOpenAICompactSSEKeepalive(c, 0)
	waitForKeepaliveBeats()
	stop()
	require.Zero(t, rec.Body.Len())
	require.False(t, StopOpenAICompactSSEKeepaliveCommitted(c))
}

func TestOpenAICompactSSEKeepalive_CommitsHeadersAndComments(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	defer stop()
	waitForKeepaliveBeats()

	require.True(t, StopOpenAICompactSSEKeepaliveCommitted(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	require.Equal(t, "no", rec.Header().Get("X-Accel-Buffering"))
	require.Contains(t, rec.Body.String(), ": keepalive\n\n")
}

func TestOpenAICompactSSEKeepalive_StopBeforeFirstBeatKeepsWriterUntouched(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
	stop()
	waitForKeepaliveBeats()
	require.Zero(t, rec.Body.Len())
	require.False(t, StopOpenAICompactSSEKeepaliveCommitted(c))
}

func TestOpenAIAdjustedWrittenSizeExcludesResponsesStreamKeepalive(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, false)
	n, err := c.Writer.Write([]byte(":\n\n"))
	require.NoError(t, err)
	RecordOpenAIStreamKeepaliveBytes(c, n)

	require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c))

	_, err = c.Writer.Write([]byte("data: semantic\n\n"))
	require.NoError(t, err)
	require.Equal(t, len("data: semantic\n\n"), OpenAICompactKeepaliveAdjustedWrittenSize(c))
	require.Equal(t, ":\n\ndata: semantic\n\n", rec.Body.String())
}

// TestOpenAICompactKeepaliveWriter_RequestSideWriteSuspendsBeats checks that direct response writes stop heartbeats.
// Run with -race to detect overlapping writes; byte counts must remain stable after stopping.
func TestOpenAICompactKeepaliveWriter_RequestSideWriteSuspendsBeats(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	defer stop()
	waitForKeepaliveBeats()

	// Simulate a direct local rejection from Forward through c.Writer.
	_, err := c.Writer.Write([]byte(`{"error":"local reject"}`))
	require.NoError(t, err)

	lenAfterWrite := rec.Body.Len()
	waitForKeepaliveBeats()
	require.Equal(t, lenAfterWrite, rec.Body.Len(), "请求侧写回后心跳必须停止")
	require.Contains(t, rec.Body.String(), ": keepalive\n\n")
	require.Contains(t, rec.Body.String(), `{"error":"local reject"}`)
}

// TestOpenAICompactKeepaliveWriter_DelegatesWhenReady checks state and write delegation for an initialized wrapper.
func TestOpenAICompactKeepaliveWriter_DelegatesWhenReady(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
	defer stop()

	w, ok := c.Writer.(*compactKeepaliveWriter)
	require.True(t, ok)

	w.Header().Set("X-Test", "ok")
	w.WriteHeader(http.StatusAccepted)
	n, err := w.WriteString("ready")
	require.NoError(t, err)
	require.Equal(t, len("ready"), n)

	require.Equal(t, http.StatusAccepted, w.Status())
	require.Equal(t, len("ready"), w.Size())
	require.True(t, w.Written())
	require.Equal(t, "ok", rec.Header().Get("X-Test"))
	require.Equal(t, "ready", rec.Body.String())
}

// TestOpenAICompactKeepaliveAdjustedWrittenSize_ExcludesHeartbeatBytes checks failover byte accounting.
// Waiting heartbeats allow failover, while protocol output increases the adjusted byte count.
func TestOpenAICompactKeepaliveAdjustedWrittenSize_ExcludesHeartbeatBytes(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	// Without heartbeats, use the Gin writer size.
	require.Equal(t, c.Writer.Size(), OpenAICompactKeepaliveAdjustedWrittenSize(c))

	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	defer stop()
	before := OpenAICompactKeepaliveAdjustedWrittenSize(c)
	waitForKeepaliveBeats()
	require.Equal(t, before, OpenAICompactKeepaliveAdjustedWrittenSize(c), "仅心跳字节不得改变判定口径")

	// Writing protocol content stops heartbeats before increasing the adjusted size.
	_, err := c.Writer.Write([]byte("real-bytes"))
	require.NoError(t, err)
	require.Equal(t, len("real-bytes"), OpenAICompactKeepaliveAdjustedWrittenSize(c))
	require.Contains(t, rec.Body.String(), ": keepalive\n\n")
}

func TestOpenAIStreamClientOutputStarted_IgnoresCompactKeepaliveBytes(t *testing.T) {
	c, _ := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	defer stop()
	waitForKeepaliveBeats()

	require.True(t, c.Writer.Written())
	require.False(t, OpenAIStreamClientOutputStarted(c, false), "compact 心跳不是业务输出")

	_, err := c.Writer.Write([]byte("real-output"))
	require.NoError(t, err)
	require.True(t, OpenAIStreamClientOutputStarted(c, false))
}

// newNativeCompactWriterTestContext supplies a separate recorder for zero-value wrapper tests.
func newNativeCompactWriterTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	MarkOpenAICompactClientStream(c)
	return c, rec
}

func TestOpenAICompactKeepaliveWriter_NilInnerWriter_NoPanic(t *testing.T) {
	w := &compactKeepaliveWriter{
		k: &openAICompactSSEKeepalive{stop: make(chan struct{})},
	}
	w.ResponseWriter = nil

	assert.NotPanics(t, func() {
		assert.Equal(t, 0, w.Status())
	})
	assert.NotPanics(t, func() {
		assert.Equal(t, 0, w.Size())
	})
	assert.NotPanics(t, func() {
		assert.False(t, w.Written())
	})
	assert.NotPanics(t, func() {
		assert.NotNil(t, w.Header())
	})
	assert.NotPanics(t, func() {
		n, err := w.Write([]byte("test"))
		assert.Equal(t, 0, n)
		assert.NoError(t, err)
	})
	assert.NotPanics(t, func() {
		n, err := w.WriteString("test")
		assert.Equal(t, 0, n)
		assert.NoError(t, err)
	})
	assert.NotPanics(t, func() {
		w.WriteHeader(http.StatusOK)
	})
	assert.NotPanics(t, func() {
		w.WriteHeaderNow()
	})
	assert.NotPanics(t, func() {
		w.Flush()
	})
	assert.NotPanics(t, func() {
		conn, rw, err := w.Hijack()
		assert.Nil(t, conn)
		assert.Nil(t, rw)
		assert.Error(t, err)
	})
	assert.NotPanics(t, func() {
		ch := w.CloseNotify()
		assert.NotNil(t, ch)
	})
	assert.NotPanics(t, func() {
		assert.Nil(t, w.Pusher())
	})
}

func TestOpenAICompactKeepaliveWriter_NilKeepalive_NoPanic(t *testing.T) {
	c, rec := newNativeCompactWriterTestContext(t)
	w := &compactKeepaliveWriter{ResponseWriter: c.Writer}

	assert.NotPanics(t, func() {
		assert.Equal(t, 0, w.Status())
	})
	assert.NotPanics(t, func() {
		assert.Equal(t, 0, w.Size())
	})
	assert.NotPanics(t, func() {
		assert.False(t, w.Written())
	})
	assert.NotPanics(t, func() {
		w.Header().Set("X-Test", "ok")
	})
	assert.NotPanics(t, func() {
		w.WriteHeader(http.StatusAccepted)
	})
	assert.NotPanics(t, func() {
		n, err := w.WriteString("ok")
		assert.Equal(t, 2, n)
		assert.NoError(t, err)
	})
	assert.NotPanics(t, func() {
		w.Flush()
	})
	require.Equal(t, "ok", rec.Header().Get("X-Test"))
	require.Equal(t, "ok", rec.Body.String())
}

// TestOpsCaptureWriter_CompactKeepaliveRestoresOriginalWriter checks that stopping restores
// the Ops middleware writer so outer middleware can read response status.
func TestOpsCaptureWriter_CompactKeepaliveRestoresOriginalWriter(t *testing.T) {
	router := gin.New()
	outerStatus := -1
	router.Use(func(c *gin.Context) {
		c.Next()
		outerStatus = c.Writer.Status()
	})
	router.Use(opsLoggerFixture(nil))
	router.GET("/compact", func(c *gin.Context) {
		MarkOpenAICompactClientStream(c)
		stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
		defer stop()
		c.Status(http.StatusOK)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/compact", nil)
	require.NotPanics(t, func() {
		router.ServeHTTP(recorder, request)
	})
	require.Equal(t, http.StatusOK, outerStatus)
	require.Equal(t, http.StatusOK, recorder.Code)
}

func newPassthroughKeepaliveTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	// Ordinary Responses passthrough has no compact client-stream marker.

	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	return c, rec
}

// TestStartOpenAISSEKeepalive_WorksWithoutCompactMarker checks heartbeats on ordinary passthrough requests.
func TestStartOpenAISSEKeepalive_WorksWithoutCompactMarker(t *testing.T) {
	c, rec := newPassthroughKeepaliveTestContext(t)

	// The compact entry requires the marker and must do nothing here.
	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	waitForKeepaliveBeats()
	stop()
	require.Zero(t, rec.Body.Len(), "无 compact 标记时 StartOpenAICompactSSEKeepalive 应当 no-op")

	// The general SSE entry starts heartbeats without the compact marker.
	c, rec = newPassthroughKeepaliveTestContext(t)
	stop = StartOpenAISSEKeepalive(c, keepaliveTestInterval)
	defer stop()
	waitForKeepaliveBeats()

	require.True(t, StopOpenAICompactSSEKeepaliveCommitted(c), "心跳应当提交响应头")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	require.Equal(t, "no", rec.Header().Get("X-Accel-Buffering"))
	require.Contains(t, rec.Body.String(), ": keepalive\n\n")
}

// TestPassthroughKeepaliveDoesNotBlockPreOutputFailover checks that heartbeat-only output still permits upstream
// failover (#3887).
func TestPassthroughKeepaliveDoesNotBlockPreOutputFailover(t *testing.T) {
	c, rec := newPassthroughKeepaliveTestContext(t)
	stop := StartOpenAISSEKeepalive(c, keepaliveTestInterval)
	defer stop()
	waitForKeepaliveBeats()
	require.True(t, StopOpenAICompactSSEKeepaliveCommitted(c))
	require.NotZero(t, rec.Body.Len(), "前提:心跳确实写出了字节")

	// Heartbeat bytes do not count as client protocol output.
	require.False(t, OpenAIStreamClientOutputStarted(c, false), "心跳字节不构成语义输出,pre-output failover 必须仍然可用")

	// A protocol event marks client output as started.
	_, err := c.Writer.Write([]byte("data: {\"type\":\"response.output_text.delta\"}\n\n"))
	require.NoError(t, err)
	require.True(t, OpenAIStreamClientOutputStarted(c, false), "真实语义输出之后应当判定为已输出")
}

// TestPassthroughKeepaliveStopsBeforeHandingOverWriter checks stable heartbeat bytes before the response loop takes
// over.
func TestPassthroughKeepaliveStopsBeforeHandingOverWriter(t *testing.T) {
	c, rec := newPassthroughKeepaliveTestContext(t)
	stop := StartOpenAISSEKeepalive(c, keepaliveTestInterval)
	waitForKeepaliveBeats()
	stop()

	before := rec.Body.String()
	waitForKeepaliveBeats()
	require.Equal(t, before, rec.Body.String(), "停拍后不应再有字节写出")

	// Protocol output after stopping must not be interleaved with heartbeat comments.
	_, err := c.Writer.Write([]byte("data: real\n\n"))
	require.NoError(t, err)
	waitForKeepaliveBeats()
	require.True(t, strings.HasSuffix(rec.Body.String(), "data: real\n\n"),
		"停拍后写入应当是响应体的最后一段")
}

// TestPassthroughKeepaliveDisabledKeepsWriterUntouched checks that nonpositive intervals write no bytes.
func TestPassthroughKeepaliveDisabledKeepsWriterUntouched(t *testing.T) {
	c, rec := newPassthroughKeepaliveTestContext(t)
	stop := StartOpenAISSEKeepalive(c, 0)
	waitForKeepaliveBeats()
	stop()
	require.Zero(t, rec.Body.Len())
	require.False(t, StopOpenAICompactSSEKeepaliveCommitted(c))
}
