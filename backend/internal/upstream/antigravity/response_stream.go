package antigravity

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	protocolgemini "github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Antigravity generation arrives as Gemini data inside v1internal SSE lines,
// even for buffered client requests. Streaming adapters observe raw lines
// before output and keep reading after a client write failure when the
// upstream reader continues, allowing later usage to reach attempt
// observation.
//
// Scanner workers own pooled buffers until reading ends; handler return
// signals delivery stop, while the exchange owner closes the body to release
// a blocked read. Idle timeout follows scanned-line activity, while keepalive
// follows delivered-line quiet time. Buffered collection keeps content-bearing
// output when metadata arrives later, then merges first-candidate parts with
// deliberately shallow map ownership. Ordinary text runs can lose per-part
// metadata, and image/text merging has separate key-presence rules that affect
// the selected final response.

// ResponseOptions supplies stream limits, raw observation, tool restoration,
// and representation-specific failure callbacks.
type ResponseOptions struct {
	// ObserveRaw optionally receives each scanned line synchronously before
	// parsing/output; Executor chains it with attempt model and usage
	// observation. Callbacks leave supplied bytes unchanged and copy retained
	// mutable data before the owner continues synchronous processing.
	ObserveRaw func([]byte)
	// MaxLineSize is scanner-growth bytes, with nonpositive selecting the
	// default. StreamDataIntervalTimeout and StreamKeepaliveInterval are
	// idle-timeout seconds and keepalive seconds; nonpositive intervals disable
	// their timers.
	MaxLineSize, StreamDataIntervalTimeout, StreamKeepaliveInterval int
	// ReverseTools restores provider-mapped tool names in compatible serialized
	// output; required when a selected compatibility adapter emits or collects
	// payloads.
	ReverseTools func([]byte) []byte
	// ClaudeError maps buffered Messages collection failures; CompatError maps
	// compatible decoding, serialization, or tool-restoration failures. Both
	// receive output status, error type, and client-facing message when their
	// representation reaches those failures.
	ClaudeError, CompatError func(int, string, string) error
	// MapCollectionError maps Claude collection failures for OpenAI buffered
	// output; required when compatibility collection returns a failure.
	MapCollectionError func(error) error
	// Failover supplies the selected empty-stream failure or switch signal;
	// adapters require it when their representation-specific empty conditions are
	// reached.
	Failover func([]byte) error
	// IsFailover recognizes supplied empty-stream signals before buffered
	// Messages error mapping; required when that handler receives a collection
	// error.
	IsFailover func(error) bool
	// MarkCommitted records commitment after compatible error emission, even if
	// writing fails; required on that selected post-content error path.
	MarkCommitted func()
}

type (
	// ResponseAdapter uses Options to adapt one upstream stream and leaves final
	// response-body closing to the exchange owner.
	ResponseAdapter struct{ Options ResponseOptions } // stream adaptation and failure callbacks
	// StreamResult carries adapter usage, first-token timing, and client-write
	// observations for attempt completion.
	StreamResult struct {
		// Usage references collected adapter observations; streaming paths can
		// return this shared state after disconnect, while Executor separately
		// observes raw usage.
		Usage *upstream.TokenUsage
		// FirstTokenMs is elapsed milliseconds from the supplied start time to the
		// path-selected first payload or event; nil means unobserved.
		FirstTokenMs *int
		// ClientDisconnect reports a failed client write or a handled
		// cancelled/read-failure path according to the selected response adapter.
		ClientDisconnect bool
	}
)

// ClientWriter writes and flushes synchronously while retaining a
// client-disconnect observation for stream handlers.
type ClientWriter struct {
	// w is the borrowed stream destination; ClientWriter writes synchronously and
	// leaves its closing or request lifecycle to the output owner.
	w upstream.OutputWriter
	// flusher is required for successful writes and supplies synchronous flush
	// calls; its interface reports no flush error to ClientWriter.
	flusher http.Flusher
	// disconnected stays true after a write error, suppressing later writes while
	// the owning handler can continue upstream usage collection.
	disconnected bool
	prefix       string // response-path diagnostic log prefix
	// beforeFirstWrite optionally prepares headers before output; it is cleared
	// before invocation so serialized reentry does not invoke the hook again.
	beforeFirstWrite func()
}

// NewClientWriter constructs a serial-use stream writer borrowing w and a
// nonnil flusher. Callers own resource closing and decide whether to continue
// upstream collection after Disconnected becomes true.
func NewClientWriter(w upstream.OutputWriter, flusher http.Flusher, prefix string) *ClientWriter {
	return &ClientWriter{w: w, flusher: flusher, prefix: prefix}
}

// Write prepares the first output, writes p synchronously, and flushes after a
// successful write. A prior disconnect or write error returns false; the
// flusher reports no delivery error, and callers serialize access.
func (cw *ClientWriter) Write(p []byte) bool {
	if cw.disconnected {
		return false
	}
	cw.prepareFirstWrite()
	if _, err := cw.w.Write(p); err != nil {
		cw.markDisconnected()
		return false
	}
	cw.flusher.Flush()
	return true
}

// Fprintf prepares the first output, formats into the borrowed destination,
// and flushes on successful writing. Write failure or prior disconnect returns
// false; callers serialize access and own downstream resource cleanup.
func (cw *ClientWriter) Fprintf(format string, args ...any) bool {
	if cw.disconnected {
		return false
	}
	cw.prepareFirstWrite()
	if _, err := fmt.Fprintf(cw.w, format, args...); err != nil {
		cw.markDisconnected()
		return false
	}
	cw.flusher.Flush()
	return true
}

// Disconnected reports whether this writer has already observed a client write
// failure.
func (cw *ClientWriter) Disconnected() bool { return cw.disconnected }

// prepareFirstWrite invokes the optional header-preparation hook before its
// first participating write. It clears the hook before invocation to tolerate
// serialized reentry; callers serialize access to writer state.
//
// Clearing precedes callback execution because a nested write can invoke this
// helper again. That nested write can begin the response before the outer
// write emits bytes, so the hook prepares its headers before starting nested
// output. A nested write failure can mark disconnect; this hook itself does
// not abort the outer Write or Fprintf call.
func (cw *ClientWriter) prepareFirstWrite() {
	if cw.beforeFirstWrite == nil {
		return
	}
	prepare := cw.beforeFirstWrite
	// Clear the hook before invoking it so a callback that performs a serialized
	// nested write does not recursively prepare the same output head.
	cw.beforeFirstWrite = nil
	prepare()
}

// markDisconnected retains the client-write failure observation and logs its
// response path.
func (cw *ClientWriter) markDisconnected() {
	cw.disconnected = true
	logger.LegacyPrintf("service.antigravity_gateway", "Client disconnected during streaming (%s), continuing to drain upstream for billing", cw.prefix)
}

// HandleStreamReadError classifies cancellation or a read error after client
// disconnect as handled collection termination. A handled result tells the
// caller to return collected usage with disconnect=true; other errors retain
// ordinary failure handling.
func HandleStreamReadError(err error, clientDisconnected bool, prefix string) (disconnect bool, handled bool) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		logger.LegacyPrintf("service.antigravity_gateway", "Context canceled during streaming (%s), returning collected usage", prefix)
		return true, true
	}
	if clientDisconnected {
		logger.LegacyPrintf("service.antigravity_gateway", "Upstream read error after client disconnect (%s): %v, returning collected usage", prefix, err)
		return true, true
	}
	return false, false
}

// HandleGeminiStreamingResponse unwraps v1internal data lines into Gemini SSE
// while observing raw input. Callers supply a nonnil response/body and
// flushing output writer, then close the body after return to release any
// blocked scanner.
//
// FirstTokenMs records the first nonempty non-DONE data payload regardless of
// semantic content. Latest projected usage replaces earlier adapter usage,
// while Executor separately accumulates raw fields. Write failures suppress
// later output but scanning continues. Read cancellation, or read failure
// after client disconnect, returns collected usage with disconnect=true;
// other read errors or idle timeout can fail. Scanned lines reset idle
// activity, delivered lines reset keepalive quiet time, and keepalive bytes
// can commit output before model content.
func (s *ResponseAdapter) HandleGeminiStreamingResponse(c *upstream.OutputContext, resp *http.Response, startTime time.Time) (*StreamResult, error) {
	c.Status(resp.StatusCode)
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "text/event-stream; charset=utf-8"
	}
	c.Header("Content-Type", contentType)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := upstream.DefaultSSELineLimit
	if s.Options.MaxLineSize > 0 {
		maxLineSize = s.Options.MaxLineSize
	}
	scanBuf := httpclient.GetSSEScannerBuf64K()
	// Use pooled 64-KiB capacity before allowing configured growth; a smaller
	// selected maximum cannot shrink this initial buffer or enforce its lower
	// cap.
	scanner.Buffer(scanBuf[:0], maxLineSize)
	usage := &upstream.TokenUsage{}
	var firstTokenMs *int

	// scanEvent transports a scanned line or terminal read error from the worker
	// to its owning response handler.
	type scanEvent struct {
		line string // copied scanned input line
		err  error  // terminal scanner read failure
	}
	// Queue sixteen line/error events so reading can advance separately from
	// handler timeout selection; the worker owns the pooled buffer until its
	// scanning work exits.
	events := make(chan scanEvent, 16)
	done := make(chan struct{})
	sendEvent := func(ev scanEvent) bool {
		select {
		case events <- ev:
			return true
		case <-done:
			return false
		}
	}
	var lastReadAt int64
	atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
	go func(scanBuf *httpclient.SSEScannerBuf64K) {
		defer httpclient.PutSSEScannerBuf64K(scanBuf)
		defer close(events)
		for scanner.Scan() {
			atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
			if !sendEvent(scanEvent{line: scanner.Text()}) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			_ = sendEvent(scanEvent{err: err})
		}
	}(scanBuf)
	// Signal stop on handler return so the sender can select termination; a
	// competing ready send can still win, and body closing releases blocked
	// Scan calls.
	defer close(done)

	// Check scanned-line activity on interval ticks, including nondata lines;
	// a disabled interval leaves a nil channel so this select path cannot fire.
	streamInterval := time.Duration(0)
	if s.Options.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.Options.StreamDataIntervalTimeout) * time.Second
	}
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

	// Send keepalive after delivered-line quiet time while the client remains
	// connected; pings do not reset that time, so subsequent ticks can continue
	// pinging.
	keepaliveInterval := time.Duration(0)
	if s.Options.StreamKeepaliveInterval > 0 {
		keepaliveInterval = time.Duration(s.Options.StreamKeepaliveInterval) * time.Second
	}
	var keepaliveTicker *time.Ticker
	if keepaliveInterval > 0 {
		keepaliveTicker = time.NewTicker(keepaliveInterval)
		defer keepaliveTicker.Stop()
	}
	var keepaliveCh <-chan time.Time
	if keepaliveTicker != nil {
		keepaliveCh = keepaliveTicker.C
	}
	lastDataAt := time.Now()

	cw := NewClientWriter(c.Writer, flusher, "antigravity gemini")

	// Suppress duplicate terminal error events after the first attempted emission
	// or a recorded disconnect; this direct writer does not itself update the
	// ClientWriter failure flag.
	errorEventSent := false
	sendErrorEvent := func(reason string) {
		if errorEventSent || cw.Disconnected() {
			return
		}
		errorEventSent = true
		_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: {\"error\":\"%s\"}\n\n", reason)
		flusher.Flush()
	}

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: cw.Disconnected()}, nil
			}
			if ev.err != nil {
				if disconnect, handled := HandleStreamReadError(ev.err, cw.Disconnected(), "antigravity gemini"); handled {
					return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: disconnect}, nil
				}
				if errors.Is(ev.err, bufio.ErrTooLong) {
					logger.LegacyPrintf("service.antigravity_gateway", "SSE line too long (antigravity): max_size=%d error=%v", maxLineSize, ev.err)
					sendErrorEvent("response_too_large")
					return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs}, ev.err
				}
				sendErrorEvent("stream_read_error")
				return nil, ev.err
			}

			lastDataAt = time.Now()

			line := ev.line
			s.observeRaw([]byte(line))
			trimmed := strings.TrimRight(line, "\r\n")
			if after, ok0 := strings.CutPrefix(trimmed, "data:"); ok0 {
				payload := strings.TrimSpace(after)
				if payload == "" || payload == "[DONE]" {
					cw.Fprintf("%s\n", line)
					continue
				}

				inner, parseErr := s.UnwrapV1InternalResponse([]byte(payload))
				if parseErr == nil && inner != nil {
					payload = string(inner)
				}

				// Replace adapter usage from the latest projected payload, while the
				// executor raw observer independently accumulates sparse fields from
				// earlier and later lines.
				if u := bridge.NativeUsageProjection(bridge.NativeExtractGeminiUsage(inner)); u != nil {
					usage = u
				}
				// Tool diagnostics require complete JSON decoding; decode failure
				// leaves the forwarding payload and already projected usage available.
				var parsed map[string]any
				if json.Unmarshal(inner, &parsed) == nil {
					// Log malformed tool output reported by the upstream.
					if candidates, ok := parsed["candidates"].([]any); ok && len(candidates) > 0 {
						if cand, ok := candidates[0].(map[string]any); ok {
							if fr, ok := cand["finishReason"].(string); ok && fr == "MALFORMED_FUNCTION_CALL" {
								logger.LegacyPrintf("service.antigravity_gateway", "[Antigravity] MALFORMED_FUNCTION_CALL detected in forward stream")
								if content, ok := cand["content"]; ok {
									if b, err := json.Marshal(content); err == nil {
										logger.LegacyPrintf("service.antigravity_gateway", "[Antigravity] Malformed content: %s", string(b))
									}
								}
							}
						}
					}
				}

				if firstTokenMs == nil {
					ms := int(time.Since(startTime).Milliseconds())
					firstTokenMs = &ms
				}

				cw.Fprintf("data: %s\n\n", payload)
				continue
			}

			cw.Fprintf("%s\n", line)

		case <-intervalCh:
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if time.Since(lastRead) < streamInterval {
				continue
			}
			if cw.Disconnected() {
				logger.LegacyPrintf("service.antigravity_gateway", "Upstream timeout after client disconnect (antigravity gemini), returning collected usage")
				return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: true}, nil
			}
			logger.LegacyPrintf("service.antigravity_gateway", "Stream data interval timeout (antigravity)")
			sendErrorEvent("stream_timeout")
			return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs}, fmt.Errorf("stream data interval timeout")

		case <-keepaliveCh:
			if cw.Disconnected() {
				continue
			}
			if time.Since(lastDataAt) < keepaliveInterval {
				continue
			}
			// An SSE comment keeps the connection active without adding response content.
			if !cw.Fprintf(":\n\n") {
				logger.LegacyPrintf("service.antigravity_gateway", "Client disconnected during keepalive ping (antigravity gemini), continuing to drain upstream for billing")
				continue
			}
		}
	}
}

// HandleGeminiStreamToNonStreaming collects Gemini SSE into one JSON response
// and returns latest projected usage. Callers keep output and response body
// valid until completion and close the body afterward; read/idle errors return
// before buffered output.
//
// First timing begins at successful JSON decoding into the map target,
// including metadata-only objects and null. Null leaves the retained map nil
// and can still select empty-response failover.
//
// The final map prefers the latest content-bearing candidate; all
// first-candidate text, including thought text, is collected separately from
// object inlineData parts. Images append only when no existing inlineData key
// is present, and concatenated text replaces the first text-key part. When
// both retained snapshots are nil, Failover handles the empty result; retained
// metadata-only input can still produce buffered JSON.
//
// Buffered write errors stay in OutputContext.Err; the collected snapshot
// keeps ClientDisconnect unchanged by that output attempt.
func (s *ResponseAdapter) HandleGeminiStreamToNonStreaming(c *upstream.OutputContext, resp *http.Response, startTime time.Time) (*StreamResult, error) {
	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := upstream.DefaultSSELineLimit
	if s.Options.MaxLineSize > 0 {
		maxLineSize = s.Options.MaxLineSize
	}
	scanBuf := httpclient.GetSSEScannerBuf64K()
	// Use pooled 64-KiB capacity before allowing configured growth; a smaller
	// selected maximum cannot shrink this initial buffer or enforce its lower
	// cap.
	scanner.Buffer(scanBuf[:0], maxLineSize)

	usage := &upstream.TokenUsage{}
	var firstTokenMs *int
	var last map[string]any
	var lastWithParts map[string]any
	var collectedImageParts []map[string]any // image parts across chunks
	var collectedTextParts []string          // text fragments across chunks

	// scanEvent transports a scanned line or terminal read error from the worker
	// to its owning response handler.
	type scanEvent struct {
		line string // copied scanned input line
		err  error  // terminal scanner read failure
	}

	// Queue sixteen line/error events so reading can advance separately from
	// handler timeout selection; the worker owns the pooled buffer until its
	// scanning work exits.
	events := make(chan scanEvent, 16)
	done := make(chan struct{})
	sendEvent := func(ev scanEvent) bool {
		select {
		case events <- ev:
			return true
		case <-done:
			return false
		}
	}

	var lastReadAt int64
	atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
	go func(scanBuf *httpclient.SSEScannerBuf64K) {
		defer httpclient.PutSSEScannerBuf64K(scanBuf)
		defer close(events)
		for scanner.Scan() {
			atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
			if !sendEvent(scanEvent{line: scanner.Text()}) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			_ = sendEvent(scanEvent{err: err})
		}
	}(scanBuf)
	// Signal stop on handler return so the sender can select termination; a
	// competing ready send can still win, and body closing releases blocked
	// Scan calls.
	defer close(done)

	// Check scanned-line activity on interval ticks, including nondata lines;
	// a disabled interval leaves a nil channel so this select path cannot fire.
	streamInterval := time.Duration(0)
	if s.Options.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.Options.StreamDataIntervalTimeout) * time.Second
	}
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				goto returnResponse
			}
			if ev.err != nil {
				if errors.Is(ev.err, bufio.ErrTooLong) {
					logger.LegacyPrintf("service.antigravity_gateway", "SSE line too long (antigravity non-stream): max_size=%d error=%v", maxLineSize, ev.err)
				}
				return nil, ev.err
			}

			line := ev.line
			s.observeRaw([]byte(line))
			trimmed := strings.TrimRight(line, "\r\n")

			if !strings.HasPrefix(trimmed, "data:") {
				continue
			}

			payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}

			inner, parseErr := s.UnwrapV1InternalResponse([]byte(payload))
			if parseErr != nil {
				continue
			}

			// Reject incomplete decoded input before retaining content or timing;
			// raw observers have already received this line for independent accounting.
			var parsed map[string]any
			if err := json.Unmarshal(inner, &parsed); err != nil {
				continue
			}

			// Successful decoding into this map target supplies first timing before
			// semantic selection; null also reaches here even though it leaves parsed
			// nil for the eventual empty-response decision.
			if firstTokenMs == nil {
				ms := int(time.Since(startTime).Milliseconds())
				firstTokenMs = &ms
			}

			last = parsed

			// Retain latest projected usage separately from the selected
			// content-bearing response, because a later metadata chunk can carry tokens
			// without replacing collected output.
			if u := bridge.NativeUsageProjection(bridge.NativeExtractGeminiUsage(inner)); u != nil {
				usage = u
			}

			// Log malformed tool output reported by the upstream.
			if candidates, ok := parsed["candidates"].([]any); ok && len(candidates) > 0 {
				if cand, ok := candidates[0].(map[string]any); ok {
					if fr, ok := cand["finishReason"].(string); ok && fr == "MALFORMED_FUNCTION_CALL" {
						logger.LegacyPrintf("service.antigravity_gateway", "[Antigravity] MALFORMED_FUNCTION_CALL detected in forward non-stream collect")
						if content, ok := cand["content"]; ok {
							if b, err := json.Marshal(content); err == nil {
								logger.LegacyPrintf("service.antigravity_gateway", "[Antigravity] Malformed content: %s", string(b))
							}
						}
					}
				}
			}

			// Retain the last content-bearing map before a metadata-only tail arrives;
			// separately collect first-candidate image maps and every text fragment,
			// including thought text.
			if parts := bridge.NativeExtractGeminiParts(parsed); len(parts) > 0 {
				lastWithParts = parsed
				for _, part := range parts {
					if inlineData, ok := part["inlineData"].(map[string]any); ok {
						collectedImageParts = append(collectedImageParts, part)
						_ = inlineData
					}
					if text, ok := part["text"].(string); ok && text != "" {
						collectedTextParts = append(collectedTextParts, text)
					}
				}
			}

		case <-intervalCh:
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if time.Since(lastRead) < streamInterval {
				continue
			}
			logger.LegacyPrintf("service.antigravity_gateway", "Stream data interval timeout (antigravity non-stream)")
			return nil, fmt.Errorf("stream data interval timeout")
		}
	}

returnResponse:
	finalResponse := bridge.NativePickGeminiCollectResult(last, lastWithParts)

	// Invoke empty-stream policy only when no decoded map was retained, allowing
	// metadata-only Gemini input to remain a buffered response while unusable
	// input selects failover.
	if last == nil && lastWithParts == nil {
		logger.LegacyPrintf("service.antigravity_gateway", "[antigravity-Forward] warning: empty stream response (gemini non-stream), triggering failover")
		return nil, s.Options.Failover([]byte(`{"error":"empty stream response from upstream"}`))
	}

	if len(collectedImageParts) > 0 {
		finalResponse = MergeImagePartsToResponse(finalResponse, collectedImageParts)
	}

	if len(collectedTextParts) > 0 {
		finalResponse = MergeTextPartsToResponse(finalResponse, collectedTextParts)
	}

	respBody, err := json.Marshal(finalResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal response: %w", err)
	}
	c.Data(http.StatusOK, "application/json", respBody)

	return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs}, nil
}

// GetOrCreateGeminiParts returns a shallow top-level response copy,
// first-candidate parts, and a setter for those parts. Callers provide
// exclusive access to nested response state because candidate slices and maps
// can remain shared.
//
// Missing or differently shaped candidates/content are created or replaced,
// including assignment into a retained candidate slice before the setter runs.
// Existing content role and other candidate data remain when usable. The
// setter writes content.parts and the copied top-level candidates reference;
// it does not clone nested parts or payload maps. Nil input can therefore
// become a newly shaped result, while existing input nested data can change.
func GetOrCreateGeminiParts(response map[string]any) (result map[string]any, existingParts []any, setParts func([]any)) {
	// Copy only the top map so usable nested candidate/content references remain
	// shared; shape repair can mutate those references before the returned
	// setter is invoked.
	result = make(map[string]any)
	maps.Copy(result, response)

	candidates, ok := result["candidates"].([]any)
	if !ok || len(candidates) == 0 {
		candidates = []any{map[string]any{}}
	}

	candidate, ok := candidates[0].(map[string]any)
	if !ok {
		candidate = make(map[string]any)
		candidates[0] = candidate
	}

	content, ok := candidate["content"].(map[string]any)
	if !ok {
		content = map[string]any{"role": "model"}
		candidate["content"] = content
	}

	existingParts, ok = content["parts"].([]any)
	if !ok {
		existingParts = []any{}
	}

	setParts = func(newParts []any) {
		content["parts"] = newParts
		result["candidates"] = candidates
	}

	return result, existingParts, setParts
}

// MergeCollectedPartsToResponse replaces first-candidate parts with the
// collected arrival sequence. Empty collection returns response as supplied;
// otherwise the shallow response helper can mutate shared candidate/content
// state, so callers isolate nested data before concurrent use.
//
// Adjacent ordinary text becomes a fresh text-only map, discarding other
// metadata from those ordinary parts. Thoughts and nontext parts flush that
// run and retain their original map references and order. A final flush emits
// any trailing nonempty text before the setter replaces parts; empty ordinary
// text runs emit no part. Collected tool arguments, images, and thought
// metadata thus remain shared with their source maps.
func MergeCollectedPartsToResponse(response map[string]any, collectedParts []map[string]any) map[string]any {
	if len(collectedParts) == 0 {
		return response
	}

	result, _, setParts := GetOrCreateGeminiParts(response)

	var mergedParts []any
	var textBuffer strings.Builder

	flushTextBuffer := func() {
		if textBuffer.Len() > 0 {
			mergedParts = append(mergedParts, map[string]any{
				"text": textBuffer.String(),
			})
			textBuffer.Reset()
		}
	}

	for _, part := range collectedParts {
		if text, ok := part["text"].(string); ok {
			if thought, _ := part["thought"].(bool); thought {
				// Flush preceding ordinary text before a thought map so its metadata and
				// identity remain separate from the newly created text-only run.
				flushTextBuffer()
				mergedParts = append(mergedParts, part)
			} else {
				// Builder writes return no error. Ordinary text accumulates until its
				// run is emitted as a fresh map that omits the source parts metadata.
				_, _ = textBuffer.WriteString(text)
			}
		} else {
			// Flush ordinary text before a nontext map so tool/image order follows
			// arrival, retaining that original map and its nested payload references.
			flushTextBuffer()
			mergedParts = append(mergedParts, part)
		}
	}

	flushTextBuffer()

	setParts(mergedParts)
	return result
}

// MergeImagePartsToResponse appends collected image maps when existing
// first-candidate parts have no inlineData key. Empty image input returns
// response as supplied; otherwise candidate/content state and appended map
// values can remain shared.
//
// Any existing inlineData key suppresses all appends, even when its value is
// null or empty, because the decision uses key presence rather than decoded
// image validity. Otherwise images follow existing parts in supplied order,
// potentially reusing the parts slice backing array. The shallow setter
// publishes that sequence; callers provide stable exclusive nested state
// during merging.
func MergeImagePartsToResponse(response map[string]any, imageParts []map[string]any) map[string]any {
	if len(imageParts) == 0 {
		return response
	}

	result, existingParts, setParts := GetOrCreateGeminiParts(response)

	// Inspect existing key presence before appending any collected image, so one
	// inlineData observation suppresses the whole additional image sequence.
	for _, p := range existingParts {
		if pm, ok := p.(map[string]any); ok {
			if _, hasInline := pm["inlineData"]; hasInline {
				return result // existing inlineData key suppresses appends
			}
		}
	}

	for _, imgPart := range imageParts {
		existingParts = append(existingParts, imgPart)
	}
	setParts(existingParts)
	return result
}

// MergeTextPartsToResponse joins supplied text strings and updates the first
// text-key part, or prepends a text-only part when absent. Empty input returns
// response as supplied; nested response state remains shared through the
// shallow helper.
//
// The selected existing part is shallow-copied so its other fields, including
// thought metadata, accompany the joined text. Key presence selects it even
// when its prior text value was not a string. Later text parts and nontext
// maps retain their references and order. The collected strings can include
// thought text, so callers decide which fragments belong in this merge before
// invoking it.
func MergeTextPartsToResponse(response map[string]any, textParts []string) map[string]any {
	if len(textParts) == 0 {
		return response
	}

	mergedText := strings.Join(textParts, "")
	result, existingParts, setParts := GetOrCreateGeminiParts(response)

	newParts := make([]any, 0, len(existingParts)+1)
	textUpdated := false

	for _, p := range existingParts {
		pm, ok := p.(map[string]any)
		if !ok {
			newParts = append(newParts, p)
			continue
		}
		// Replace only the first text-key map with a shallow copy, preserving its
		// other metadata while later part references remain in their source order.
		if _, hasText := pm["text"]; hasText && !textUpdated {
			newPart := make(map[string]any)
			maps.Copy(newPart, pm)
			newPart["text"] = mergedText
			newParts = append(newParts, newPart)
			textUpdated = true
		} else {
			newParts = append(newParts, pm)
		}
	}

	// When no existing text key accepts replacement, prepend the joined text
	// before retained parts so the collected fragments remain represented in output.
	if !textUpdated {
		newParts = append([]any{map[string]any{"text": mergedText}}, newParts...)
	}

	setParts(newParts)
	return result
}

// CollectClaudeStreamResponse collects first-candidate Gemini parts and
// converts the selected map into Claude JSON using originalModel. Callers
// supply a readable body, required failure callbacks, and close the body after
// return; read/idle failures return no collected response.
//
// Parts or a nonblank finish reason mark the stream meaningful and set first
// timing; metadata alone remains eligible for Failover. Content-bearing maps
// win over later metadata-only maps, and collected ordinary text merges
// separately from thought/tool/image parts. Conversion usage comes from that
// selected final map, which can precede later raw usage observations; Executor
// independently tracks raw fields. Scanner termination stops delivery, while
// body ownership remains with the exchange caller.
func (s *ResponseAdapter) CollectClaudeStreamResponse(resp *http.Response, startTime time.Time, originalModel string) ([]byte, *StreamResult, error) {
	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := upstream.DefaultSSELineLimit
	if s.Options.MaxLineSize > 0 {
		maxLineSize = s.Options.MaxLineSize
	}
	scanBuf := httpclient.GetSSEScannerBuf64K()
	// Use pooled 64-KiB capacity before allowing configured growth; a smaller
	// selected maximum cannot shrink this initial buffer or enforce its lower
	// cap.
	scanner.Buffer(scanBuf[:0], maxLineSize)

	var firstTokenMs *int
	var last map[string]any
	var lastWithParts map[string]any
	var collectedParts []map[string]any // all part types in arrival order
	var meaningfulResponse bool

	// scanEvent transports a scanned line or terminal read error from the worker
	// to its owning response handler.
	type scanEvent struct {
		line string // copied scanned input line
		err  error  // terminal scanner read failure
	}

	// Queue sixteen line/error events so reading can advance separately from
	// handler timeout selection; the worker owns the pooled buffer until its
	// scanning work exits.
	events := make(chan scanEvent, 16)
	done := make(chan struct{})
	sendEvent := func(ev scanEvent) bool {
		select {
		case events <- ev:
			return true
		case <-done:
			return false
		}
	}

	var lastReadAt int64
	atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
	go func(scanBuf *httpclient.SSEScannerBuf64K) {
		defer httpclient.PutSSEScannerBuf64K(scanBuf)
		defer close(events)
		for scanner.Scan() {
			atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
			if !sendEvent(scanEvent{line: scanner.Text()}) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			_ = sendEvent(scanEvent{err: err})
		}
	}(scanBuf)
	// Signal stop on handler return so the sender can select termination; a
	// competing ready send can still win, and body closing releases blocked
	// Scan calls.
	defer close(done)

	// Check scanned-line activity on interval ticks, including nondata lines;
	// a disabled interval leaves a nil channel so this select path cannot fire.
	streamInterval := time.Duration(0)
	if s.Options.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.Options.StreamDataIntervalTimeout) * time.Second
	}
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				goto returnResponse
			}
			if ev.err != nil {
				if errors.Is(ev.err, bufio.ErrTooLong) {
					logger.LegacyPrintf("service.antigravity_gateway", "SSE line too long (antigravity claude non-stream): max_size=%d error=%v", maxLineSize, ev.err)
				}
				return nil, nil, ev.err
			}

			line := ev.line
			s.observeRaw([]byte(line))
			trimmed := strings.TrimRight(line, "\r\n")

			if !strings.HasPrefix(trimmed, "data:") {
				continue
			}

			payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}

			inner, parseErr := s.UnwrapV1InternalResponse([]byte(payload))
			if parseErr != nil {
				continue
			}

			// Reject incomplete decoded input before retaining content or timing;
			// raw observers have already received this line for independent accounting.
			var parsed map[string]any
			if err := json.Unmarshal(inner, &parsed); err != nil {
				continue
			}

			last = parsed

			// Keep the content-bearing map and all first-candidate part references when
			// later metadata arrives, because final conversion needs both selected
			// content and collected arrival order.
			parts := bridge.NativeExtractGeminiParts(parsed)
			if len(parts) > 0 {
				lastWithParts = parsed

				collectedParts = append(collectedParts, parts...)
			}
			if len(parts) > 0 || strings.TrimSpace(bridge.NativeExtractGeminiFinishReason(parsed)) != "" {
				meaningfulResponse = true
				if firstTokenMs == nil {
					ms := int(time.Since(startTime).Milliseconds())
					firstTokenMs = &ms
				}
			}

		case <-intervalCh:
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if time.Since(lastRead) < streamInterval {
				continue
			}
			logger.LegacyPrintf("service.antigravity_gateway", "Stream data interval timeout (antigravity claude non-stream)")
			return nil, nil, fmt.Errorf("stream data interval timeout")
		}
	}

returnResponse:
	// Select failover when neither parts nor a finish reason was observed,
	// because metadata-only input cannot satisfy this Claude collection path
	// meaningful-response condition.
	if !meaningfulResponse {
		logger.LegacyPrintf("service.antigravity_gateway", "[antigravity-Forward] warning: empty stream response (claude non-stream), triggering failover")
		return nil, nil, s.Options.Failover([]byte(`{"error":"empty stream response from upstream"}`))
	}

	finalResponse := bridge.NativePickGeminiCollectResult(last, lastWithParts)

	if len(collectedParts) > 0 {
		finalResponse = MergeCollectedPartsToResponse(finalResponse, collectedParts)
	}

	geminiBody, err := json.Marshal(finalResponse)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal gemini response: %w", err)
	}

	claudeResp, agUsage, err := TransformGeminiToClaude(geminiBody, originalModel)
	if err != nil {
		logger.LegacyPrintf("service.antigravity_gateway", "[antigravity-Forward] transform_error error=%v body=%s", err, string(geminiBody))
		return nil, nil, fmt.Errorf("failed to parse upstream response: %w", err)
	}

	usage := &upstream.TokenUsage{
		InputTokens:              agUsage.InputTokens,
		OutputTokens:             agUsage.OutputTokens,
		CacheCreationInputTokens: agUsage.CacheCreationInputTokens,
		CacheReadInputTokens:     agUsage.CacheReadInputTokens,
		ImageOutputTokens:        agUsage.ImageOutputTokens,
	}

	return claudeResp, &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs}, nil
}

// HandleClaudeStreamToNonStreaming delegates collection and writes successful
// Claude JSON. Failover and cancellation errors pass through; other failures
// use ClaudeError mapping, and the exchange owner closes the supplied response
// body after return. Buffered write failures remain in OutputContext.Err;
// the collection result itself keeps its prior ClientDisconnect value.
func (s *ResponseAdapter) HandleClaudeStreamToNonStreaming(c *upstream.OutputContext, resp *http.Response, startTime time.Time, originalModel string) (*StreamResult, error) {
	claudeResp, streamRes, err := s.CollectClaudeStreamResponse(resp, startTime, originalModel)
	if err != nil {
		if s.Options.IsFailover(err) {
			return nil, err
		}

		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}

		errMsg := "Failed to parse upstream response"
		errType := "upstream_error"
		if strings.Contains(err.Error(), "stream data interval timeout") {
			errMsg = "Upstream stream data interval timeout"
			errType = "upstream_timeout"
		} else if errors.Is(err, bufio.ErrTooLong) {
			errMsg = "Upstream response line too long"
			errType = "response_too_large"
		}

		return nil, s.Options.ClaudeError(http.StatusBadGateway, errType, errMsg)
	}
	c.Data(http.StatusOK, "application/json", claudeResp)
	return streamRes, nil
}

// HandleClaudeStreamingResponse converts wrapped Gemini data into Claude SSE
// and returns adapter observations. Callers supply a flushing output writer
// and close the response body after return; raw input is observed before
// per-stream conversion.
//
// First timing records the first nonempty ProcessLine byte batch before its
// client write; EOF final events do not set that timing. A write failure
// suppresses later writes while upstream collection continues; handled
// cancellation or
// read failure returns finalized usage. Too-long and live idle-timeout paths
// return zero adapter usage alongside error, while Executor can retain raw
// partial usage. EOF finalizes events, and an unstarted stream with no final
// events selects Failover, even though keepalive output may already have
// occurred. Converted usage here omits image-output tokens.
func (s *ResponseAdapter) HandleClaudeStreamingResponse(c *upstream.OutputContext, resp *http.Response, startTime time.Time, originalModel string) (*StreamResult, error) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}

	processor := NewStreamingProcessor(originalModel)
	var firstTokenMs *int
	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := upstream.DefaultSSELineLimit
	if s.Options.MaxLineSize > 0 {
		maxLineSize = s.Options.MaxLineSize
	}
	scanBuf := httpclient.GetSSEScannerBuf64K()
	// Use pooled 64-KiB capacity before allowing configured growth; a smaller
	// selected maximum cannot shrink this initial buffer or enforce its lower
	// cap.
	scanner.Buffer(scanBuf[:0], maxLineSize)

	// Project only the four Claude token/cache fields used by this streaming
	// result; ImageOutputTokens is omitted here even when raw payload fields
	// describe image usage.
	convertUsage := func(agUsage *protocolanthropic.ClaudeUsage) *upstream.TokenUsage {
		if agUsage == nil {
			return &upstream.TokenUsage{}
		}
		return &upstream.TokenUsage{
			InputTokens:              agUsage.InputTokens,
			OutputTokens:             agUsage.OutputTokens,
			CacheCreationInputTokens: agUsage.CacheCreationInputTokens,
			CacheReadInputTokens:     agUsage.CacheReadInputTokens,
		}
	}

	// scanEvent transports a scanned line or terminal read error from the worker
	// to its owning response handler.
	type scanEvent struct {
		line string // copied scanned input line
		err  error  // terminal scanner read failure
	}
	// Queue sixteen line/error events so reading can advance separately from
	// handler timeout selection; the worker owns the pooled buffer until its
	// scanning work exits.
	events := make(chan scanEvent, 16)
	done := make(chan struct{})
	sendEvent := func(ev scanEvent) bool {
		select {
		case events <- ev:
			return true
		case <-done:
			return false
		}
	}
	var lastReadAt int64
	atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
	go func(scanBuf *httpclient.SSEScannerBuf64K) {
		defer httpclient.PutSSEScannerBuf64K(scanBuf)
		defer close(events)
		for scanner.Scan() {
			atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
			if !sendEvent(scanEvent{line: scanner.Text()}) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			_ = sendEvent(scanEvent{err: err})
		}
	}(scanBuf)
	// Signal stop on handler return so the sender can select termination; a
	// competing ready send can still win, and body closing releases blocked
	// Scan calls.
	defer close(done)

	streamInterval := time.Duration(0)
	if s.Options.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.Options.StreamDataIntervalTimeout) * time.Second
	}
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

	// Send keepalive after delivered-line quiet time while the client remains
	// connected; pings do not reset that time, so subsequent ticks can continue
	// pinging.
	keepaliveInterval := time.Duration(0)
	if s.Options.StreamKeepaliveInterval > 0 {
		keepaliveInterval = time.Duration(s.Options.StreamKeepaliveInterval) * time.Second
	}
	var keepaliveTicker *time.Ticker
	if keepaliveInterval > 0 {
		keepaliveTicker = time.NewTicker(keepaliveInterval)
		defer keepaliveTicker.Stop()
	}
	var keepaliveCh <-chan time.Time
	if keepaliveTicker != nil {
		keepaliveCh = keepaliveTicker.C
	}
	lastDataAt := time.Now()

	cw := NewClientWriter(c.Writer, flusher, "antigravity claude")

	// Suppress duplicate terminal error events after the first attempted emission
	// or a recorded disconnect; this direct writer does not itself update the
	// ClientWriter failure flag.
	errorEventSent := false
	sendErrorEvent := func(reason string) {
		if errorEventSent || cw.Disconnected() {
			return
		}
		errorEventSent = true
		_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: {\"error\":\"%s\"}\n\n", reason)
		flusher.Flush()
	}

	finishUsage := func() *upstream.TokenUsage {
		_, agUsage := processor.Finish()
		return convertUsage(agUsage)
	}

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				finalEvents, agUsage := processor.Finish()
				if len(finalEvents) > 0 {
					cw.Write(finalEvents)
				} else if !processor.MessageStartSent() && !cw.Disconnected() {
					// Select failover when processor finalization yields no events and no
					// message start exists; keepalive writes may already have committed
					// output through the owning sink.
					logger.LegacyPrintf("service.antigravity_gateway", "[antigravity-Claude-Stream] empty stream response (no valid events parsed), triggering failover")
					return nil, s.Options.Failover([]byte(`{"error":"empty stream response from upstream"}`))
				}
				return &StreamResult{Usage: convertUsage(agUsage), FirstTokenMs: firstTokenMs, ClientDisconnect: cw.Disconnected()}, nil
			}
			if ev.err != nil {
				if disconnect, handled := HandleStreamReadError(ev.err, cw.Disconnected(), "antigravity claude"); handled {
					return &StreamResult{Usage: finishUsage(), FirstTokenMs: firstTokenMs, ClientDisconnect: disconnect}, nil
				}
				if errors.Is(ev.err, bufio.ErrTooLong) {
					logger.LegacyPrintf("service.antigravity_gateway", "SSE line too long (antigravity): max_size=%d error=%v", maxLineSize, ev.err)
					sendErrorEvent("response_too_large")
					return &StreamResult{Usage: convertUsage(nil), FirstTokenMs: firstTokenMs}, ev.err
				}
				sendErrorEvent("stream_read_error")
				return nil, fmt.Errorf("stream read error: %w", ev.err)
			}

			lastDataAt = time.Now()

			s.observeRaw([]byte(ev.line))
			claudeEvents := processor.ProcessLine(strings.TrimRight(ev.line, "\r\n"))
			if len(claudeEvents) > 0 {
				if firstTokenMs == nil {
					ms := int(time.Since(startTime).Milliseconds())
					firstTokenMs = &ms
				}
				cw.Write(claudeEvents)
			}

		case <-intervalCh:
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if time.Since(lastRead) < streamInterval {
				continue
			}
			if cw.Disconnected() {
				logger.LegacyPrintf("service.antigravity_gateway", "Upstream timeout after client disconnect (antigravity claude), returning collected usage")
				return &StreamResult{Usage: finishUsage(), FirstTokenMs: firstTokenMs, ClientDisconnect: true}, nil
			}
			logger.LegacyPrintf("service.antigravity_gateway", "Stream data interval timeout (antigravity)")
			sendErrorEvent("stream_timeout")
			return &StreamResult{Usage: convertUsage(nil), FirstTokenMs: firstTokenMs}, fmt.Errorf("stream data interval timeout")

		case <-keepaliveCh:
			if cw.Disconnected() {
				continue
			}
			if time.Since(lastDataAt) < keepaliveInterval {
				continue
			}
			// Anthropic ping events keep idle connections active without adding content.
			if !cw.Fprintf("event: ping\ndata: {\"type\": \"ping\"}\n\n") {
				logger.LegacyPrintf("service.antigravity_gateway", "Client disconnected during keepalive ping (antigravity claude), continuing to drain upstream for billing")
				continue
			}
		}
	}
}

// ExtractImageInputSize returns trimmed imageSize from decoded Gemini
// generation image settings. Invalid JSON, absent generation/image settings,
// and blank size return empty; the helper follows the shared Gemini wire field
// spelling.
func (s *ResponseAdapter) ExtractImageInputSize(body []byte) string {
	// A failed complete request decode supplies no selected image size. Return
	// empty text so callers retain their ordinary image-size default or policy.
	var req protocolgemini.GeminiRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}

	if req.GenerationConfig != nil && req.GenerationConfig.ImageConfig != nil {
		return strings.TrimSpace(req.GenerationConfig.ImageConfig.ImageSize)
	}

	return ""
}

// IsImageGenerationModel delegates Gemini image-model recognition to the
// shared upstream predicate.
func IsImageGenerationModel(model string) bool { return upstream.IsGeminiImageGenerationModel(model) }

// observeRaw forwards data synchronously to the optional callback without
// copying bytes. A missing callback has no effect; retaining callbacks follow
// supplied storage ownership and synchronize their own shared state. Callbacks
// leave the supplied bytes unchanged before subsequent parsing and output.
func (s *ResponseAdapter) observeRaw(data []byte) {
	if s.Options.ObserveRaw != nil {
		s.Options.ObserveRaw(data)
	}
}

// UnwrapV1InternalResponse extracts raw JSON for any existing response field,
// including scalar or null values. Absent fields return supplied bytes by
// reference; this helper performs no full JSON validation and returns no
// error. Callers keep the supplied body stable while reading a borrowed
// return value; extracted response bytes have their own allocation.
func (s *ResponseAdapter) UnwrapV1InternalResponse(body []byte) ([]byte, error) {
	result := gjson.GetBytes(body, "response")
	if result.Exists() {
		return []byte(result.Raw), nil
	}
	return body, nil
}
