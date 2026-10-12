package openai

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

	"go.uber.org/zap"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// CompatResponseResult records the upstream response and usage; the inbound handler supplies billing-model context.
type CompatResponseResult struct {
	ReasoningEffort, ResolvedTier                *string
	ResponseID                                   string
	ClientDisconnect                             bool
	RequestID, Model, UpstreamModel, ServiceTier string
	UpstreamHeaders                              http.Header
	Usage                                        wire.ForwardUsage
	Stream                                       bool
	Duration                                     time.Duration
	FirstTokenMs                                 *int
	SearchCount                                  int
}
type ChatFailure struct {
	Failover      error
	Status        int
	Type, Message string
}

// ChatResponseOptions configures Chat conversion, output, reading and error handling.
type ChatResponseOptions struct {
	Runtime                           bridge.Runtime
	RequestContext                    context.Context
	ReadBuffered                      func() (*wire.ResponsesResponse, wire.ForwardUsage, *bridge.BufferedResponseAccumulator, error)
	BufferedReadFailure               func(error) error
	BufferedFailure                   func(*wire.ResponsesResponse, wire.ForwardUsage) error
	ObserveFinal                      func(*wire.ResponsesResponse)
	MissingUsage                      func(*wire.ResponsesResponse, wire.ForwardUsage) error
	Headers                           func(http.Header, http.Header)
	WriteError                        func(int, string, string)
	ServiceTier                       func() string
	CountSearch                       bool
	JSONSearch                        func([]byte) int
	StreamSearch                      func([]byte, map[string]struct{}) int
	Scanner                           func(io.Reader) *bufio.Scanner
	StreamInterval, KeepaliveInterval func() time.Duration
	RestoreToolNames                  func([]byte) []byte
	Observe                           func([]byte, string)
	CyberForwarded                    error
	MarkCyber                         func(CyberObservation)
	Truncate                          func(string, int) string
	BuildStreamError                  func(string, string) string
	StreamFailure                     func([]byte, string, bool) ChatFailure
	SilentRefusal                     func() error
	ReadFailure                       func(error) error
}

// NewCompatStreamHeaderWriter returns a one-shot header writer to invoke at first output.
// Creating the closure does not commit the response.
func NewCompatStreamHeaderWriter(c *upstream.OutputContext, headers http.Header, filter func(http.Header, http.Header)) func() {
	written := false
	return func() {
		if written {
			return
		}
		written = true
		filter(c.Writer.Header(), headers)
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
		c.Writer.WriteHeader(http.StatusOK)
	}
}

// ReadChatBuffered collects the Responses terminal result and converts it to Chat JSON.
func ReadChatBuffered(resp *http.Response, c *upstream.OutputContext, options ChatResponseOptions, originalModel, upstreamModel string, startTime time.Time) (*CompatResponseResult, error) {
	requestID := resp.Header.Get("x-request-id")

	finalResponse, usage, acc, err := options.ReadBuffered()
	if err != nil {
		return nil, options.BufferedReadFailure(err)
	}

	if finalResponse == nil {
		options.WriteError(http.StatusBadGateway, "api_error", "Upstream stream ended without a terminal response event")
		return nil, fmt.Errorf("upstream stream ended without terminal event")
	}
	options.ObserveFinal(finalResponse)
	if strings.TrimSpace(finalResponse.Status) == "failed" {
		return nil, options.BufferedFailure(finalResponse, usage)
	}

	if err := options.MissingUsage(finalResponse, usage); err != nil {
		return nil, err
	}

	// When the terminal event has an empty output array, reconstruct from
	// accumulated delta events so the client receives the full content.
	acc.SupplementResponseOutput(finalResponse)

	chatResp := bridge.ResponsesToChatCompletions(options.Runtime, finalResponse, originalModel)

	options.Headers(c.Writer.Header(), resp.Header)
	// Replace the forwarded SSE Content-Type before writing aggregated JSON.
	// Downstream proxies that select a parser from this header would otherwise treat
	// the buffered JSON body as an SSE stream.
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.JSON(http.StatusOK, chatResp)

	result := &CompatResponseResult{
		RequestID:       requestID,
		UpstreamHeaders: resp.Header,
		Usage:           usage,
		Model:           originalModel,
		UpstreamModel:   upstreamModel,
		ServiceTier:     options.ServiceTier(),
		Stream:          false,
		Duration:        time.Since(startTime),
	}
	// Grok chat bridge: bill native search tools found in the terminal Responses body.
	if options.CountSearch && finalResponse != nil {
		if body, err := json.Marshal(finalResponse); err == nil {
			if n := options.JSONSearch(body); n > 0 {
				result.SearchCount = n
			}
		}
	}
	return result, nil
}

// ReadChatStreaming converts Responses events to a Chat stream and collects usage.
func ReadChatStreaming(resp *http.Response, c *upstream.OutputContext, options ChatResponseOptions, originalModel, upstreamModel string, startTime time.Time, requestBodyLen int) (*CompatResponseResult, error) {
	requestID := resp.Header.Get("x-request-id")
	writeStreamHeaders := NewCompatStreamHeaderWriter(c, resp.Header, options.Headers)

	state := bridge.NewResponsesEventToChatState(options.Runtime)
	state.Model = originalModel
	// Include usage even when the client did not request it so chained billing proxies
	// receive token counts, matching the direct Chat forwarding path.
	state.IncludeUsage = true

	var usage wire.ForwardUsage
	var firstTokenMs *int
	firstChunk := true
	clientDisconnected := false
	clientOutputStarted := false
	pendingSSE := make([]string, 0, 4)
	refusalDetector := NewChatSilentRefusalDetector(requestBodyLen)
	var streamFailoverErr error
	var streamNonFailoverErr error
	var cyberPolicyErr error
	// Deduplicate native search calls across Responses events for Grok search billing.
	searchCount := 0
	streamSearchSeen := make(map[string]struct{})
	countSearch := options.CountSearch

	scanner := options.Scanner(resp.Body)

	streamInterval := options.StreamInterval()
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

	resultWithUsage := func() *CompatResponseResult {
		out := &CompatResponseResult{
			RequestID:       requestID,
			UpstreamHeaders: resp.Header,
			Usage:           usage,
			Model:           originalModel,
			UpstreamModel:   upstreamModel,
			ServiceTier:     options.ServiceTier(),
			Stream:          true,
			Duration:        time.Since(startTime),
			FirstTokenMs:    firstTokenMs,
		}
		if searchCount > 0 {
			out.SearchCount = searchCount
		}
		return out
	}

	processDataLine := func(payload string) bool {
		payload = string(options.RestoreToolNames([]byte(payload)))
		if firstChunk {
			firstChunk = false
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}
		if countSearch {
			searchCount += options.StreamSearch([]byte(payload), streamSearchSeen)
		}

		var event wire.ResponsesStreamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			logger.L().Warn("openai chat_completions stream: failed to parse event",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
			return false
		}
		options.Observe([]byte(payload), event.Type)
		refusalDetector.ObservePayload([]byte(payload))

		isTerminalEvent := IsCompatResponsesTerminalEvent(event.Type)
		if isTerminalEvent {
			if event.Usage != nil {
				usage = wire.CopyForwardUsage(event.Usage)
			}
			if event.Response != nil && event.Response.Usage != nil {
				usage = wire.CopyForwardUsage(event.Response.Usage)
			}
		}
		if strings.TrimSpace(event.Type) == "response.failed" || strings.TrimSpace(event.Type) == "error" {
			payloadBytes := []byte(payload)
			message := ExtractOpenAISSEErrorMessage(payloadBytes)
			if hit, code, msg := DetectOpenAICyberPolicy(payloadBytes); hit {
				options.MarkCyber(CyberObservation{
					Code:           code,
					Message:        msg,
					Body:           options.Truncate(string(payloadBytes), 4096),
					UpstreamStatus: http.StatusOK,
					UpstreamInTok:  usage.InputTokens,
					UpstreamOutTok: usage.OutputTokens,
				})
				if !clientDisconnected {
					writeStreamHeaders()
					clientMsg := msg
					if clientMsg == "" {
						clientMsg = "Request blocked by upstream cyber-security policy"
					}
					if _, err := fmt.Fprint(c.Writer, options.BuildStreamError(code, clientMsg)); err == nil {
						_, _ = fmt.Fprint(c.Writer, "data: [DONE]\n\n")
						if flusher, ok := c.Writer.(http.Flusher); ok {
							flusher.Flush()
						}
					}
				}
				cyberPolicyErr = options.CyberForwarded
				return true
			}
			failure := options.StreamFailure(payloadBytes, message, clientOutputStarted)
			if failure.Failover != nil {
				streamFailoverErr = failure.Failover
				return true
			}
			defaultStatus, defaultErrType, defaultMsg := failure.Status, failure.Type, failure.Message

			errorPayload, _ := json.Marshal(map[string]any{
				"error": map[string]any{
					"type":    defaultErrType,
					"message": defaultMsg,
				},
			})
			if !clientDisconnected {
				if !clientOutputStarted {
					options.WriteError(defaultStatus, defaultErrType, defaultMsg)
					clientOutputStarted = true
				} else if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", errorPayload); err != nil {
					clientDisconnected = true
					logger.L().Info("openai chat_completions stream: client disconnected while writing upstream error",
						zap.String("request_id", requestID),
					)
				}
			}
			if !clientDisconnected {
				c.Writer.Flush()
			}
			streamNonFailoverErr = fmt.Errorf("upstream response failed: %s", defaultMsg)
			return true
		}

		chunks := bridge.ResponsesEventToChatChunks(&event, state)
		for _, chunk := range chunks {
			refusalDetector.ObserveChatChunk(chunk)
		}
		if !clientDisconnected {
			for _, chunk := range chunks {
				sse, err := bridge.ChatChunkToSSE(chunk)
				if err != nil {
					logger.L().Warn("openai chat_completions stream: failed to marshal chunk",
						zap.Error(err),
						zap.String("request_id", requestID),
					)
					continue
				}
				if !clientOutputStarted && !refusalDetector.ShouldReleaseClientOutput() {
					pendingSSE = append(pendingSSE, sse)
					continue
				}
				if !clientOutputStarted {
					writeStreamHeaders()
					for _, pending := range pendingSSE {
						if _, err := fmt.Fprint(c.Writer, pending); err != nil {
							clientDisconnected = true
							logger.L().Info("openai chat_completions stream: client disconnected while flushing pending chunks",
								zap.String("request_id", requestID),
							)
							break
						}
					}
					pendingSSE = pendingSSE[:0]
					clientOutputStarted = !clientDisconnected
					if clientDisconnected {
						break
					}
				}
				if _, err := fmt.Fprint(c.Writer, sse); err != nil {
					clientDisconnected = true
					logger.L().Info("openai chat_completions stream: client disconnected, continuing to drain upstream for billing",
						zap.String("request_id", requestID),
					)
					break
				}
			}
		}
		if len(chunks) > 0 && !clientDisconnected && clientOutputStarted {
			c.Writer.Flush()
		}
		return isTerminalEvent
	}

	finalizeStream := func() (*CompatResponseResult, error) {
		if cyberPolicyErr != nil {
			return resultWithUsage(), cyberPolicyErr
		}
		if streamFailoverErr != nil {
			if c == nil || c.Writer == nil || !c.Writer.Written() {
				return nil, streamFailoverErr
			}
			return resultWithUsage(), streamFailoverErr
		}
		if streamNonFailoverErr != nil {
			return resultWithUsage(), streamNonFailoverErr
		}
		finalChunks := bridge.FinalizeResponsesChatStream(state)
		for _, chunk := range finalChunks {
			refusalDetector.ObserveChatChunk(chunk)
		}
		if len(finalChunks) > 0 && !clientDisconnected {
			for _, chunk := range finalChunks {
				sse, err := bridge.ChatChunkToSSE(chunk)
				if err != nil {
					continue
				}
				if !clientOutputStarted && !refusalDetector.ShouldReleaseClientOutput() {
					pendingSSE = append(pendingSSE, sse)
					continue
				}
				if !clientOutputStarted {
					writeStreamHeaders()
					for _, pending := range pendingSSE {
						if _, err := fmt.Fprint(c.Writer, pending); err != nil {
							clientDisconnected = true
							logger.L().Info("openai chat_completions stream: client disconnected during pending final flush",
								zap.String("request_id", requestID),
							)
							break
						}
					}
					pendingSSE = pendingSSE[:0]
					clientOutputStarted = !clientDisconnected
					if clientDisconnected {
						break
					}
				}
				if _, err := fmt.Fprint(c.Writer, sse); err != nil {
					clientDisconnected = true
					logger.L().Info("openai chat_completions stream: client disconnected during final flush",
						zap.String("request_id", requestID),
					)
					break
				}
			}
		}
		if !clientDisconnected && !clientOutputStarted {
			if refusalDetector.IsSilentRefusal() {
				return nil, options.SilentRefusal()
			}
			if len(pendingSSE) > 0 {
				writeStreamHeaders()
				for _, pending := range pendingSSE {
					if _, err := fmt.Fprint(c.Writer, pending); err != nil {
						clientDisconnected = true
						logger.L().Info("openai chat_completions stream: client disconnected during final pending flush",
							zap.String("request_id", requestID),
						)
						break
					}
				}
				pendingSSE = pendingSSE[:0]
				clientOutputStarted = !clientDisconnected
			}
		}
		// Send [DONE] sentinel
		if !clientDisconnected {
			writeStreamHeaders()
			if _, err := fmt.Fprint(c.Writer, "data: [DONE]\n\n"); err != nil {
				clientDisconnected = true
				logger.L().Info("openai chat_completions stream: client disconnected during done flush",
					zap.String("request_id", requestID),
				)
			}
			clientOutputStarted = !clientDisconnected
		}
		if !clientDisconnected {
			c.Writer.Flush()
		}
		return resultWithUsage(), nil
	}

	handleScanErr := func(err error) {
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			logger.FromContext(options.RequestContext).Warn("openai chat_completions stream: read error",
				zap.Error(err),
				zap.String("upstream_request_id", requestID),
			)
		}
	}
	missingTerminalErr := func() (*CompatResponseResult, error) {
		return resultWithUsage(), fmt.Errorf("stream usage incomplete: missing terminal event")
	}
	processFrame := func(frame wire.OpenAICompatSSEFrame) bool {
		payload := wire.OpenAICompatPayloadWithEventType(frame.Data, frame.EventType)
		if strings.TrimSpace(payload) == "[DONE]" {
			return false
		}
		return processDataLine(payload)
	}

	// Determine keepalive interval
	keepaliveInterval := options.KeepaliveInterval()

	// No keepalive: fast synchronous path
	if streamInterval <= 0 && keepaliveInterval <= 0 {
		var parser wire.OpenAICompatSSEFrameParser
		for scanner.Scan() {
			line := scanner.Text()
			frame, ok := parser.AddLine(line)
			if !ok {
				continue
			}
			if strings.TrimSpace(frame.Data) == "[DONE]" {
				return missingTerminalErr()
			}
			if processFrame(frame) {
				return finalizeStream()
			}
		}
		if err := scanner.Err(); err != nil {
			handleScanErr(err)
			if clientDisconnected || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return resultWithUsage(), fmt.Errorf("stream usage incomplete: %w", err)
			}
			return resultWithUsage(), options.ReadFailure(err)
		}
		if frame, ok := parser.Finish(); ok {
			if strings.TrimSpace(frame.Data) == "[DONE]" {
				return missingTerminalErr()
			}
			if processFrame(frame) {
				return finalizeStream()
			}
		}
		return missingTerminalErr()
	}

	// Read upstream asynchronously so keepalive and timeout handling can run while reads block.
	type scanEvent struct {
		line string
		err  error
	}
	events := make(chan scanEvent, 16)
	done := make(chan struct{})
	var lastReadAt int64
	atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
	sendEvent := func(ev scanEvent) bool {
		select {
		case events <- ev:
			return true
		case <-done:
			return false
		}
	}
	go func() {
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
	}()
	defer close(done)

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
	var parser wire.OpenAICompatSSEFrameParser

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				if frame, ok := parser.Finish(); ok {
					if strings.TrimSpace(frame.Data) == "[DONE]" {
						return missingTerminalErr()
					}
					if processFrame(frame) {
						return finalizeStream()
					}
				}
				return missingTerminalErr()
			}
			if ev.err != nil {
				handleScanErr(ev.err)
				if clientDisconnected || errors.Is(ev.err, context.Canceled) || errors.Is(ev.err, context.DeadlineExceeded) {
					return resultWithUsage(), fmt.Errorf("stream usage incomplete: %w", ev.err)
				}
				return resultWithUsage(), options.ReadFailure(ev.err)
			}
			lastDataAt = time.Now()
			line := ev.line
			frame, ok := parser.AddLine(line)
			if !ok {
				continue
			}
			if strings.TrimSpace(frame.Data) == "[DONE]" {
				return missingTerminalErr()
			}
			if processFrame(frame) {
				return finalizeStream()
			}

		case <-intervalCh:
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if time.Since(lastRead) < streamInterval {
				continue
			}
			if clientDisconnected {
				return resultWithUsage(), fmt.Errorf("stream usage incomplete after timeout")
			}
			logger.L().Warn("openai chat_completions stream: data interval timeout",
				zap.String("request_id", requestID),
				zap.String("model", originalModel),
				zap.Duration("interval", streamInterval),
			)
			return resultWithUsage(), fmt.Errorf("stream data interval timeout")

		case <-keepaliveCh:
			if clientDisconnected {
				continue
			}
			if refusalDetector.Enabled() && !clientOutputStarted {
				continue
			}
			if time.Since(lastDataAt) < keepaliveInterval {
				continue
			}
			// Send SSE comment as keepalive
			writeStreamHeaders()
			if _, err := fmt.Fprint(c.Writer, ":\n\n"); err != nil {
				logger.L().Info("openai chat_completions stream: client disconnected during keepalive",
					zap.String("request_id", requestID),
				)
				clientDisconnected = true
				continue
			}
			c.Writer.Flush()
		}
	}
}
