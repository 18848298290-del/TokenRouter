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

var sseDataPattern = regexp.MustCompile(`^data:\s*`)

type StreamResult struct {
	Usage            *upstream.TokenUsage
	FirstTokenMs     *int
	ClientDisconnect bool
}
type StreamOptions struct {
	Observe             func(wire.Observation)
	ObserveState        func(*upstream.TokenUsage, *int)
	ProviderID          int64
	MaxLineSize         int
	Interval, Keepalive time.Duration
	UserAgent           string
	ToolNames           *ToolNameRewrite
	UpdateWindow        func(context.Context, http.Header)
	WriteHeaders        func(http.Header, http.Header)
	OverrideCache       func(context.Context) (string, bool)
	OnTimeout           func(context.Context, string)
	Failover            func([]byte) error
}

type StreamReadFailure struct{ Body []byte }

// StreamErrorEventError represents an SSE event:error frame.
// RawData retains its data payload for failover handling and diagnostic logs.
type StreamErrorEventError struct {
	RawData string
}

func (o StreamOptions) override(ctx context.Context) (string, bool) {
	if o.OverrideCache == nil {
		return "", false
	}
	return o.OverrideCache(ctx)
}

func (o StreamOptions) failover(body []byte) error {
	if o.Failover != nil {
		return o.Failover(body)
	}
	return &StreamReadFailure{Body: body}
}

func (e *StreamReadFailure) Error() string { return "upstream error: 502 (failover)" }
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
	// Allow configured large SSE lines while reusing the pooled initial buffer.
	maxLineSize := options.MaxLineSize
	if maxLineSize <= 0 {
		maxLineSize = upstream.DefaultSSELineLimit
	}
	scanBuf := httpclient.GetSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)

	type scanEvent struct {
		line string
		err  error
	}
	// Read upstream separately so blocked reads do not prevent timeout or keepalive handling.
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
	defer close(done)

	streamInterval := options.Interval
	// Measure idle time from upstream reads, independent of downstream write timing.
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

	// Send downstream keepalives to prevent idle proxy connections from closing.
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
			// The current payload contains only strings; retain a defensive encoding path.
			body = []byte(fmt.Sprintf(`{"type":"error","error":{"type":%q,"message":%q}}`, reason, message))
		}
		_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", body)
		flusher.Flush()
	}

	needModelReplace := originalModel != mappedModel
	clientDisconnected := false // Keep draining usage after a downstream write fails.
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

		if dataLine == "" {
			return []string{strings.Join(lines, "\n") + "\n\n"}, "", nil, nil
		}

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
			// Preserve the data payload when it cannot be decoded.
			block := ""
			if eventName != "" {
				block = "event: " + eventName + "\n"
			}
			block += "data: " + dataLine + "\n\n"
			return []string{block}, dataLine, nil, nil
		}

		eventType, _ := event["type"].(string)
		if eventName == "" {
			eventName = eventType
		}
		eventChanged := false

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

		// Normalize Kimi cache usage into the Anthropic cache-read field.
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

		// Apply the resolved cache billing category to each usage event.
		// The OverrideCache callback resolves provider/global precedence.
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
			// Preserve the data payload if re-encoding fails.
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
				// EOF is successful only after a terminal event.
				if !sawTerminalEvent {
					return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: clientDisconnected}, fmt.Errorf("stream usage incomplete: missing terminal event")
				}
				return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: clientDisconnected}, nil
			}
			if ev.err != nil {
				if sawTerminalEvent {
					return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: clientDisconnected}, nil
				}
				// Cancellation may interrupt upstream reads before terminal usage arrives.
				if errors.Is(ev.err, context.Canceled) || errors.Is(ev.err, context.DeadlineExceeded) {
					return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: true}, fmt.Errorf("stream usage incomplete: %w", ev.err)
				}
				// Return partial usage if upstream also fails after a client disconnect.
				if clientDisconnected {
					return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: true}, fmt.Errorf("stream usage incomplete after disconnect: %w", ev.err)
				}
				if errors.Is(ev.err, bufio.ErrTooLong) {
					logger.LegacyPrintf("service.gateway", "SSE line too long: provider=%d max_size=%d error=%v", options.ProviderID, maxLineSize, ev.err)
					sendErrorEvent("response_too_large", fmt.Sprintf("upstream SSE line exceeded %d bytes", maxLineSize))
					return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs}, ev.err
				}
				// Before any client write, return a failover error so the caller can retry.
				// After output starts, report an SSE error because the stream cannot resume.
				// Sanitize client-visible text: network errors can expose internal addresses.
				// Keep the complete error in internal logs for diagnosis.
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
							// Keep processing this event and later usage after the write fails.
							// Skipping this event could omit tokens already consumed upstream.
						} else {
							flusher.Flush()
							lastDataAt = time.Now()
							resetKeepaliveTimer()
						}
					}
					if data != "" {
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
			// Let the caller update provider state for the timeout.
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

// StreamEventIsTerminal recognizes message_stop or the [DONE] sentinel.
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

func (e *StreamErrorEventError) Error() string { return "have error in stream" }
