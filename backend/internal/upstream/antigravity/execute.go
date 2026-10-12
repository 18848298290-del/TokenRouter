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

const (
	ModeClaudeResponse ResponseMode = iota
	ModeGeminiResponse
	ModeChatResponse
	ModeResponsesResponse
)

type ResponseMode uint8

// Target supplies the model, output mode, exchange, and response callbacks for one attempt.
type Target struct {
	OutputError    func(error)
	ProviderID     int64
	Model          string
	Mode           ResponseMode
	StartedAt      time.Time
	IncludeUsage   bool
	ClientTools    bridge.ResponsesClientToolMapping
	Exchange       func(context.Context) (*http.Response, error)
	BeforeResponse func(context.Context, *http.Response) (bool, error)
	Response       ResponseOptions
	Enter          func() (func(), error)
}

type Executor struct{}

// observedSink marks semantic and terminal output on each emitted segment.
type observedSink struct {
	upstream.OutputSink
	nonStream bool
}

func (t *Target) TargetID() int64 {
	if t == nil {
		return 0
	}
	return t.ProviderID
}

func (t *Target) String() string   { return fmt.Sprintf("antigravity target provider=%d", t.TargetID()) }
func (t *Target) GoString() string { return t.String() }

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
	// Merge fields seen in this attempt so partial usage updates retain earlier observations.
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
			// Convert the accumulated Gemini token fields to normalized usage.
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

func (s *observedSink) InitialOutput() upstream.OutputHead {
	if source, ok := s.OutputSink.(interface{ InitialOutput() upstream.OutputHead }); ok {
		return source.InitialOutput()
	}
	return upstream.OutputHead{}
}

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
	// Emitting bytes closes the retry window; semantic output and TTFT are tracked separately.
	event.CommitForRetry = event.CommitForRetry || len(event.Data) > 0
	return s.OutputSink.Emit(event)
}
