package openai

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// images_apikey_response.go forwards API-key image responses while collecting
// image counts, dimensions, usage, and stream timing.
// API-key image streams can contain SSE events or a plain JSON response despite
// being requested as streams. Raw lines reach the client as they arrive, while
// event payloads feed usage and completed-image accounting independently of
// downstream write success. A plain-body observation is retained only before
// the first SSE data event and within the caller's fallback limit.
//
// Idle checks use the reader's upstream arrival timestamp, while heartbeats use
// the last successful downstream write. The timed path separates reading from
// consumption through a bounded channel. Returning signals queued sends to
// stop; the executor still owns closing the response body to release a blocked
// read. Generated image counts and usage remain available after disconnection.

// ReadImagesNonStreaming reads an API-key image body, captures upstream usage
// and image dimensions, and writes the backfilled representation with the
// upstream status. ReadBody, Backfill, and ResponseHeaders are required for
// this path; the executor retains response-body ownership.
//
// Usage and output counts are extracted before Backfill can replace URL entries
// with encoded data. PreserveContentType allows a supplied upstream type to
// override the JSON default. Sink delivery uses OutputContext.Data, whose write
// outcome is not returned by this reader.
func ReadImagesNonStreaming(resp *http.Response, sink upstream.OutputSink, options ImageResponseOptions) (wire.ForwardUsage, int, []string, error) {
	body, err := options.ReadBody(resp.Body)
	if err != nil {
		return wire.ForwardUsage{}, 0, nil, err
	}
	// Observe accounting before Backfill changes the client representation, so URL
	// download outcomes cannot remove upstream-reported image usage or dimensions.
	usage, _ := wire.ExtractOpenAIUsageFromJSONBytes(body)
	imageCount := wire.CountOpenAIResponseImageOutputsFromJSONBytes(body)
	imageSizes := wire.CollectOpenAIResponseImageOutputSizesFromJSONBytes(body)
	body = options.Backfill(body)
	c := upstream.NewOutputContext(sink)
	options.ResponseHeaders(c.Writer.Header(), resp.Header)
	contentType := "application/json"
	if options.PreserveContentType {
		if upstreamType := resp.Header.Get("Content-Type"); upstreamType != "" {
			contentType = upstreamType
		}
	}
	c.Data(resp.StatusCode, contentType, body)

	return usage, imageCount, imageSizes, nil
}

// ReadImagesStreaming forwards API-key image lines while accumulating usage,
// image counts and sizes. The writer needs http.Flusher;
// ReadLimit, ResponseHeaders, StreamInterval, KeepaliveInterval, and Logf
// support the selected paths. The executor closes resp.Body after return,
// including when a timed reader remains blocked.
//
// SSE data supersedes the bounded plain-body buffer. EOF flushes a pending SSE
// event and examines a retained plain JSON body, while read errors flush SSE
// alone. A failed downstream write disables further writes but keeps reading
// for accounting until EOF, an error, or idle timeout.
func ReadImagesStreaming(resp *http.Response, c *upstream.OutputContext, options ImageResponseOptions, startTime time.Time) (wire.ForwardUsage, int, []string, error) {
	options.ResponseHeaders(c.Writer.Header(), resp.Header)
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "text/event-stream"
	}
	c.Status(resp.StatusCode)
	c.Header("Content-Type", contentType)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return wire.ForwardUsage{}, 0, nil, fmt.Errorf("streaming is not supported by response writer")
	}

	usage := wire.ForwardUsage{}
	imageCounter := wire.NewOpenAIImageOutputCounter()

	clientDisconnected := false
	lastDownstreamWriteAt := time.Now()
	var fallbackBody bytes.Buffer
	fallbackBytes := int64(0)
	fallbackLimit := options.ReadLimit()
	seenSSEData := false
	fallbackTooLarge := false
	var sseData wire.SSEDataAccumulator

	processSSEData := func(dataBytes []byte) {
		// Once an SSE payload is observed, discard speculative plain-body bytes so
		// framing text cannot also be billed as JSON.
		seenSSEData = true
		fallbackBody.Reset()
		fallbackBytes = 0
		wire.MergeImageResponseUsage(&usage, dataBytes)
		imageCounter.AddSSEData(dataBytes)
	}

	flushSSEEvent := func() {
		sseData.Flush(processSSEData)
	}

	processLine := func(line []byte) {
		if len(line) == 0 {
			return
		}
		// Latch a failed sink write while continuing payload observation, so usage
		// can still be collected from later upstream events.
		if !clientDisconnected {
			if _, writeErr := c.Writer.Write(line); writeErr != nil {
				clientDisconnected = true
				options.Logf("[OpenAI] Images stream client disconnected, continue draining upstream for billing")
			} else {
				flusher.Flush()
				lastDownstreamWriteAt = time.Now()
			}
		}

		trimmedLine := strings.TrimRight(string(line), "\r\n")
		if _, ok := wire.ExtractSSEDataLine(trimmedLine); ok || strings.TrimSpace(trimmedLine) == "" {
			sseData.AddLine(trimmedLine, processSSEData)
			return
		}
		// Keep possible plain JSON only before SSE data. Overflow clears retained
		// bytes and disables further retention, while line reads remain uncapped
		// here.
		if !seenSSEData && !fallbackTooLarge {
			fallbackBytes += int64(len(line))
			if fallbackBytes <= fallbackLimit {
				_, _ = fallbackBody.Write(line)
			} else {
				fallbackTooLarge = true
				fallbackBody.Reset()
			}
		}
	}

	finalizeFallbackBody := func() {
		if seenSSEData || fallbackBody.Len() == 0 {
			return
		}
		body := bytes.TrimSpace(fallbackBody.Bytes())
		if len(body) == 0 {
			return
		}
		// Plain-body accounting is an EOF alternative to event parsing, preventing
		// the same response from contributing through both representations.
		wire.MergeImageResponseUsage(&usage, body)
		imageCounter.AddJSONResponse(body)
	}

	streamInterval := options.StreamInterval()
	keepaliveInterval := options.KeepaliveInterval()
	if streamInterval <= 0 && keepaliveInterval <= 0 {
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadBytes('\n')
			processLine(line)
			if err == io.EOF {
				break
			}
			if err != nil {
				flushSSEEvent()
				return usage, imageCounter.Count(), imageCounter.Sizes(), err
			}
		}
		flushSSEEvent()
		finalizeFallbackBody()
		return usage, imageCounter.Count(), imageCounter.Sizes(), nil
	}

	// readEvent carries one owned read line or failure to the timed consumer.
	type readEvent struct {
		line []byte // upstream read line
		err  error  // upstream read failure
	}
	// Bound pending lines while the consumer writes or checks timers; done
	// releases blocked sends when that consumer returns.
	events := make(chan readEvent, 16)
	done := make(chan struct{})
	sendEvent := func(ev readEvent) bool {
		select {
		case events <- ev:
			return true
		case <-done:
			return false
		}
	}
	var lastReadAt int64
	atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
	// Publish arrival time before enqueueing, so downstream work does not look
	// like upstream silence. Body closure belongs to the executor.
	go func() {
		defer close(events)
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
			}
			if len(line) > 0 && !sendEvent(readEvent{line: line}) {
				return
			}
			if err == io.EOF {
				return
			}
			if err != nil {
				_ = sendEvent(readEvent{err: err})
				return
			}
		}
	}()
	// Stop sends on every exit. Closing done cannot interrupt ReadBytes; the
	// response owner closes Body to release that read.
	defer close(done)

	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
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

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				flushSSEEvent()
				finalizeFallbackBody()
				return usage, imageCounter.Count(), imageCounter.Sizes(), nil
			}
			if ev.err != nil {
				flushSSEEvent()
				return usage, imageCounter.Count(), imageCounter.Sizes(), ev.err
			}
			processLine(ev.line)
		case <-intervalCh:
			// Recheck actual upstream arrival after a coarse tick, since queued lines
			// and downstream processing can delay timer consumption.
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if time.Since(lastRead) < streamInterval {
				continue
			}
			if clientDisconnected {
				return usage, imageCounter.Count(), imageCounter.Sizes(), fmt.Errorf("image stream incomplete after timeout")
			}
			options.Logf("[OpenAI] Images stream data interval timeout: interval=%s", streamInterval)
			// Report idle failure even if its diagnostic write fails; this return
			// carries the observed usage and the timeout outcome.
			_ = WriteImagesStreamEvent(c, options, flusher, "error", BuildOpenAIImagesStreamErrorBody(fmt.Sprintf("upstream image stream idle for %s", streamInterval)))
			return usage, imageCounter.Count(), imageCounter.Sizes(), fmt.Errorf("image stream data interval timeout")
		case <-keepaliveCh:
			// Heartbeats depend on successful client writes; a heartbeat failure latches
			// disconnection and leaves upstream accounting active.
			if clientDisconnected || time.Since(lastDownstreamWriteAt) < keepaliveInterval {
				continue
			}
			n, writeErr := io.WriteString(c.Writer, ":\n\n")
			if options.RecordKeepalive != nil {
				options.RecordKeepalive(n)
			}
			if writeErr != nil {
				clientDisconnected = true
				options.Logf("[OpenAI] Images stream client disconnected during keepalive, continue draining upstream for billing")
				continue
			}
			flusher.Flush()
			lastDownstreamWriteAt = time.Now()
		}
	}
}
