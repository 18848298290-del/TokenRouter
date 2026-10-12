package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// CCResponseOptions supplies per-read tier observation and response-body limits.
type CCResponseOptions struct {
	MaxLineSize               int
	ObserveChunk, ObserveJSON func([]byte)
	ServiceTier               func() string
	ReadBody                  func(io.Reader) ([]byte, error)
	BodyLimitError            error
	WriteError                func(int, string, string)
}

// CCStreamScanState is the state returned by ScanCCStream.
type CCStreamScanState struct {
	// Usage holds the latest include_usage chunk; callers may replace it with terminal usage during finalization.
	Usage wire.ForwardUsage
	// FirstTokenMs is arrival latency in milliseconds for the first output chunk, excluding usage-only chunks.
	FirstTokenMs *int
	// ServiceTier is the observed upstream processing tier.
	ServiceTier string
	// SawDone reports whether upstream sent the [DONE] sentinel.
	SawDone bool
	// Err is the scanner error, including context cancellation.
	// Callers skip finalization on error so truncated streams cannot synthesize a successful ending.
	Err error
}

// ScanCCStream emits parsed Chat chunks in order and retains the latest usage, tier and scanner error.
func ScanCCStream(resp *http.Response, options CCResponseOptions, logPrefix, requestID string, startTime time.Time, emit func(*wire.ChatCompletionsChunk)) CCStreamScanState {
	var st CCStreamScanState

	scanner := NewCompatSSEScanner(resp.Body, options.MaxLineSize)
	for scanner.Scan() {
		line := scanner.Text()
		payload, ok := wire.ExtractSSEDataLine(line)
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			st.SawDone = true
			break
		}
		options.ObserveChunk([]byte(payload))
		// Observe the echoed model and tier for billing. Chat chunks have no event type
		// and therefore use the untyped-payload tier observation path.

		if u := wire.ExtractCCStreamUsage(payload); u != nil {
			st.Usage = *u
		}

		var chunk wire.ChatCompletionsChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			logger.L().Warn(logPrefix+": failed to parse chat stream chunk",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
			// Skip malformed chunks so later events remain readable. Responses conversion
			// validates assembled tool arguments separately at finalization.
			continue
		}
		if st.FirstTokenMs == nil && !wire.IsOpenAIChatUsageOnlyStreamChunk(payload) && wire.ChatChunkStartsResponsesOutput(&chunk) {
			ms := int(time.Since(startTime).Milliseconds())
			st.FirstTokenMs = &ms
		}
		emit(&chunk)
	}
	st.ServiceTier = options.ServiceTier()

	if err := scanner.Err(); err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			logger.L().Warn(logPrefix+": stream read error",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
		}
		st.Err = err
	}
	return st
}

// LogCCStreamMissingDoneSentinel records a debug diagnostic for a missing [DONE] marker.
func LogCCStreamMissingDoneSentinel(logPrefix, requestID string) {
	logger.L().Debug(logPrefix+": upstream stream ended without done sentinel",
		zap.String("request_id", requestID),
	)
}

// ReadCCJSONResponse reads and parses Chat JSON, observes its model and tier, and extracts usage.
func ReadCCJSONResponse(resp *http.Response, options CCResponseOptions) (*wire.ChatCompletionsResponse, wire.ForwardUsage, error) {
	respBody, err := options.ReadBody(resp.Body)
	if err != nil {
		if !errors.Is(err, options.BodyLimitError) {
			options.WriteError(http.StatusBadGateway, "api_error", "Failed to read upstream response")
		}
		return nil, wire.ForwardUsage{}, fmt.Errorf("read upstream body: %w", err)
	}

	var ccResp wire.ChatCompletionsResponse
	if err := json.Unmarshal(respBody, &ccResp); err != nil {
		options.WriteError(http.StatusBadGateway, "api_error", "Failed to parse upstream response")
		return nil, wire.ForwardUsage{}, fmt.Errorf("parse chat completions response: %w", err)
	}
	options.ObserveJSON(respBody)
	// Observe model and service_tier as an untyped Chat JSON payload for billing.

	usage := wire.ForwardUsage{}
	if parsed, ok := wire.ExtractOpenAIUsageFromJSONBytes(respBody); ok {
		usage = parsed
	}
	return &ccResp, usage, nil
}
