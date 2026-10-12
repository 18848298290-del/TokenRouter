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

type ResponseOptions struct {
	ObserveRaw                                                      func([]byte)
	MaxLineSize, StreamDataIntervalTimeout, StreamKeepaliveInterval int
	ReverseTools                                                    func([]byte) []byte
	ClaudeError, CompatError                                        func(int, string, string) error
	MapCollectionError                                              func(error) error
	Failover                                                        func([]byte) error
	IsFailover                                                      func(error) bool
	MarkCommitted                                                   func()
}

type (
	ResponseAdapter struct{ Options ResponseOptions }
	StreamResult    struct {
		Usage            *upstream.TokenUsage
		FirstTokenMs     *int
		ClientDisconnect bool // client disconnected during streaming
	}
)

// ClientWriter flushes stream writes and records the first write failure.
// Later writes return false; callers may continue draining upstream for usage.
type ClientWriter struct {
	w                upstream.OutputWriter
	flusher          http.Flusher
	disconnected     bool
	prefix           string // identifies the response path in logs
	beforeFirstWrite func()
}

func NewClientWriter(w upstream.OutputWriter, flusher http.Flusher, prefix string) *ClientWriter {
	return &ClientWriter{w: w, flusher: flusher, prefix: prefix}
}

// Write flushes data and returns false after a write failure or prior disconnect.
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

// Fprintf formats and flushes data, returning false after a write failure or disconnect.
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

func (cw *ClientWriter) Disconnected() bool { return cw.disconnected }

func (cw *ClientWriter) prepareFirstWrite() {
	if cw.beforeFirstWrite == nil {
		return
	}
	prepare := cw.beforeFirstWrite
	cw.beforeFirstWrite = nil
	prepare()
}

func (cw *ClientWriter) markDisconnected() {
	cw.disconnected = true
	logger.LegacyPrintf("service.antigravity_gateway", "Client disconnected during streaming (%s), continuing to drain upstream for billing", cw.prefix)
}

// HandleStreamReadError recognizes cancellation and read failures after disconnect.
// When handled is true, the caller should return the usage collected so far.
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

	// Bound each scanned line so a malformed upstream cannot grow it without limit.
	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := upstream.DefaultSSELineLimit
	if s.Options.MaxLineSize > 0 {
		maxLineSize = s.Options.MaxLineSize
	}
	scanBuf := httpclient.GetSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)
	usage := &upstream.TokenUsage{}
	var firstTokenMs *int

	type scanEvent struct {
		line string
		err  error
	}
	// Read separately so a blocked upstream read does not block timeout handling.
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

	// Stop waiting when the upstream has been silent for the configured interval.
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

	// Keep idle downstream connections active through intermediaries.
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

	// Emit at most one error event for this stream.
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

				if u := bridge.NativeUsageProjection(bridge.NativeExtractGeminiUsage(inner)); u != nil {
					usage = u
				}
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

// HandleGeminiStreamToNonStreaming collects the upstream SSE response into Gemini JSON.
// Text and image parts from incremental chunks are accumulated across the stream.
func (s *ResponseAdapter) HandleGeminiStreamToNonStreaming(c *upstream.OutputContext, resp *http.Response, startTime time.Time) (*StreamResult, error) {
	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := upstream.DefaultSSELineLimit
	if s.Options.MaxLineSize > 0 {
		maxLineSize = s.Options.MaxLineSize
	}
	scanBuf := httpclient.GetSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)

	usage := &upstream.TokenUsage{}
	var firstTokenMs *int
	var last map[string]any
	var lastWithParts map[string]any
	var collectedImageParts []map[string]any // image parts across chunks
	var collectedTextParts []string          // text fragments across chunks

	type scanEvent struct {
		line string
		err  error
	}

	// Read separately so a blocked upstream read does not block timeout handling.
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

	// Stop waiting when the upstream has been silent for the configured interval.
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

			var parsed map[string]any
			if err := json.Unmarshal(inner, &parsed); err != nil {
				continue
			}

			// This path measures the first decoded JSON payload, even if it has no content.
			if firstTokenMs == nil {
				ms := int(time.Since(startTime).Milliseconds())
				firstTokenMs = &ms
			}

			last = parsed

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

			// Keep content-bearing output when a trailing chunk contains only metadata.
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

	// Delegate empty-stream retry or provider switching to the supplied failure policy.
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

// GetOrCreateGeminiParts returns a shallow top-level copy and a first-candidate parts setter.
func GetOrCreateGeminiParts(response map[string]any) (result map[string]any, existingParts []any, setParts func([]any)) {
	// Nested candidates and content remain shared with the input response.
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

// MergeCollectedPartsToResponse replaces candidate parts with collected stream content.
// Adjacent ordinary text parts are concatenated; thought and non-text parts retain their order.
// Nested candidate state may be shared with response.
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
				// Thoughts interrupt ordinary text runs and retain their own metadata.
				flushTextBuffer()
				mergedParts = append(mergedParts, part)
			} else {
				_, _ = textBuffer.WriteString(text)
			}
		} else {
			// Tool and image parts interrupt ordinary text runs.
			flushTextBuffer()
			mergedParts = append(mergedParts, part)
		}
	}

	flushTextBuffer()

	setParts(mergedParts)
	return result
}

// MergeImagePartsToResponse appends images unless the response already has inlineData.
func MergeImagePartsToResponse(response map[string]any, imageParts []map[string]any) map[string]any {
	if len(imageParts) == 0 {
		return response
	}

	result, existingParts, setParts := GetOrCreateGeminiParts(response)

	for _, p := range existingParts {
		if pm, ok := p.(map[string]any); ok {
			if _, hasInline := pm["inlineData"]; hasInline {
				return result // the response already contains image data
			}
		}
	}

	for _, imgPart := range imageParts {
		existingParts = append(existingParts, imgPart)
	}
	setParts(existingParts)
	return result
}

// MergeTextPartsToResponse puts concatenated text in the first text part, or prepends one.
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

	if !textUpdated {
		newParts = append([]any{map[string]any{"text": mergedText}}, newParts...)
	}

	setParts(newParts)
	return result
}

// CollectClaudeStreamResponse collects upstream SSE and returns Claude JSON plus usage.
// This supports non-streaming clients when the upstream supplies a stream.
func (s *ResponseAdapter) CollectClaudeStreamResponse(resp *http.Response, startTime time.Time, originalModel string) ([]byte, *StreamResult, error) {
	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := upstream.DefaultSSELineLimit
	if s.Options.MaxLineSize > 0 {
		maxLineSize = s.Options.MaxLineSize
	}
	scanBuf := httpclient.GetSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)

	var firstTokenMs *int
	var last map[string]any
	var lastWithParts map[string]any
	var collectedParts []map[string]any // all part types in arrival order
	var meaningfulResponse bool

	type scanEvent struct {
		line string
		err  error
	}

	// Read separately so a blocked upstream read does not block timeout handling.
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

	// Stop waiting when the upstream has been silent for the configured interval.
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

			var parsed map[string]any
			if err := json.Unmarshal(inner, &parsed); err != nil {
				continue
			}

			last = parsed

			// Retain content-bearing output when the final chunk contains only metadata.
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
	// A stream with neither content parts nor a finish reason is eligible for failover.
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

// HandleClaudeStreamToNonStreaming collects upstream SSE and writes a Claude JSON response.
// Collection failures are mapped to the configured Claude error policy.
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

// HandleClaudeStreamingResponse converts Gemini SSE to Claude SSE.
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
	// Bound each scanned line so a malformed upstream cannot grow it without limit.
	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := upstream.DefaultSSELineLimit
	if s.Options.MaxLineSize > 0 {
		maxLineSize = s.Options.MaxLineSize
	}
	scanBuf := httpclient.GetSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)

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

	type scanEvent struct {
		line string
		err  error
	}
	// Read separately so a blocked upstream read does not block timeout handling.
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

	// Keep idle downstream connections active through intermediaries.
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

	// Emit at most one error event for this stream.
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
					// No message_start was emitted and finalization produced no events.
					// Delegate retry selection before sending an incomplete Claude stream.
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

func (s *ResponseAdapter) ExtractImageInputSize(body []byte) string {
	var req protocolgemini.GeminiRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}

	if req.GenerationConfig != nil && req.GenerationConfig.ImageConfig != nil {
		return strings.TrimSpace(req.GenerationConfig.ImageConfig.ImageSize)
	}

	return ""
}

func IsImageGenerationModel(model string) bool { return upstream.IsGeminiImageGenerationModel(model) }

func (s *ResponseAdapter) observeRaw(data []byte) {
	if s.Options.ObserveRaw != nil {
		s.Options.ObserveRaw(data)
	}
}

// UnwrapV1InternalResponse extracts response, returning the input when that field is absent.
func (s *ResponseAdapter) UnwrapV1InternalResponse(body []byte) ([]byte, error) {
	result := gjson.GetBytes(body, "response")
	if result.Exists() {
		return []byte(result.Raw), nil
	}
	return body, nil
}
