package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// MessagesResponseOptions adds Messages failure and usage handling to shared response options.
type MessagesResponseOptions struct {
	ChatResponseOptions
	MessagesFailure       func([]byte, string, bool, bool) ChatFailure
	MissingUsage          func(*wire.ForwardUsage, string, bool)
	MissingTerminal       func(string) error
	RecordMissingTerminal func(string)
}

// ReadMessagesBuffered converts a Responses terminal result to Messages JSON and observes usage.
func ReadMessagesBuffered(resp *http.Response, c *upstream.OutputContext, options MessagesResponseOptions, originalModel, upstreamModel string, startTime time.Time) (*CompatResponseResult, error) {
	requestID := resp.Header.Get("x-request-id")

	finalResponse, usage, acc, err := options.ReadBuffered()
	if err != nil {
		var readErr *CompatBufferedReadError
		if errors.As(err, &readErr) && readErr != nil {
			return nil, readErr.Unwrap()
		}
		return nil, err
	}

	if finalResponse == nil {
		options.WriteError(http.StatusBadGateway, "api_error", "Upstream stream ended without a terminal response event")
		return nil, fmt.Errorf("upstream stream ended without terminal event")
	}
	options.ObserveFinal(finalResponse)
	if strings.TrimSpace(finalResponse.Status) == "failed" {
		return nil, options.BufferedFailure(finalResponse, usage)
	}

	if strings.TrimSpace(finalResponse.Status) == "completed" {
		options.MissingUsage(&usage, "response.completed", false)
	}

	// When the terminal event has an empty output array, reconstruct from
	// accumulated delta events so the client receives the full content.
	acc.SupplementResponseOutput(finalResponse)

	anthropicResp := bridge.ResponsesToAnthropic(finalResponse, originalModel)

	options.Headers(c.Writer.Header(), resp.Header)
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.JSON(http.StatusOK, anthropicResp)

	result := &CompatResponseResult{
		RequestID:       requestID,
		UpstreamHeaders: resp.Header,
		ResponseID:      finalResponse.ID,
		Usage:           usage,
		Model:           originalModel,
		UpstreamModel:   upstreamModel,
		ServiceTier:     options.ServiceTier(),
		Stream:          false,
		Duration:        time.Since(startTime),
	}
	// Grok /v1/messages uses Responses upstream; count native search for surcharge.
	if options.CountSearch && finalResponse != nil {
		if body, err := json.Marshal(finalResponse); err == nil {
			if n := options.JSONSearch(body); n > 0 {
				result.SearchCount = n
			}
		}
	}
	return result, nil
}

// ReadMessagesStreaming converts Responses events to Messages SSE and returns observed usage on failures.
func ReadMessagesStreaming(resp *http.Response, c *upstream.OutputContext, options MessagesResponseOptions, originalModel, upstreamModel string, startTime time.Time) (*CompatResponseResult, error) {
	requestID := resp.Header.Get("x-request-id")
	writeStreamHeaders := NewCompatStreamHeaderWriter(c, resp.Header, options.Headers)

	state := bridge.NewResponsesEventToAnthropicState(options.Runtime)
	state.Model = originalModel
	var usage wire.ForwardUsage
	responseID := ""
	var firstTokenMs *int
	firstChunk := true
	clientDisconnected := false
	clientOutputStarted := false
	var cyberPolicyErr error
	var streamFailoverErr error
	var streamNonFailoverErr error
	terminalEventType := ""
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

	// resultWithUsage builds the final result snapshot.
	resultWithUsage := func() *CompatResponseResult {
		out := &CompatResponseResult{
			RequestID:        requestID,
			UpstreamHeaders:  resp.Header,
			ResponseID:       responseID,
			Usage:            usage,
			Model:            originalModel,
			UpstreamModel:    upstreamModel,
			ServiceTier:      options.ServiceTier(),
			Stream:           true,
			Duration:         time.Since(startTime),
			FirstTokenMs:     firstTokenMs,
			ClientDisconnect: clientDisconnected,
		}
		if searchCount > 0 {
			out.SearchCount = searchCount
		}
		return out
	}

	// processDataLine handles a single "data: ..." SSE line from upstream.
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
			logger.L().Warn("openai messages stream: failed to parse event",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
			return false
		}
		options.Observe([]byte(payload), event.Type)
		wire.ParseSSEUsageBytesWithType([]byte(payload), event.Type, &usage)

		eventType := strings.TrimSpace(event.Type)
		isBareErrorEvent := eventType == "error"
		isTerminalEvent := IsCompatResponsesTerminalEvent(eventType) || isBareErrorEvent
		if isTerminalEvent {
			terminalEventType = eventType
			if event.Response != nil {
				if id := strings.TrimSpace(event.Response.ID); id != "" {
					responseID = id
				}
				if event.Response.Usage != nil {
					usage = wire.CopyForwardUsage(event.Response.Usage)
				}
			}
			if event.Usage != nil {
				usage = wire.CopyForwardUsage(event.Usage)
			}
		}
		if eventType == "response.failed" || isBareErrorEvent {
			payloadBytes := []byte(payload)
			// Record cyber_policy as non-retryable and send an Anthropic error event.
			// Stop conversion after this rejection so no later content follows the error.
			if hit, code, msg := DetectOpenAICyberPolicy(payloadBytes); hit {
				options.MarkCyber(CyberObservation{
					Code:           code,
					Message:        msg,
					Body:           options.Truncate(payload, 4096),
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
					if _, err := fmt.Fprint(c.Writer, options.BuildStreamError("invalid_request_error", clientMsg)); err == nil {
						c.Writer.Flush()
					}
					clientDisconnected = true
				}
				cyberPolicyErr = options.CyberForwarded
				return true
			}
			message := ExtractOpenAISSEErrorMessage(payloadBytes)
			failure := options.MessagesFailure(payloadBytes, message, clientOutputStarted, isBareErrorEvent)
			if failure.Failover != nil {
				streamFailoverErr = failure.Failover
				return true
			}
			errStatus, errType, errMsg := failure.Status, failure.Type, failure.Message

			if !clientDisconnected {
				if !clientOutputStarted {
					options.WriteError(errStatus, errType, errMsg)
					clientOutputStarted = true
				} else {
					writeStreamHeaders()
					if _, err := fmt.Fprint(c.Writer, options.BuildStreamError(errType, errMsg)); err == nil {
						c.Writer.Flush()
					}
				}
			}
			streamNonFailoverErr = fmt.Errorf("upstream response failed: %s", errMsg)
			return true
		}

		// Convert to Anthropic events
		events := bridge.ResponsesEventToAnthropicEvents(&event, state)
		if !clientDisconnected {
			for _, evt := range events {
				sse, err := bridge.ResponsesAnthropicEventToSSE(evt)
				if err != nil {
					logger.L().Warn("openai messages stream: failed to marshal event",
						zap.Error(err),
						zap.String("request_id", requestID),
					)
					continue
				}
				writeStreamHeaders()
				if _, err := fmt.Fprint(c.Writer, sse); err != nil {
					clientDisconnected = true
					logger.L().Info("openai messages stream: client disconnected, continuing to drain upstream for billing",
						zap.String("request_id", requestID),
					)
					break
				}
				clientOutputStarted = true
			}
		}
		if len(events) > 0 && !clientDisconnected {
			c.Writer.Flush()
		}
		return isTerminalEvent
	}

	// finalizeStream sends any remaining Anthropic events and returns the result.
	finalizeStream := func() (*CompatResponseResult, error) {
		if cyberPolicyErr != nil {
			return resultWithUsage(), cyberPolicyErr
		}
		if streamFailoverErr != nil {
			return resultWithUsage(), streamFailoverErr
		}
		if streamNonFailoverErr != nil {
			return resultWithUsage(), streamNonFailoverErr
		}
		finalEvents := bridge.FinalizeResponsesAnthropicStream(state)
		if len(finalEvents) > 0 && !clientDisconnected {
			for _, evt := range finalEvents {
				sse, err := bridge.ResponsesAnthropicEventToSSE(evt)
				if err != nil {
					continue
				}
				writeStreamHeaders()
				if _, err := fmt.Fprint(c.Writer, sse); err != nil {
					clientDisconnected = true
					logger.L().Info("openai messages stream: client disconnected during final flush",
						zap.String("request_id", requestID),
					)
					break
				}
				clientOutputStarted = true
			}
			if !clientDisconnected {
				c.Writer.Flush()
			}
		}
		options.MissingUsage(&usage, terminalEventType, clientDisconnected)
		return resultWithUsage(), nil
	}

	// handleScanErr logs read errors other than cancellation and deadline expiration.
	handleScanErr := func(err error) {
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			logger.L().Warn("openai messages stream: read error",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
		}
	}
	missingTerminalErr := func() (*CompatResponseResult, error) {
		result := resultWithUsage()
		if clientDisconnected {
			return result, fmt.Errorf("stream usage incomplete: missing terminal event")
		}
		message := "OpenAI messages stream ended before a terminal event"
		if !clientOutputStarted {
			return result, options.MissingTerminal(message)
		}
		options.RecordMissingTerminal(message)
		return result, fmt.Errorf("stream usage incomplete: missing terminal event")
	}
	processFrame := func(frame wire.OpenAICompatSSEFrame) bool {
		payload := wire.OpenAICompatPayloadWithEventType(frame.Data, frame.EventType)
		return processDataLine(payload)
	}

	// Resolve the downstream keepalive interval.
	keepaliveInterval := options.KeepaliveInterval()

	// Scan synchronously when neither timeout nor keepalive work is needed.
	if streamInterval <= 0 && keepaliveInterval <= 0 {
		var parser wire.OpenAICompatSSEFrameParser
		for scanner.Scan() {
			line := scanner.Text()
			if IsCompatDoneSentinelLine(line) {
				return missingTerminalErr()
			}
			frame, ok := parser.AddLine(line)
			if !ok {
				continue
			}
			if processFrame(frame) {
				return finalizeStream()
			}
		}
		if err := scanner.Err(); err != nil {
			handleScanErr(err)
			return resultWithUsage(), fmt.Errorf("stream usage incomplete: %w", err)
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

	// Read asynchronously so timers continue while upstream reads block.
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
				// Upstream closed
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
				return resultWithUsage(), fmt.Errorf("stream usage incomplete: %w", ev.err)
			}
			lastDataAt = time.Now()
			line := ev.line
			if IsCompatDoneSentinelLine(line) {
				return missingTerminalErr()
			}
			frame, ok := parser.AddLine(line)
			if !ok {
				continue
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
			logger.L().Warn("openai messages stream: data interval timeout",
				zap.String("request_id", requestID),
				zap.String("model", originalModel),
				zap.Duration("interval", streamInterval),
			)
			return resultWithUsage(), fmt.Errorf("stream data interval timeout")

		case <-keepaliveCh:
			if clientDisconnected {
				continue
			}
			if time.Since(lastDataAt) < keepaliveInterval {
				continue
			}
			// Send Anthropic-format ping event
			writeStreamHeaders()
			if _, err := fmt.Fprint(c.Writer, "event: ping\ndata: {\"type\":\"ping\"}\n\n"); err != nil {
				// Client disconnected
				logger.L().Info("openai messages stream: client disconnected during keepalive",
					zap.String("request_id", requestID),
				)
				clientDisconnected = true
				continue
			}
			clientOutputStarted = true
			c.Writer.Flush()
		}
	}
}
