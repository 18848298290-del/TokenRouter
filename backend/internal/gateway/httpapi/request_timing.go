package httpapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// request_timing.go observes HTTP text admission and streaming delivery across
// native, passthrough, and converted platform paths. Observation follows the
// original body reader and the final HTTP writer, so body replays and discarded
// output staging cannot masquerade as network reads or client delivery. The
// upstream observer sees decoded transport bytes without advancing the reader.
//
// The framing observer retains only the current event and never rewrites,
// prefetches, flushes, or closes the underlying exchange. Completed events are
// classified independently from billing and retry commitment. Failed writes
// discard their observation, while successful writes can establish content
// before a later flush. Every platform shares the same request collector and
// first-event rules, including protocol conversion at the HTTP boundary.

// The request-local writer binding lets upstream sinks identify delivery through
// intervening model-restoration and capture writers without bypassing those writers.
const requestTimingWriterKey = "http_text_timing_writer"

// timingResponseBody observes consumed response bytes without changing read ownership.
type timingResponseBody struct {
	io.ReadCloser               // original upstream response body
	frames        *timingFrames // per-response framing state
	done          func()        // exchange completion observer
	arrivedAt     time.Time     // last underlying read completion
}

// timingWriter observes successful HTTP writes and existing downstream flushes.
type timingWriter struct {
	gin.ResponseWriter                 // original HTTP writer and optional capabilities
	stages             *timing.Stages  // shared request timing state
	ctx                context.Context // request lifecycle observations
	frames             timingFrames    // delivered event framing state
	pending            bool            // unflushed response event data
	// upstreamWrites scopes synchronous upstream sink emission. Heartbeat workers
	// may read the counter concurrently, so source publication uses atomic access.
	upstreamWrites atomic.Int32 // active upstream emission scopes
}

// timingFrames buffers one complete SSE frame or newline-delimited JSON object.
type timingFrames struct {
	stopped bool                 // all required observations already recorded
	line    []byte               // partial input line
	data    []byte               // current SSE data fields
	event   string               // current SSE event name
	observe func(string, string) // synchronous completed payload callback
}

// TextRequestTiming installs admission observation before authentication reads
// the request body. Only POST text endpoints participate; WebSocket, media and
// auxiliary routes keep their existing behavior and observation format.
//
// The original reader, protocol observer and delivered writer share one
// collector. Authentication can replace the body without wrapping its replay,
// and detached attempts retain the collector through context values. Completion
// publishes the frozen split even after rejection or cancellation. HTTP writer
// capabilities continue to delegate to the original writer, and observation
// never admits, retries, releases, or bills a request.
// @project-doc docs/interfaces/http_api.md#response_errors
func TextRequestTiming() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := strings.TrimSuffix(c.Request.URL.Path, "/")
		text := strings.HasSuffix(path, "/messages") || strings.HasSuffix(path, "/responses") || strings.HasSuffix(path, "/responses/compact") || strings.HasSuffix(path, "/chat/completions") || strings.HasSuffix(path, "/systemone") || strings.HasSuffix(path, ":generateContent") || strings.HasSuffix(path, ":streamGenerateContent")
		if c.Request.Method != http.MethodPost || !text {
			c.Next()
			return
		}
		start, _ := c.Request.Context().Value(telemetry.RequestStartedAt).(time.Time)
		if start.IsZero() {
			start = time.Now()
		}
		var stages *timing.Stages
		ctx, stages := timing.WithStages(c.Request.Context(), start, func(resp *http.Response) {
			if resp.Body == nil {
				return
			}
			attempt := stages.Attempt()
			body := &timingResponseBody{ReadCloser: resp.Body, done: func() { stages.FinishAttempt(attempt) }}
			contentType := resp.Header.Get("Content-Type")
			sse := strings.Contains(contentType, "text/event-stream")
			nativeStream := resp.Request != nil && strings.Contains(resp.Request.URL.Path, "streamGenerateContent")
			if resp.StatusCode < http.StatusBadRequest && !strings.Contains(contentType, "eventstream") && (sse || nativeStream || stages.Streaming()) {
				body.frames = &timingFrames{observe: func(data, event string) {
					if event != "@json" || nativeStream {
						stages.ObserveEventAt(attempt, data, event, body.arrivedAt)
					}
				}}
			}
			resp.Body = body
		})
		stages.SetMode(timing.ModeFromContext(ctx))
		stages.SetStream(strings.HasSuffix(path, ":streamGenerateContent"))
		stages.SetContentLength(c.Request.ContentLength)
		ctx = scheduler.WithWaitObservation(ctx, stages.Wait)
		ctx = scheduler.WithAdmissionObservation(ctx, stages.Admit)
		c.Request = c.Request.WithContext(ctx)
		c.Request.Body = stages.ReadBody(c.Request.Body)
		writer := &timingWriter{ResponseWriter: c.Writer, stages: stages, ctx: ctx}
		writer.frames.observe = writer.observe
		c.Writer = writer
		c.Set(requestTimingWriterKey, writer)
		stopCancellation := context.AfterFunc(ctx, func() { writer.disconnected() })
		defer stopCancellation()
		defer func() {
			stages.Finish()
			if ctx.Err() != nil {
				writer.disconnected()
			}
			// No-usage failures keep admission diagnostics; successful performance
			// remains owned by the immutable completion snapshot and usage storage.
			telemetry.UpdateRequest(ctx, func(record *telemetry.RequestRecord) {
				if record.UsageWriteState == "" && (c.Writer.Status() >= 400 || record.State == "failed" || record.State == "canceled") {
					record.FailureTimings = stages.FailureSnapshot()
				}
			})
		}()
		c.Next()
	}
}

// Read forwards bytes and errors unchanged before observing complete events.
// EOF permits an unterminated final event only when its JSON is complete;
// other failures leave incomplete frames unobserved and preserve their error.
func (r *timingResponseBody) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.arrivedAt = time.Now()
	if r.frames != nil {
		r.frames.feed(p[:n])
	}
	if err == io.EOF {
		if r.frames != nil {
			r.frames.finish()
		}
	}
	if err != nil && r.done != nil {
		r.done()
	}
	return n, err
}

// Close releases the upstream body and records this exchange's observed endpoint.
// The response observer owns only measurement; the underlying reader retains its
// existing close errors and connection release behavior.
func (r *timingResponseBody) Close() error {
	err := r.ReadCloser.Close()
	if r.done != nil {
		r.done()
	}
	return err
}

// Write observes complete successful writes while leaving error handling unchanged.
// Only writes scoped by the upstream output sink can establish delivery. Local
// error responses remain outside that scope, even when they use identical SSE.
// Partial or failed writes cannot establish client delivery; their parser state
// is discarded so subsequent writes cannot complete an undelivered frame.
//
// Framing occurs after the original writer returns, so an event buffered by a
// converter is not visible until the final HTTP boundary receives it. Earlier
// successful events retain their first-observation times across later errors.
// The parser can span several successful writes, while only the existing Flush
// call records delivery of pending response data.
func (w *timingWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if err != nil || n != len(p) {
		w.disconnected()
	}
	if w.upstreamWrites.Load() > 0 && err == nil && n == len(p) && (w.stages.Streaming() || strings.Contains(w.Header().Get("Content-Type"), "text/event-stream")) {
		w.frames.feed(p)
	} else {
		w.frames.reset()
	}
	return n, err
}

// WriteString preserves the underlying string-write path and observes successful bytes.
// Partial and failed writes discard incomplete frames just like Write. The
// original byte count and error return unchanged to the response adapter.
func (w *timingWriter) WriteString(s string) (int, error) {
	n, err := w.ResponseWriter.WriteString(s)
	if err != nil || n != len(s) {
		w.disconnected()
	}
	if w.upstreamWrites.Load() > 0 && err == nil && n == len(s) && (w.stages.Streaming() || strings.Contains(w.Header().Get("Content-Type"), "text/event-stream")) {
		w.frames.feed([]byte(s))
	} else {
		w.frames.reset()
	}
	return n, err
}

// observe records actual data and useful content after successful frame writes.
// Heartbeats and non-streaming JSON leave stream milestones absent. The content
// classifier is diagnostic only and never changes gateway retry or billing flags.
//
// A complete data event establishes pending response bytes even when it has no
// useful content, allowing an initial metadata frame to establish the first
// response Flush. Useful content ends further parser work, but its pending flag
// remains available to the following Flush. Thus content observation never
// performs a flush itself or commits staged output early.
func (w *timingWriter) observe(data, event string) {
	if (event == "@json" && !w.stages.Streaming()) || !timingDataEvent(data, event) {
		return
	}
	w.stages.SetStream(true)
	w.pending = true
	if timingVisible(data, event) {
		w.stages.Mark("first_visible_output_ms")
		w.frames.stopped = true
	}
}

// Unwrap retains ResponseController access to the original writer's capabilities.
func (w *timingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush delegates the existing flush and records only pending response events.
// Queue heartbeats, comments and empty flushes do not establish this milestone;
// no additional flush is introduced and ordinary writer ownership remains unchanged.
func (w *timingWriter) Flush() {
	w.ResponseWriter.Flush()
	if w.pending {
		w.stages.Mark("first_downstream_flush_ms")
		w.pending = false
	}
}

// feed accepts borrowed bytes and dispatches only complete event boundaries.
// It handles arbitrary transport chunk splits and CRLF framing without modifying
// the borrowed bytes or retaining an entire response across multiple events.
//
// A transport read or successful HTTP write can end inside a line, inside JSON,
// or between the fields of one frame. Keeping line and frame state separately
// prevents those chunk boundaries from becoming artificial events. Once the
// caller has observed every milestone it needs, parsing stops and retained
// buffers are released on the next feed.
func (f *timingFrames) feed(p []byte) {
	for _, b := range p {
		// Diagnostic framing uses the existing default SSE size ceiling. Oversized
		// observations stop parsing without changing the platform reader's own limit,
		// response bytes, or error handling.
		if len(f.line)+len(f.data) > OpenAIResponseDefaultMaxLineSize {
			f.stopped = true
		}
		if f.stopped {
			f.line = nil
			f.data = nil
			return
		}
		if b == '\n' {
			f.consume(strings.TrimSuffix(string(f.line), "\r"))
			f.line = f.line[:0]
		} else {
			f.line = append(f.line, b)
		}
	}
}

// consume processes one framing line and preserves multiline SSE payload order.
// Comments do not count as data. A newline-delimited complete JSON object is
// reported as a native event rather than an SSE data observation.
func (f *timingFrames) consume(line string) {
	if line == "" {
		f.dispatch()
		return
	}
	if value, ok := strings.CutPrefix(line, "event:"); ok {
		f.event = strings.TrimSpace(value)
		return
	}
	if value, ok := strings.CutPrefix(line, "data:"); ok {
		if len(f.data) > 0 {
			f.data = append(f.data, '\n')
		}
		f.data = append(f.data, strings.TrimPrefix(value, " ")...)
		return
	}
	if len(f.data) == 0 && gjson.Valid(line) {
		f.observe(line, "@json")
	}
}

// dispatch delivers a valid payload and clears framing before another event.
// Invalid or truncated JSON is not timing evidence. DONE remains a complete
// terminal data event even though it is not itself a JSON document.
func (f *timingFrames) dispatch() {
	data := strings.TrimSpace(string(f.data))
	if data == "[DONE]" || gjson.Valid(data) {
		f.observe(data, f.event)
	} else if documents, repaired := openai.SplitConcatenatedJSONDocuments([]byte(data)); repaired {
		// Use the codec's bounded repair rule so observed content matches the valid
		// documents it accepts, while truncated or otherwise invalid payloads remain unobserved.
		for _, document := range documents {
			f.observe(string(document), f.event)
		}
	}
	f.data = f.data[:0]
	f.event = ""
}

// finish processes a complete EOF frame without accepting truncated JSON.
func (f *timingFrames) finish() {
	if f.stopped {
		return
	}
	if len(f.line) > 0 {
		f.consume(strings.TrimSuffix(string(f.line), "\r"))
		f.line = f.line[:0]
	}
	f.dispatch()
}

// reset discards partial delivery evidence after a failed downstream write.
func (f *timingFrames) reset() { f.line = f.line[:0]; f.data = f.data[:0]; f.event = "" }

// timingDataEvent applies the shared protocol event classification for HTTP frames.
func timingDataEvent(data, event string) bool { return timing.DataEvent(data, event) }

// timingVisible applies the shared useful-content classification after successful writes.
func timingVisible(data, event string) bool { return timing.UsefulOutput(data, event) }

// disconnected freezes the observed downstream end before any remaining usage drain.
// Repeated failures retain the earliest timestamp and never erase an existing error;
// request completion can still report the upstream and persistence outcomes later.
func (w *timingWriter) disconnected() {
	timing.ObserveClientDisconnect(w.ctx, time.Now())
}
