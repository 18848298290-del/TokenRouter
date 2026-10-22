package openaiforward

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// anthropic_direct_output.go delivers native Anthropic output while collecting
// model and usage observations.
// Native Anthropic output can forward body bytes while independently collecting
// model and usage evidence for completion. Buffered output validates the
// response as JSON before optional cache classification and tool restoration.
// Streaming output preserves SSE lines, records terminal evidence, and
// continues collecting usage after client writes fail.
//
// A scanner goroutine owns the pooled buffer until input ends, while the
// consumer separately monitors last upstream reads and downstream keepalive
// spacing. Heartbeats are emitted only between complete SSE events, preventing
// a ping from splitting a vendor frame. Return signals stop channel
// publication, and the execution caller closes the body to release a scanner
// blocked on upstream input; partial usage remains available beside incomplete-
// stream errors.

// NativeAnthropicOptions binds direct Messages output to request observations and HTTP effects.
type NativeAnthropicOptions struct {
	ProviderID int64 // selected provider diagnostic identifier
	// response-header session-window observer
	UpdateWindow func(context.Context, http.Header)
	// bounded response-body byte reader
	ReadBody func(io.Reader) ([]byte, error)
	// non-JSON upstream failure presenter
	InvalidJSON func(context.Context, *http.Response, []byte, error, string) error
	// buffered cache-classification policy reader
	ForceCache func(context.Context) bool
	// ClassifyCache prepares buffered response usage for client output when
	// ForceCache permits it. The installed adapter rewrites body counts while
	// retaining the parsed usage pointer unchanged.
	ClassifyCache func([]byte, *upstream.TokenUsage) ([]byte, error)
	CopyHeaders   func(http.Header, http.Header) // filtered response header copier
	ReverseTools  func([]byte) []byte            // request-bound tool name restorer
	MaxLineSize   func() int                     // configured scanner growth limit
	// StreamInterval governs upstream idle checks from the last scanner read;
	// KeepaliveInterval governs client pings between complete events. Nonpositive
	// durations disable their corresponding timer paths.
	StreamInterval, KeepaliveInterval func() time.Duration        // StreamInterval is upstream read spacing; KeepaliveInterval is downstream ping spacing
	ExtractData                       func(string) (string, bool) // SSE data value extractor
	IsTerminal                        func(string, string) bool   // SSE terminal evidence classifier
	Log                               func(string, ...any)        // native output diagnostic writer
	// provider stream-timeout policy observer
	HandleTimeout func(context.Context, string)
}

// NativeAnthropicBuffered reads and validates a native Messages body before
// forwarding its tool-restored bytes. The execution caller owns response
// closure, and installed header/body callbacks provide bounded reading and
// session-window observation. JSON validation accepts a complete raw JSON value
// before usage parsing. The standard forced-cache callback changes client
// response counts while the returned result retains parsed token buckets, and
// vendor model observation occurs before output restoration.
func NativeAnthropicBuffered(ctx context.Context, resp *http.Response, c *upstream.OutputContext, o NativeAnthropicOptions, originalModel, billingModel, upstreamModel string, reasoningEffort *string, startTime time.Time) (out *Result, failure error) {
	var modelObserver protocol.ResponseModelObserver
	defer func() {
		if out != nil {
			out.UpstreamResponseModel = modelObserver.Model()
		}
	}()
	o.UpdateWindow(ctx, resp.Header)

	body, err := o.ReadBody(resp.Body)
	if err != nil {
		return nil, err
	}
	modelObserver.ObserveAnthropic(body)

	var raw json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, o.InvalidJSON(ctx, resp, body, err, billingModel)
	}

	usage := protocolanthropic.ParseClaudeUsageFromResponseBody(body)
	// Forced cache classification rewrites buffered client-visible usage after
	// parsing; the installed classifier keeps the parsed observation available for
	// the returned completion result.
	if o.ForceCache(ctx) && usage.InputTokens > 0 {
		body, err = o.ClassifyCache(body, usage)
		if err != nil {
			return nil, err
		}
	}

	o.CopyHeaders(c.Writer.Header(), resp.Header)
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/json"
	}
	body = o.ReverseTools(body)
	c.Data(resp.StatusCode, contentType, body)

	return &Result{
		RequestID:        resp.Header.Get("x-request-id"),
		Headers:          resp.Header,
		Usage:            AnthropicUsageToOpenAI(usage),
		Model:            originalModel,
		BillingModel:     billingModel,
		UpstreamModel:    upstreamModel,
		UpstreamEndpoint: "/v1/messages",
		Stream:           false,
		ReasoningEffort:  reasoningEffort,
		Duration:         time.Since(startTime),
	}, nil
}

// NativeAnthropicStreaming forwards native SSE lines while collecting vendor
// model, token usage, and terminal evidence. It requires an output writer with
// Flush; the execution caller closes the response body on every return so an
// abandoned scanner can finish and release pooled storage.
//
// The scanner publishes lines independently and atomically records the latest
// read time. Consumer idle checks use that read time, while keepalives use
// completed output events and wait whenever a frame is partial. Client write
// failure stops output but continues usage collection. EOF or scanner failure
// succeeds after terminal evidence; incomplete input and idle expiry return the
// accumulated result beside an error.
func NativeAnthropicStreaming(ctx context.Context, resp *http.Response, c *upstream.OutputContext, o NativeAnthropicOptions, originalModel, billingModel, upstreamModel string, reasoningEffort *string, startTime time.Time) (out *Result, failure error) {
	var modelObserver protocol.ResponseModelObserver
	defer func() {
		if out != nil {
			out.UpstreamResponseModel = modelObserver.Model()
		}
	}()
	o.UpdateWindow(ctx, resp.Header)

	o.CopyHeaders(c.Writer.Header(), resp.Header)

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

	clientDisconnected := false
	sawTerminalEvent := false

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := o.MaxLineSize()
	scanBuf := httpclient.GetSSEScannerBuf64K()
	// The 64 KiB starting capacity remains usable when the configured maximum
	// is smaller; that maximum bounds subsequent scanner growth for this
	// native stream.
	scanner.Buffer(scanBuf[:0], maxLineSize)

	// scanEvent carries a scanned native SSE line or scanner failure to the consumer.
	type scanEvent struct {
		line string // scanned native SSE line
		err  error  // scanner failure evidence
	}
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
	// Keep pooled scanner storage with the reader goroutine until Scan ends; a
	// returning consumer may still need the caller's body closure to unblock that
	// reader.
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
	// Signal abandoned channel publication on return; this releases sends but not
	// a blocked Scan, which remains tied to the execution caller's body cleanup.
	defer close(done)

	streamInterval := o.StreamInterval()
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

	keepaliveInterval := o.KeepaliveInterval()
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
		// Drain an available expired notification before resetting keepalive spacing
		// so an earlier idle period does not trigger a ping immediately after fresh
		// output.
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
				if !sawTerminalEvent {
					return NativeAnthropicStreamResult(resp, usage, clientDisconnected, originalModel, billingModel, upstreamModel, reasoningEffort, startTime),
						fmt.Errorf("stream usage incomplete: missing terminal event")
				}
				return NativeAnthropicStreamResult(resp, usage, clientDisconnected, originalModel, billingModel, upstreamModel, reasoningEffort, startTime), nil
			}
			if ev.err != nil {
				// Terminal evidence makes a later scanner failure a completed stream;
				// return the collected observation before incomplete-usage handling can
				// replace that conclusion.
				if sawTerminalEvent {
					return NativeAnthropicStreamResult(resp, usage, clientDisconnected, originalModel, billingModel, upstreamModel, reasoningEffort, startTime), nil
				}
				if clientDisconnected {
					return NativeAnthropicStreamResult(resp, usage, clientDisconnected, originalModel, billingModel, upstreamModel, reasoningEffort, startTime),
						fmt.Errorf("stream usage incomplete after disconnect: %w", ev.err)
				}
				if errors.Is(ev.err, context.Canceled) || errors.Is(ev.err, context.DeadlineExceeded) {
					return NativeAnthropicStreamResult(resp, usage, clientDisconnected, originalModel, billingModel, upstreamModel, reasoningEffort, startTime),
						fmt.Errorf("stream usage incomplete: %w", ev.err)
				}
				if errors.Is(ev.err, bufio.ErrTooLong) {
					o.Log("[CN Anthropic passthrough] SSE line too long: provider=%d max_size=%d error=%v", o.ProviderID, maxLineSize, ev.err)
					return NativeAnthropicStreamResult(resp, usage, clientDisconnected, originalModel, billingModel, upstreamModel, reasoningEffort, startTime), ev.err
				}
				return NativeAnthropicStreamResult(resp, usage, clientDisconnected, originalModel, billingModel, upstreamModel, reasoningEffort, startTime),
					fmt.Errorf("stream read error: %w", ev.err)
			}

			line := ev.line
			if data, ok := o.ExtractData(line); ok {
				modelObserver.ObserveAnthropic([]byte(data))
				trimmed := strings.TrimSpace(data)
				if o.IsTerminal("", trimmed) {
					sawTerminalEvent = true
				}

				protocolanthropic.ParseSSEUsagePassthrough(data, usage)
			} else {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "event:") && o.IsTerminal(strings.TrimSpace(strings.TrimPrefix(trimmed, "event:")), "") {
					sawTerminalEvent = true
				}
			}

			// Output failure changes the client-disconnect flag while the scanner
			// continues publishing; later data still updates usage and terminal evidence
			// for completion handling.
			if !clientDisconnected {
				restored := string(o.ReverseTools([]byte(line)))
				if _, err := io.WriteString(w, restored); err != nil {
					clientDisconnected = true
					o.Log("[CN Anthropic passthrough] Client disconnected during streaming, continue draining upstream for usage: provider=%d", o.ProviderID)
				} else if _, err := io.WriteString(w, "\n"); err != nil {
					clientDisconnected = true
					o.Log("[CN Anthropic passthrough] Client disconnected during streaming, continue draining upstream for usage: provider=%d", o.ProviderID)
				} else if line == "" {
					// A blank line completes the SSE frame; flush then refresh keepalive
					// spacing so heartbeats are measured from completed client output.
					flusher.Flush()
					lastDataAt = time.Now()
					resetKeepaliveTimer()
					inPartialEvent = false
				} else {
					inPartialEvent = true
				}
			}

		// Check elapsed upstream silence from the atomically published scanner
		// timestamp; queued consumer work alone does not reset the vendor read
		// interval.
		case <-intervalCh:
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if time.Since(lastRead) < streamInterval {
				continue
			}
			if clientDisconnected {
				return NativeAnthropicStreamResult(resp, usage, clientDisconnected, originalModel, billingModel, upstreamModel, reasoningEffort, startTime),
					fmt.Errorf("stream usage incomplete after timeout")
			}
			o.Log("[CN Anthropic passthrough] Stream data interval timeout: provider=%d model=%s interval=%s", o.ProviderID, upstreamModel, streamInterval)
			o.HandleTimeout(ctx, upstreamModel)
			return NativeAnthropicStreamResult(resp, usage, clientDisconnected, originalModel, billingModel, upstreamModel, reasoningEffort, startTime),
				fmt.Errorf("stream data interval timeout")

		case <-keepaliveCh:
			if clientDisconnected {
				continue
			}
			// Wait while a vendor frame is partial, because inserting a ping between its
			// lines would combine two event payloads into an invalid SSE frame.
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
				o.Log("[CN Anthropic passthrough] Client disconnected during keepalive ping, continue draining upstream for usage: provider=%d", o.ProviderID)
				continue
			}
			flusher.Flush()
			lastDataAt = time.Now()
			resetKeepaliveTimer()
		}
	}
}

// NativeAnthropicStreamResult assembles the current native stream observation
// for success or failure. Nil usage selects empty token buckets; headers and
// pointer fields retain supplied storage, while duration is measured from the
// supplied start time.
func NativeAnthropicStreamResult(resp *http.Response, usage *upstream.TokenUsage, clientDisconnect bool, originalModel, billingModel, upstreamModel string, reasoningEffort *string, startTime time.Time) *Result {
	if usage == nil {
		usage = &upstream.TokenUsage{}
	}
	return &Result{
		RequestID:        resp.Header.Get("x-request-id"),
		Headers:          resp.Header,
		Usage:            AnthropicUsageToOpenAI(usage),
		Model:            originalModel,
		BillingModel:     billingModel,
		UpstreamModel:    upstreamModel,
		UpstreamEndpoint: "/v1/messages",
		Stream:           true,
		ReasoningEffort:  reasoningEffort,
		Duration:         time.Since(startTime),
		Timings:          nil,
		ClientDisconnect: clientDisconnect,
	}
}
