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

// A Gemini attempt can return explicit zero usage, semantic output, and an
// observed upstream model as independent facts. Client response identity
// remains the requested response model, while the selected target model
// identifies the provider request and the payload observer records what the
// server reports.
//
// The executor binds response observers to one synchronous attempt and closes
// its final response before lifecycle release. Usage callbacks replace
// counters while presence and served flags accumulate across payloads;
// inline-image observation uses the maximum count from one payload. A native
// count fallback emits an estimate on its own path and leaves upstream usage
// and semantic-output observations unset.

const (
	// Response mode values select the exchange and response adapter used for
	// Messages, native Gemini, or OpenAI-compatible client output.
	MessagesResponse ResponseMode = iota // Anthropic Messages conversion
	NativeResponse                       // Gemini native output
	OpenAIResponse                       // OpenAI-compatible output
)

// ResponseMode selects one Gemini exchange and its matching client response
// adapter.
type ResponseMode uint8

// Target supplies one provider attempt and its Gemini response conversion
// callbacks.
type Target struct {
	ProviderID int64           // selected provider audit identifier
	Model      string          // selected upstream model identifier
	Mode       ResponseMode    // response exchange and adapter selection
	Exchange   ExchangeOptions // provider-local exchange callback settings
	Response   ResponseOptions // response read and output settings
	// StartedAt selects the duration and first-output origin. A zero value starts
	// timing after successful attempt registration rather than using request
	// arrival.
	StartedAt time.Time // caller request timing origin
	// UpstreamStream describes server framing; OAuth selects unwrapping;
	// IncludeUsage selects Chat usage chunks.
	UpstreamStream, OAuth, IncludeUsage bool
	OpenAIProtocol                      OpenAICompatProtocol // OpenAI client response protocol
	// ClientTools shares its maps with response restoration. Callers keep those
	// maps unchanged until this synchronous attempt finishes converting its output.
	ClientTools bridge.ResponsesClientToolMapping
	// Enter optionally registers this attempt and returns a nonnil release
	// callback. Release runs after the response body closes or a later phase
	// fails.
	// optional attempt lifecycle registration
	Enter func() (func(), error)
	// BeforeResponse optionally consumes policy or provider errors before
	// conversion. Returning stop or an error ends adapter handling while the
	// executor still closes the response body.
	BeforeResponse func(context.Context, *http.Response, string) (stop bool, err error)
}

// Executor performs one Gemini provider exchange and observes synchronous
// protocol output.
type Executor struct{}

// TargetID returns the provider identifier used by execution observers and
// diagnostics. A nil target returns zero, so callers can format an absent
// target without dereferencing its provider metadata.
func (t *Target) TargetID() int64 {
	if t == nil {
		return 0
	}
	return t.ProviderID
}

// String formats a provider label from TargetID for diagnostic output. A nil
// target uses provider zero; authentication, project, and callback data remain
// outside this formatted label.
func (t *Target) String() string { return fmt.Sprintf("gemini target provider=%d", t.TargetID()) }

// GoString delegates to the same provider label used by String for Go-syntax
// diagnostics. A nil target uses provider zero, while the label contains the
// audit identifier alone.
func (t *Target) GoString() string { return t.String() }

// Execute exchanges one configured Gemini target and returns its observed
// attempt result. The sink receives synchronous protocol output, the final
// response is closed here, and an optional lifecycle entry remains held
// through that closing.
//
// Response mode chooses both exchange and output adaptation. Raw observations
// accumulate usage presence, semantic output, model identity, and the largest
// per-payload image count, while later usage values replace earlier counters.
// An estimated token result emits a native count response and remains separate
// from billed usage. Deferred timing and cancellation annotate failures after
// the attempt timing origin has been selected.
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
		// Emit the count estimate on its dedicated response path so fallback tokens
		// never become observed server usage or semantic generation output in the
		// attempt result.
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
	// Chain the caller observation before attempt accounting. Record presence
	// independently of numerical counts so explicit zero usage and later semantic
	// output remain distinguishable.
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
	// Keep adapter state snapshots alongside raw observations because conversion
	// paths can report final merged usage or their legacy first-token timing
	// after payload processing.
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
