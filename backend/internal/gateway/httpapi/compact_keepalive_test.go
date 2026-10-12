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

// Keepalive comments commit SSE headers while leaving the model-output retry
// check unstarted. These cases exercise first-beat timing, response-writer takeover,
// byte accounting, missing wrapper components, and restoration for middleware
// that reads final status after request handling.

// TestStartOpenAICompactSSEKeepalive_NoopWhenUnmarkedOrDisabled omits the client-stream
// marker or supplies a zero interval. Neither configuration may emit comments,
// and stopping must report that no heartbeat selected the response headers.
func TestStartOpenAICompactSSEKeepalive_NoopWhenUnmarkedOrDisabled(t *testing.T) {
	// Use the normal heartbeat window before cleanup so this fixture checks
	// marker gating after the scheduled first beat could have produced output.
	c, rec := newCompactBridgeTestContext(t, false)
	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	waitForKeepaliveBeats()
	stop()
	require.Zero(t, rec.Body.Len())
	require.False(t, StopOpenAICompactSSEKeepaliveCommitted(c))

	// Repeat with the marker present and timing disabled, separating interval
	// gating from the preceding marker check while keeping the same observation window.
	c, rec = newCompactBridgeTestContext(t, true)
	stop = StartOpenAICompactSSEKeepalive(c, 0)
	waitForKeepaliveBeats()
	stop()
	require.Zero(t, rec.Body.Len())
	require.False(t, StopOpenAICompactSSEKeepaliveCommitted(c))
}

// TestOpenAICompactSSEKeepalive_CommitsHeadersAndComments waits for enabled Compact
// heartbeats before stopping the controller. It must report started output
// and expose HTTP 200, SSE headers, disabled proxy buffering, and a keepalive comment.
func TestOpenAICompactSSEKeepalive_CommitsHeadersAndComments(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	defer stop()
	waitForKeepaliveBeats()

	// Stop under the controller mutex before reading recorder state, so these
	// header and byte assertions observe a completed beat after any active write finishes.
	require.True(t, StopOpenAICompactSSEKeepaliveCommitted(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	require.Equal(t, "no", rec.Header().Get("X-Accel-Buffering"))
	require.Contains(t, rec.Body.String(), ": keepalive\n\n")
}

// TestOpenAICompactSSEKeepalive_StopBeforeFirstBeatKeepsWriterUntouched stops a
// controller configured with a distant first beat. Output must stay empty,
// and commitment lookup must remain false after the ordinary short test wait.
func TestOpenAICompactSSEKeepalive_StopBeforeFirstBeatKeepsWriterUntouched(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	// Put the first tick beyond this test's wait, then stop immediately so
	// an unwritten response exercises explicit stopping before heartbeat framing is selected.
	stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
	stop()
	waitForKeepaliveBeats()
	require.Zero(t, rec.Body.Len())
	require.False(t, StopOpenAICompactSSEKeepaliveCommitted(c))
}

// TestOpenAIAdjustedWrittenSizeExcludesResponsesStreamKeepalive records ordinary
// stream comment bytes before writing data. Adjusted size must report the
// unwritten sentinel for the comment alone and count the subsequent data bytes exactly.
func TestOpenAIAdjustedWrittenSizeExcludesResponsesStreamKeepalive(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, false)
	// Record the count returned for this comment before checking output. The
	// counter discounts committed comment bytes that raw writer size would otherwise count as protocol output.
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

	// Write a local rejection through the installed wrapper so its takeover
	// stops the controller before later beats can append comments after that payload.
	_, err := c.Writer.Write([]byte(`{"error":"local reject"}`))
	require.NoError(t, err)

	lenAfterWrite := rec.Body.Len()
	waitForKeepaliveBeats()
	require.Equal(t, lenAfterWrite, rec.Body.Len(), "请求侧写回后心跳必须停止")
	require.Contains(t, rec.Body.String(), ": keepalive\n\n")
	require.Contains(t, rec.Body.String(), `{"error":"local reject"}`)
}

// TestOpenAICompactKeepaliveWriter_DelegatesWhenReady checks state and write delegation for an initialized wrapper.
// Header and string output must reach the recorder, and the wrapper must
// report the delegated status, byte size, and written flag after response takeover.
func TestOpenAICompactKeepaliveWriter_DelegatesWhenReady(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	// Delay the first beat while Header takes over through the wrapper, so
	// subsequent assertions isolate response delegation from heartbeat-driven status and byte changes.
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

// TestOpenAICompactKeepaliveAdjustedWrittenSize_ExcludesHeartbeatBytes observes
// Compact comments before direct protocol output. Adjusted size must retain
// its pre-heartbeat value until data arrives, then report the data byte count.
func TestOpenAICompactKeepaliveAdjustedWrittenSize_ExcludesHeartbeatBytes(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	// Without heartbeats, use the Gin writer size.
	require.Equal(t, c.Writer.Size(), OpenAICompactKeepaliveAdjustedWrittenSize(c))

	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	defer stop()
	before := OpenAICompactKeepaliveAdjustedWrittenSize(c)
	waitForKeepaliveBeats()
	require.Equal(t, before, OpenAICompactKeepaliveAdjustedWrittenSize(c), "仅心跳字节不得改变判定口径")

	// Take over through wrapper Write before measuring data. The stored heartbeat
	// count must stay subtracted after normal bytes increase the captured writer's size.
	_, err := c.Writer.Write([]byte("real-bytes"))
	require.NoError(t, err)
	require.Equal(t, len("real-bytes"), OpenAICompactKeepaliveAdjustedWrittenSize(c))
	require.Contains(t, rec.Body.String(), ": keepalive\n\n")
}

// TestOpenAIStreamClientOutputStarted_IgnoresCompactKeepaliveBytes distinguishes
// Gin's written flag from model-output detection after comments. Heartbeats must
// leave the output check false, and a later data write must make it true.
func TestOpenAIStreamClientOutputStarted_IgnoresCompactKeepaliveBytes(t *testing.T) {
	c, _ := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	defer stop()
	waitForKeepaliveBeats()

	require.True(t, c.Writer.Written())
	require.False(t, OpenAIStreamClientOutputStarted(c, false), "compact 心跳不是业务输出")

	// This write takes over the wrapper and contributes bytes outside its
	// heartbeat counter, which must change the output decision after comment-only traffic.
	_, err := c.Writer.Write([]byte("real-output"))
	require.NoError(t, err)
	require.True(t, OpenAIStreamClientOutputStarted(c, false))
}

// newNativeCompactWriterTestContext supplies a request and recorder for wrappers with missing components.
func newNativeCompactWriterTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	MarkOpenAICompactClientStream(c)
	return c, rec
}

// TestOpenAICompactKeepaliveWriter_NilInnerWriter_NoPanic supplies a controller
// with no response writer. Every exercised wrapper operation must return its
// documented empty or error result without panicking during output and interface lookup.
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

// TestOpenAICompactKeepaliveWriter_NilKeepalive_NoPanic supplies a response writer
// with no heartbeat controller. State reads must return fallback values, while
// headers and string bytes reach the recorder and flush remains callable.
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

// TestOpsCaptureWriter_CompactKeepaliveRestoresOriginalWriter installs keepalive
// inside Ops and outer status-reading middleware. Request cleanup must restore
// usable writer delegation so both the outer observation and recorder report HTTP 200.
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
		// Hold off the first beat while status selection stops the wrapper.
		// Cleanup must restore the inner Ops writer before outer middleware reads final status.
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

// newPassthroughKeepaliveTestContext returns an unmarked Responses request and its recorder.
func newPassthroughKeepaliveTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	return c, rec
}

// TestStartOpenAISSEKeepalive_WorksWithoutCompactMarker checks heartbeats on ordinary passthrough requests.
// The Compact entry must remain inactive without its marker, while the general
// SSE entry must select streaming headers and write comments for the same request shape.
func TestStartOpenAISSEKeepalive_WorksWithoutCompactMarker(t *testing.T) {
	c, rec := newPassthroughKeepaliveTestContext(t)

	// Run the unmarked context through the Compact entry for the normal heartbeat
	// window, checking that its marker gate leaves output empty before comparing the general starter.
	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	waitForKeepaliveBeats()
	stop()
	require.Zero(t, rec.Body.Len(), "无 compact 标记时 StartOpenAICompactSSEKeepalive 应当 no-op")

	// Use a fresh unmarked context so the general starter's framing and comments
	// are observed independently of controller state from the preceding Compact call.
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

// TestPassthroughKeepaliveDoesNotBlockPreOutputFailover checks the output marker
// after ordinary SSE heartbeats. Comments must leave the marker false, while
// a later protocol event makes it true for retry-window consumers.
func TestPassthroughKeepaliveDoesNotBlockPreOutputFailover(t *testing.T) {
	c, rec := newPassthroughKeepaliveTestContext(t)
	stop := StartOpenAISSEKeepalive(c, keepaliveTestInterval)
	defer stop()
	waitForKeepaliveBeats()
	require.True(t, StopOpenAICompactSSEKeepaliveCommitted(c))
	require.NotZero(t, rec.Body.Len(), "前提:心跳确实写出了字节")

	// Heartbeat bytes do not count as client protocol output.
	require.False(t, OpenAIStreamClientOutputStarted(c, false), "心跳字节不构成语义输出,pre-output failover 必须仍然可用")

	// Add bytes outside the heartbeat counter. This checks the byte-based output
	// decision after comment-only traffic without executing an upstream retry or validating event content.
	_, err := c.Writer.Write([]byte("data: {\"type\":\"response.output_text.delta\"}\n\n"))
	require.NoError(t, err)
	require.True(t, OpenAIStreamClientOutputStarted(c, false), "真实语义输出之后应当判定为已输出")
}

// TestPassthroughKeepaliveStopsBeforeHandingOverWriter stops the controller
// before writing protocol data. Comment bytes must remain stable after stop,
// and the later data frame must remain the response's final segment.
func TestPassthroughKeepaliveStopsBeforeHandingOverWriter(t *testing.T) {
	c, rec := newPassthroughKeepaliveTestContext(t)
	stop := StartOpenAISSEKeepalive(c, keepaliveTestInterval)
	waitForKeepaliveBeats()
	stop()

	before := rec.Body.String()
	waitForKeepaliveBeats()
	require.Equal(t, before, rec.Body.String(), "停拍后不应再有字节写出")

	// Take over the captured writer after stopping, then wait through another
	// heartbeat window to detect any comment appended after the final data frame.
	_, err := c.Writer.Write([]byte("data: real\n\n"))
	require.NoError(t, err)
	waitForKeepaliveBeats()
	require.True(t, strings.HasSuffix(rec.Body.String(), "data: real\n\n"),
		"停拍后写入应当是响应体的最后一段")
}

// TestPassthroughKeepaliveDisabledKeepsWriterUntouched checks that nonpositive intervals write no bytes.
// The exercised zero interval must leave the recorder empty and return a
// cleanup function whose use does not report heartbeat commitment.
func TestPassthroughKeepaliveDisabledKeepsWriterUntouched(t *testing.T) {
	c, rec := newPassthroughKeepaliveTestContext(t)
	stop := StartOpenAISSEKeepalive(c, 0)
	waitForKeepaliveBeats()
	stop()
	require.Zero(t, rec.Body.Len())
	require.False(t, StopOpenAICompactSSEKeepaliveCommitted(c))
}
