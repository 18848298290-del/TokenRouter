package httpapi

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	// openAIStreamKeepaliveBytesContextKey stores the Responses SSE heartbeat byte count.
	openAIStreamKeepaliveBytesContextKey = "openai_stream_keepalive_bytes"

	// openAICompactSSEKeepaliveKey stores the downstream SSE heartbeat for compact requests.
	openAICompactSSEKeepaliveKey = "openai_compact_sse_keepalive"
)

// openAICompactSSEKeepalive sends SSE comments while waiting for an upstream Compact JSON response.
// Heartbeats keep idle proxies from disconnecting long compactions and causing quota-consuming retries (#3887).
// The first heartbeat waits one interval, allowing earlier failures to use JSON and their HTTP status.
// After a heartbeat commits HTTP 200, later failures use response.failed events.
type openAICompactSSEKeepalive struct {
	mu      sync.Mutex
	writer  gin.ResponseWriter
	started bool
	stopped bool
	// bytes counts heartbeat comment bytes, which OpenAICompactKeepaliveAdjustedWrittenSize
	// excludes when determining whether protocol output has started.
	bytes int
	stop  chan struct{}
}

// compactKeepaliveWriter stops heartbeats under the mutex before writes and locks state reads.
// Size reads leave heartbeats running; the heartbeat goroutine writes directly to k.writer.
type compactKeepaliveWriter struct {
	gin.ResponseWriter
	k *openAICompactSSEKeepalive
}

// StartOpenAICompactSSEKeepalive starts heartbeats for compact requests marked as client-streaming and returns an
// idempotent stop function.
// A missing marker or nonpositive interval disables heartbeats. The wrapper stops them
// before response construction, including local rejections written inside Forward.
func StartOpenAICompactSSEKeepalive(c *gin.Context, interval time.Duration) func() {
	if !OpenAICompactClientWantsStream(c) {
		return func() {}
	}
	return StartOpenAISSEKeepalive(c, interval)
}

// StartOpenAISSEKeepalive starts heartbeats for callers that have already selected SSE output.
// Callers include Responses passthrough after the upstream returns text/event-stream.
// Use OpenAICompactKeepaliveAdjustedWrittenSize to exclude heartbeat bytes from failover checks (#3887).
func StartOpenAISSEKeepalive(c *gin.Context, interval time.Duration) func() {
	if c == nil || c.Writer == nil || interval <= 0 {
		return func() {}
	}
	originalWriter := c.Writer
	k := &openAICompactSSEKeepalive{
		writer: originalWriter,
		stop:   make(chan struct{}),
	}
	c.Set(openAICompactSSEKeepaliveKey, k)
	wrappedWriter := &compactKeepaliveWriter{ResponseWriter: originalWriter, k: k}
	c.Writer = wrappedWriter

	var reqDone <-chan struct{}
	if c.Request != nil {
		reqDone = c.Request.Context().Done()
	}
	go func() {
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-k.stop:
				return
			case <-reqDone:
				return
			case <-timer.C:
			}
			if !k.beat() {
				return
			}
			timer.Reset(interval)
		}
	}()
	return func() {
		k.Stop()
		// Restore the middleware writer when this request releases its compact wrapper.
		if current, ok := c.Writer.(*compactKeepaliveWriter); ok && current == wrappedWriter {
			c.Writer = originalWriter
		}
	}
}

// beat commits headers on the first beat and writes an SSE comment under the mutex.
// A false result tells the goroutine to exit after stopping or a downstream write failure.
func (k *openAICompactSSEKeepalive) beat() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.stopped {
		return false
	}
	if !k.started {
		header := k.writer.Header()
		header.Set("Content-Type", "text/event-stream")
		header.Set("Cache-Control", "no-cache")
		header.Set("Connection", "keep-alive")
		header.Set("X-Accel-Buffering", "no")
		k.writer.WriteHeader(http.StatusOK)
		k.started = true
	}
	n, err := k.writer.Write([]byte(": keepalive\n\n"))
	k.bytes += n
	if err != nil {
		k.stopped = true
		return false
	}
	k.writer.Flush()
	return true
}

// Stop stops heartbeats and is safe to repeat or call concurrently with response writes.
func (k *openAICompactSSEKeepalive) Stop() {
	k.mu.Lock()
	k.markStoppedLocked()
	k.mu.Unlock()
}

func (k *openAICompactSSEKeepalive) markStoppedLocked() {
	if k.stopped {
		return
	}
	k.stopped = true
	close(k.stop)
}

// StopOpenAICompactSSEKeepaliveCommitted stops heartbeats and reports whether they committed HTTP 200.
// It waits for any heartbeat write under the mutex before the caller takes over the writer and chooses JSON or SSE
// error output.
func StopOpenAICompactSSEKeepaliveCommitted(c *gin.Context) bool {
	if c == nil {
		return false
	}
	value, ok := c.Get(openAICompactSSEKeepaliveKey)
	if !ok {
		return false
	}
	k, ok := value.(*openAICompactSSEKeepalive)
	if !ok || k == nil {
		return false
	}
	k.mu.Lock()
	k.markStoppedLocked()
	committed := k.started
	k.mu.Unlock()
	return committed
}

// OpenAICompactKeepaliveAdjustedWrittenSize returns bytes written excluding compact and stream heartbeats.
// Handlers compare values around Forward to detect protocol output that prevents failover.
// Heartbeat-only output returns Gin's unwritten value, -1, so upstream 429/5xx failures can still fail over (#3887).
func OpenAICompactKeepaliveAdjustedWrittenSize(c *gin.Context) int {
	if c == nil || c.Writer == nil {
		return -1
	}
	streamKeepaliveBytes := 0
	if value, ok := c.Get(openAIStreamKeepaliveBytesContextKey); ok {
		streamKeepaliveBytes, _ = value.(int)
	}
	size := c.Writer.Size()
	compactKeepaliveBytes := 0
	if value, ok := c.Get(openAICompactSSEKeepaliveKey); ok {
		if k, valid := value.(*openAICompactSSEKeepalive); valid && k != nil {
			k.mu.Lock()
			size = k.writer.Size()
			compactKeepaliveBytes = k.bytes
			k.mu.Unlock()
		}
	}
	if size < 0 {
		return size
	}
	keepaliveBytes := compactKeepaliveBytes + streamKeepaliveBytes
	if keepaliveBytes <= 0 {
		return size
	}
	if real := size - keepaliveBytes; real > 0 {
		return real
	}
	return -1
}

// suspend idempotently stops heartbeats before response construction, including Header access.
func (w *compactKeepaliveWriter) suspend() {
	if w.k == nil {
		return
	}
	w.k.Stop()
}

func (w *compactKeepaliveWriter) Header() http.Header {
	w.suspend()
	if w.ResponseWriter == nil {
		return http.Header{}
	}
	return w.ResponseWriter.Header()
}

func (w *compactKeepaliveWriter) Write(data []byte) (int, error) {
	w.suspend()
	if w.ResponseWriter == nil {
		return 0, nil
	}
	return w.ResponseWriter.Write(data)
}

func (w *compactKeepaliveWriter) WriteString(s string) (int, error) {
	w.suspend()
	if w.ResponseWriter == nil {
		return 0, nil
	}
	return w.ResponseWriter.WriteString(s)
}

func (w *compactKeepaliveWriter) WriteHeader(code int) {
	w.suspend()
	if w.ResponseWriter == nil {
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *compactKeepaliveWriter) WriteHeaderNow() {
	w.suspend()
	if w.ResponseWriter == nil {
		return
	}
	w.ResponseWriter.WriteHeaderNow()
}

func (w *compactKeepaliveWriter) Flush() {
	w.suspend()
	if w.ResponseWriter == nil {
		return
	}
	w.ResponseWriter.Flush()
}

// Hijack returns an error when the inner writer has been released.
func (w *compactKeepaliveWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.ResponseWriter == nil {
		return nil, nil, errors.New("response writer released")
	}
	return w.ResponseWriter.Hijack()
}

func (w *compactKeepaliveWriter) CloseNotify() <-chan bool {
	if w.ResponseWriter == nil {
		ch := make(chan bool)
		close(ch)
		return ch
	}
	return w.ResponseWriter.CloseNotify()
}

func (w *compactKeepaliveWriter) Pusher() http.Pusher {
	if w.ResponseWriter == nil {
		return nil
	}
	return w.ResponseWriter.Pusher()
}

// Status reads the inner writer under the heartbeat mutex, or returns zero if either is absent.
func (w *compactKeepaliveWriter) Status() int {
	if w.k == nil || w.ResponseWriter == nil {
		return 0
	}
	w.k.mu.Lock()
	defer w.k.mu.Unlock()
	return w.ResponseWriter.Status()
}

func (w *compactKeepaliveWriter) Size() int {
	if w.k == nil || w.ResponseWriter == nil {
		return 0
	}
	w.k.mu.Lock()
	defer w.k.mu.Unlock()
	return w.ResponseWriter.Size()
}

func (w *compactKeepaliveWriter) Written() bool {
	if w.k == nil || w.ResponseWriter == nil {
		return false
	}
	w.k.mu.Lock()
	defer w.k.mu.Unlock()
	return w.ResponseWriter.Written()
}

// RecordOpenAIStreamKeepaliveBytes accumulates bytes written by stream heartbeats.
func RecordOpenAIStreamKeepaliveBytes(c *gin.Context, written int) {
	if c == nil || written <= 0 {
		return
	}
	current := 0
	if value, ok := c.Get(openAIStreamKeepaliveBytesContextKey); ok {
		current, _ = value.(int)
	}
	c.Set(openAIStreamKeepaliveBytesContextKey, current+written)
}

// OpenAIStreamClientOutputStarted checks the local output marker, then the byte count excluding heartbeats.
func OpenAIStreamClientOutputStarted(c *gin.Context, localStarted bool) bool {
	if localStarted {
		return true
	}
	if c == nil || c.Writer == nil {
		return false
	}
	// A compact heartbeat commits HTTP 200 but carries no model output, so safe retries remain possible.
	return OpenAICompactKeepaliveAdjustedWrittenSize(c) >= 0
}
