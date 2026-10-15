package anthropic

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// stream_passthrough.go forwards API-key SSE lines and collects usage through
// downstream write failures.
// API-key SSE passthrough sends upstream lines while parsing their usage for
// local accounting. The first nonempty data payload supplies legacy arrival
// timing, so an initial message_start can establish that timestamp before any
// semantic content appears.
//
// Scanner reads and downstream writes use a bounded channel. A full channel
// pauses further scans until the loop receives a line. Read timestamps govern
// the idle limit, while keepalive timing follows completed client frames
// and avoids injecting a ping into a partially written event. A failed client
// write stops further delivery but leaves upstream parsing active for final
// usage. message_stop or DONE provides completion evidence; EOF or read failure
// before that evidence returns accumulated state with an error. The reader
// signals the scanner to stop delivering on return, and its caller closes the
// body to unblock any remaining transport read.

// StreamResponsePassthrough forwards API-key SSE lines and returns parsed
// usage, legacy arrival timing, and client-disconnect state. Callers provide a
// serialized output context with flushing support and close resp.Body after
// use. Nonpositive MaxLineSize uses the shared default; nonpositive timer
// settings disable those timer paths.
//
// Data lines are observed before delivery and feed the compatible usage parser.
// A write failure leaves upstream reads active, while idle timeout or
// incomplete terminal evidence returns partial usage. Keepalives wait for
// complete forwarded frames, preserving line framing during upstream pauses.
// The first nonempty non-DONE data line sets FirstTokenMs independently of
// semantic output, and a terminal observation allows later EOF or read error to
// complete successfully.
//
// FirstTokenMs is assigned before writing its selected data line. Earlier
// event-name lines may already have been written, so their synchronous sink
// delay can precede that legacy timing observation.
func StreamResponsePassthrough(
	ctx context.Context,
	resp *http.Response,
	c *upstream.OutputContext,
	options StreamOptions,
	startTime time.Time,
	model string,
) (*StreamResult, error) {
	if options.UpdateWindow != nil {
		options.UpdateWindow(ctx, resp.Header)
	}

	if options.WriteHeaders != nil {
		options.WriteHeaders(c.Writer.Header(), resp.Header)
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "text/event-stream"
	}
	c.Header("Content-Type", contentType)
	if c.Writer.Header().Get("Cache-Control") == "" {
		c.Header("Cache-Control", "no-cache")
	}
	if c.Writer.Header().Get("Connection") == "" {
		c.Header("Connection", "keep-alive")
	}
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
	clientDisconnected := false
	sawTerminalEvent := false

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := options.MaxLineSize
	if maxLineSize <= 0 {
		maxLineSize = upstream.DefaultSSELineLimit
	}
	// The pooled initial buffer keeps its 64 KiB capacity even when the
	// configured growth limit is smaller, so that setting cannot shrink the
	// already available scanner storage.
	scanBuf := httpclient.GetSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)

	// scanEvent carries one scanner line or read error to the passthrough loop.
	type scanEvent struct {
		line string // upstream scanner line text
		err  error  // upstream scanner read failure
	}
	// Scanner reads advance separately from synchronous client writes and timer
	// handling. The bounded event channel carries lines and read failure, while
	// done lets delivery stop once the response loop returns.
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
	// The scanner publishes successful read time atomically, even when client
	// writes delay event processing. Idle decisions read that timestamp rather
	// than using downstream progress as upstream activity.
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
	// Signal the scanner to stop channel delivery when processing returns. A
	// blocked body read still relies on caller-owned closure, and buffer return
	// waits for that goroutine's exit.
	defer close(done)

	streamInterval := options.Interval
	var intervalTimer *time.Timer
	if streamInterval > 0 {
		intervalTimer = time.NewTimer(streamInterval)
		defer intervalTimer.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTimer != nil {
		intervalCh = intervalTimer.C
	}
	// Dequeuing a line restarts interval delivery while the scanner timestamp
	// remains the idle authority. Stop and drain prior timer delivery before
	// reset so an old tick does not carry forward.
	resetIntervalTimer := func() {
		if intervalTimer == nil {
			return
		}
		if !intervalTimer.Stop() {
			select {
			case <-intervalTimer.C:
			default:
			}
		}
		intervalTimer.Reset(streamInterval)
	}

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
	// A completed client frame restarts downstream idle scheduling. Stop and
	// drain pending timer delivery before resetting so a prior heartbeat tick
	// cannot immediately follow that fresh frame.
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

	inPartialEvent := false

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				if !clientDisconnected {
					flusher.Flush()
				}
				// EOF completes successfully only when terminal evidence was
				// already seen. An incomplete stream retains its usage and
				// disconnect state, with elapsed idle failure selected for the
				// disconnected timeout case.
				if !sawTerminalEvent {
					if clientDisconnected && streamInterval > 0 {
						lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
						if time.Since(lastRead) >= streamInterval {
							return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: true}, fmt.Errorf("stream usage incomplete after timeout")
						}
					}
					return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: clientDisconnected}, fmt.Errorf("stream usage incomplete: missing terminal event")
				}
				return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: clientDisconnected}, nil
			}
			if ev.err != nil {
				// A terminal marker was observed before this read error. Retain
				// the collected completed result, because that later scanner
				// failure does not remove the earlier completion evidence.
				if sawTerminalEvent {
					return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: clientDisconnected}, nil
				}
				// A client write failure left upstream reads active for
				// accounting. When the scanner also fails, return accumulated
				// usage with that disconnected error so the partial result
				// stays available.
				if clientDisconnected {
					return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: true}, fmt.Errorf("stream usage incomplete after disconnect: %w", ev.err)
				}
				// Context interruption before terminal evidence can leave final
				// usage absent. Retain earlier counters and wrap the
				// cancellation error with a disconnect marker for attempt
				// completion handling.
				if errors.Is(ev.err, context.Canceled) || errors.Is(ev.err, context.DeadlineExceeded) {
					return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: true}, fmt.Errorf("stream usage incomplete: %w", ev.err)
				}
				// An oversized Scanner token cannot provide another complete
				// usage line. Keep previously accumulated observations and
				// return the scan failure for caller handling of that
				// incomplete response.
				if errors.Is(ev.err, bufio.ErrTooLong) {
					logger.LegacyPrintf("service.gateway", "[Anthropic passthrough] SSE line too long: provider=%d max_size=%d error=%v", options.ProviderID, maxLineSize, ev.err)
					return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs}, ev.err
				}
				return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs}, fmt.Errorf("stream read error: %w", ev.err)
			}
			resetIntervalTimer()

			line := ev.line
			if data, ok := ExtractSSEDataLine(line); ok {
				observation := wire.ObserveEvent(data)
				if options.Observe != nil {
					options.Observe(observation)
				}
				c.NextEvent(observation.Semantic, observation.Terminal)
				trimmed := strings.TrimSpace(data)
				if StreamEventIsTerminal("", trimmed) {
					sawTerminalEvent = true
				}
				// Legacy arrival time can start at message metadata rather than
				// content. Record it for the first nonempty non-DONE data line
				// while semantic observation continues using its separate
				// evidence flag.
				if firstTokenMs == nil && trimmed != "" && trimmed != "[DONE]" {
					ms := int(time.Since(startTime).Milliseconds())
					firstTokenMs = &ms
				}
				// Compatible usage parsing can reconcile prompt totals and
				// cache aliases while passthrough forwards the original line.
				// Keep that local accounting normalization independent of
				// client JSON presentation.
				wire.ParseSSEUsagePassthrough(data, usage)
				if options.ObserveState != nil {
					options.ObserveState(usage, firstTokenMs)
				}
			} else {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "event:") && StreamEventIsTerminal(strings.TrimSpace(strings.TrimPrefix(trimmed, "event:")), "") {
					sawTerminalEvent = true
				}
			}

			// A failed line or newline write ends further client delivery,
			// while the loop continues reading usage. Flush at blank lines so
			// successful output reaches complete frame boundaries before
			// keepalive scheduling resets.
			if !clientDisconnected {
				restored := string(RestoreToolNamesInBytes([]byte(line), options.ToolNames))
				if _, err := io.WriteString(w, restored); err != nil {
					clientDisconnected = true
					logger.LegacyPrintf("service.gateway", "[Anthropic passthrough] Client disconnected during streaming, continue draining upstream for usage: provider=%d", options.ProviderID)
				} else if _, err := io.WriteString(w, "\n"); err != nil {
					clientDisconnected = true
					logger.LegacyPrintf("service.gateway", "[Anthropic passthrough] Client disconnected during streaming, continue draining upstream for usage: provider=%d", options.ProviderID)
				} else if line == "" {
					flusher.Flush()
					lastDataAt = time.Now()
					resetKeepaliveTimer()
					inPartialEvent = false
				} else {
					inPartialEvent = true
				}
			}

		case <-intervalCh:
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if elapsed := time.Since(lastRead); elapsed < streamInterval {
				// The scanner can read after the interval timer was scheduled
				// but before its tick is handled. Wait the remaining idle
				// duration so timeout follows that latest successful read.
				intervalTimer.Reset(streamInterval - elapsed)
				continue
			}
			if clientDisconnected {
				return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs, ClientDisconnect: true}, fmt.Errorf("stream usage incomplete after timeout")
			}
			logger.LegacyPrintf("service.gateway", "[Anthropic passthrough] Stream data interval timeout: provider=%d model=%s interval=%s", options.ProviderID, model, streamInterval)
			if options.OnTimeout != nil {
				options.OnTimeout(ctx, model)
			}
			return &StreamResult{Usage: usage, FirstTokenMs: firstTokenMs}, fmt.Errorf("stream data interval timeout")

		case <-keepaliveCh:
			if clientDisconnected {
				continue
			}
			// A forwarded frame is still open, so injecting ping would mix
			// another event's lines into it. Defer the heartbeat until
			// blank-line output completes that frame.
			if inPartialEvent {
				resetKeepaliveTimer()
				continue
			}
			if time.Since(lastDataAt) < keepaliveInterval {
				resetKeepaliveTimer()
				continue
			}
			if _, err := fmt.Fprint(w, "event: ping\ndata: {\"type\": \"ping\"}\n\n"); err != nil {
				clientDisconnected = true
				logger.LegacyPrintf("service.gateway", "[Anthropic passthrough] Client disconnected during keepalive ping, continue draining upstream for usage: provider=%d", options.ProviderID)
				continue
			}
			flusher.Flush()
			lastDataAt = time.Now()
			resetKeepaliveTimer()
		}
	}
}

// ExtractSSEDataLine returns the payload after an exact leading data: prefix.
// It removes subsequent spaces and tabs while retaining the remaining bytes;
// other lines return an empty payload and false.
func ExtractSSEDataLine(line string) (string, bool) {
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	start := len("data:")
	for start < len(line) {
		if line[start] != ' ' && line[start] != '\t' {
			break
		}
		start++
	}
	return line[start:], true
}
