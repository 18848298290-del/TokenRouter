package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	anthropicwire "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	geminiwire "github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Antigravity execution keeps upstream observation active while adapting one
// exchange into Claude, Gemini, Chat, or Responses output. Raw usage fields
// accumulate across partial payloads, so a later observation containing only
// output tokens does not erase earlier input tokens.
//
// The executor holds optional lifecycle registration through final body
// closing and selects output behavior independently from the accepted client
// protocol. Successful adapter usage can replace raw accumulation, while a
// read failure retains accumulated usage already seen. Semantic output,
// first-content timing, and retry commitment remain separate observations: any
// nonempty emitted bytes can close the retry window, including keepalive
// output that carries no model content.

// Response modes choose the platform adapter representation independently from
// the client protocol admitted by Executor.
const (
	ModeClaudeResponse    ResponseMode = iota // Messages response conversion mode
	ModeGeminiResponse                        // Gemini response forwarding mode
	ModeChatResponse                          // Chat Completions compatibility mode
	ModeResponsesResponse                     // Responses API compatibility mode
)

// ResponseMode selects the client response representation used by an
// Antigravity execution target.
type ResponseMode uint8

// Target supplies provider identity, exchange ownership, output selection, and
// response policy for one attempt.
type Target struct {
	// OutputError optionally observes an adapter failure after response handling;
	// target validation, lifecycle, exchange, and pre-response failures return
	// earlier.
	OutputError func(error)
	ProviderID  int64        // selected execution provider identifier
	Model       string       // selected upstream model identity
	Mode        ResponseMode // selected response adapter representation
	// StartedAt supplies the elapsed-time origin; zero selects the current time
	// after optional lifecycle admission and before upstream exchange.
	StartedAt time.Time
	// IncludeUsage selects Chat stream usage output; other response modes use
	// their own adapter usage behavior for collected observations.
	IncludeUsage bool
	// ClientTools supplies Responses return-name mappings shared with conversion;
	// callers keep the mapping stable through this attempt output.
	ClientTools bridge.ResponsesClientToolMapping
	// Exchange returns the final upstream response using ctx; successful results
	// require a nonnil body that Executor reads and closes. The callback cleans
	// up any response returned together with an error.
	Exchange func(context.Context) (*http.Response, error)
	// BeforeResponse optionally inspects the owned response before adaptation;
	// stop or error returns immediately and deferred body/lifecycle cleanup still
	// runs.
	BeforeResponse func(context.Context, *http.Response) (bool, error)
	// Response supplies adapter callbacks and limits copied for the attempt;
	// ObserveRaw is chained with executor model and usage observation.
	Response ResponseOptions
	// Enter optionally registers lifecycle ownership and returns a nonnil release
	// callback on success, held until the final response body closes.
	Enter func() (func(), error)
}

// Executor performs one configured Antigravity exchange and synchronously
// adapts its response output.
type Executor struct{}

// observedSink supplements output-event meaning and retry commitment before
// delegating to the underlying sink.
type observedSink struct {
	upstream.OutputSink      // borrowed synchronous output event destination
	nonStream           bool // buffered JSON observation selection
}

// TargetID returns the selected provider identifier, using zero for a nil
// target. That nil-safe result also supports String and GoString when
// diagnostic formatting encounters an absent target.
func (t *Target) TargetID() int64 {
	if t == nil {
		return 0
	}
	return t.ProviderID
}

// String formats the provider identifier using TargetID and its nil-safe
// behavior.
func (t *Target) String() string { return fmt.Sprintf("antigravity target provider=%d", t.TargetID()) }

// GoString returns the same provider-identifying diagnostic spelling as
// String.
func (t *Target) GoString() string { return t.String() }

// Execute performs one supported client attempt using a configured Target and
// synchronous sink. A successful exchange transfers response-body cleanup to
// the executor; optional Enter supplies a release callback held through body
// closing, and BeforeResponse can stop adaptation.
//
// Raw model and usage observation is chained ahead of adapter output. Present
// nonnull usage fields replace their earlier values while missing fields
// retain prior observations. Final adapter usage replaces that accumulation on
// success or when no raw usage was seen; read failures retain previously
// accumulated usage. Adapter semantic/timing/disconnect observations join the
// attempt result, and failure cancellation classification uses ctx. Invalid
// target/protocol and lifecycle-admission failures return before duration
// tracking starts.
// @project-doc docs/interfaces/antigravity_upstream.md#antigravity_native_execution
func (Executor) Execute(ctx context.Context, input upstream.AttemptInput, sink upstream.OutputSink) (result upstream.AttemptResult, failure error) {
	target, ok := input.Target.(*Target)
	if !ok || target == nil || target.Exchange == nil {
		return result, errors.New("antigravity execution target is not configured")
	}
	switch input.Protocol {
	case protocol.ProtocolAnthropicMessages, protocol.ProtocolGeminiGenerateContent, protocol.ProtocolOpenAIChatCompletions, protocol.ProtocolOpenAIResponses:
	default:
		return result, errors.New("unsupported antigravity client protocol")
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
	resp, err := target.Exchange(ctx)
	if err != nil {
		return result, err
	}
	if resp == nil || resp.Body == nil {
		return result, errors.New("antigravity upstream returned empty response")
	}
	defer func() { _ = resp.Body.Close() }()
	result.RequestID = resp.Header.Get("x-request-id")
	result.UpstreamHeaders = resp.Header
	if target.BeforeResponse != nil {
		stop, err := target.BeforeResponse(ctx, resp)
		if stop || err != nil {
			return result, err
		}
	}
	options := target.Response
	var modelObserver protocol.ResponseModelObserver
	defer func() { result.UpstreamResponseModel = modelObserver.Model() }()
	priorRaw := options.ObserveRaw
	// Accumulate only present nonnull Gemini usage fields across raw payloads so
	// sparse later updates can retain earlier token observations during read failure.
	observedUsage := make(map[string]json.RawMessage)
	options.ObserveRaw = func(raw []byte) {
		if priorRaw != nil {
			priorRaw(raw)
		}
		data := strings.TrimSpace(string(raw))
		if after, ok0 := strings.CutPrefix(data, "data:"); ok0 {
			data = strings.TrimSpace(after)
		}
		body, _ := (&ResponseAdapter{}).UnwrapV1InternalResponse([]byte(data))
		modelObserver.ObserveGemini(body)
		observed := geminiwire.ObservePayload(body)
		hasUsage, served := observed.HasUsage, observed.Semantic
		if hasUsage {
			for _, field := range []string{"promptTokenCount", "candidatesTokenCount", "cachedContentTokenCount", "thoughtsTokenCount", "totalTokenCount", "candidatesTokensDetails"} {
				value := gjson.GetBytes(body, "usageMetadata."+field)
				if value.Exists() && value.Type != gjson.Null {
					observedUsage[field] = json.RawMessage(value.Raw)
				}
			}
			// Project accumulated fields together because raw chunks may expose only
			// part of usage; later null or missing fields do not erase prior data. A
			// discarded encoding error leaves counters unchanged, while usage presence
			// still accumulates from the raw observation.
			payload, _ := json.Marshal(map[string]any{"usageMetadata": observedUsage})
			if usage := bridge.NativeUsageProjection(bridge.NativeExtractGeminiUsage(payload)); usage != nil {
				result.Usage = *usage
			}
		}
		result.HasUsage = result.HasUsage || hasUsage
		result.Served = result.Served || served
		if served && result.FirstSemanticOutput == nil {
			elapsed := time.Since(started)
			result.FirstSemanticOutput = &elapsed
		}
	}
	output := upstream.NewOutputContext(&observedSink{OutputSink: sink, nonStream: !input.Stream})
	if result.RequestID != "" {
		output.Header("x-request-id", result.RequestID)
	}
	adapter := &ResponseAdapter{Options: options}
	var stream *StreamResult
	switch target.Mode {
	case ModeClaudeResponse:
		if input.Stream {
			stream, err = adapter.HandleClaudeStreamingResponse(output, resp, started, input.ResponseModel)
		} else {
			stream, err = adapter.HandleClaudeStreamToNonStreaming(output, resp, started, input.ResponseModel)
		}
	case ModeGeminiResponse:
		if input.Stream {
			stream, err = adapter.HandleGeminiStreamingResponse(output, resp, started)
		} else {
			stream, err = adapter.HandleGeminiStreamToNonStreaming(output, resp, started)
		}
	case ModeChatResponse:
		if input.Stream {
			stream, err = adapter.HandleChatCompletionsStreamingFromAntigravity(output, resp, started, input.ResponseModel, target.IncludeUsage)
		} else {
			stream, err = adapter.HandleChatCompletionsNonStreamingFromAntigravity(output, resp, started, input.ResponseModel)
		}
	case ModeResponsesResponse:
		if input.Stream {
			stream, err = adapter.HandleResponsesStreamingFromAntigravity(output, resp, started, input.ResponseModel, target.ClientTools)
		} else {
			stream, err = adapter.HandleResponsesNonStreamingFromAntigravity(output, resp, started, input.ResponseModel, target.ClientTools)
		}
	default:
		return result, errors.New("unsupported antigravity response mode")
	}
	if stream != nil {
		// Prefer final adapter usage after success, but retain accumulated raw
		// fields on read failure when any usage was observed before that failure.
		if stream.Usage != nil && (err == nil || !result.HasUsage) {
			result.Usage = *stream.Usage
		}
		result.FirstTokenMs = stream.FirstTokenMs
		result.ClientDisconnect = stream.ClientDisconnect
	}
	if err != nil && target.OutputError != nil {
		target.OutputError(err)
	}
	return result, err
}

// InitialOutput delegates an optional underlying output-head snapshot. Sinks
// without that extension return a zero head; delegated header storage follows
// the underlying sink ownership and copying rules.
func (s *observedSink) InitialOutput() upstream.OutputHead {
	if source, ok := s.OutputSink.(interface{ InitialOutput() upstream.OutputHead }); ok {
		return source.InitialOutput()
	}
	return upstream.OutputHead{}
}

// Emit supplements event meaning before passing the event to its underlying
// synchronous sink. Buffered nonempty data is checked for client JSON content
// and valid-JSON termination; streamed line checks combine compatible,
// Messages, and Gemini observations.
//
// Existing semantic and terminal flags remain set as observations accumulate
// across lines. Any nonempty data marks CommitForRetry even when it carries
// only keepalive or framing text, so retry closure is independent from model
// content and first-content timing. The underlying sink decides whether
// writing succeeds, and its error returns unchanged.
func (s *observedSink) Emit(event upstream.OutputEvent) error {
	if s.nonStream && len(event.Data) > 0 {
		event.Semantic = event.Semantic || bridge.CompatJSONHasContent(event.Data) || anthropicwire.ObserveMessage(string(event.Data)).Semantic || geminiwire.ObservePayload(event.Data).Semantic
		event.Terminal = event.Terminal || json.Valid(event.Data)
	}

	for line := range strings.SplitSeq(string(event.Data), "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			line = strings.TrimSpace(after)
		}
		semantic, terminal := bridge.CompatOutputMeaning([]byte("data: " + line + "\n\n"))
		a := anthropicwire.ObserveEvent(line)
		g := geminiwire.ObservePayload([]byte(line))
		event.Semantic = event.Semantic || semantic || a.Semantic || g.Semantic
		event.Terminal = event.Terminal || terminal || a.Terminal || g.Terminal
	}
	// Mark nonempty bytes for retry commitment before delegation, including
	// framing/keepalive output; the underlying sink records its actual write
	// outcome separately.
	event.CommitForRetry = event.CommitForRetry || len(event.Data) > 0
	return s.OutputSink.Emit(event)
}
