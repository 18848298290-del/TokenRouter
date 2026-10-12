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

// Nonstreaming image generation can wait for an OAuth upstream that emits SSE
// internally. JSON whitespace keeps the HTTP response active while the
// eventual image document is still being assembled; the first heartbeat
// commits status 200, so an error arriving afterward uses that status with a
// JSON error body.
//
// Heartbeat writes and stopping share a mutex. Accessing the wrapped writer
// for final headers or output stops future heartbeats and waits for any
// current write to finish. The retained heartbeat byte count lets retry
// checks distinguish committed whitespace from actual response content, even
// after the wrapper has been restored.

// openAIImagesJSONKeepaliveKey identifies retained heartbeat state in Gin so
// error and retry checks can inspect accepted padding bytes.
const openAIImagesJSONKeepaliveKey = "openai_images_json_keepalive"

// openAIImagesJSONKeepalive coordinates whitespace writes with the final
// response writer.
type openAIImagesJSONKeepalive struct {
	// mu protects writer operations and started, stopped, and bytes together. stop
	// is closed while holding mu when ordinary stopping first disables future beats.
	mu      sync.Mutex         // heartbeat and stopping lock
	writer  gin.ResponseWriter // unwrapped original response writer
	started bool               // first heartbeat committed status
	stopped bool               // further heartbeat writes disabled
	bytes   int                // accepted heartbeat byte count
	stop    chan struct{}      // heartbeat exit notification channel
}

// openAIImagesJSONKeepaliveWriter stops heartbeat output when final response
// access begins.
type openAIImagesJSONKeepaliveWriter struct {
	// ResponseWriter and k reference one installed response. The constructor pairs
	// the underlying writer with its heartbeat state; callers serialize final output.
	gin.ResponseWriter                            // wrapped HTTP response writer
	k                  *openAIImagesJSONKeepalive // associated heartbeat coordination state
}

// StartOpenAIImagesJSONKeepalive installs whitespace heartbeats and returns a
// cleanup function. A missing context or writer, or a nonpositive interval,
// returns a no-op cleanup function. The request context can end the heartbeat
// loop while retained state remains available to error handling.
//
// The wrapper stops beats before final header or output operations; heartbeat
// writes use the unwrapped writer so they do not stop themselves. Call
// cleanup at request exit. It disables beats under the same mutex used for
// writing and restores the original writer only when its own wrapper is still
// installed.
func StartOpenAIImagesJSONKeepalive(c *gin.Context, interval time.Duration) func() {
	if c == nil || c.Writer == nil || interval <= 0 {
		return func() {}
	}
	originalWriter := c.Writer
	k := &openAIImagesJSONKeepalive{
		writer: originalWriter,
		stop:   make(chan struct{}),
	}
	c.Set(openAIImagesJSONKeepaliveKey, k)
	wrappedWriter := &openAIImagesJSONKeepaliveWriter{ResponseWriter: originalWriter, k: k}
	c.Writer = wrappedWriter

	var reqDone <-chan struct{}
	if c.Request != nil {
		reqDone = c.Request.Context().Done()
	}
	// The timer exits on explicit stopping or request cancellation. Writes use the
	// saved writer so heartbeat output cannot trigger the final-output wrapper.
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

	// Stop before restoring the writer so a final response cannot race a padding
	// write; retain a replacement wrapper installed by another owner.
	return func() {
		k.Stop()
		if current, ok := c.Writer.(*openAIImagesJSONKeepaliveWriter); ok && current == wrappedWriter {
			c.Writer = originalWriter
		}
	}
}

// beat writes and flushes one JSON whitespace heartbeat unless stopping has
// already won. It returns false after stopping or a writer error, allowing
// the timer loop to exit. The first heartbeat attempt sets JSON headers and
// commits status 200 before writing padding.
//
// The mutex covers header changes, writing, byte accounting, and flushing.
// Stop and final response access wait for this sequence, so padding cannot
// interleave with a final JSON document. Accepted bytes remain counted even
// when Write also reports an error.
func (k *openAIImagesJSONKeepalive) beat() bool {
	// Keep header commitment and padding accounting inside the stopping lock so
	// final output begins after this heartbeat finishes writing and flushing.
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.stopped {
		return false
	}
	if !k.started {
		header := k.writer.Header()
		header.Set("Content-Type", "application/json; charset=utf-8")
		header.Set("Cache-Control", "no-cache")
		header.Set("X-Accel-Buffering", "no")
		k.writer.WriteHeader(http.StatusOK)
		k.started = true
	}
	n, err := k.writer.Write([]byte(" \n"))
	k.bytes += n
	// A failed client write ends this loop, while accepted padding stays counted
	// for subsequent error and output observations on the same response.
	if err != nil {
		k.stopped = true
		return false
	}
	k.writer.Flush()
	return true
}

// Stop disables further heartbeat writes and signals the timer loop when
// stopping first occurs. It waits for any heartbeat holding the mutex;
// repeated calls leave the current state unchanged.
func (k *openAIImagesJSONKeepalive) Stop() {
	k.mu.Lock()
	k.markStoppedLocked()
	k.mu.Unlock()
}

// markStoppedLocked disables beats and closes their exit channel on the first
// ordinary stop. Callers hold mu so concurrent cleanup and writer access
// cannot close that channel twice.
func (k *openAIImagesJSONKeepalive) markStoppedLocked() {
	if k.stopped {
		return
	}
	// Publish stopping and notify the timer within one locked sequence so multiple
	// cleanup paths cannot close the exit channel more than once.
	k.stopped = true
	close(k.stop)
}

// StopOpenAIImagesJSONKeepaliveCommitted disables beats and reports whether a
// heartbeat started the status-200 response. Missing heartbeat state returns
// false. The shared mutex waits for an active heartbeat write, while the
// timer goroutine observes stopping separately.
func StopOpenAIImagesJSONKeepaliveCommitted(c *gin.Context) bool {
	k := openAIImagesJSONKeepaliveFromContext(c)
	if k == nil {
		return false
	}
	k.mu.Lock()
	k.markStoppedLocked()
	committed := k.started
	k.mu.Unlock()
	return committed
}

// OpenAIImagesJSONKeepalivePresent reports whether Gin contains typed image
// heartbeat state. Presence includes the interval before the first beat and
// the retained state after stopping, allowing error writers to interpret
// padding.
func OpenAIImagesJSONKeepalivePresent(c *gin.Context) bool {
	return openAIImagesJSONKeepaliveFromContext(c) != nil
}

// OpenAIImagesJSONKeepaliveAdjustedWrittenSize reports accepted response
// bytes after subtracting heartbeat padding. Missing context or writer
// returns -1, and a writer without heartbeat state supplies its own Size
// value. A heartbeat response containing no positive remaining byte count
// also returns -1.
//
// Retry callers compare these observations before and after an attempt. The
// mutex gives byte count and original writer size one consistent observation,
// so padding alone can commit HTTP status while the attempt remains eligible
// for output-based retry checks.
func OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c *gin.Context) int {
	if c == nil || c.Writer == nil {
		return -1
	}
	k := openAIImagesJSONKeepaliveFromContext(c)
	if k == nil {
		return c.Writer.Size()
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	size := k.writer.Size()
	if size < 0 {
		return size
	}
	// Treat accepted whitespace alone as absent business output so provider retry
	// checks remain usable after the first heartbeat has committed HTTP status.
	if real := size - k.bytes; real > 0 {
		return real
	}
	return -1
}

// openAIImagesJSONKeepaliveFromContext retrieves typed heartbeat state
// retained on the request. Missing contexts, absent keys, and values of
// another type return nil, letting response helpers work before installation.
func openAIImagesJSONKeepaliveFromContext(c *gin.Context) *openAIImagesJSONKeepalive {
	if c == nil {
		return nil
	}
	value, ok := c.Get(openAIImagesJSONKeepaliveKey)
	if !ok {
		return nil
	}
	k, _ := value.(*openAIImagesJSONKeepalive)
	return k
}

// suspend stops the associated heartbeat before a final response operation
// proceeds. A wrapper without heartbeat state needs no coordination, while
// Stop waits for any active padding write.
func (w *openAIImagesJSONKeepaliveWriter) suspend() {
	if w.k != nil {
		w.k.Stop()
	}
}

// Header stops heartbeat writes and returns the underlying mutable header
// map. A missing writer returns a fresh empty map; accessing headers
// therefore ends heartbeats even before response bytes are written.
func (w *openAIImagesJSONKeepaliveWriter) Header() http.Header {
	w.suspend()
	if w.ResponseWriter == nil {
		return http.Header{}
	}
	return w.ResponseWriter.Header()
}

// Write stops heartbeat writes before forwarding final response bytes to the
// underlying writer. A missing writer returns zero and nil, so the return
// value alone does not guarantee delivery.
func (w *openAIImagesJSONKeepaliveWriter) Write(data []byte) (int, error) {
	w.suspend()
	if w.ResponseWriter == nil {
		return 0, nil
	}
	return w.ResponseWriter.Write(data)
}

// WriteString stops heartbeat writes before forwarding the supplied string to
// the underlying writer. A missing writer returns zero and nil, so callers
// still interpret the returned byte count.
func (w *openAIImagesJSONKeepaliveWriter) WriteString(s string) (int, error) {
	w.suspend()
	if w.ResponseWriter == nil {
		return 0, nil
	}
	return w.ResponseWriter.WriteString(s)
}

// WriteHeader stops heartbeat writes before requesting the supplied HTTP
// status from the underlying writer. A missing writer is ignored, and an
// already committed heartbeat status follows the writer's rules.
func (w *openAIImagesJSONKeepaliveWriter) WriteHeader(code int) {
	w.suspend()
	if w.ResponseWriter != nil {
		w.ResponseWriter.WriteHeader(code)
	}
}

// WriteHeaderNow stops heartbeat writes before asking the underlying writer
// to commit its current status. A missing writer is ignored; stopping
// coordinates pending padding even when no final body follows.
func (w *openAIImagesJSONKeepaliveWriter) WriteHeaderNow() {
	w.suspend()
	if w.ResponseWriter != nil {
		w.ResponseWriter.WriteHeaderNow()
	}
}

// Flush stops heartbeat writes before flushing the underlying response
// writer. A missing writer is ignored, and the heartbeat goroutine uses its
// original writer directly for its own flushes.
func (w *openAIImagesJSONKeepaliveWriter) Flush() {
	w.suspend()
	if w.ResponseWriter != nil {
		w.ResponseWriter.Flush()
	}
}

// Hijack delegates connection ownership transfer and returns an error when
// the underlying writer is missing. Callers stop installed heartbeats before
// hijacking because this method leaves heartbeat coordination untouched.
func (w *openAIImagesJSONKeepaliveWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.ResponseWriter == nil {
		return nil, nil, errors.New("response writer released")
	}
	return w.ResponseWriter.Hijack()
}

// CloseNotify returns the underlying writer's connection-close notification
// channel. A missing writer returns an already closed channel, allowing
// callers waiting for departure to finish immediately without a live
// connection.
func (w *openAIImagesJSONKeepaliveWriter) CloseNotify() <-chan bool {
	if w.ResponseWriter == nil {
		ch := make(chan bool)
		close(ch)
		return ch
	}
	return w.ResponseWriter.CloseNotify()
}

// Pusher returns the underlying writer's HTTP push capability, or nil when
// the writer is missing. Reading this capability leaves heartbeat activity
// unchanged; downstream code checks the result before pushing.
func (w *openAIImagesJSONKeepaliveWriter) Pusher() http.Pusher {
	if w.ResponseWriter == nil {
		return nil
	}
	return w.ResponseWriter.Pusher()
}

// Status observes the underlying HTTP status while excluding concurrent
// heartbeat writer mutations. Missing heartbeat state or an underlying writer
// returns zero, and observing status leaves future beats enabled.
func (w *openAIImagesJSONKeepaliveWriter) Status() int {
	if w.k == nil || w.ResponseWriter == nil {
		return 0
	}
	w.k.mu.Lock()
	defer w.k.mu.Unlock()
	return w.ResponseWriter.Status()
}

// Size observes accepted underlying response bytes while excluding concurrent
// heartbeat writes. Missing heartbeat state or an underlying writer returns
// zero; the reported size includes padding and leaves beats enabled.
func (w *openAIImagesJSONKeepaliveWriter) Size() int {
	if w.k == nil || w.ResponseWriter == nil {
		return 0
	}
	w.k.mu.Lock()
	defer w.k.mu.Unlock()
	return w.ResponseWriter.Size()
}

// Written observes whether the underlying HTTP response has committed under
// the heartbeat mutex. Missing heartbeat state or an underlying writer
// returns false; heartbeat-only commitment also counts as written.
func (w *openAIImagesJSONKeepaliveWriter) Written() bool {
	if w.k == nil || w.ResponseWriter == nil {
		return false
	}
	w.k.mu.Lock()
	defer w.k.mu.Unlock()
	return w.ResponseWriter.Written()
}
