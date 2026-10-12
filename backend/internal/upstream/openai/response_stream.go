package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

const (
	StreamTimeFlush   StreamTime = "first_downstream_flush"
	StreamTimeVisible StreamTime = "first_visible_output"
	StreamTimeData    StreamTime = "first_sse_data"
)

type StreamTime string

type CyberObservation struct {
	Code           string
	Message        string
	Body           string
	UpstreamStatus int
	UpstreamInTok  int
	UpstreamOutTok int
}

// StreamOptions configures timeouts, output and observation for one stream read.
type StreamOptions struct {
	ProviderID                                                           int64
	NativeOpenAI, StageFirstOutput, CodexFailureTerminal, GrokIdlePolicy bool
	FirstOutputTimeout, StreamInterval, KeepaliveInterval                time.Duration
	MaxLineSize                                                          int
	TTFTMode                                                             func() string
	PrepareHeaders                                                       func(http.Header, bool, http.Header) http.Header
	StagedHeadersCommitted                                               func(http.Header)
	Observe                                                              func([]byte, string)
	Logf                                                                 func(string, ...any)
	MarkTime                                                             func(StreamTime)
	IsCommitted                                                          func() bool
	MarkCommitted                                                        func()
	ClientOutputStarted                                                  func(bool) bool
	ClearDisconnect                                                      func()
	RecordDisconnect                                                     func(error, string)
	TerminalSideEffects                                                  func([]byte, string, http.Header, string)
	Failover                                                             func(string, []byte, string) error
	FailoverWithModel                                                    func(string, []byte, string, string, http.Header) error
	MarkSafeFailover                                                     func(error)
	RecordError                                                          func(string, string, []byte, string)
	CompactFallback                                                      func([]byte, string) error
	ErrorRule                                                            func([]byte, string) (int, string, string, bool)
	CapacitySuppressed                                                   func(string, string)
	MarkCyber                                                            func(CyberObservation)
	ToolCorrector                                                        *CodexToolCorrector
	RestoreClientTools, RestoreNamespace                                 func([]byte) ([]byte, error)
	RestoreToolNames                                                     func([]byte, string) []byte
	EmptyCompleted                                                       func(string) error
	CountSearch                                                          func([]byte, map[string]struct{}) int
	StreamTimeout                                                        func(string)
	IdleCooldown                                                         func()
	IdleFailover                                                         func(time.Duration) error
	FirstOutputError                                                     func(time.Time, string, string, time.Duration, string, http.Header) error
	KeepaliveBytes                                                       func(int)
	BuildOpenAIResponseFailedSSE                                         func(string, string, []byte, string) string
	WrapOpenAIUpstreamWarningIfCyber                                     func(int, []byte, string, error) error
	TruncateString                                                       func(string, int) string
	OpenAIStreamDataStartsTTFT                                           func(string, string, bool, string) bool
	OpenAIStreamEventIsTerminalWithType                                  func(string, string) bool
}

// StreamingResult records delivered output, observed usage and stream timing.
type StreamingResult struct {
	// Track semantic delivery, usage, HTTP commitment and retry commitment independently.
	Served, HasUsage, HttpCommitted, RetryCommitted, ClientDisconnected, ObservedOnly bool
	FirstSemanticOutput                                                               *time.Duration
	Usage                                                                             *wire.ForwardUsage
	FirstTokenMs                                                                      *int
	ResponseID                                                                        string
	ImageCount                                                                        int
	ImageOutputSizes                                                                  []string
	SearchCount                                                                       int
}

func ReadStreamingResponse(ctx context.Context, resp *http.Response, c *upstream.OutputContext, options StreamOptions, startTime time.Time, originalModel, mappedModel, reasoningEffort string) (observed *StreamingResult, failure error) {
	firstOutputTimeout := options.FirstOutputTimeout
	guardFirstOutput := firstOutputTimeout > 0
	stageFirstOutput := options.StageFirstOutput
	attemptResponseHeaders := options.PrepareHeaders(resp.Header, stageFirstOutput, c.Writer.Header())

	// Set SSE response headers
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	// Pass through other headers
	if !stageFirstOutput && resp.Header.Get("x-request-id") != "" {
		v := resp.Header.Get("x-request-id")
		c.Header("x-request-id", v)
	}
	applyAttemptResponseHeaders := func() {
		if !stageFirstOutput || len(attemptResponseHeaders) == 0 || c.Writer.Written() {
			return
		}
		for key, values := range attemptResponseHeaders {
			for _, value := range values {
				c.Writer.Header().Add(key, value)
			}
		}
		options.StagedHeadersCommitted(attemptResponseHeaders)
		// Gateway SSE headers remain stable across provider attempts and override upstream values.
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
	}

	w := c.Writer
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}
	maxLineSize := options.MaxLineSize
	var firstTokenMs *int
	ttftMode := options.TTFTMode()
	firstOutputProgressObserved := false
	bufferedWriter := bufio.NewWriterSize(w, 4*1024)
	var firstOutputStage *OpenAIFirstOutputStage
	if stageFirstOutput {
		firstOutputStage = NewDefaultOpenAIFirstOutputStage()
		defer func() {
			if err := firstOutputStage.Close(); err != nil {
				options.Logf("OpenAI first-output staging cleanup failed: provider=%d model=%s error=%v", options.ProviderID, originalModel, err)
			}
		}()
	}
	writePendingString := func(value string) (int, error) {
		if firstOutputStage != nil && !firstOutputStage.Closed() {
			return firstOutputStage.WriteString(value)
		}
		return bufferedWriter.WriteString(value)
	}
	pendingBytes := func() int64 {
		if firstOutputStage != nil && !firstOutputStage.Closed() {
			return firstOutputStage.Buffered()
		}
		return int64(bufferedWriter.Buffered())
	}
	flushBuffered := func() error {
		// Mark a downstream data flush only when the buffer contained bytes.
		hadPendingBytes := pendingBytes() > 0
		if firstOutputStage != nil && !firstOutputStage.Closed() {
			if err := firstOutputStage.CommitTo(w); err != nil {
				return err
			}
		} else {
			if err := bufferedWriter.Flush(); err != nil {
				return err
			}
		}
		flusher.Flush()
		if hadPendingBytes {
			options.MarkTime(StreamTimeFlush)
		}
		return nil
	}

	usage := &wire.ForwardUsage{}
	hasObservedUsage := false
	var firstSemanticOutput *time.Duration
	imageCounter := wire.NewOpenAIImageOutputCounter()
	responseID := ""
	var firstOutputScanGuard atomic.Bool
	firstOutputScanGuard.Store(stageFirstOutput)
	scanner := bufio.NewScanner(resp.Body)
	scanBuf := httpclient.GetSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)
	if stageFirstOutput {
		scanner.Split(OpenAIFirstOutputDynamicScanLines(&firstOutputScanGuard))
	}
	documentScanner := wire.NewSSEJSONDocumentScanner(scanner)

	streamInterval := options.StreamInterval
	// Track upstream arrival gaps independently of downstream write time.
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

	keepaliveInterval := options.KeepaliveInterval
	// Downstream keepalive prevents proxy idle disconnects.
	var keepaliveTicker *time.Ticker
	if keepaliveInterval > 0 {
		keepaliveTicker = time.NewTicker(keepaliveInterval)
		defer keepaliveTicker.Stop()
	}
	var keepaliveCh <-chan time.Time
	if keepaliveTicker != nil {
		keepaliveCh = keepaliveTicker.C
	}

	var firstOutputTimer *time.Timer
	var firstOutputCh <-chan time.Time
	if firstOutputTimeout > 0 {
		remaining := time.Until(startTime.Add(firstOutputTimeout))
		if remaining <= 0 {
			remaining = time.Nanosecond
		}
		firstOutputTimer = time.NewTimer(remaining)
		firstOutputCh = firstOutputTimer.C
		defer firstOutputTimer.Stop()
	}
	stopFirstOutputTimer := func() {
		if firstOutputTimer == nil {
			return
		}
		if !firstOutputTimer.Stop() {
			select {
			case <-firstOutputTimer.C:
			default:
			}
		}
		firstOutputTimer = nil
		firstOutputCh = nil
	}
	// Track downstream writes separately from upstream reads: pre-output failover
	// can buffer response.created / response.in_progress, so keepalive must be
	// based on downstream idle time.
	lastDownstreamWriteAt := time.Now()

	// Emit at most one error event using the Responses schema; custom event shapes
	// can fail validation in downstream SDKs.
	errorEventSent := false
	clientDisconnected := false // Continue draining after disconnect to collect usage.
	sawTerminalEvent := false
	sawFailedEvent := false
	sawBareError := false
	sawResponseFailed := false
	responsesSemanticOutputSeen := false
	capacityFailoverSuppressedLogged := false
	failedMessage := ""
	var failedPayload []byte
	clientOutputStarted := false
	codexFailureTerminal := options.CodexFailureTerminal
	upstreamRequestID := strings.TrimSpace(resp.Header.Get("x-request-id"))
	var streamEarlyErr error
	terminalFailurePending := false
	failureDelivered := false
	suppressCurrentEvent := false
	var bareErrorPayload []byte
	bareErrorProviderSideEffectsPending := false
	pendingSSEEventType := ""
	eventInProgress := false
	eventStartsClientOutput := false
	eventStartsVisibleOutput := false
	eventStartsTTFTOutput := false
	eventShouldFlush := false
	handlePendingWriteError := func(err error) {
		if firstOutputStage != nil && !firstOutputStage.Closed() {
			message := "OpenAI first-output staging failed"
			if errors.Is(err, ErrOpenAIFirstOutputStageLimit) {
				message = "OpenAI first-output staging limit exceeded"
			}
			options.Logf("%s: provider=%d model=%s error=%v", message, options.ProviderID, originalModel, err)
			failoverErr := options.Failover(upstreamRequestID, nil, message)
			options.MarkSafeFailover(failoverErr)
			streamEarlyErr = failoverErr
			_ = resp.Body.Close()
			return
		}
		clientDisconnected = true
		options.Logf("Client disconnected during streaming, continuing to drain upstream for billing")
	}
	completeGuardedEvent := func(queueDrained bool) {
		completedProgressEvent := eventStartsClientOutput
		completedVisibleEvent := eventStartsVisibleOutput
		completedTTFTEvent := eventStartsTTFTOutput
		shouldFlush := eventShouldFlush || (queueDrained && clientOutputStarted)
		eventInProgress = false
		if !clientDisconnected {
			if completedProgressEvent {
				applyAttemptResponseHeaders()
			}
			if shouldFlush {
				if err := flushBuffered(); err != nil {
					clientDisconnected = true
					options.Logf("Client disconnected during streaming flush, continuing to drain upstream for billing")
				} else {
					clientOutputStarted = true
					lastDownstreamWriteAt = time.Now()
				}
			}
		}
		if completedProgressEvent && !firstOutputProgressObserved {
			firstOutputScanGuard.Store(false)
			firstOutputProgressObserved = true
			stopFirstOutputTimer()
		}
		if completedVisibleEvent && firstTokenMs == nil {
			options.MarkTime(StreamTimeVisible)
		}
		if completedTTFTEvent && firstTokenMs == nil {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}
		eventStartsClientOutput = false
		eventStartsVisibleOutput = false
		eventStartsTTFTOutput = false
		eventShouldFlush = false
	}
	sendErrorEvent := func(reason string) {
		if errorEventSent || clientDisconnected {
			return
		}
		errorEventSent = true
		payload := `{"type":"error","sequence_number":0,"error":{"type":"upstream_error","message":` + strconv.Quote(reason) + `,"code":` + strconv.Quote(reason) + `}}`
		if err := flushBuffered(); err != nil {
			clientDisconnected = true
			return
		}
		if _, err := writePendingString("data: " + payload + "\n\n"); err != nil {
			clientDisconnected = true
			return
		}
		if err := flushBuffered(); err != nil {
			clientDisconnected = true
			return
		}
		clientOutputStarted = true
		lastDownstreamWriteAt = time.Now()
	}

	needModelReplace := originalModel != mappedModel
	responseAccumulator := bridge.NewBufferedResponseAccumulator()
	streamDoneItems := bridge.NewResponsesStreamOutputItems()
	streamImageOutputs := make([]json.RawMessage, 0, 1)
	streamSeenImages := make(map[string]struct{})
	searchCounter := 0
	// Deduplicate search call IDs repeated in item.done and response.completed
	// so the same call cannot incur two search surcharges.
	streamSearchSeen := make(map[string]struct{})
	resultWithUsage := func() *StreamingResult {
		return &StreamingResult{
			Served:              firstSemanticOutput != nil,
			HasUsage:            hasObservedUsage,
			HttpCommitted:       c.Writer.Written(),
			RetryCommitted:      options.IsCommitted(),
			ClientDisconnected:  clientDisconnected,
			FirstSemanticOutput: firstSemanticOutput,

			Usage: usage,

			FirstTokenMs: firstTokenMs,

			ResponseID: responseID,

			ImageCount: imageCounter.Count(),

			ImageOutputSizes: imageCounter.Sizes(),

			SearchCount: searchCounter,
		}
	}
	// If a failure has no result, return observed usage marked ObservedOnly.
	defer func() {
		if observed == nil && failure != nil {
			observed = resultWithUsage()
			observed.ObservedOnly = true
		}
	}()
	flushPending := func(disconnectMessage string) {
		if clientDisconnected || pendingBytes() == 0 {
			return
		}
		if err := flushBuffered(); err != nil {
			clientDisconnected = true
			options.Logf("%s", disconnectMessage)
			return
		}
		clientOutputStarted = true
		lastDownstreamWriteAt = time.Now()
	}
	synthesizeBareErrorFailure := func() bool {
		if !codexFailureTerminal || !sawBareError || sawResponseFailed || clientDisconnected {
			return false
		}
		if bareErrorProviderSideEffectsPending {
			options.TerminalSideEffects(bareErrorPayload, failedMessage, resp.Header, mappedModel)
			bareErrorProviderSideEffectsPending = false
		}
		applyAttemptResponseHeaders()
		if _, err := writePendingString(options.BuildOpenAIResponseFailedSSE(responseID, originalModel, bareErrorPayload, failedMessage)); err != nil {
			handlePendingWriteError(err)
			return false
		}
		failedPayload = append(failedPayload[:0], bareErrorPayload...)
		sawResponseFailed = true
		sawFailedEvent = true
		sawTerminalEvent = true
		failureDelivered = true
		return true
	}
	finalizeStream := func() (*StreamingResult, error) {
		if stageFirstOutput && eventInProgress {
			// Dispatch the final staged event at EOF even without a trailing blank line.
			completeGuardedEvent(true)
		}
		synthesizeBareErrorFailure()
		if sawTerminalEvent && !sawFailedEvent {
			options.ClearDisconnect()
		}
		if !sawTerminalEvent && !options.ClientOutputStarted(clientOutputStarted) && !eventShouldFlush {
			return resultWithUsage(), options.Failover(
				upstreamRequestID,
				nil,
				"OpenAI stream ended before a terminal event",
			)
		}
		flushPending("Client disconnected during final flush, returning collected usage")
		if !sawTerminalEvent {
			if options.ClientOutputStarted(clientOutputStarted) && !clientDisconnected {
				options.RecordDisconnect(errors.New("stream ended before terminal event"), upstreamRequestID)
			}
			return resultWithUsage(), fmt.Errorf("stream usage incomplete: missing terminal event")
		}
		if sawFailedEvent {
			err := fmt.Errorf("upstream response failed: %s", failedMessage)
			return resultWithUsage(), options.WrapOpenAIUpstreamWarningIfCyber(resp.StatusCode, failedPayload, failedMessage, err)
		}
		return resultWithUsage(), nil
	}
	handleScanErr := func(scanErr error) (*StreamingResult, error, bool) {
		if scanErr == nil {
			return nil, nil, false
		}
		if errors.Is(scanErr, ErrOpenAIFirstOutputScannerLimit) && !firstOutputProgressObserved {
			options.Logf("SSE token exceeded guarded first-output limit: provider=%d limit=%d error=%v", options.ProviderID, OpenAIFirstOutputStageMaxBytes+OpenAIFirstOutputScannerFramingAllowance, scanErr)
			failoverErr := options.Failover(upstreamRequestID, nil,
				"OpenAI SSE line exceeds guarded first-output limit",
			)
			options.MarkSafeFailover(failoverErr)
			return resultWithUsage(), failoverErr, true
		}
		if errors.Is(scanErr, bufio.ErrTooLong) && stageFirstOutput && !firstOutputProgressObserved {
			options.Logf("SSE line too long before first output: provider=%d max_size=%d error=%v", options.ProviderID, maxLineSize, scanErr)
			failoverErr := options.Failover(upstreamRequestID, nil,
				"OpenAI SSE line exceeds guarded first-output limit",
			)
			options.MarkSafeFailover(failoverErr)
			return resultWithUsage(), failoverErr, true
		}
		if sawTerminalEvent {
			if !sawFailedEvent {
				options.ClearDisconnect()
				options.Logf("Upstream scan ended after terminal event: %v", scanErr)
			}
			result, err := finalizeStream()
			return result, err, true
		}
		// Cancellation often interrupts the upstream read. Return incomplete usage
		// without injecting an error event that violates the Responses schema.
		if errors.Is(scanErr, context.Canceled) || errors.Is(scanErr, context.DeadlineExceeded) {
			if eventShouldFlush {
				flushPending("Client disconnected during canceled stream flush, returning collected usage")
			}
			return resultWithUsage(), fmt.Errorf("stream usage incomplete: %w", scanErr), true
		}
		if errors.Is(scanErr, bufio.ErrTooLong) {
			options.Logf("SSE line too long: provider=%d max_size=%d error=%v", options.ProviderID, maxLineSize, scanErr)
			sendErrorEvent("response_too_large")
			return resultWithUsage(), scanErr, true
		}
		if !options.ClientOutputStarted(clientOutputStarted) && !eventShouldFlush {
			msg := "OpenAI stream disconnected before completion"
			if errText := strings.TrimSpace(scanErr.Error()); errText != "" {
				msg += ": " + errText
			}
			return resultWithUsage(), options.Failover(upstreamRequestID, nil, msg), true
		}
		// After disconnect, return collected usage with the upstream read error.
		if clientDisconnected {
			return resultWithUsage(), fmt.Errorf("stream usage incomplete after disconnect: %w", scanErr), true
		}
		options.RecordDisconnect(scanErr, upstreamRequestID)
		sendErrorEvent("stream_read_error")
		return resultWithUsage(), fmt.Errorf("stream read error: %w", scanErr), true
	}
	var lifecycleNormalizer wire.ResponseLifecycleNormalizer
	pendingLifecycleHeader := ""
	processSSELine := func(line string, queueDrained bool) {
		if streamEarlyErr != nil {
			return
		}
		if eventType, ok := wire.ExtractSSEEventLine(line); ok {
			pendingSSEEventType = eventType
			eventType = strings.TrimSpace(eventType)
			suppressCurrentEvent = codexFailureTerminal && (eventType == "error" || (sawBareError && !sawResponseFailed && eventType != "response.failed"))
			if wire.IsResponseLifecycleEvent(eventType) {
				// Delay lifecycle headers until data arrives so renaming and duplicate filtering cover the whole event.
				pendingLifecycleHeader = line
				return
			}
		}
		// Extract data from SSE line (supports both "data: " and "data:" formats)
		if data, ok := wire.ExtractSSEDataLine(line); ok {
			options.MarkTime(StreamTimeData)
			dataBytes := []byte(data)
			eventType := wire.EffectiveOpenAISSEEventType(dataBytes, pendingSSEEventType)
			options.Observe(dataBytes, eventType)
			normalizedData, normalizedType, duplicateTerminal := lifecycleNormalizer.Normalize(dataBytes, eventType)
			if !bytes.Equal(normalizedData, dataBytes) {
				dataBytes = normalizedData
				data = string(dataBytes)
				line = "data: " + data
			}
			eventType = normalizedType
			if codexFailureTerminal && sawBareError && !sawResponseFailed &&
				eventType == "response.completed" {
				// Only a normalized successful terminal can replace a pending bare error.
				sawBareError = false
				sawFailedEvent = false
				terminalFailurePending = false
				suppressCurrentEvent = false
				bareErrorPayload = nil
				bareErrorProviderSideEffectsPending = false
				failedMessage = ""
			}
			// Clear stale header suppression for failed terminals so the failure is delivered.
			suppressCurrentEvent = codexFailureTerminal && (eventType == "error" || (sawBareError && !sawResponseFailed && eventType != "response.failed"))
			// Use the normalized type for lifecycle events whose data omitted type.
			if options.OpenAIStreamEventIsTerminalWithType(data, eventType) {
				sawTerminalEvent = true
			}
			if responseID == "" {
				responseID = wire.ExtractOpenAIResponseIDFromJSONBytes(dataBytes)
			}
			forceFlushFailedEvent := false
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
				failedMessage = ExtractOpenAISSEErrorMessage(dataBytes)
				if failedMessage == "" {
					failedMessage = "Upstream response failed"
				}
				// Parse terminal usage before marking cyber_policy so the observation includes
				// consumed tokens instead of stale zero counts.
				hasObservedUsage = wire.ParseSSEUsageBytesWithType(dataBytes, eventType, usage) || hasObservedUsage
				if hit, code, msg := DetectOpenAICyberPolicy(dataBytes); hit {
					cyberHit = true
					options.MarkCyber(CyberObservation{
						Code: code,

						Message: msg,

						Body: options.TruncateString(string(dataBytes), 4096),

						UpstreamStatus: http.StatusOK,

						UpstreamInTok: usage.InputTokens,

						UpstreamOutTok: usage.OutputTokens,
					})
				}
				outputStarted := options.ClientOutputStarted(clientOutputStarted)
				if !outputStarted && !cyberHit {
					if compactErr := options.CompactFallback(dataBytes, failedMessage); compactErr != nil {
						sawFailedEvent = true
						streamEarlyErr = compactErr
						return
					}
				}
				if outputStarted && !cyberHit {
					if codexFailureTerminal && eventType == "error" {
						// OpenAI commonly follows a bare error with response.failed.
						// Defer provider health updates so the pair is applied once.
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
						sawFailedEvent = true
						streamEarlyErr = options.FailoverWithModel(upstreamRequestID, dataBytes, failedMessage, mappedModel, resp.Header)
						return
					}
					if !cyberHit && !sawBareError {
						if status, errType, errMsg, matched := options.ErrorRule(dataBytes, failedMessage); matched {
							sawFailedEvent = true
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
							streamEarlyErr = fmt.Errorf("upstream response failed: passthrough rule matched message=%s", errMsg)
							return
						}
					}
				}
				forceFlushFailedEvent = true
				sawFailedEvent = true
				failedPayload = append(failedPayload[:0], dataBytes...)
				terminalFailurePending = !codexFailureTerminal || eventType == "response.failed"
			}
			if normalizedData, normalized := wire.NormalizeCompletedImageGenerationStatus(dataBytes); normalized {
				dataBytes = normalizedData
				data = string(normalizedData)
				line = "data: " + data
			}
			imageCounter.AddSSEData(dataBytes)
			searchCounter += options.CountSearch(dataBytes, streamSearchSeen)

			// Correct Codex tool calls if needed (apply_patch -> edit, etc.)
			if correctedData, corrected := options.ToolCorrector.CorrectToolCallsInSSEBytes(dataBytes); corrected {
				dataBytes = correctedData
				data = string(correctedData)
				line = "data: " + data
				eventType = wire.EffectiveOpenAISSEEventType(dataBytes, eventType)
			}
			if imageOutput, ok := wire.ExtractImageGenerationOutputFromSSEData(dataBytes, streamSeenImages); ok {
				streamImageOutputs = append(streamImageOutputs, imageOutput)
			}
			streamDoneItems.Observe(dataBytes)
			if wire.ResponsesStreamEventMayContributeToOutput(eventType) {
				var streamEvent wire.ResponsesStreamEvent
				if err := json.Unmarshal(dataBytes, &streamEvent); err == nil {
					responseAccumulator.ProcessEvent(&streamEvent)
				}
			}
			if normalizedData, normalized := bridge.NormalizeResponsesStreamingTerminalOutput(dataBytes, responseAccumulator, streamDoneItems, streamImageOutputs); normalized {
				dataBytes = normalizedData
				data = string(normalizedData)
				line = "data: " + data
				eventType = wire.EffectiveOpenAISSEEventType(dataBytes, eventType)
			}
			restoredData, restoreErr := options.RestoreClientTools(dataBytes)
			if restoreErr != nil {
				streamEarlyErr = fmt.Errorf("restore Grok Responses client tool response: %w", restoreErr)
				return
			}
			restoredData, restoreErr = options.RestoreNamespace(restoredData)
			if restoreErr != nil {
				streamEarlyErr = fmt.Errorf("restore OpenAI namespace response: %w", restoreErr)
				return
			}
			restoredData = options.RestoreToolNames(restoredData, eventType)
			if !bytes.Equal(restoredData, dataBytes) {
				dataBytes = restoredData
				data = string(restoredData)
				line = "data: " + data
				eventType = wire.EffectiveOpenAISSEEventType(dataBytes, eventType)
			}
			if sanitizedData, sanitized := SanitizeOpenAIResponseFailedEventForClient(
				dataBytes,
				eventType,
				options.ClientOutputStarted(clientOutputStarted),
			); sanitized {
				dataBytes = sanitizedData
				data = string(sanitizedData)
				line = "data: " + data
			}
			// Replace model in response if needed.
			// Fast path: most events do not contain model field values.
			if needModelReplace && mappedModel != "" && strings.Contains(line, mappedModel) {
				line = wire.ReplaceModelInSSELine(line, mappedModel, originalModel)
			}
			startsClientOutput := forceFlushFailedEvent || OpenAIStreamDataStartsClientOutput(data, eventType)
			startsVisibleOutput := wire.StreamDataStartsVisibleOutput(data, eventType)
			if startsVisibleOutput && firstSemanticOutput == nil {
				elapsed := time.Since(startTime)
				firstSemanticOutput = &elapsed
			}
			startsTTFTOutput := options.OpenAIStreamDataStartsTTFT(data, eventType, forceFlushFailedEvent, ttftMode)
			if stageFirstOutput {
				eventStartsClientOutput = eventStartsClientOutput || startsClientOutput
				eventStartsVisibleOutput = eventStartsVisibleOutput || startsVisibleOutput
				eventStartsTTFTOutput = eventStartsTTFTOutput || startsTTFTOutput
				if startsClientOutput {
					firstOutputScanGuard.Store(false)
				}
			}
			if startsClientOutput && !wire.OpenAIStreamEventTypeIsTerminal(eventType) {
				responsesSemanticOutputSeen = true
			}
			// OpenAI Responses streams that terminate with an empty
			// response.completed (no output, no usage, no error, nothing sent
			// to the client) are silent upstream refusals: fail over instead of
			// recording a successful 0/0 usage turn (issue #5009).
			if options.NativeOpenAI &&
				(eventType == "response.completed" || eventType == "response.done") &&
				!sawFailedEvent && !responsesSemanticOutputSeen && !clientOutputStarted &&
				wire.OpenAIResponsesCompletedEventIsEmpty(dataBytes, usage) {
				sawTerminalEvent = true
				streamEarlyErr = options.EmptyCompleted(upstreamRequestID)
				return
			}

			if pendingLifecycleHeader != "" {
				header := pendingLifecycleHeader
				if eventType != strings.TrimSpace(pendingSSEEventType) {
					header = "event: " + eventType
				}
				line = header + "\n" + line
				pendingLifecycleHeader = ""
			}
			suppressCurrentEvent = suppressCurrentEvent || duplicateTerminal
			// Skip downstream writes after disconnect while continuing to drain upstream.
			if !clientDisconnected && !failureDelivered && !suppressCurrentEvent {
				shouldFlush := queueDrained && (clientOutputStarted || startsClientOutput)
				if firstTokenMs == nil && startsTTFTOutput {
					// Flush the first token promptly to avoid adding buffering latency to TTFT.
					shouldFlush = true
				}
				eventShouldFlush = eventShouldFlush || shouldFlush
				if _, err := writePendingString(line); err != nil {
					handlePendingWriteError(err)
				} else if _, err := writePendingString("\n"); err != nil {
					handlePendingWriteError(err)
				} else {
					eventInProgress = true
				}
			}

			// Record first token time
			if startsVisibleOutput {
				options.MarkTime(StreamTimeVisible)
			}
			if !guardFirstOutput && firstTokenMs == nil && startsTTFTOutput {
				ms := int(time.Since(startTime).Milliseconds())
				firstTokenMs = &ms
				stopFirstOutputTimer()
			}
			hasObservedUsage = wire.ParseSSEUsageBytesWithType(dataBytes, eventType, usage) || hasObservedUsage
			return
		}

		// A blank line dispatches a guarded event from the attempt-local stage.
		if stageFirstOutput && line == "" {
			pendingLifecycleHeader = ""
			pendingSSEEventType = ""
			if suppressCurrentEvent {
				suppressCurrentEvent = false
				terminalFailurePending = false
				eventInProgress = false
				eventStartsClientOutput = false
				eventStartsVisibleOutput = false
				eventShouldFlush = false
				return
			}
			if failureDelivered {
				terminalFailurePending = false
				eventInProgress = false
				eventStartsClientOutput = false
				eventStartsVisibleOutput = false
				eventShouldFlush = false
				return
			}
			if !clientDisconnected {
				if _, err := writePendingString("\n"); err != nil {
					handlePendingWriteError(err)
				}
			}
			if streamEarlyErr == nil {
				completeGuardedEvent(queueDrained)
			}
			if terminalFailurePending && streamEarlyErr == nil {
				terminalFailurePending = false
				failureDelivered = true
			}
			return
		}
		// Non-guarded streams retain upstream's event-boundary flushing: a keepalive
		// or queue-drain flush must never split an open SSE event.
		shouldFlush := false
		if line == "" {
			pendingLifecycleHeader = ""
			pendingSSEEventType = ""
			if suppressCurrentEvent {
				suppressCurrentEvent = false
				terminalFailurePending = false
				eventInProgress = false
				eventShouldFlush = false
				return
			}
			shouldFlush = eventShouldFlush || (queueDrained && clientOutputStarted)
			eventShouldFlush = false
			if failureDelivered {
				terminalFailurePending = false
			}
		}
		if !clientDisconnected && !failureDelivered && !suppressCurrentEvent {
			if _, err := writePendingString(line); err != nil {
				handlePendingWriteError(err)
			} else if _, err := writePendingString("\n"); err != nil {
				handlePendingWriteError(err)
			} else {
				eventInProgress = line != ""
				if shouldFlush {
					if err := flushBuffered(); err != nil {
						clientDisconnected = true
						options.Logf("Client disconnected during streaming flush, continuing to drain upstream for billing")
					} else {
						clientOutputStarted = true
						lastDownstreamWriteAt = time.Now()
					}
				}
			}
			if line == "" && terminalFailurePending && streamEarlyErr == nil {
				terminalFailurePending = false
				failureDelivered = true
			}
		}
	}

	// Use synchronous scanning when no timeout or keepalive requires concurrent work.
	if streamInterval <= 0 && keepaliveInterval <= 0 && firstOutputTimeout <= 0 {
		defer httpclient.PutSSEScannerBuf64K(scanBuf)
		for documentScanner.Scan() {
			processSSELine(documentScanner.Text(), true)
			if streamEarlyErr != nil {
				return resultWithUsage(), streamEarlyErr
			}
		}
		if result, err, done := handleScanErr(documentScanner.Err()); done {
			return result, err
		}
		return finalizeStream()
	}

	type scanEvent struct {
		line      string
		err       error
		processed chan struct{}
	}
	// Read asynchronously so blocked reads do not stall timers. The first-output timeout
	// selects a one-entry queue; otherwise the queue holds 16 entries. While staging is
	// active, each token waits for processing before the reader advances, bounding read-ahead.
	events := make(chan scanEvent, OpenAIFirstOutputEventQueueSize(guardFirstOutput))
	done := make(chan struct{})
	sendEvent := func(ev scanEvent) bool {
		if firstOutputScanGuard.Load() {
			ev.processed = make(chan struct{})
		}
		select {
		case events <- ev:
		case <-done:
			return false
		}
		if ev.processed == nil {
			return true
		}
		select {
		case <-ev.processed:
			return true
		case <-done:
			return false
		}
	}
	markEventProcessed := func(ev scanEvent) {
		if ev.processed != nil {
			close(ev.processed)
		}
	}
	var lastReadAt int64
	atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
	go func(scanBuf *httpclient.SSEScannerBuf64K) {
		defer httpclient.PutSSEScannerBuf64K(scanBuf)
		defer close(events)
		for documentScanner.Scan() {
			atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
			if !sendEvent(scanEvent{line: documentScanner.Text()}) {
				return
			}
		}
		if err := documentScanner.Err(); err != nil {
			_ = sendEvent(scanEvent{err: err})
		}
	}(scanBuf)
	defer close(done)

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				if stageFirstOutput && eventInProgress {
					// Dispatch the last staged event at EOF without adding a synthetic blank line.
					completeGuardedEvent(true)
				}
				return finalizeStream()
			}
			if result, err, done := handleScanErr(ev.err); done {
				markEventProcessed(ev)
				return result, err
			}
			processSSELine(ev.line, len(events) == 0)
			markEventProcessed(ev)
			if streamEarlyErr != nil {
				return resultWithUsage(), streamEarlyErr
			}

		case <-intervalCh:
			if failureDelivered {
				return resultWithUsage(), fmt.Errorf("upstream response failed: %s", failedMessage)
			}
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if time.Since(lastRead) < streamInterval {
				continue
			}
			if codexFailureTerminal && sawBareError && !sawResponseFailed {
				_ = resp.Body.Close()
				return finalizeStream()
			}
			if clientDisconnected {
				return resultWithUsage(), fmt.Errorf("stream usage incomplete after timeout")
			}
			options.Logf("Stream data interval timeout: provider=%d model=%s interval=%s", options.ProviderID, originalModel, streamInterval)
			// Let the caller apply provider policy for the stream timeout.
			options.StreamTimeout(originalModel)
			// Grok idle policy applies a short cooldown; failover is available before output.
			// After output, report stream_timeout while retaining the delivered SSE.
			if options.GrokIdlePolicy {
				options.IdleCooldown()
				if !options.ClientOutputStarted(clientOutputStarted) && !eventShouldFlush {
					_ = resp.Body.Close()
					return resultWithUsage(), options.IdleFailover(streamInterval)
				}
			}
			if synthesizeBareErrorFailure() {
				flushPending("Client disconnected during bare error timeout flush, returning collected usage")
				return resultWithUsage(), fmt.Errorf("upstream response failed: %s", failedMessage)
			}
			sendErrorEvent("stream_timeout")
			return resultWithUsage(), fmt.Errorf("stream data interval timeout")

		case <-firstOutputCh:
			if firstOutputProgressObserved {
				stopFirstOutputTimer()
				continue
			}
			if synthesizeBareErrorFailure() {
				flushPending("Client disconnected during bare error first-output timeout flush, returning collected usage")
				_ = resp.Body.Close()
				for ev := range events {
					markEventProcessed(ev)
				}
				return resultWithUsage(), fmt.Errorf("upstream response failed: %s", failedMessage)
			}
			_ = resp.Body.Close()
			for ev := range events {
				markEventProcessed(ev)
			}
			return resultWithUsage(), options.FirstOutputError(startTime, originalModel, reasoningEffort,
				firstOutputTimeout, "semantic_output", resp.Header,
			)

		case <-keepaliveCh:
			if clientDisconnected {
				continue
			}
			if eventInProgress {
				continue
			}
			if time.Since(lastDownstreamWriteAt) < keepaliveInterval {
				continue
			}
			if stageFirstOutput {
				// Write stable SSE comments outside the attempt buffer while retaining provider
				// headers until semantic output starts.
				n, err := w.Write([]byte(":\n\n"))
				options.KeepaliveBytes(n)
				if err != nil {
					clientDisconnected = true
					options.Logf("Client disconnected during streaming, continuing to drain upstream for billing")
					continue
				}
				flusher.Flush()
				lastDownstreamWriteAt = time.Now()
				continue
			}
			if _, err := writePendingString(":\n\n"); err != nil {
				clientDisconnected = true
				options.Logf("Client disconnected during streaming, continuing to drain upstream for billing")
				continue
			}
			if err := flushBuffered(); err != nil {
				clientDisconnected = true
				options.Logf("Client disconnected during keepalive flush, continuing to drain upstream for billing")
			} else {
				lastDownstreamWriteAt = time.Now()
			}
		}
	}
}
