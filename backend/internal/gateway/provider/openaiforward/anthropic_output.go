package openaiforward

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

type AnthropicOutputOptions struct {
	Runtime        bridge.Runtime
	MaxLineSize    func() int
	StreamInterval func() time.Duration
	CopyHeaders    func(http.Header, http.Header)
	ReverseTools   func([]byte) []byte
	Error          func(int, string, string)
	Warn           func(string, ...zap.Field)
}

// AnthropicUsageToOpenAI includes cache creation and cache reads in the OpenAI input total.
func AnthropicUsageToOpenAI(u *upstream.TokenUsage) protocolopenai.ForwardUsage {
	if u == nil {
		return protocolopenai.ForwardUsage{}
	}
	return protocolopenai.ForwardUsage{InputTokens: u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens, OutputTokens: u.OutputTokens, CacheCreationInputTokens: u.CacheCreationInputTokens, CacheReadInputTokens: u.CacheReadInputTokens}
}

func ResponsesFromAnthropicBuffered(resp *http.Response, c *upstream.OutputContext, o AnthropicOutputOptions, originalModel, billingModel, upstreamModel string, reasoningEffort *string, startTime time.Time, clientToolMapping bridge.ResponsesClientToolMapping) (out *Result, failure error) {
	var modelObserver protocol.ResponseModelObserver
	defer func() {
		if out != nil {
			out.UpstreamResponseModel = modelObserver.Model()
		}
	}()
	requestID := resp.Header.Get("x-request-id")

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := o.MaxLineSize()
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	var finalResp *protocolanthropic.AnthropicResponse
	var usage upstream.TokenUsage

	// Bound idle reads so a stalled upstream can return 502 before buffered output is committed.
	streamInterval := o.StreamInterval()
	pump := NewAnthropicLinePump(scanner, streamInterval)
	defer pump.Stop()

	logReadErr := func(err error) {
		if !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			o.Warn("openai responses via native anthropic buffered: read error",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
		}
	}
	onIdle := func() (*Result, error) {
		_ = resp.Body.Close()
		o.Warn("openai responses via native anthropic buffered: data interval timeout",
			zap.String("request_id", requestID),
			zap.Duration("interval", streamInterval),
		)
		o.Error(http.StatusBadGateway, "server_error", "Upstream stream data interval timeout")
		return nil, fmt.Errorf("stream data interval timeout")
	}

	for {
		line, rerr := pump.Next()
		if rerr != nil {
			if errors.Is(rerr, ErrAnthropicStreamIdle) {
				return onIdle()
			}
			logReadErr(rerr)
			break
		}
		// SSE permits event:xxx without a space, as emitted by compatible upstreams such as Kimi.
		if _, ok := protocolopenai.ExtractSSEEventLine(line); !ok {
			continue
		}

		dataLine, rerr := pump.Next()
		if rerr != nil {
			if errors.Is(rerr, ErrAnthropicStreamIdle) {
				return onIdle()
			}
			logReadErr(rerr)
			break
		}
		payload, ok := protocolopenai.ExtractSSEDataLine(dataLine)
		if !ok {
			continue
		}

		modelObserver.ObserveAnthropic([]byte(payload))
		var event protocolanthropic.AnthropicStreamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}

		if event.Type == "message_start" && event.Message != nil {
			finalResp = event.Message
			protocolanthropic.MergeAnthropicUsage(&usage, event.Message.Usage)
		}
		if event.Type == "message_delta" {
			if event.Usage != nil {
				protocolanthropic.MergeAnthropicUsage(&usage, *event.Usage)
			}
			if event.Delta != nil && event.Delta.StopReason != "" && finalResp != nil {
				finalResp.StopReason = bridge.AnthropicStopReasonPtr(event.Delta.StopReason)
			}
		}
		if event.Type == "content_block_start" && event.ContentBlock != nil && finalResp != nil {
			finalResp.Content = append(finalResp.Content, *event.ContentBlock)
		}
		if event.Type == "content_block_delta" && event.Delta != nil && finalResp != nil && event.Index != nil {
			idx := *event.Index
			if idx < len(finalResp.Content) {
				switch event.Delta.Type {
				case "text_delta":
					finalResp.Content[idx].Text += event.Delta.Text
				case "thinking_delta":
					finalResp.Content[idx].Thinking += event.Delta.Thinking
				case "input_json_delta":
					finalResp.Content[idx].Input = forwardcore.AppendRawJSON(finalResp.Content[idx].Input, event.Delta.PartialJSON)
				}
			}
		}
	}

	if finalResp == nil {
		o.Error(http.StatusBadGateway, "server_error", "Upstream stream ended without a response")
		return nil, fmt.Errorf("upstream stream ended without response")
	}

	if usage.InputTokens > 0 || usage.OutputTokens > 0 {
		finalResp.Usage = protocolanthropic.AnthropicUsage{
			InputTokens:              usage.InputTokens,
			OutputTokens:             usage.OutputTokens,
			CacheCreationInputTokens: usage.CacheCreationInputTokens,
			CacheReadInputTokens:     usage.CacheReadInputTokens,
		}
	}

	responsesResp := bridge.AnthropicToResponsesResponse(o.Runtime, finalResp)
	responsesResp.Model = originalModel

	o.CopyHeaders(c.Writer.Header(), resp.Header)
	// The client receives JSON even though the upstream Content-Type describes an SSE stream.
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	if respBytes, err := json.Marshal(responsesResp); err == nil {
		respBytes = o.ReverseTools(respBytes)
		respBytes, _, err = bridge.RestoreResponsesClientToolPayload(respBytes, clientToolMapping)
		if err != nil {
			return nil, fmt.Errorf("restore responses client tools: %w", err)
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", respBytes)
	} else {
		c.JSON(http.StatusOK, responsesResp)
	}

	return &Result{
		RequestID:        requestID,
		Headers:          resp.Header,
		Usage:            AnthropicUsageToOpenAI(&usage),
		Model:            originalModel,
		BillingModel:     billingModel,
		UpstreamModel:    upstreamModel,
		UpstreamEndpoint: "/v1/messages",
		ReasoningEffort:  reasoningEffort,
		Stream:           false,
		Duration:         time.Since(startTime),
	}, nil
}

func ResponsesFromAnthropicStreaming(resp *http.Response, c *upstream.OutputContext, o AnthropicOutputOptions, originalModel, billingModel, upstreamModel string, reasoningEffort *string, startTime time.Time, clientToolMapping bridge.ResponsesClientToolMapping) (out *Result, failure error) {
	var modelObserver protocol.ResponseModelObserver
	defer func() {
		if out != nil {
			out.UpstreamResponseModel = modelObserver.Model()
		}
	}()
	requestID := resp.Header.Get("x-request-id")

	o.CopyHeaders(c.Writer.Header(), resp.Header)
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)

	state := bridge.NewAnthropicEventToResponsesState(o.Runtime)
	state.Model = originalModel
	clientToolRestorer := bridge.NewResponsesClientToolStreamRestorer(clientToolMapping)

	var usage upstream.TokenUsage
	var firstTokenMs *int
	firstChunk := true
	clientDisconnected := false

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := o.MaxLineSize()
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	resultWithUsage := func() *Result {
		return &Result{
			RequestID:        requestID,
			Headers:          resp.Header,
			Usage:            AnthropicUsageToOpenAI(&usage),
			Model:            originalModel,
			BillingModel:     billingModel,
			UpstreamModel:    upstreamModel,
			UpstreamEndpoint: "/v1/messages",
			ReasoningEffort:  reasoningEffort,
			Stream:           true,
			Duration:         time.Since(startTime),
			FirstTokenMs:     firstTokenMs,
			ClientDisconnect: clientDisconnected,
		}
	}

	// Bound idle reads so draining a detached upstream request cannot wait indefinitely
	// when the upstream keeps its connection open without sending more lines.
	streamInterval := o.StreamInterval()
	pump := NewAnthropicLinePump(scanner, streamInterval)
	defer pump.Stop()

	logReadErr := func(err error) {
		if !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			o.Warn("openai responses via native anthropic stream: read error",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
		}
	}
	onIdle := func() (*Result, error) {
		_ = resp.Body.Close()
		o.Warn("openai responses via native anthropic stream: data interval timeout",
			zap.String("request_id", requestID),
			zap.Duration("interval", streamInterval),
		)
		return resultWithUsage(), fmt.Errorf("stream data interval timeout")
	}

	// After client disconnection, keep advancing conversion state and draining usage.
	// The final output token count arrives in a trailing message_delta event.
	processAnthropicEvent := func(event *protocolanthropic.AnthropicStreamEvent) {
		if firstChunk {
			firstChunk = false
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}

		if event.Type == "message_delta" && event.Usage != nil {
			protocolanthropic.MergeAnthropicUsage(&usage, *event.Usage)
		}
		if event.Type == "message_start" && event.Message != nil {
			protocolanthropic.MergeAnthropicUsage(&usage, event.Message.Usage)
		}

		events := bridge.AnthropicEventToResponsesEvents(o.Runtime, event, state)
		if clientDisconnected {
			return
		}
		for _, evt := range events {
			payload, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			payload = o.ReverseTools(payload)
			payloads, _, err := clientToolRestorer.RestoreEvent(payload)
			if err != nil {
				continue
			}
			for _, restored := range payloads {
				eventType := gjson.GetBytes(restored, "type").String()
				if _, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", eventType, restored); err != nil {
					clientDisconnected = true
					return
				}
			}
		}
		if len(events) > 0 {
			c.Writer.Flush()
		}
	}

	for {
		line, rerr := pump.Next()
		if rerr != nil {
			if errors.Is(rerr, ErrAnthropicStreamIdle) {
				return onIdle()
			}
			logReadErr(rerr)
			break
		}
		if _, ok := protocolopenai.ExtractSSEEventLine(line); !ok {
			continue
		}

		dataLine, rerr := pump.Next()
		if rerr != nil {
			if errors.Is(rerr, ErrAnthropicStreamIdle) {
				return onIdle()
			}
			logReadErr(rerr)
			break
		}
		payload, ok := protocolopenai.ExtractSSEDataLine(dataLine)
		if !ok {
			continue
		}

		modelObserver.ObserveAnthropic([]byte(payload))
		var event protocolanthropic.AnthropicStreamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}

		processAnthropicEvent(&event)
	}

	// Finalize after disconnection too; write restored terminal events only while the client remains connected.
	if finalEvents := bridge.FinalizeAnthropicResponsesStream(state); len(finalEvents) > 0 && !clientDisconnected {
		wrote := false
		for _, evt := range finalEvents {
			payload, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			payload = o.ReverseTools(payload)
			payloads, _, err := clientToolRestorer.RestoreEvent(payload)
			if err != nil {
				continue
			}
			for _, restored := range payloads {
				eventType := gjson.GetBytes(restored, "type").String()
				if _, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", eventType, restored); err != nil {
					clientDisconnected = true
					break
				}
				wrote = true
			}
			if clientDisconnected {
				break
			}
		}
		if wrote {
			c.Writer.Flush()
		}
	}

	return resultWithUsage(), nil
}

func ChatFromAnthropicBuffered(resp *http.Response, c *upstream.OutputContext, o AnthropicOutputOptions, originalModel, billingModel, upstreamModel string, reasoningEffort *string, startTime time.Time) (out *Result, failure error) {
	var modelObserver protocol.ResponseModelObserver
	defer func() {
		if out != nil {
			out.UpstreamResponseModel = modelObserver.Model()
		}
	}()
	requestID := resp.Header.Get("x-request-id")

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := o.MaxLineSize()
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	var finalResp *protocolanthropic.AnthropicResponse
	var usage upstream.TokenUsage

	// Bound idle reads so a stalled upstream can return 502 before buffered output is committed.
	streamInterval := o.StreamInterval()
	pump := NewAnthropicLinePump(scanner, streamInterval)
	defer pump.Stop()

	logReadErr := func(err error) {
		if !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			o.Warn("openai cc via native anthropic buffered: read error",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
		}
	}
	onIdle := func() (*Result, error) {
		_ = resp.Body.Close()
		o.Warn("openai cc via native anthropic buffered: data interval timeout",
			zap.String("request_id", requestID),
			zap.Duration("interval", streamInterval),
		)
		o.Error(http.StatusBadGateway, "server_error", "Upstream stream data interval timeout")
		return nil, fmt.Errorf("stream data interval timeout")
	}

	for {
		line, rerr := pump.Next()
		if rerr != nil {
			if errors.Is(rerr, ErrAnthropicStreamIdle) {
				return onIdle()
			}
			logReadErr(rerr)
			break
		}
		// SSE permits event:xxx without a space. Requiring "event: " would drop compact
		// events from compatible upstreams such as Kimi (issue #4653).
		if _, ok := protocolopenai.ExtractSSEEventLine(line); !ok {
			continue
		}

		dataLine, rerr := pump.Next()
		if rerr != nil {
			if errors.Is(rerr, ErrAnthropicStreamIdle) {
				return onIdle()
			}
			logReadErr(rerr)
			break
		}
		payload, ok := protocolopenai.ExtractSSEDataLine(dataLine)
		if !ok {
			continue
		}

		modelObserver.ObserveAnthropic([]byte(payload))
		var event protocolanthropic.AnthropicStreamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}

		if event.Type == "message_start" && event.Message != nil {
			finalResp = event.Message
			protocolanthropic.MergeAnthropicUsage(&usage, event.Message.Usage)
		}
		if event.Type == "message_delta" {
			if event.Usage != nil {
				protocolanthropic.MergeAnthropicUsage(&usage, *event.Usage)
			}
			if event.Delta != nil && event.Delta.StopReason != "" && finalResp != nil {
				finalResp.StopReason = bridge.AnthropicStopReasonPtr(event.Delta.StopReason)
			}
		}
		if event.Type == "content_block_start" && event.ContentBlock != nil && finalResp != nil {
			finalResp.Content = append(finalResp.Content, *event.ContentBlock)
		}
		if event.Type == "content_block_delta" && event.Delta != nil && finalResp != nil && event.Index != nil {
			idx := *event.Index
			if idx < len(finalResp.Content) {
				switch event.Delta.Type {
				case "text_delta":
					finalResp.Content[idx].Text += event.Delta.Text
				case "thinking_delta":
					finalResp.Content[idx].Thinking += event.Delta.Thinking
				case "input_json_delta":
					finalResp.Content[idx].Input = forwardcore.AppendRawJSON(finalResp.Content[idx].Input, event.Delta.PartialJSON)
				}
			}
		}
	}

	if finalResp == nil {
		o.Error(http.StatusBadGateway, "server_error", "Upstream stream ended without a response")
		return nil, fmt.Errorf("upstream stream ended without response")
	}

	if usage.InputTokens > 0 || usage.OutputTokens > 0 {
		finalResp.Usage = protocolanthropic.AnthropicUsage{
			InputTokens:              usage.InputTokens,
			OutputTokens:             usage.OutputTokens,
			CacheCreationInputTokens: usage.CacheCreationInputTokens,
			CacheReadInputTokens:     usage.CacheReadInputTokens,
		}
	}

	responsesResp := bridge.AnthropicToResponsesResponse(o.Runtime, finalResp)
	ccResp := bridge.ResponsesToChatCompletions(o.Runtime, responsesResp, originalModel)

	o.CopyHeaders(c.Writer.Header(), resp.Header)
	// The client receives JSON even though the upstream Content-Type describes an SSE stream.
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	if respBytes, err := json.Marshal(ccResp); err == nil {
		respBytes = o.ReverseTools(respBytes)
		c.Data(http.StatusOK, "application/json; charset=utf-8", respBytes)
	} else {
		c.JSON(http.StatusOK, ccResp)
	}

	return &Result{
		RequestID:        requestID,
		Headers:          resp.Header,
		Usage:            AnthropicUsageToOpenAI(&usage),
		Model:            originalModel,
		BillingModel:     billingModel,
		UpstreamModel:    upstreamModel,
		UpstreamEndpoint: "/v1/messages",
		ReasoningEffort:  reasoningEffort,
		Stream:           false,
		Duration:         time.Since(startTime),
	}, nil
}

func ChatFromAnthropicStreaming(resp *http.Response, c *upstream.OutputContext, o AnthropicOutputOptions, originalModel, billingModel, upstreamModel string, reasoningEffort *string, startTime time.Time, includeUsage bool) (out *Result, failure error) {
	var modelObserver protocol.ResponseModelObserver
	defer func() {
		if out != nil {
			out.UpstreamResponseModel = modelObserver.Model()
		}
	}()
	requestID := resp.Header.Get("x-request-id")

	o.CopyHeaders(c.Writer.Header(), resp.Header)
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)

	anthState := bridge.NewAnthropicEventToResponsesState(o.Runtime)
	anthState.Model = originalModel
	ccState := bridge.NewResponsesEventToChatState(o.Runtime)
	ccState.Model = originalModel
	ccState.IncludeUsage = includeUsage

	var usage upstream.TokenUsage
	var firstTokenMs *int
	firstChunk := true
	clientDisconnected := false

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := o.MaxLineSize()
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	resultWithUsage := func() *Result {
		return &Result{
			RequestID:        requestID,
			Headers:          resp.Header,
			Usage:            AnthropicUsageToOpenAI(&usage),
			Model:            originalModel,
			BillingModel:     billingModel,
			UpstreamModel:    upstreamModel,
			UpstreamEndpoint: "/v1/messages",
			ReasoningEffort:  reasoningEffort,
			Stream:           true,
			Duration:         time.Since(startTime),
			FirstTokenMs:     firstTokenMs,
			ClientDisconnect: clientDisconnected,
		}
	}

	// Bound idle reads while draining a detached upstream request after client disconnection.
	// A connection left open without more data would otherwise keep the scanner blocked.
	streamInterval := o.StreamInterval()
	pump := NewAnthropicLinePump(scanner, streamInterval)
	defer pump.Stop()

	logReadErr := func(err error) {
		if !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			o.Warn("openai cc via native anthropic stream: read error",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
		}
	}
	// Close the body to unblock the reader, and return the usage collected before the timeout.
	onIdle := func() (*Result, error) {
		_ = resp.Body.Close()
		if !clientDisconnected {
			o.Warn("openai cc via native anthropic stream: data interval timeout",
				zap.String("request_id", requestID),
				zap.Duration("interval", streamInterval),
			)
		}
		return resultWithUsage(), fmt.Errorf("stream data interval timeout")
	}

	writeChunk := func(chunk protocolopenai.ChatCompletionsChunk) bool {
		if clientDisconnected {
			return false // Continue draining usage after writes stop.
		}
		sse, err := bridge.ChatChunkToSSE(chunk)
		if err != nil {
			return false
		}
		out := string(o.ReverseTools([]byte(sse)))
		if _, err := fmt.Fprint(c.Writer, out); err != nil {
			clientDisconnected = true
			return false
		}
		return false
	}

	processAnthropicEvent := func(event *protocolanthropic.AnthropicStreamEvent) bool {
		if firstChunk {
			firstChunk = false
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}

		// Keep collecting billable upstream usage after the client disconnects.
		if event.Type == "message_delta" && event.Usage != nil {
			protocolanthropic.MergeAnthropicUsage(&usage, *event.Usage)
		}
		if event.Type == "message_start" && event.Message != nil {
			protocolanthropic.MergeAnthropicUsage(&usage, event.Message.Usage)
		}

		// After disconnection, skip conversion and output while the outer loop drains usage.
		if clientDisconnected {
			return false
		}

		responsesEvents := bridge.AnthropicEventToResponsesEvents(o.Runtime, event, anthState)
		for _, resEvt := range responsesEvents {
			ccChunks := bridge.ResponsesEventToChatChunks(&resEvt, ccState)
			for _, chunk := range ccChunks {
				writeChunk(chunk)
			}
		}
		if len(responsesEvents) > 0 {
			c.Writer.Flush()
		}
		return false
	}

	for {
		line, rerr := pump.Next()
		if rerr != nil {
			if errors.Is(rerr, ErrAnthropicStreamIdle) {
				return onIdle()
			}
			logReadErr(rerr)
			break
		}
		if _, ok := protocolopenai.ExtractSSEEventLine(line); !ok {
			continue
		}

		dataLine, rerr := pump.Next()
		if rerr != nil {
			if errors.Is(rerr, ErrAnthropicStreamIdle) {
				return onIdle()
			}
			// Finalize when the stream ends or fails between the event and data lines.
			logReadErr(rerr)
			break
		}
		payload, ok := protocolopenai.ExtractSSEDataLine(dataLine)
		if !ok {
			continue
		}

		modelObserver.ObserveAnthropic([]byte(payload))
		var event protocolanthropic.AnthropicStreamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}

		if processAnthropicEvent(&event) {
			return resultWithUsage(), nil
		}
	}

	// Finalize both converters; writeChunk suppresses output after disconnection.
	finalResEvents := bridge.FinalizeAnthropicResponsesStream(anthState)
	for _, resEvt := range finalResEvents {
		ccChunks := bridge.ResponsesEventToChatChunks(&resEvt, ccState)
		for _, chunk := range ccChunks {
			writeChunk(chunk)
		}
	}
	finalCCChunks := bridge.FinalizeResponsesChatStream(ccState)
	for _, chunk := range finalCCChunks {
		writeChunk(chunk)
	}

	if !clientDisconnected {
		fmt.Fprint(c.Writer, "data: [DONE]\n\n")
		c.Writer.Flush()
	}

	return resultWithUsage(), nil
}
