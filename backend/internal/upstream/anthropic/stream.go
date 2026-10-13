package anthropic

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Messages SSE events can report prompt usage before content and final output
// usage after a downstream client stops accepting writes. StreamResponse keeps
// those observations separate from delivery, continuing to parse upstream usage
// after a write failure until completion, read failure, or idle timeout.
//
// A scanner goroutine sends complete lines while the main loop handles event
// framing, timeout checks, and keepalives. Blank lines trigger processing of
// the first nonempty data payload in the pending frame; cache categories,
// matching model names, and tool aliases are adjusted for output. Raw
// observations precede those edits. Terminal evidence controls whether EOF or a
// later read error completes successfully, while pre-commitment read failures
// can request failover. The reader signals scanner delivery to stop on return;
// its caller closes the body to release any read still blocked in the
// transport.

// sseDataPattern identifies a data prefix after event lines are trimmed,
// allowing the standard reader to select its first nonempty data payload from a
// framed event.
var sseDataPattern = regexp.MustCompile(`^data:\s*`)

// StreamResult contains accumulated Messages usage, legacy first-data timing,
// and the reader's downstream-disconnect observation.
type StreamResult struct {
	// Usage points to the counters accumulated by the selected reader.
	// ObserveState receives the same mutable value during processing, and the
	// returned result can retain partial counts after failure.
	Usage *upstream.TokenUsage
	// FirstTokenMs stores elapsed milliseconds for the first nonempty,
	// non-DONE data payload under the selected reader's processing rule. Nil
	// means that rule has not assigned timing, even when headers were sent.
	FirstTokenMs *int
	// ClientDisconnect records ordinary stream or keepalive write failure, or
	// a context-cancelled read before terminal completion. Synthesized error
	// writes do not update it, so callers also inspect the returned error and
	// accumulated usage.
	ClientDisconnect bool
}

// StreamOptions supplies observations, presentation helpers, and timer settings
// for one Messages reader. Callers retain these callbacks and serialize their
// shared state while reading the response.
type StreamOptions struct {
	// Observe receives raw data-payload evidence before that payload is edited
	// or delivered. Passthrough event-name lines can already be written;
	// Semantic describes observed content independently of successful delivery.
	Observe func(wire.Observation)
	// ObserveState receives accumulated mutable usage and first-data timing
	// after a payload follows the selected reader's usage-processing path.
	// Standard named error frames return before this callback. Consumers copy
	// retained counters while later events can still update the shared usage.
	ObserveState func(*upstream.TokenUsage, *int)
	ProviderID   int64 // selected provider record ID
	// MaxLineSize selects Scanner's buffer-growth limit in bytes; nonpositive
	// values use upstream.DefaultSSELineLimit. The initial pooled buffer is
	// already 64 KiB, so smaller positive values can still accept tokens within
	// that initial capacity.
	MaxLineSize int
	// Interval selects the idle duration compared against scanner reads, while
	// Keepalive schedules downstream activity after completed writes.
	// Nonpositive values disable their timer paths; the clocks serve separate
	// purposes and are handled by the response loop.
	Interval, Keepalive time.Duration // Interval: upstream idle duration; Keepalive: downstream heartbeat duration
	// UserAgent supplies the inbound CLI version for standard no-op delta
	// selection. Passthrough emits ordinary pings, so it does not use this
	// version rule.
	UserAgent    string
	ToolNames    *ToolNameRewrite                   // request-local tool restoration mapping
	UpdateWindow func(context.Context, http.Header) // optional response-header quota observation
	WriteHeaders func(http.Header, http.Header)     // optional client header preparation
	// OverrideCache resolves current TTL classification when the standard
	// reader processes usage events. Absent callbacks retain observed
	// categories; passthrough usage parsing does not consult this override.
	OverrideCache func(context.Context) (string, bool)
	// OnTimeout observes an idle failure while the client is still connected.
	// After a write disconnect, timeout returns partial state without invoking
	// this provider-update callback.
	OnTimeout func(context.Context, string)
	// Failover converts a standard read failure before recorded writer
	// commitment into the caller's retry error. Absence selects
	// StreamReadFailure holding the synthesized diagnostic bytes.
	Failover func([]byte) error
}

// StreamReadFailure carries a pre-output read failure body for default failover
// handling by the caller.
type StreamReadFailure struct {
	Body []byte // synthesized retry error body
}

// StreamErrorEventError reports a named upstream SSE error frame and retains
// its raw data payload for the caller's error handling.
type StreamErrorEventError struct {
	RawData string // raw upstream SSE payload
}

// override queries the optional cache-TTL classification callback for this
// context. An absent callback returns an empty target and false, leaving
// response usage under its observed categories.
func (o StreamOptions) override(ctx context.Context) (string, bool) {
	if o.OverrideCache == nil {
		return "", false
	}
	return o.OverrideCache(ctx)
}

// failover constructs the configured pre-output read failure from body. When no
// callback is supplied, it returns StreamReadFailure holding those bytes for
// the caller's retry and diagnostic handling.
func (o StreamOptions) failover(body []byte) error {
	if o.Failover != nil {
		return o.Failover(body)
	}
	return &StreamReadFailure{Body: body}
}

// Error returns the fixed 502 failover description for a stream read failure.
func (e *StreamReadFailure) Error() string { return "upstream error: 502 (failover)" }

// StreamResponse forwards standard Messages SSE and returns usage, data-arrival
// timing, and disconnect state. Callers serialize c, retain ownership of
// resp.Body, and close it after the reader returns. The writer needs flushing
// support; nonpositive MaxLineSize selects the shared default growth limit.
//
// A scanner goroutine timestamps complete lines before bounded-channel
// delivery; a full channel pauses further reads. Idle checks use those read
// timestamps and keepalives follow completed downstream writes. Each
// blank-delimited event selects its first nonempty data payload, observes raw
// data, applies presentation changes, and merges usage after write failure. EOF
// requires DONE or a successfully decoded data frame recognized as
// message_stop; pending unclosed frames remain unprocessed. Read failure
// before recorded writer commitment can invoke Failover, while
// later read failures return partial state and selected client error events.
//
// An event:error frame returns StreamErrorEventError with no StreamResult
// before a recorded client write failure. After that disconnection it ends
// draining with accumulated state and nil error. Previous callback observations
// remain available on either path, and FirstTokenMs can precede semantic
// content.
//
// FirstTokenMs is assigned after the first selected output block's write
// attempt and can include synchronous sink delay. Its timing therefore differs
// from semantic observation made before that write.
func StreamResponse(ctx context.Context, resp *http.Response, c *upstream.OutputContext, options StreamOptions, startTime time.Time, originalModel, mappedModel string, mimicClaudeCode bool) (*StreamResult, error) {
	if options.UpdateWindow != nil {
		options.UpdateWindow(ctx, resp.Header)
	}

	if options.WriteHeaders != nil {
		options.WriteHeaders(c.Writer.Header(), resp.Header)
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	if v := resp.Header.Get("x-request-id"); v != "" {
		c.Header("x-request-id", v)
	}

	w := c.Writer
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}

	usage := &upstream.TokenUsage{}
	var firstTokenMs *int
	scanner := bufio.NewScanner(resp.Body)
	// Scanner grows only after its pooled 64 KiB storage fills; a smaller
	// MaxLineSize does not shrink that initial capacity for accepted lines.
	maxLineSize := options.MaxLineSize
	if maxLineSize <= 0 {
		maxLineSize = upstream.DefaultSSELineLimit
	}
	scanBuf := httpclient.GetSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)

	// scanEvent carries one scanner line or read error to the response loop.
	type scanEvent struct {
		line string // upstream scanner line text
		err  error  // upstream scanner read failure
	}
	// The scanner sends lines through a bounded channel while the response loop
	// handles timers and writes. Closing done makes delivery cancellable when
	// the reader returns, leaving body closure to its caller.
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
	// The scanner publishes completed-read timestamps atomically for the
	// response loop's idle checks. Downstream write delays can postpone
	// dequeuing a line, so timer decisions use that independent read clock.
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
	// Stop scanner delivery when response processing returns. A scanner blocked
	// in Body.Read still needs the caller's body close; the pooled buffer stays
	// with that goroutine until it exits.
	defer close(done)

	streamInterval := options.Interval
	// The idle ticker compares the scanner's last successful read time, so a
	// slow downstream write does not redefine when upstream last supplied a
	// complete line.
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

	// Keepalive scheduling follows completed downstream output independently of
	// upstream read timing. A positive interval permits idle client connections
	// to receive a protocol-shaped heartbeat between processed events.
	keepaliveInterval := options.Keepalive
	var keepaliveTimer *time.Timer
	if keepaliveInterval > 0 {
		keepaliveTimer = time.NewTimer(keepaliveInterval)
		defer keepaliveTimer.Stop()
	}
	var keepaliveCh <-chan time.Time
	if keepaliveTimer != nil {
		keepaliveCh = keepaliveTimer.C
	}
	lastDataAt := time.Now()
	// Completed output restarts the downstream idle wait. Stop and drain a
	// fired timer before reset so an older tick cannot trigger a heartbeat
	// immediately after fresh output.
	resetKeepaliveTimer := func() {
		if keepaliveTimer == nil {
			return
		}
		if !keepaliveTimer.Stop() {
			select {
			case <-keepaliveTimer.C:
			default:
			}
		}
		keepaliveTimer.Reset(keepaliveInterval)
	}

	// Attempt one Anthropic error event, even if writing it fails. The envelope
	// lets clients and error extraction read the same type and message fields.
	errorEventSent := false
	sendErrorEvent := func(reason, message string) {
		if errorEventSent {
			return
		}
		errorEventSent = true
		if message == "" {
			message = reason
		}
		body, err := json.Marshal(map[string]any{
			"type": "error",
			"error": map[string]string{
				"type":    reason,
				"message": message,
			},
		})
		if err != nil {
			// If structured error encoding fails, keep the selected reason and
			// message in the manually formatted envelope so the attempted
			// client error still uses those fields.
			body = []byte(fmt.Sprintf(`{"type":"error","error":{"type":%q,"message":%q}}`, reason, message))
		}
		_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", body)
		flusher.Flush()
	}

	needModelReplace := originalModel != mappedModel
	clientDisconnected := false // downstream writes have not failed
	sawTerminalEvent := false
	useNoopDeltaKeepalive := ShouldUseClaudeCodeNoopDeltaKeepalive(options.UserAgent)
	noopDeltaKeepaliveBlockIndex := -1
	noopDeltaKeepaliveDeltaType := ""

	pendingEventLines := make([]string, 0, 4)

	processSSEEvent := func(lines []string) ([]string, string, *wire.SseUsagePatch, error) {
		if len(lines) == 0 {
			return nil, "", nil, nil
		}

		eventName := ""
		dataLine := ""
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if after, ok0 := strings.CutPrefix(trimmed, "event:"); ok0 {
				eventName = strings.TrimSpace(after)
				continue
			}
			if dataLine == "" && sseDataPattern.MatchString(trimmed) {
				dataLine = sseDataPattern.ReplaceAllString(trimmed, "")
			}
		}

		if options.Observe != nil {
			options.Observe(wire.ObserveEvent(dataLine))
		}
		if eventName == "error" {
			return nil, dataLine, nil, &StreamErrorEventError{RawData: dataLine}
		}

		// Frames with no selected nonempty payload retain their received
		// lines, allowing comments and event-only activity to reach the client
		// before any map-based presentation editing is possible. An event-only
		// message_stop follows this path too and leaves terminal state
		// unchanged.
		if dataLine == "" {
			return []string{strings.Join(lines, "\n") + "\n\n"}, "", nil, nil
		}

		// The DONE sentinel establishes completion before JSON decoding.
		// Keep its selected event name when emitting it so sentinel-based
		// closure survives the normalized block construction.
		if dataLine == "[DONE]" {
			sawTerminalEvent = true
			block := ""
			if eventName != "" {
				block = "event: " + eventName + "\n"
			}
			block += "data: " + dataLine + "\n\n"
			return []string{block}, dataLine, nil, nil
		}

		var event map[string]any
		if err := json.Unmarshal([]byte(dataLine), &event); err != nil {
			// A malformed data payload still travels as its selected SSE block.
			// Forward the original data instead of applying map-based
			// presentation edits that require a decoded event.
			block := ""
			if eventName != "" {
				block = "event: " + eventName + "\n"
			}
			block += "data: " + dataLine + "\n\n"
			return []string{block}, dataLine, nil, nil
		}

		eventType, _ := event["type"].(string)
		// A payload can omit the SSE event line while naming its JSON type.
		// Use that type for the output event name after explicit named-error
		// detection, which is based on the received event header.
		if eventName == "" {
			eventName = eventType
		}
		eventChanged := false

		// Track the latest supported content block or delta for the selected
		// CLI heartbeat shape. Matching block stop or message completion clears
		// that choice so idle output no longer references a closed block.
		if useNoopDeltaKeepalive {
			switch eventType {
			case "content_block_start":
				if idx, ok := SseEventIndex(event); ok {
					noopDeltaKeepaliveBlockIndex = -1
					noopDeltaKeepaliveDeltaType = ""
					if contentBlock, ok := event["content_block"].(map[string]any); ok {
						blockType, _ := contentBlock["type"].(string)
						if deltaType := ClaudeCodeKeepaliveDeltaTypeForContentBlock(blockType); deltaType != "" {
							noopDeltaKeepaliveBlockIndex = idx
							noopDeltaKeepaliveDeltaType = deltaType
						}
					}
				}
			case "content_block_delta":
				if idx, ok := SseEventIndex(event); ok {
					if delta, ok := event["delta"].(map[string]any); ok {
						deltaType, _ := delta["type"].(string)
						if ClaudeCodeKeepaliveFieldForDeltaType(deltaType) != "" {
							noopDeltaKeepaliveBlockIndex = idx
							noopDeltaKeepaliveDeltaType = deltaType
						}
					}
				}
			case "content_block_stop":
				if idx, ok := SseEventIndex(event); ok && idx == noopDeltaKeepaliveBlockIndex {
					noopDeltaKeepaliveBlockIndex = -1
					noopDeltaKeepaliveDeltaType = ""
				}
			case "message_stop":
				noopDeltaKeepaliveBlockIndex = -1
				noopDeltaKeepaliveDeltaType = ""
			}
		}

		// Compatible message_start and message_delta usage can report
		// cached_tokens. Normalize those selected usage maps before TTL
		// classification and patch extraction so the canonical cache-read
		// observation is available.
		if eventType == "message_start" {
			if msg, ok := event["message"].(map[string]any); ok {
				if u, ok := msg["usage"].(map[string]any); ok {
					eventChanged = ReconcileCachedTokens(u) || eventChanged
				}
			}
		}
		if eventType == "message_delta" {
			if u, ok := event["usage"].(map[string]any); ok {
				eventChanged = ReconcileCachedTokens(u) || eventChanged
			}
		}

		// Resolve current TTL classification while processing each event.
		// Rewriting the selected usage map precedes patch extraction so output
		// and local billing counts use the same classified detail buckets.
		if overrideTarget, ok := options.override(ctx); ok {
			if eventType == "message_start" {
				if msg, ok := event["message"].(map[string]any); ok {
					if u, ok := msg["usage"].(map[string]any); ok {
						eventChanged = RewriteCacheCreationJSON(u, overrideTarget) || eventChanged
					}
				}
			}
			if eventType == "message_delta" {
				if u, ok := event["usage"].(map[string]any); ok {
					eventChanged = RewriteCacheCreationJSON(u, overrideTarget) || eventChanged
				}
			}
		}

		// Only a matching embedded message model uses the client's requested
		// name. The raw model observation has already run, retaining upstream
		// identity before this presentation edit.
		if needModelReplace {
			if msg, ok := event["message"].(map[string]any); ok {
				if model, ok := msg["model"].(string); ok && model == mappedModel {
					msg["model"] = originalModel
					eventChanged = true
				}
			}
		}

		usagePatch := wire.ExtractSSEUsagePatch(event)
		if StreamEventIsTerminal(eventName, dataLine) {
			sawTerminalEvent = true
		}
		// Keep selected JSON text in its received representation when no
		// presentation edit changed the map. Re-encoding would otherwise
		// reorder fields despite those unchanged values.
		if !eventChanged {
			block := ""
			if eventName != "" {
				block = "event: " + eventName + "\n"
			}
			block += "data: " + dataLine + "\n\n"
			return []string{block}, dataLine, usagePatch, nil
		}

		newData, err := json.Marshal(event)
		if err != nil {
			// Usage was already extracted from the decoded event. If
			// re-encoding fails, forward the original payload while retaining
			// that extracted local usage patch for accumulation.
			block := ""
			if eventName != "" {
				block = "event: " + eventName + "\n"
			}
			block += "data: " + dataLine + "\n\n"
			return []string{block}, dataLine, usagePatch, nil
		}

		block := ""
		if eventName != "" {
			block = "event: " + eventName + "\n"
		}
		block += "data: " + string(newData) + "\n\n"
		return []string{block}, string(newData), usagePatch, nil
	}

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				// The blank-line processor must have observed message_stop or
				// DONE before EOF counts as complete. Pending unclosed frames
				// do not supply that evidence, so return partial usage on
				// incomplete closure.
				if !sawTerminalEvent {
					return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: clientDisconnected}, fmt.Errorf("stream usage incomplete: missing terminal event")
				}
				return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: clientDisconnected}, nil
			}
			if ev.err != nil {
				// Completion evidence was already processed before this scanner
				// failure. Keep the accumulated result successful here so a
				// later transport error does not erase that terminal
				// observation.
				if sawTerminalEvent {
					return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: clientDisconnected}, nil
				}
				// A cancelled or expired read can interrupt the stream before
				// terminal usage. Return observed counters with the wrapped
				// context error and disconnect marker so completion can assess
				// that partial attempt.
				if errors.Is(ev.err, context.Canceled) || errors.Is(ev.err, context.DeadlineExceeded) {
					return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: true}, fmt.Errorf("stream usage incomplete: %w", ev.err)
				}
				// After a downstream failure, upstream parsing continues for
				// usage. If that read also fails before completion, return
				// those accumulated counters and mark the disconnected partial
				// result.
				if clientDisconnected {
					return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: true}, fmt.Errorf("stream usage incomplete after disconnect: %w", ev.err)
				}
				// An oversized Scanner token cannot be processed as a complete
				// event. Attempt the selected client error and return the
				// original scan failure with any previously collected usage.
				if errors.Is(ev.err, bufio.ErrTooLong) {
					logger.LegacyPrintf("service.gateway", "SSE line too long: provider=%d max_size=%d error=%v", options.ProviderID, maxLineSize, ev.err)
					sendErrorEvent("response_too_large", fmt.Sprintf("upstream SSE line exceeded %d bytes", maxLineSize))
					return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs}, ev.err
				}
				// Writer commitment can originate from headers or a heartbeat
				// before semantic content. An uncommitted writer permits
				// Failover; after commitment, append a sanitized SSE error and
				// return partial state. Internal logs retain transport details.
				disconnectMsg := "upstream stream disconnected: " + upstream.SanitizeStreamError(ev.err)
				if !c.Writer.Written() {
					logger.LegacyPrintf("service.gateway", "Upstream stream read error before any client output (provider=%d), failing over: %v", options.ProviderID, ev.err)
					body, _ := json.Marshal(map[string]any{
						"type": "error",
						"error": map[string]string{
							"type":    "upstream_disconnected",
							"message": disconnectMsg,
						},
					})
					return nil, options.failover(body)
				}
				sendErrorEvent("stream_read_error", disconnectMsg)
				return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs}, fmt.Errorf("stream read error: %w", ev.err)
			}
			line := ev.line
			trimmed := strings.TrimSpace(line)

			if trimmed == "" {
				if len(pendingEventLines) == 0 {
					continue
				}

				outputBlocks, data, usagePatch, err := processSSEEvent(pendingEventLines)
				pendingEventLines = pendingEventLines[:0]
				if err != nil {
					// A named error frame ends a stream whose client already
					// failed writing. Return the usage retained from earlier
					// processed frames because further delivery cannot provide
					// that client another event.
					if clientDisconnected {
						return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: true}, nil
					}
					return nil, err
				}
				for _, block := range outputBlocks {
					if !clientDisconnected {
						observation := wire.ObserveEvent(data)
						c.NextEvent(observation.Semantic, observation.Terminal)
						restored := RestoreToolNamesInBytes([]byte(block), options.ToolNames)
						if _, werr := fmt.Fprint(w, string(restored)); werr != nil {
							clientDisconnected = true
							logger.LegacyPrintf("service.gateway", "Client disconnected during streaming, continuing to drain upstream for billing")
							// Keep processing this event and later usage after
							// the write fails. Skipping this event could omit
							// tokens already consumed upstream.
						} else {
							flusher.Flush()
							lastDataAt = time.Now()
							resetKeepaliveTimer()
						}
					}
					if data != "" {
						// Legacy timing begins with any non-DONE data payload,
						// including metadata. Semantic-output observation is
						// recorded separately before delivery is attempted, so
						// the two timing fields answer different questions.
						if firstTokenMs == nil && data != "[DONE]" {
							ms := int(time.Since(startTime).Milliseconds())
							firstTokenMs = &ms
						}
						if usagePatch != nil {
							wire.MergeSSEUsagePatch(usage, usagePatch)
						}
						if options.ObserveState != nil {
							options.ObserveState(usage, firstTokenMs)
						}
					}
				}
				continue
			}

			pendingEventLines = append(pendingEventLines, line)

		case <-intervalCh:
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if time.Since(lastRead) < streamInterval {
				continue
			}
			if clientDisconnected {
				return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: true}, fmt.Errorf("stream usage incomplete after timeout")
			}
			logger.LegacyPrintf("service.gateway", "Stream data interval timeout: provider=%d model=%s interval=%s", options.ProviderID, originalModel, streamInterval)
			// This connected-client idle failure is reported through OnTimeout
			// before the error frame. A disconnected-client timeout already
			// returned partial usage above and skips this provider observation.
			if options.OnTimeout != nil {
				options.OnTimeout(ctx, originalModel)
			}
			sendErrorEvent("stream_timeout", fmt.Sprintf("upstream stream idle for %s", streamInterval))
			return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs}, fmt.Errorf("stream data interval timeout")

		case <-keepaliveCh:
			if clientDisconnected {
				continue
			}
			if time.Since(lastDataAt) < keepaliveInterval {
				resetKeepaliveTimer()
				continue
			}
			// A supported active block allows a version-selected empty delta;
			// otherwise the heartbeat uses ping. This synthesized activity
			// carries no parsed usage and does not start legacy data-arrival
			// timing.
			keepaliveBlock := "event: ping\ndata: {\"type\": \"ping\"}\n\n"
			if useNoopDeltaKeepalive && noopDeltaKeepaliveBlockIndex >= 0 {
				if block, ok := BuildClaudeCodeNoopDeltaKeepalive(noopDeltaKeepaliveBlockIndex, noopDeltaKeepaliveDeltaType); ok {
					keepaliveBlock = block
				}
			}
			if _, werr := fmt.Fprint(w, keepaliveBlock); werr != nil {
				clientDisconnected = true
				logger.LegacyPrintf("service.gateway", "Client disconnected during keepalive ping, continuing to drain upstream for billing")
				continue
			}
			flusher.Flush()
			lastDataAt = time.Now()
			resetKeepaliveTimer()
		}
	}
}

// StreamEventIsTerminal recognizes a message_stop name, JSON type, or DONE
// sentinel. Event names are trimmed and case-insensitive; JSON type matching
// remains exact. Empty data returns false unless its event name already
// identifies message_stop.
func StreamEventIsTerminal(eventName, data string) bool {
	if strings.EqualFold(strings.TrimSpace(eventName), "message_stop") {
		return true
	}
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return false
	}
	if trimmed == "[DONE]" {
		return true
	}
	return gjson.Get(trimmed, "type").String() == "message_stop"
}

// Error returns the fixed description for an observed upstream SSE error frame.
func (e *StreamErrorEventError) Error() string { return "have error in stream" }
