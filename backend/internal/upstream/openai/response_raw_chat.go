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

// response_raw_chat.go forwards raw Chat responses and converts Chat output
// into Responses or Messages protocols.
// Raw Chat streams observe usage, service tier, refusal evidence, and terminal
// signals before delivery repairs change the client line. Large-request refusal
// detection can hold lines before headers, while downstream write failure
// suppresses delivery and leaves scanning active for accounting. Terminal
// evidence includes DONE, a usage object, or a nonblank finish reason;
// delivered non-SSE bodies have a separate truncation exemption.
//
// Chat-to-Responses and Chat-to-Messages conversion retains one bridge state
// across the upstream stream. Responses output/events reach reasoning-cache
// callbacks before attempted delivery, including after client disconnect, and
// argument validation precedes final Responses events. Read failures skip
// synthetic successful closure, while clean EOF can finalize those Chat
// conversion paths even when DONE was absent. The executor owns response-body
// closure for each buffered and streaming reader.

// RawResponseOptions binds raw Chat delivery and protocol conversion to
// attempt-specific observation and policy.
type RawResponseOptions struct {
	Runtime bridge.Runtime // conversion clock and random source
	// ProviderID identifies the selected provider in raw-stream truncation
	// diagnostics; a nil gateway provider leaves it zero without disabling bound
	// output callbacks.
	ProviderID int64
	// Scanner supplies raw Chat line reading under caller limits; Chat conversion
	// readers instead obtain scanner policy through their CC callback.
	Scanner func(io.Reader) *bufio.Scanner
	// CC supplies native Chat response options for the four protocol-conversion
	// readers; those options bind observation and read/error handling to this
	// attempt.
	CC func() CCResponseOptions
	// ReadBody consumes the raw buffered response under caller size/error policy;
	// response-body closure remains with the executor after this callback returns.
	ReadBody func(io.Reader) ([]byte, error)
	// BodyLimitError identifies an already presented size rejection via errors.Is;
	// other raw buffered read failures invoke the generic WriteError callback.
	BodyLimitError error
	// Headers copies approved upstream headers before raw forwarding or converted
	// JSON/SSE delivery; content type is then selected by the particular reader.
	Headers func(http.Header, http.Header)
	// WriteError presents raw buffered read failures except the configured size
	// sentinel; its void result does not report downstream delivery success.
	WriteError func(int, string, string)
	// Observe receives native payloads before raw delivery repairs; gateway binding
	// observes service tier independently of usage parsing, and buffered JSON uses
	// the completed-event label.
	Observe func([]byte, string)
	// ObserveSSE receives buffered bodies with explicit or sniffed SSE framing
	// before transformation; the gateway inspects their response-tier observations.
	ObserveSSE func(string)
	// TransformLine repairs a raw Chat line after upstream observation; the reader
	// then strips empty tool-call identity fields from the delivery copy.
	TransformLine func(string) string
	// TransformBody repairs buffered bytes after upstream usage/tier and
	// MissingUsage policy, keeping those observations tied to the pre-repair body.
	TransformBody func([]byte) []byte
	// MissingUsage can reject raw buffered output before transformation; it
	// receives the top-level model and accumulated usage observed from the upstream
	// bytes.
	MissingUsage func(string, wire.ForwardUsage) error
	// ServiceTier supplies request-local observed tier for raw/buffered results;
	// converted stream results instead normalize the tier captured by ScanCCStream.
	ServiceTier func() string
	// ResolvedServiceTier supplies an optional request tier pointer for result
	// adaptation; callers keep pointed-to text stable while downstream accounting
	// consumes it.
	ResolvedServiceTier func() *string
	// NormalizeServiceTier converts ScanCCStream tier observations for converted
	// stream results, including read-error results returned before bridge
	// finalization.
	NormalizeServiceTier func(string) string
	// TruncatedFailover supplies recovery classification when a non-aborted raw
	// Chat stream lacks terminal evidence before client output has begun.
	TruncatedFailover func(error) error
	// RecordTruncation records non-aborted raw Chat truncation after output; the
	// reader then returns typed upstream read failure with observed usage.
	RecordTruncation func(error)
	// SilentRefusal supplies recovery for cleanly read held raw Chat refusal
	// candidates before output, under the detector's large-request eligibility
	// rule.
	SilentRefusal func() error
	// CacheOutput receives converted Responses output before JSON serialization;
	// its shared slice and nested values remain stable until client delivery uses
	// them.
	CacheOutput func([]wire.ResponsesOutput)
	// CacheEvents observes converted Responses events before attempted delivery,
	// including after disconnection; shared event data remains stable while the
	// writer still consumes it.
	CacheEvents func([]wire.ResponsesStreamEvent)
}

// ReadRawChatStreaming forwards Chat lines after observing upstream usage,
// tier, refusal evidence, and terminal state. Supply scanner, transformation,
// header, tier, and selected failure callbacks; the executor owns body closure.
// Reasoning/tier pointers and headers remain shared in the returned result.
//
// Held refusal candidates settle before final delivery. Non-aborted truncation fails over
// before output and returns typed failure with usage afterward. Client write
// failure or scanner cancellation suppresses that truncation classification;
// those exits can return observed usage with nil error, and ClientDisconnect
// remains unpopulated.
func ReadRawChatStreaming(c *upstream.OutputContext, resp *http.Response, options RawResponseOptions, originalModel, upstreamModel string, reasoningEffort *string, startTime time.Time, requestBodyLen int) (*CompatResponseResult, error) {
	requestID := resp.Header.Get("x-request-id")
	writeStreamHeaders := NewCompatStreamHeaderWriter(c, resp.Header, options.Headers)
	scanner := options.Scanner(resp.Body)

	var usage wire.ForwardUsage

	clientDisconnected := false
	clientOutputStarted := false
	pendingLines := make([]string, 0, 8)
	refusalDetector := NewChatSilentRefusalDetector(requestBodyLen)
	var terminal wire.RawStreamTerminalState

	writeLine := func(line string) {
		if clientDisconnected {
			return
		}
		// Hold native lines while the large-request detector lacks release evidence,
		// keeping a terminal refusal eligible for recovery before semantic client output.
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
				if u := wire.ExtractCCStreamUsage(payload); u != nil {
					usage = *u
				}
			}
		}
		// Observe upstream evidence first, then repair the client copy and remove
		// empty tool identity fields that would interfere with delta accumulation downstream.
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
		}
	}

	scanErr := scanner.Err()
	if scanErr != nil && !errors.Is(scanErr, context.Canceled) && !errors.Is(scanErr, context.DeadlineExceeded) {
		logger.L().Warn("openai chat_completions raw: stream read error",
			zap.Error(scanErr),
			zap.String("request_id", requestID),
		)
	}

	// Delivery failure or canceled/deadline reads suppress truncation attribution;
	// observed usage remains returnable and these aborted paths can finish with nil error.
	clientAborted := clientDisconnected ||
		errors.Is(scanErr, context.Canceled) ||
		errors.Is(scanErr, context.DeadlineExceeded)

	// The terminal tracker requires DONE, usage-object, or finish-reason evidence
	// after SSE data or before any output; an already delivered non-SSE body is exempt.
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
			// No semantic client output has been delivered, so classify missing
			// terminal evidence as recoverable through the caller's selected provider retry policy.
			return nil, options.TruncatedFailover(cause)
		}
		// Retain observed usage after delivered content while recording truncation;
		// the typed read error lets the handler present an incomplete stream and account failure.
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

// ReadRawChatBuffered observes usage and service tier before repairing and
// forwarding a buffered Chat body with HTTP 200. Supply ReadBody, observation,
// usage, transformation, headers, and tier callbacks; the executor closes the
// response body. A configured body-limit error suppresses duplicate generic
// error presentation.
//
// Explicit or sniffed SSE is inspected frame by frame for usage and tier, but
// forwarding still uses the returned transformed bytes and upstream content
// type. MissingUsage can prevent delivery. Writer errors are discarded by this
// reader, and request reasoning/tier pointers remain available in the result
// independently of the upstream body's metadata.
func ReadRawChatBuffered(c *upstream.OutputContext, resp *http.Response, options RawResponseOptions, originalModel, upstreamModel string, reasoningEffort *string, startTime time.Time) (*CompatResponseResult, error) {
	requestID := resp.Header.Get("x-request-id")

	respBody, err := options.ReadBody(resp.Body)
	if err != nil {
		// The configured size sentinel already has caller presentation; other
		// read failures receive this generic error before the wrapped cause is returned.
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
		// Compatible upstreams can return SSE for a buffered request; observe
		// framed usage/tier before raw forwarding, with later recognized snapshots
		// replacing earlier ones.
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
	// Raw buffered delivery does not surface sink write errors through this
	// return; the result still contains usage observed before representation repair.
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

// ReadCCAsResponsesBuffered converts Chat JSON to Responses JSON using the
// supplied tool declaration maps, then caches converted output before writing
// it. CC supplies the native Chat read policy, and the executor retains
// response-body ownership.
//
// Custom, function, search, and namespace declarations tell the bridge how
// returned calls match client tool representations. CacheOutput receives the
// same output representation later serialized for delivery, so callback
// consumers keep shared data stable. HTTP 200 output uses approved upstream
// headers; the returned compatibility result retains usage and request
// annotations but leaves ResponseID unpopulated.
func ReadCCAsResponsesBuffered(c *upstream.OutputContext, resp *http.Response, options RawResponseOptions, originalModel, upstreamModel string, reasoningEffort *string, startTime time.Time, customTools, functionTools map[string]bool, toolSearch bool, namespaceTools map[string]bridge.NamespacedToolName) (*CompatResponseResult, error) {
	requestID := resp.Header.Get("x-request-id")
	ccResp, usage, err := ReadCCJSONResponse(resp, options.CC())
	if err != nil {
		return nil, err
	}
	responsesResp := bridge.ChatCompletionsResponseToResponses(options.Runtime, ccResp, originalModel, customTools, functionTools, toolSearch, namespaceTools)
	// Register reasoning output before serialization and sink delivery, so
	// cache observation uses the same converted client representation even if delivery fails.
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

// ReadCCAsResponsesStreaming converts Chat chunks to Responses events while
// collecting native usage and timing. CC supplies read policy; the executor
// owns body closure. Tool declaration maps and namespace metadata remain stable
// while one bridge state uses them.
//
// Converted events are cached before writing, and conversion/cache observation
// continues after sink failure. Read errors or invalid accumulated tool
// arguments return observed usage and skip final events. On clean scanning,
// final events are cached/delivered and DONE is attempted; absent upstream DONE
// is logged after finalization rather than rejected. The result leaves
// ClientDisconnect and ResponseID unpopulated despite local delivery state.
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
			// Skip unsupported event encoding after conversion/cache observation;
			// later events can still be delivered and native usage parsing continues upstream.
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
		// Cache conversion before the writer checks disconnection, retaining
		// reasoning observations from later chunks after the client sink has stopped
		// accepting events.
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
		}, fmt.Errorf("stream usage incomplete: %w", scan.Err)
	}
	// Validate assembled arguments before final Responses events, preventing
	// malformed accumulated tool JSON from receiving a synthesized successful terminal.
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
	}, nil
}

// ReadCCAsMessagesBuffered reads Chat JSON and converts it to Messages JSON
// with the supplied client model and runtime. CC supplies native read/error
// policy, and the executor retains body ownership.
//
// Usage is taken from the Chat reader before conversion. Request reasoning/tier
// pointers and the response header map are retained for inbound adaptation,
// while approved headers are copied before HTTP 200 JSON delivery. The bridge
// owns tool/content conversion, and this wrapper's result leaves the Responses
// turn-ID field unpopulated.
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

// ReadCCAsMessagesStreaming converts Chat chunks to Messages events while
// retaining native usage. CC supplies scanner and observation policy, and
// the executor owns response-body closure.
// Conversion continues after client disconnect even though later delivery is
// suppressed.
//
// Tool argument deltas can yield no content events while the bridge accumulates
// them; an Anthropic ping keeps that interval active downstream. Scanner
// failure returns observed usage and skips synthetic message closure. Clean
// scanning finalizes content blocks and message terminal events even when
// upstream DONE was missing, which is logged afterward. ClientDisconnect
// records local delivery failure in this reader's result.
func ReadCCAsMessagesStreaming(c *upstream.OutputContext, resp *http.Response, options RawResponseOptions, originalModel, upstreamModel string, reasoningEffort *string, startTime time.Time) (*CompatResponseResult, error) {
	requestID := resp.Header.Get("x-request-id")
	writeStreamHeaders := NewCompatStreamHeaderWriter(c, resp.Header, options.Headers)

	anthropicState := bridge.NewChatCompletionsToAnthropicStreamState(options.Runtime, originalModel)
	clientDisconnected := false

	// Continue bridge accumulation after sink failure, so ScanCCStream can
	// retain native usage while final protocol state sees all decoded upstream chunks.
	emitChunk := func(chunk *wire.ChatCompletionsChunk) {
		hasToolCallDelta := wire.ChatCompletionsChunkHasToolCallDelta(chunk)
		// Convert each decoded chunk before testing delivery state, letting the
		// bridge retain complete argument/content accumulation despite later
		// downstream write suppression.
		anthropicEvents := bridge.ChatCompletionsChunkToAnthropicEvents(options.Runtime, chunk, anthropicState)
		if hasToolCallDelta && len(anthropicEvents) == 0 {
			// Tool-only deltas can remain buffered in the bridge until their
			// arguments close; send a Messages ping so that interval still produces downstream activity.
			anthropicEvents = append(anthropicEvents, anthropic.AnthropicStreamEvent{Type: "ping"})
		}
		if clientDisconnected {
			return
		}
		for _, aEvt := range anthropicEvents {
			// Encoding failure drops this converted event while retaining bridge
			// state and native accounting for subsequent chunks or final message events.
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
		// A read error prevents final message closure, which would otherwise
		// imply successful completion despite missing upstream bytes; return observed
		// usage with the error.
		return &CompatResponseResult{
			RequestID:       requestID,
			UpstreamHeaders: resp.Header,
			Usage:           usage,
			Model:           originalModel,
			UpstreamModel:   upstreamModel,
			ServiceTier:     options.NormalizeServiceTier(scan.ServiceTier),
			ReasoningEffort: reasoningEffort,
			ResolvedTier:    options.ResolvedServiceTier(),
			Stream:          true,
			Duration:        time.Since(startTime),

			ClientDisconnect: clientDisconnected,
		}, fmt.Errorf("stream usage incomplete: %w", scan.Err)
	}

	// Clean scanning permits bridge closure of remaining blocks and message
	// totals; upstream DONE presence is logged separately and does not gate this finalization.
	finalEvents := bridge.FinalizeChatCompletionsAnthropicStream(options.Runtime, anthropicState)
	if !clientDisconnected {
		for _, aEvt := range finalEvents {
			// Encoding failure drops this converted event while retaining bridge
			// state and native accounting for subsequent chunks or final message events.
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
		RequestID:       requestID,
		UpstreamHeaders: resp.Header,
		Usage:           usage,
		Model:           originalModel,
		UpstreamModel:   upstreamModel,
		ServiceTier:     options.NormalizeServiceTier(scan.ServiceTier),
		ReasoningEffort: reasoningEffort,
		ResolvedTier:    options.ResolvedServiceTier(),
		Stream:          true,
		Duration:        time.Since(startTime),

		ClientDisconnect: clientDisconnected,
	}, nil
}
