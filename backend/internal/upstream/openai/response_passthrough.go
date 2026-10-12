package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// PassthroughOptions configures response observation, HTTP output and keepalive.
type PassthroughOptions struct {
	// ObserveModel reads the upstream model before restoring the client-facing model.
	ObserveModel func([]byte, string)
	StreamOptions
	NonStream                    NonStreamOptions
	Headers                      func(http.Header, http.Header)
	StartKeepalive               func(http.Header) func()
	MissingUsage                 func(*http.Response, *wire.ForwardUsage, string, bool)
	MissingTerminal              func(string)
	PassthroughFailoverWithModel func(string, []byte, string, string) error
}

// ReadPassthroughStreaming forwards SSE while recording usage and terminal state.
func ReadPassthroughStreaming(ctx context.Context, resp *http.Response, c *upstream.OutputContext, options PassthroughOptions, startTime time.Time, originalModel, mappedModel string) (*StreamingResult, error) {
	options.Headers(c.Writer.Header(), resp.Header)

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

	usage := &wire.ForwardUsage{}
	imageCounter := wire.NewOpenAIImageOutputCounter()
	var firstTokenMs *int
	ttftMode := options.TTFTMode()
	responseID := ""
	clientDisconnected := false
	sawDone := false
	sawTerminalEvent := false
	sawFailedEvent := false
	sawBareError := false
	sawResponseFailed := false
	terminalEventType := ""
	semanticOutputSeen := false
	capacityFailoverSuppressedLogged := false
	failedMessage := ""
	var failedPayload []byte
	clientOutputStarted := false
	codexFailureTerminal := options.NativeOpenAI
	failureDelivered := false
	suppressCurrentEvent := false
	responseFailedPending := false
	var bareErrorPayload []byte
	bareErrorProviderSideEffectsPending := false
	upstreamRequestID := strings.TrimSpace(resp.Header.Get("x-request-id"))
	// Buffer preamble events until output starts so empty failures can still fail over.
	pendingLines := make([]string, 0, 8)

	// Send SSE comment heartbeats during long reasoning waits to refresh proxy idle timers.
	// Preamble events and provider headers remain staged until content starts. Keepalive
	// bytes are excluded from the output-commit check so pre-output failover remains available
	// (issue #3887). This upstream response is SSE; Compact has its own keepalive eligibility.
	stopKeepalive := options.StartKeepalive(c.Writer.Header())
	// Stop the heartbeat on every return path and wait for any active heartbeat write.
	defer stopKeepalive()
	// flushPending tracks bytes not yet followed by a blank SSE line; flush on return unless disconnected.
	flushPending := false
	pendingSSEEventType := ""
	flushPendingOutput := func() {
		if clientDisconnected || !flushPending {
			return
		}
		flusher.Flush()
		options.MarkTime(StreamTimeFlush)
		flushPending = false
	}
	defer flushPendingOutput()
	writePendingLines := func() bool {
		for _, pending := range pendingLines {
			if _, err := fmt.Fprintln(w, pending); err != nil {
				clientDisconnected = true
				options.Logf("[OpenAI passthrough] Client disconnected during streaming, continue draining upstream for usage: provider=%d", options.ProviderID)
				return false
			}
		}
		pendingLines = pendingLines[:0]
		return true
	}
	ensureResponseFailedTerminal := func() {
		if !sawBareError || sawResponseFailed || failureDelivered {
			return
		}
		if bareErrorProviderSideEffectsPending {
			options.TerminalSideEffects(bareErrorPayload, failedMessage, resp.Header, mappedModel)
			bareErrorProviderSideEffectsPending = false
		}
		if clientDisconnected || !writePendingLines() {
			return
		}
		if _, err := fmt.Fprint(w, options.BuildOpenAIResponseFailedSSE(responseID, originalModel, bareErrorPayload, failedMessage)); err != nil {
			clientDisconnected = true
			return
		}
		clientOutputStarted = true
		failureDelivered = true
		flushPending = true
		flushPendingOutput()
	}

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := options.MaxLineSize
	scanBuf := httpclient.GetSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)
	defer httpclient.PutSSEScannerBuf64K(scanBuf)
	documentScanner := wire.NewSSEJSONDocumentScanner(scanner)

	needModelReplace := strings.TrimSpace(originalModel) != "" && strings.TrimSpace(mappedModel) != "" && strings.TrimSpace(originalModel) != strings.TrimSpace(mappedModel)
	responseAccumulator := bridge.NewBufferedResponseAccumulator()
	streamDoneItems := bridge.NewResponsesStreamOutputItems()
	streamImageOutputs := make([]json.RawMessage, 0, 1)
	streamSeenImages := make(map[string]struct{})
	resultWithUsage := func() *StreamingResult {
		return &StreamingResult{
			Usage:            usage,
			FirstTokenMs:     firstTokenMs,
			ResponseID:       responseID,
			ImageCount:       imageCounter.Count(),
			ImageOutputSizes: imageCounter.Sizes(),
		}
	}

	var lifecycleNormalizer wire.ResponseLifecycleNormalizer
	pendingLifecycleHeader := ""
	for documentScanner.Scan() {
		line := documentScanner.Text()
		if eventType, ok := wire.ExtractSSEEventLine(line); ok {
			pendingSSEEventType = eventType
			eventType = strings.TrimSpace(eventType)
			suppressCurrentEvent = codexFailureTerminal && (eventType == "error" || (sawBareError && !sawResponseFailed && eventType != "response.failed"))
			if wire.IsResponseLifecycleEvent(eventType) {
				// Deliver lifecycle headers with their data so filtering a terminal event also removes its header.
				pendingLifecycleHeader = line
				continue
			}
		}
		lineStartsClientOutput := false
		forceFlushFailedEvent := false
		if data, ok := wire.ExtractSSEDataLine(line); ok {
			options.MarkTime(StreamTimeData)
			dataBytes := []byte(data)
			trimmedData := strings.TrimSpace(data)
			rawEventType := wire.EffectiveOpenAISSEEventType(dataBytes, pendingSSEEventType)
			if options.ObserveModel != nil {
				options.ObserveModel(dataBytes, rawEventType)
			}
			normalizedData, normalizedType, duplicateTerminal := lifecycleNormalizer.Normalize(dataBytes, rawEventType)
			if !bytes.Equal(normalizedData, dataBytes) {
				dataBytes = normalizedData
				data = string(dataBytes)
				trimmedData = strings.TrimSpace(data)
				line = "data: " + data
			}
			rawEventType = normalizedType
			if needModelReplace && strings.Contains(data, mappedModel) {
				line = wire.ReplaceModelInSSELine(line, mappedModel, originalModel)
				if replacedData, replaced := wire.ExtractSSEDataLine(line); replaced {
					dataBytes = []byte(replacedData)
					trimmedData = strings.TrimSpace(replacedData)
				}
			}
			if normalizedData, normalized := wire.NormalizeOpenAIResponsesFunctionCallArguments(dataBytes); normalized {
				dataBytes = normalizedData
				trimmedData = strings.TrimSpace(string(normalizedData))
				line = "data: " + string(normalizedData)
			}
			if normalizedData, normalized := wire.NormalizeCompletedImageGenerationStatus(dataBytes); normalized {
				dataBytes = normalizedData
				trimmedData = strings.TrimSpace(string(normalizedData))
				line = "data: " + string(normalizedData)
			}
			if trimmedData != "[DONE]" {
				restoredData, restoreErr := options.RestoreNamespace(dataBytes)
				if restoreErr != nil {
					return resultWithUsage(), fmt.Errorf("restore OpenAI passthrough namespace response: %w", restoreErr)
				}
				restoredData = options.RestoreToolNames(restoredData, rawEventType)
				if !bytes.Equal(restoredData, dataBytes) {
					dataBytes = restoredData
					trimmedData = strings.TrimSpace(string(restoredData))
					line = "data: " + string(restoredData)
				}
			}
			eventType := wire.EffectiveOpenAISSEEventType(dataBytes, rawEventType)
			// Recompute suppression after normalization, including legacy done events renamed to failed.
			suppressCurrentEvent = codexFailureTerminal && (eventType == "error" || (sawBareError && !sawResponseFailed && eventType != "response.failed"))
			if !capacityFailoverSuppressedLogged && options.NativeOpenAI &&
				(eventType == "error" || eventType == "response.failed") &&
				options.ClientOutputStarted(clientOutputStarted) &&
				IsOpenAIUpstreamCapacityShedEvent(dataBytes) {
				options.CapacitySuppressed(upstreamRequestID, eventType)
				capacityFailoverSuppressedLogged = true
			}
			cyberHit := false
			if eventType == "response.failed" || eventType == "error" {
				if codexFailureTerminal && eventType == "error" {
					sawBareError = true
					bareErrorPayload = append(bareErrorPayload[:0], dataBytes...)
					suppressCurrentEvent = true
				} else if codexFailureTerminal && eventType == "response.failed" {
					sawResponseFailed = true
				}
				responseFailedPending = !codexFailureTerminal || eventType == "response.failed"
				failedMessage = ExtractOpenAISSEErrorMessage(dataBytes)
				if failedMessage == "" {
					failedMessage = "Upstream response failed"
				}
				// Read terminal usage before marking cyber_policy so the observation records consumed
				// tokens instead of stale zero counts.
				wire.ParseSSEUsageBytesWithType(dataBytes, eventType, usage)
				if hit, code, msg := DetectOpenAICyberPolicy(dataBytes); hit {
					cyberHit = true
					options.MarkCyber(CyberObservation{
						Code:           code,
						Message:        msg,
						Body:           options.TruncateString(string(dataBytes), 4096),
						UpstreamStatus: http.StatusOK,
						UpstreamInTok:  usage.InputTokens,
						UpstreamOutTok: usage.OutputTokens,
					})
				}
				outputStarted := options.ClientOutputStarted(clientOutputStarted)
				if !outputStarted && !cyberHit {
					if compactErr := options.CompactFallback(dataBytes, failedMessage); compactErr != nil {
						return resultWithUsage(), compactErr
					}
				}
				if outputStarted && !cyberHit {
					if codexFailureTerminal && eventType == "error" {
						// Wait for the authoritative response.failed before mutating
						// provider health; EOF synthesis applies the pending effect.
						bareErrorProviderSideEffectsPending = true
					} else {
						options.TerminalSideEffects(dataBytes, failedMessage, resp.Header, mappedModel)
						bareErrorProviderSideEffectsPending = false
					}
				}
				if !outputStarted {
					shouldFailover := false
					if !cyberHit {
						if eventType == "error" {
							shouldFailover = OpenAIStreamErrorEventShouldFailover(dataBytes, failedMessage)
						} else {
							shouldFailover = OpenAIStreamFailedEventShouldFailover(dataBytes, failedMessage)
						}
					}
					if shouldFailover {
						return resultWithUsage(),
							options.PassthroughFailoverWithModel(upstreamRequestID, dataBytes, failedMessage, mappedModel)
					}
					if !cyberHit && !sawBareError {
						if status, errType, errMsg, matched := options.ErrorRule(dataBytes, failedMessage); matched {
							// Record upstream errors even when a passthrough rule handles the client response.
							options.RecordError(upstreamRequestID, "http_error", dataBytes, failedMessage)
							options.MarkCommitted()
							c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
							c.JSON(status, map[string]any{
								"error": map[string]any{
									"type":    errType,
									"message": errMsg,
								},
							})
							return resultWithUsage(), fmt.Errorf("upstream response failed: passthrough rule matched message=%s", errMsg)
						}
					}
				}
				forceFlushFailedEvent = true
				sawFailedEvent = true
			}
			if trimmedData == "[DONE]" {
				sawDone = true
				terminalEventType = "[DONE]"
			}
			if options.OpenAIStreamEventIsTerminalWithType(trimmedData, eventType) {
				sawTerminalEvent = true
				if trimmedData != "[DONE]" {
					terminalEventType = eventType
				}
			}
			if responseID == "" {
				responseID = wire.ExtractOpenAIResponseIDFromJSONBytes(dataBytes)
			}
			var responseEvent wire.ResponsesStreamEvent
			if err := json.Unmarshal(dataBytes, &responseEvent); err == nil {
				responseAccumulator.ProcessEvent(&responseEvent)
			}
			if imageOutput, ok := wire.ExtractImageGenerationOutputFromSSEData(dataBytes, streamSeenImages); ok {
				streamImageOutputs = append(streamImageOutputs, imageOutput)
			}
			streamDoneItems.Observe(dataBytes)
			if normalizedData, normalized := bridge.NormalizeResponsesStreamingTerminalOutput(dataBytes, responseAccumulator, streamDoneItems, streamImageOutputs); normalized {
				dataBytes = normalizedData
				data = string(normalizedData)
				trimmedData = strings.TrimSpace(data)
				line = "data: " + data
				eventType = strings.TrimSpace(gjson.GetBytes(dataBytes, "type").String())
			}
			imageCounter.AddSSEData(dataBytes)
			if sanitizedData, sanitized := SanitizeOpenAIResponseFailedEventForClient(
				dataBytes,
				eventType,
				options.ClientOutputStarted(clientOutputStarted),
			); sanitized {
				dataBytes = sanitizedData
				trimmedData = strings.TrimSpace(string(sanitizedData))
				line = "data: " + string(sanitizedData)
			}
			lineStartsClientOutput = forceFlushFailedEvent || OpenAIStreamDataStartsClientOutput(trimmedData, eventType)
			if lineStartsClientOutput && trimmedData != "[DONE]" && !wire.OpenAIStreamEventTypeIsTerminal(eventType) {
				semanticOutputSeen = true
			}
			// Detect empty completion before writing so passthrough can still fail over.
			if (eventType == "response.completed" || eventType == "response.done") &&
				!sawFailedEvent && !semanticOutputSeen && !clientOutputStarted &&
				wire.OpenAIResponsesCompletedEventIsEmpty(dataBytes, usage) {
				return resultWithUsage(), options.EmptyCompleted(upstreamRequestID)
			}
			startsVisibleOutput := wire.StreamDataStartsVisibleOutput(trimmedData, eventType)
			if startsVisibleOutput {
				options.MarkTime(StreamTimeVisible)
			}
			if firstTokenMs == nil && options.OpenAIStreamDataStartsTTFT(trimmedData, eventType, forceFlushFailedEvent, ttftMode) {
				ms := int(time.Since(startTime).Milliseconds())
				firstTokenMs = &ms
			}
			wire.ParseSSEUsageBytesWithType(dataBytes, eventType, usage)
			if pendingLifecycleHeader != "" {
				header := pendingLifecycleHeader
				if eventType != strings.TrimSpace(pendingSSEEventType) {
					header = "event: " + eventType
				}
				line = header + "\n" + line
				pendingLifecycleHeader = ""
			}
			suppressCurrentEvent = suppressCurrentEvent || duplicateTerminal
		}
		if line == "" {
			pendingLifecycleHeader = ""
			pendingSSEEventType = ""
			if suppressCurrentEvent {
				suppressCurrentEvent = false
				responseFailedPending = false
				continue
			}
		}

		if !clientDisconnected && !failureDelivered && !suppressCurrentEvent {
			if !clientOutputStarted && !lineStartsClientOutput {
				pendingLines = append(pendingLines, line)
				continue
			}
			// Stop and join heartbeat writes before content output; repeated calls are safe.
			// This loop then owns ResponseWriter writes.
			if !clientOutputStarted {
				stopKeepalive()
			}
			if !clientOutputStarted && len(pendingLines) > 0 {
				if !writePendingLines() {
					continue
				}
			}
			if _, err := fmt.Fprintln(w, line); err != nil {
				clientDisconnected = true
				options.Logf("[OpenAI passthrough] Client disconnected during streaming, continue draining upstream for usage: provider=%d", options.ProviderID)
			} else {
				clientOutputStarted = true
				flushPending = true
				if line == "" {
					flushPendingOutput()
				}
			}
		}
		if line == "" && responseFailedPending {
			responseFailedPending = false
			failureDelivered = true
		}
	}
	ensureResponseFailedTerminal()
	if err := documentScanner.Err(); err != nil {
		if (sawDone || sawTerminalEvent) && !sawFailedEvent {
			options.ClearDisconnect()
			return resultWithUsage(), nil
		}
		if sawFailedEvent {
			err := fmt.Errorf("upstream response failed: %s", failedMessage)
			return resultWithUsage(), options.WrapOpenAIUpstreamWarningIfCyber(resp.StatusCode, failedPayload, failedMessage, err)
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return resultWithUsage(), fmt.Errorf("stream usage incomplete: %w", err)
		}
		if errors.Is(err, bufio.ErrTooLong) {
			options.Logf("[OpenAI passthrough] SSE line too long: provider=%d max_size=%d error=%v", options.ProviderID, maxLineSize, err)
			return resultWithUsage(), err
		}
		if !options.ClientOutputStarted(clientOutputStarted) {
			msg := "OpenAI stream disconnected before completion"
			if errText := strings.TrimSpace(err.Error()); errText != "" {
				msg += ": " + errText
			}
			return resultWithUsage(),
				options.Failover(upstreamRequestID, nil, msg)
		}
		if clientDisconnected {
			return resultWithUsage(), fmt.Errorf("stream usage incomplete after disconnect: %w", err)
		}
		options.RecordDisconnect(err, upstreamRequestID)
		options.Logf("[OpenAI passthrough] 流读取异常中断: provider=%d request_id=%s err=%v",
			options.ProviderID,
			upstreamRequestID,
			err,
		)
		return resultWithUsage(), fmt.Errorf("stream read error: %w", err)
	}
	if sawFailedEvent {
		err := fmt.Errorf("upstream response failed: %s", failedMessage)
		return resultWithUsage(), options.WrapOpenAIUpstreamWarningIfCyber(resp.StatusCode, failedPayload, failedMessage, err)
	}
	if !clientDisconnected && !sawDone && !sawTerminalEvent && ctx.Err() == nil {
		options.MissingTerminal(upstreamRequestID)
		if !options.ClientOutputStarted(clientOutputStarted) {
			return resultWithUsage(),
				options.Failover(upstreamRequestID, nil, "OpenAI stream ended before a terminal event")
		}
		options.RecordDisconnect(errors.New("stream ended before terminal event"), upstreamRequestID)
		return resultWithUsage(), errors.New("stream usage incomplete: missing terminal event")
	}
	if (sawDone || sawTerminalEvent) && !sawFailedEvent {
		options.ClearDisconnect()
	}
	options.MissingUsage(resp, usage, terminalEventType, clientDisconnected)

	return resultWithUsage(), nil
}

// ReadPassthroughNonStreaming reads and forwards a buffered response while observing usage.
func ReadPassthroughNonStreaming(ctx context.Context, resp *http.Response, sink upstream.OutputSink, options PassthroughOptions, originalModel, mappedModel string) (*NonStreamingResult, error) {
	body, err := options.NonStream.ReadBody(resp.Body)
	if err != nil {
		return nil, err
	}

	if IsEventStreamResponse(resp.Header) {
		options.NonStream.ObserveSSE(string(body))
		return ReadPassthroughSSEAsJSON(resp, sink, options, body, originalModel, mappedModel)
	}
	options.NonStream.ObserveTier(body)

	usage := &wire.ForwardUsage{}
	usageParsed := false
	if len(body) > 0 {
		if parsedUsage, ok := wire.ExtractOpenAIUsageFromJSONBytes(body); ok {
			*usage = parsedUsage
			usageParsed = true
		}
	}
	if !usageParsed {
		usage = wire.ParseSSEUsageFromBody(string(body))
	}
	options.MissingUsage(resp, usage, "json", false)

	c := upstream.NewOutputContext(sink)
	options.Headers(c.Writer.Header(), resp.Header)

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	if originalModel != "" && mappedModel != "" && originalModel != mappedModel {
		body = wire.ReplaceModelInResponseBody(body, mappedModel, originalModel)
	}
	body, err = options.RestoreNamespace(body)
	if err != nil {
		return nil, fmt.Errorf("restore OpenAI passthrough namespace response: %w", err)
	}
	body = options.NonStream.RestoreToolNames(body)
	body, err = options.NonStream.RestoreOpenAITools(body)
	if err != nil {
		return nil, fmt.Errorf("restore OpenAI Responses client tools: %w", err)
	}
	if !options.NonStream.WriteCompactBridge(resp.StatusCode, body) {
		c.Data(resp.StatusCode, contentType, body)
	}
	return &NonStreamingResult{
		Usage:            usage,
		ResponseID:       wire.ExtractOpenAIResponseIDFromJSONBytes(body),
		ImageCount:       wire.CountOpenAIResponseImageOutputsFromJSONBytes(body),
		ImageOutputSizes: wire.CollectOpenAIResponseImageOutputSizesFromJSONBytes(body),
	}, nil
}

// ReadPassthroughSSEAsJSON collects an SSE terminal response and writes JSON.
func ReadPassthroughSSEAsJSON(resp *http.Response, sink upstream.OutputSink, options PassthroughOptions, body []byte, originalModel, mappedModel string) (*NonStreamingResult, error) {
	bodyText := string(body)
	terminalType, terminalPayload, terminalOK := wire.ExtractOpenAISSETerminalEvent(bodyText)
	if terminalOK && (terminalType == "response.failed" || terminalType == "error") {
		msg := ExtractOpenAISSEErrorMessage(terminalPayload)
		if msg == "" {
			msg = "Upstream compact response failed"
		}
		if compactErr := options.CompactFallback(terminalPayload, msg); compactErr != nil {
			return nil, compactErr
		}
		if failoverErr := options.NonStream.TerminalFailover(resp, terminalType, terminalPayload, msg, mappedModel); failoverErr != nil {
			return nil, failoverErr
		}
		return nil, options.NonStream.ProtocolError(resp, msg)
	}
	finalResponse, ok := wire.ExtractCodexFinalResponse(bodyText)

	usage := wire.ParseSSEUsageFromBody(bodyText)
	if ok {
		if parsedUsage, parsed := wire.ExtractOpenAIUsageFromJSONBytes(finalResponse); parsed {
			*usage = parsedUsage
		}

		if len(gjson.GetBytes(finalResponse, "output").Array()) == 0 {
			if outputJSON, reconstructed := bridge.ReconstructResponseOutputFromSSE(bodyText); reconstructed {
				if patched, err := sjson.SetRawBytes(finalResponse, "output", outputJSON); err == nil {
					finalResponse = patched
				}
			}
		}
		finalResponse = options.NonStream.SupplementCompaction(finalResponse, bodyText)
		body = finalResponse
		if originalModel != "" && mappedModel != "" && originalModel != mappedModel {
			body = wire.ReplaceModelInResponseBody(body, mappedModel, originalModel)
		}

		body = options.NonStream.CorrectToolCalls(body)
		restoredBody, restoreErr := options.RestoreNamespace(body)
		if restoreErr != nil {
			return nil, fmt.Errorf("restore OpenAI passthrough namespace response: %w", restoreErr)
		}
		restoredBody = options.NonStream.RestoreToolNames(restoredBody)
		body = restoredBody
		body, restoreErr = options.NonStream.RestoreOpenAITools(body)
		if restoreErr != nil {
			return nil, fmt.Errorf("restore OpenAI Responses client tools: %w", restoreErr)
		}
	} else {
		if originalModel != "" && mappedModel != "" && originalModel != mappedModel {
			bodyText = wire.ReplaceModelInSSEBody(bodyText, mappedModel, originalModel)
		}
		body = []byte(bodyText)
	}

	c := upstream.NewOutputContext(sink)
	options.Headers(c.Writer.Header(), resp.Header)
	options.MissingUsage(resp, usage, terminalType, false)

	contentType := "application/json; charset=utf-8"
	if !ok {
		contentType = resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "text/event-stream"
		}
	}
	if !options.NonStream.WriteCompactBridge(resp.StatusCode, body) {
		c.Data(resp.StatusCode, contentType, body)
	}

	return &NonStreamingResult{
		Usage:            usage,
		ResponseID:       wire.ExtractOpenAIResponseIDFromJSONBytes(body),
		ImageCount:       wire.CountOpenAIImageOutputsFromSSEBody(bodyText),
		ImageOutputSizes: wire.CollectOpenAIImageOutputSizesFromSSEBody(bodyText),
	}, nil
}
