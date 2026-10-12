package openai

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// RawResponseOptions configures response IO and observation for Chat passthrough and conversion.
type RawResponseOptions struct {
	Runtime              bridge.Runtime
	ProviderID           int64
	Scanner              func(io.Reader) *bufio.Scanner
	CC                   func() CCResponseOptions
	ReadBody             func(io.Reader) ([]byte, error)
	BodyLimitError       error
	Headers              func(http.Header, http.Header)
	WriteError           func(int, string, string)
	Observe              func([]byte, string)
	ObserveSSE           func(string)
	TransformLine        func(string) string
	TransformBody        func([]byte) []byte
	MissingUsage         func(string, wire.ForwardUsage) error
	ServiceTier          func() string
	ResolvedServiceTier  func() *string
	NormalizeServiceTier func(string) string
	TruncatedFailover    func(error) error
	RecordTruncation     func(error)
	SilentRefusal        func() error
	CacheOutput          func([]wire.ResponsesOutput)
	CacheEvents          func([]wire.ResponsesStreamEvent)
}

// ReadRawChatStreaming forwards Chat SSE, collecting usage and checking for truncation before final delivery.
func ReadRawChatStreaming(c *upstream.OutputContext, resp *http.Response, options RawResponseOptions, originalModel, upstreamModel string, reasoningEffort *string, startTime time.Time, requestBodyLen int) (*CompatResponseResult, error) {
	requestID := resp.Header.Get("x-request-id")
	writeStreamHeaders := NewCompatStreamHeaderWriter(c, resp.Header, options.Headers)
	scanner := options.Scanner(resp.Body)

	var usage wire.ForwardUsage
	var firstTokenMs *int
	clientDisconnected := false
	clientOutputStarted := false
	pendingLines := make([]string, 0, 8)
	refusalDetector := NewChatSilentRefusalDetector(requestBodyLen)
	var terminal wire.RawStreamTerminalState

	writeLine := func(line string) {
		if clientDisconnected {
			return
		}
		if !clientOutputStarted && !refusalDetector.ShouldReleaseClientOutput() {
			pendingLines = append(pendingLines, line)
			return
		}
		if !clientOutputStarted {
			writeStreamHeaders()
			for _, pending := range pendingLines {
				if _, werr := io.WriteString(c.Writer, pending+"\n"); werr != nil {
					clientDisconnected = true
					logger.L().Debug("openai chat_completions raw: client disconnected, continuing to drain upstream for billing",
						zap.Error(werr),
						zap.String("request_id", requestID),
					)
					return
				}
			}
			pendingLines = pendingLines[:0]
			clientOutputStarted = true
		}
		if _, werr := io.WriteString(c.Writer, line+"\n"); werr != nil {
			clientDisconnected = true
			logger.L().Debug("openai chat_completions raw: client disconnected, continuing to drain upstream for billing",
				zap.Error(werr),
				zap.String("request_id", requestID),
			)
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		refusalDetector.ObserveSSELine(line)
		if payload, ok := wire.ExtractSSEDataLine(line); ok {
			trimmedPayload := strings.TrimSpace(payload)
			terminal.ObserveDataLine(trimmedPayload)
			if trimmedPayload != "[DONE]" {
				options.Observe([]byte(trimmedPayload), wire.OpenAIChatCompletionServiceTierEventType([]byte(trimmedPayload)))
				usageOnlyChunk := wire.IsOpenAIChatUsageOnlyStreamChunk(payload)
				if u := wire.ExtractCCStreamUsage(payload); u != nil {
					usage = *u
				}
				if firstTokenMs == nil && !usageOnlyChunk {
					elapsed := int(time.Since(startTime).Milliseconds())
					firstTokenMs = &elapsed
				}
			}
		}
		line = options.TransformLine(line)
		line = wire.StripEmptyChatToolCallIdentityFromSSELine(line)

		writeLine(line)
		if line == "" {
			if !clientDisconnected && clientOutputStarted {
				c.Writer.Flush()
			}
			continue
		}
		if !clientDisconnected && clientOutputStarted {
			c.Writer.Flush()
		}
	}

	resultWithUsage := func() *CompatResponseResult {
		return &CompatResponseResult{
			RequestID:       requestID,
			UpstreamHeaders: resp.Header,
			Usage:           usage,
			Model:           originalModel,
			UpstreamModel:   upstreamModel,
			ServiceTier:     options.ServiceTier(),
			ReasoningEffort: reasoningEffort,
			ResolvedTier:    options.ResolvedServiceTier(),
			Stream:          true,
			Duration:        time.Since(startTime),
			FirstTokenMs:    firstTokenMs,
		}
	}

	scanErr := scanner.Err()
	if scanErr != nil && !errors.Is(scanErr, context.Canceled) && !errors.Is(scanErr, context.DeadlineExceeded) {
		logger.L().Warn("openai chat_completions raw: stream read error",
			zap.Error(scanErr),
			zap.String("request_id", requestID),
		)
	}

	// Cancellation can cause the upstream read to fail, so a disconnected client cannot
	// be distinguished from upstream truncation here. Return the usage already observed.
	clientAborted := clientDisconnected ||
		errors.Is(scanErr, context.Canceled) ||
		errors.Is(scanErr, context.DeadlineExceeded)

	// An upstream end without a terminal signal is truncation, whether the read failed
	// or reached clean EOF. Retain observed usage once content has been delivered.
	if !clientAborted && terminal.IsTruncated(clientOutputStarted) {
		cause := scanErr
		if cause == nil {
			cause = ErrOpenAIUpstreamStreamTruncated
		}
		logger.L().Warn("openai chat_completions raw: upstream stream truncated before terminal chunk",
			zap.Error(cause),
			zap.String("request_id", requestID),
			zap.Int64("provider_id", options.ProviderID),
			zap.String("upstream_model", upstreamModel),
			zap.Bool("saw_sse_data", terminal.SawDataLine()),
			zap.Bool("client_output_started", clientOutputStarted),
		)
		if !clientOutputStarted {
			// Before first output, return a failover error so another provider can retry.
			return nil, options.TruncatedFailover(cause)
		}
		// After output, return a typed error so the handler can emit an SSE error
		// and record the request as an SLA failure.
		options.RecordTruncation(cause)
		return resultWithUsage(), NewUpstreamStreamReadError(cause)
	}

	if scanErr == nil && !clientDisconnected && !clientOutputStarted {
		if refusalDetector.IsSilentRefusal() {
			return nil, options.SilentRefusal()
		}
		if len(pendingLines) > 0 {
			writeStreamHeaders()
			for _, pending := range pendingLines {
				if _, werr := io.WriteString(c.Writer, pending+"\n"); werr != nil {
					clientDisconnected = true
					logger.L().Debug("openai chat_completions raw: client disconnected during final flush",
						zap.Error(werr),
						zap.String("request_id", requestID),
					)
					break
				}
			}
			if !clientDisconnected {
				c.Writer.Flush()
				clientOutputStarted = true
			}
		}
	}

	return resultWithUsage(), nil
}

// ReadRawChatBuffered observes usage and tier before transforming and forwarding the body.
func ReadRawChatBuffered(c *upstream.OutputContext, resp *http.Response, options RawResponseOptions, originalModel, upstreamModel string, reasoningEffort *string, startTime time.Time) (*CompatResponseResult, error) {
	requestID := resp.Header.Get("x-request-id")

	respBody, err := options.ReadBody(resp.Body)
	if err != nil {
		if !errors.Is(err, options.BodyLimitError) {
			options.WriteError(http.StatusBadGateway, "api_error", "Failed to read upstream response")
		}
		return nil, fmt.Errorf("read upstream body: %w", err)
	}

	var usage wire.ForwardUsage
	if parsedUsage, ok := wire.ExtractOpenAIUsageFromJSONBytes(respBody); ok {
		usage = parsedUsage
	}
	if IsEventStreamResponse(resp.Header) || wire.BodyHasSSEFraming(respBody) {
		// Some compatible upstreams return SSE for stream=false; inspect each frame
		// to observe the service tier declared by response.completed.
		options.ObserveSSE(string(respBody))
		wire.ForEachOpenAISSEFrame(string(respBody), func(_ string, payload []byte) {
			if parsed, ok := wire.ExtractOpenAIUsageFromJSONBytes(payload); ok {
				usage = parsed
			}
			if parsed := wire.ExtractCCStreamUsage(string(payload)); parsed != nil {
				usage = *parsed
			}
		})
	} else {
		options.Observe(respBody, "response.completed")
	}
	responseModel := gjson.GetBytes(respBody, "model").String()
	if err := options.MissingUsage(responseModel, usage); err != nil {
		return nil, err
	}

	respBody = options.TransformBody(respBody)

	options.Headers(c.Writer.Header(), resp.Header)
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		c.Writer.Header().Set("Content-Type", ct)
	} else {
		c.Writer.Header().Set("Content-Type", "application/json")
	}
	c.Writer.WriteHeader(http.StatusOK)
	_, _ = c.Writer.Write(respBody)

	return &CompatResponseResult{
		RequestID:       requestID,
		UpstreamHeaders: resp.Header,
		Usage:           usage,
		Model:           originalModel,
		UpstreamModel:   upstreamModel,
		ServiceTier:     options.ServiceTier(),
		ReasoningEffort: reasoningEffort,
		ResolvedTier:    options.ResolvedServiceTier(),
		Stream:          false,
		Duration:        time.Since(startTime),
	}, nil
}

// ReadCCAsResponsesBuffered converts Chat JSON to Responses JSON and caches its output.
func ReadCCAsResponsesBuffered(c *upstream.OutputContext, resp *http.Response, options RawResponseOptions, originalModel, upstreamModel string, reasoningEffort *string, startTime time.Time, customTools, functionTools map[string]bool, toolSearch bool, namespaceTools map[string]bridge.NamespacedToolName) (*CompatResponseResult, error) {
	requestID := resp.Header.Get("x-request-id")
	ccResp, usage, err := ReadCCJSONResponse(resp, options.CC())
	if err != nil {
		return nil, err
	}
	responsesResp := bridge.ChatCompletionsResponseToResponses(options.Runtime, ccResp, originalModel, customTools, functionTools, toolSearch, namespaceTools)
	options.CacheOutput(responsesResp.Output)

	options.Headers(c.Writer.Header(), resp.Header)
	c.JSON(http.StatusOK, responsesResp)

	return &CompatResponseResult{
		RequestID:       requestID,
		UpstreamHeaders: resp.Header,
		Usage:           usage,
		Model:           originalModel,
		UpstreamModel:   upstreamModel,
		ServiceTier:     options.ServiceTier(),
		ReasoningEffort: reasoningEffort,
		ResolvedTier:    options.ResolvedServiceTier(),
		Stream:          false,
		Duration:        time.Since(startTime),
	}, nil
}

// ReadCCAsResponsesStreaming converts Chat chunks to Responses events, validating tools before finalization.
func ReadCCAsResponsesStreaming(c *upstream.OutputContext, resp *http.Response, options RawResponseOptions, originalModel, upstreamModel string, reasoningEffort *string, startTime time.Time, customTools, functionTools map[string]bool, toolSearch bool, namespaceTools map[string]bridge.NamespacedToolName) (*CompatResponseResult, error) {
	requestID := resp.Header.Get("x-request-id")
	writeStreamHeaders := NewCompatStreamHeaderWriter(c, resp.Header, options.Headers)

	state := bridge.NewChatCompletionsToResponsesStreamState(options.Runtime, originalModel)
	state.CustomTools = customTools
	state.FunctionTools = functionTools
	state.ToolSearchDeclared = toolSearch
	state.NamespaceTools = namespaceTools
	clientDisconnected := false

	writeEvents := func(events []wire.ResponsesStreamEvent) {
		if clientDisconnected || len(events) == 0 {
			return
		}
		writeStreamHeaders()
		for _, event := range events {
			sse, err := bridge.ResponsesEventToSSE(event)
			if err != nil {
				logger.L().Warn("openai responses chat fallback: failed to marshal stream event",
					zap.Error(err),
					zap.String("request_id", requestID),
				)
				continue
			}
			if _, err := fmt.Fprint(c.Writer, sse); err != nil {
				clientDisconnected = true
				logger.L().Debug("openai responses chat fallback: client disconnected, continuing to drain upstream for billing",
					zap.Error(err),
					zap.String("request_id", requestID),
				)
				return
			}
		}
		c.Writer.Flush()
	}

	scan := ScanCCStream(resp, options.CC(), "openai responses chat fallback", requestID, startTime, func(chunk *wire.ChatCompletionsChunk) {
		events := bridge.ChatCompletionsChunkToResponsesEvents(options.Runtime, chunk, state)
		options.CacheEvents(events)
		writeEvents(events)
	})

	if scan.Err != nil {
		return &CompatResponseResult{
			RequestID:       requestID,
			UpstreamHeaders: resp.Header,
			Usage:           scan.Usage,
			Model:           originalModel,
			UpstreamModel:   upstreamModel,
			ServiceTier:     options.NormalizeServiceTier(scan.ServiceTier),
			ReasoningEffort: reasoningEffort,
			ResolvedTier:    options.ResolvedServiceTier(),
			Stream:          true,
			Duration:        time.Since(startTime),
			FirstTokenMs:    scan.FirstTokenMs,
		}, fmt.Errorf("stream usage incomplete: %w", scan.Err)
	}
	if err := state.ValidateToolCallArguments(); err != nil {
		return &CompatResponseResult{
			RequestID:       requestID,
			UpstreamHeaders: resp.Header,
			Usage:           scan.Usage,
			Model:           originalModel,
			UpstreamModel:   upstreamModel,
			ServiceTier:     options.NormalizeServiceTier(scan.ServiceTier),
			ReasoningEffort: reasoningEffort,
			ResolvedTier:    options.ResolvedServiceTier(),
			Stream:          true,
			Duration:        time.Since(startTime),
			FirstTokenMs:    scan.FirstTokenMs,
		}, fmt.Errorf("invalid tool call arguments from upstream: %w", err)
	}

	finalEvents := bridge.FinalizeChatCompletionsResponsesStream(options.Runtime, state)
	options.CacheEvents(finalEvents)
	writeEvents(finalEvents)
	if !clientDisconnected {
		writeStreamHeaders()
		if _, err := fmt.Fprint(c.Writer, "data: [DONE]\n\n"); err != nil {
			clientDisconnected = true
		}
		if !clientDisconnected {
			c.Writer.Flush()
		}
	}
	if !scan.SawDone {
		LogCCStreamMissingDoneSentinel("openai responses chat fallback", requestID)
	}

	return &CompatResponseResult{
		RequestID:       requestID,
		UpstreamHeaders: resp.Header,
		Usage:           scan.Usage,
		Model:           originalModel,
		UpstreamModel:   upstreamModel,
		ServiceTier:     options.NormalizeServiceTier(scan.ServiceTier),
		ReasoningEffort: reasoningEffort,
		ResolvedTier:    options.ResolvedServiceTier(),
		Stream:          true,
		Duration:        time.Since(startTime),
		FirstTokenMs:    scan.FirstTokenMs,
	}, nil
}

// ReadCCAsMessagesBuffered converts Chat JSON to an Anthropic Messages response.
func ReadCCAsMessagesBuffered(c *upstream.OutputContext, resp *http.Response, options RawResponseOptions, originalModel, upstreamModel string, reasoningEffort *string, startTime time.Time) (*CompatResponseResult, error) {
	requestID := resp.Header.Get("x-request-id")
	ccResp, usage, err := ReadCCJSONResponse(resp, options.CC())
	if err != nil {
		return nil, err
	}
	anthropicResp := bridge.ChatCompletionsResponseToAnthropic(options.Runtime, ccResp, originalModel)

	options.Headers(c.Writer.Header(), resp.Header)
	c.JSON(http.StatusOK, anthropicResp)

	return &CompatResponseResult{
		RequestID:       requestID,
		UpstreamHeaders: resp.Header,
		Usage:           usage,
		Model:           originalModel,
		UpstreamModel:   upstreamModel,
		ServiceTier:     options.ServiceTier(),
		ReasoningEffort: reasoningEffort,
		ResolvedTier:    options.ResolvedServiceTier(),
		Stream:          false,
		Duration:        time.Since(startTime),
	}, nil
}

// ReadCCAsMessagesStreaming converts Chat chunks to Messages events and collects usage.
func ReadCCAsMessagesStreaming(c *upstream.OutputContext, resp *http.Response, options RawResponseOptions, originalModel, upstreamModel string, reasoningEffort *string, startTime time.Time) (*CompatResponseResult, error) {
	requestID := resp.Header.Get("x-request-id")
	writeStreamHeaders := NewCompatStreamHeaderWriter(c, resp.Header, options.Headers)

	anthropicState := bridge.NewChatCompletionsToAnthropicStreamState(options.Runtime, originalModel)
	clientDisconnected := false

	// Continue feeding the conversion state after disconnect so final accounting
	// retains all observed chunks; skip only downstream writes.
	emitChunk := func(chunk *wire.ChatCompletionsChunk) {
		hasToolCallDelta := wire.ChatCompletionsChunkHasToolCallDelta(chunk)
		// One conversion state maps Chat chunks directly to Anthropic events.
		anthropicEvents := bridge.ChatCompletionsChunkToAnthropicEvents(options.Runtime, chunk, anthropicState)
		if hasToolCallDelta && len(anthropicEvents) == 0 {
			// Ping while tool arguments accumulate so long argument streams do not appear idle.
			anthropicEvents = append(anthropicEvents, anthropic.AnthropicStreamEvent{Type: "ping"})
		}
		if clientDisconnected {
			return
		}
		for _, aEvt := range anthropicEvents {
			sse, err := bridge.ResponsesAnthropicEventToSSE(aEvt)
			if err != nil {
				continue
			}
			writeStreamHeaders()
			if _, err := fmt.Fprint(c.Writer, sse); err != nil {
				clientDisconnected = true
				break
			}
		}
		if !clientDisconnected && len(anthropicEvents) > 0 {
			c.Writer.Flush()
		}
	}

	scan := ScanCCStream(resp, options.CC(), "openai messages chat fallback", requestID, startTime, emitChunk)
	usage := scan.Usage

	if scan.Err != nil {
		// Skip finalization after read errors: a synthesized message_stop would hide truncation.
		// Return the observed usage with an incomplete-usage error.
		return &CompatResponseResult{
			RequestID:        requestID,
			UpstreamHeaders:  resp.Header,
			Usage:            usage,
			Model:            originalModel,
			UpstreamModel:    upstreamModel,
			ServiceTier:      options.NormalizeServiceTier(scan.ServiceTier),
			ReasoningEffort:  reasoningEffort,
			ResolvedTier:     options.ResolvedServiceTier(),
			Stream:           true,
			Duration:         time.Since(startTime),
			FirstTokenMs:     scan.FirstTokenMs,
			ClientDisconnect: clientDisconnected,
		}, fmt.Errorf("stream usage incomplete: %w", scan.Err)
	}

	// Close unfinished content blocks and emit message_delta and message_stop.
	finalEvents := bridge.FinalizeChatCompletionsAnthropicStream(options.Runtime, anthropicState)
	if !clientDisconnected {
		for _, aEvt := range finalEvents {
			sse, err := bridge.ResponsesAnthropicEventToSSE(aEvt)
			if err != nil {
				continue
			}
			writeStreamHeaders()
			if _, err := fmt.Fprint(c.Writer, sse); err != nil {
				clientDisconnected = true
				break
			}
		}
		c.Writer.Flush()
	}
	if !scan.SawDone {
		LogCCStreamMissingDoneSentinel("openai messages chat fallback", requestID)
	}

	return &CompatResponseResult{
		RequestID:        requestID,
		UpstreamHeaders:  resp.Header,
		Usage:            usage,
		Model:            originalModel,
		UpstreamModel:    upstreamModel,
		ServiceTier:      options.NormalizeServiceTier(scan.ServiceTier),
		ReasoningEffort:  reasoningEffort,
		ResolvedTier:     options.ResolvedServiceTier(),
		Stream:           true,
		Duration:         time.Since(startTime),
		FirstTokenMs:     scan.FirstTokenMs,
		ClientDisconnect: clientDisconnected,
	}, nil
}
