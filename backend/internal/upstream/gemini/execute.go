package gemini

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	geminiwire "github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

const (
	MessagesResponse ResponseMode = iota
	NativeResponse
	OpenAIResponse
)

type ResponseMode uint8

type Target struct {
	ProviderID                          int64
	Model                               string
	Mode                                ResponseMode
	Exchange                            ExchangeOptions
	Response                            ResponseOptions
	StartedAt                           time.Time
	UpstreamStream, OAuth, IncludeUsage bool
	OpenAIProtocol                      OpenAICompatProtocol
	ClientTools                         bridge.ResponsesClientToolMapping
	Enter                               func() (func(), error)
	// BeforeResponse handles HTTP and provider errors; stop ends response processing.
	BeforeResponse func(context.Context, *http.Response, string) (stop bool, err error)
}

type Executor struct{}

func (t *Target) TargetID() int64 {
	if t == nil {
		return 0
	}
	return t.ProviderID
}
func (t *Target) String() string   { return fmt.Sprintf("gemini target provider=%d", t.TargetID()) }
func (t *Target) GoString() string { return t.String() }

// @project-doc docs/interfaces/gemini_upstream.md#gemini_native_execution
func (Executor) Execute(ctx context.Context, input upstream.AttemptInput, sink upstream.OutputSink) (result upstream.AttemptResult, failure error) {
	target, ok := input.Target.(*Target)
	if !ok || target == nil || target.Exchange.Build == nil || target.Exchange.Do == nil {
		return result, errors.New("gemini execution target is not configured")
	}
	switch input.Protocol {
	case protocol.ProtocolAnthropicMessages, protocol.ProtocolGeminiGenerateContent, protocol.ProtocolOpenAIChatCompletions, protocol.ProtocolOpenAIResponses:
	default:
		return result, errors.New("unsupported gemini client protocol")
	}
	if target.Enter != nil {
		done, err := target.Enter()
		if err != nil {
			return result, err
		}
		defer done()
	}
	started := target.StartedAt
	if started.IsZero() {
		started = time.Now()
	}
	result = upstream.AttemptResult{Model: input.ResponseModel, UpstreamModel: target.Model, Stream: input.Stream}
	defer func() {
		result.Duration = time.Since(started)
		if failure != nil {
			result.FailureClass = "upstream"
			result.Cancelled = ctx.Err() != nil || errors.Is(failure, context.Canceled) || errors.Is(failure, context.DeadlineExceeded)
		}
	}()
	var exchange ExchangeResult
	var err error
	switch target.Mode {
	case MessagesResponse:
		exchange, err = ExchangeMessages(ctx, target.Exchange)
	case NativeResponse:
		exchange, err = ExchangeNative(ctx, target.Exchange)
	case OpenAIResponse:
		exchange, err = ExchangeOpenAI(ctx, target.Exchange)
	default:
		return result, errors.New("unsupported gemini response mode")
	}
	if err != nil {
		return result, err
	}
	if exchange.EstimatedTokens != nil {
		result.EstimatedTokenCount = exchange.EstimatedTokens
		// A failed countTokens request can return an estimate, separate from billable usage.
		output := upstream.NewOutputContext(sink)
		output.JSON(http.StatusOK, map[string]any{"totalTokens": *exchange.EstimatedTokens})
		result.Stream = false
		result.ClientDisconnect = output.Err() != nil
		return result, nil
	}
	resp := exchange.Response
	if resp == nil || resp.Body == nil {
		return result, errors.New("gemini upstream returned empty response")
	}
	defer func() { _ = resp.Body.Close() }()
	result.RequestID = resp.Header.Get(exchange.RequestIDHeader)
	if result.RequestID == "" {
		result.RequestID = resp.Header.Get("x-goog-request-id")
	}
	result.UpstreamHeaders = resp.Header
	if target.BeforeResponse != nil {
		stop, err := target.BeforeResponse(ctx, resp, exchange.RequestIDHeader)
		if stop || err != nil {
			return result, err
		}
	} else if resp.StatusCode >= 400 {
		return result, fmt.Errorf("gemini upstream status %d", resp.StatusCode)
	}
	output := upstream.NewOutputContext(sink)
	if result.RequestID != "" {
		output.Header("x-request-id", result.RequestID)
	}
	response := target.Response
	var modelObserver protocol.ResponseModelObserver
	defer func() { result.UpstreamResponseModel = modelObserver.Model() }()
	priorRaw, priorState := response.ObserveRaw, response.ObserveState
	response.ObserveRaw = func(payload []byte) {
		if priorRaw != nil {
			priorRaw(payload)
		}
		modelObserver.ObserveGemini(payload)
		observed := geminiwire.ObservePayload(payload)
		result.HasUsage = result.HasUsage || observed.HasUsage
		result.Served = result.Served || observed.Semantic
		if observed.Semantic && result.FirstSemanticOutput == nil {
			elapsed := time.Since(started)
			result.FirstSemanticOutput = &elapsed
		}
		if observed.HasUsage {
			if usage := ExtractGeminiUsage(payload); usage != nil {
				result.Usage = *usage
			}
		}
		if images := CountGeminiInlineImageOutputs(payload); images > result.ObservedImages {
			result.ObservedImages = images
		}
	}
	response.ObserveState = func(usage *upstream.TokenUsage, first *int) {
		if priorState != nil {
			priorState(usage, first)
		}
		if usage != nil {
			result.Usage = *usage
		}
		if first != nil {
			result.FirstTokenMs = first
		}
	}
	adapter := ResponseAdapter{Options: response}
	var usage *upstream.TokenUsage
	var first *int
	switch target.Mode {
	case MessagesResponse:
		usage, first, err = adapter.CompleteMessages(output, resp, started, input.ResponseModel, input.Stream, target.UpstreamStream)
	case NativeResponse:
		usage, first, err = adapter.CompleteNative(output, resp, started, input.Stream, target.UpstreamStream, target.OAuth)
	case OpenAIResponse:
		usage, first, err = adapter.CompleteOpenAI(output, resp, started, input.ResponseModel, input.Stream, target.UpstreamStream, target.OAuth, target.IncludeUsage, target.OpenAIProtocol, target.ClientTools)
	}
	if usage != nil {
		result.Usage = *usage
	}
	if first != nil {
		result.FirstTokenMs = first
	}
	result.ClientDisconnect = output.Err() != nil
	return result, err
}
