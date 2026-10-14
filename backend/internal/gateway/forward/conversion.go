package forward

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	protocolcore "github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// Anthropic conversion keeps the client's logical model and output mode while
// selecting a provider wire model and a streaming upstream request. Responses
// client tools can require reversible function-tool adaptations before typed
// conversion; their restoration mapping accompanies only the Responses output
// path. Both entrypoints prepare OAuth requests and cache limits before their
// credential/build/send sequence, then apply health decisions to source errors.
//
// Recorded effort combines client syntax, selected model, converted Anthropic
// output configuration, and model-specific thinking defaults. A native Anthropic
// effort overrides OpenAI capability normalization so max survives that
// representation change. Selected wire speed supplies missing result metadata
// after output handling, leaving any observed response speed authoritative.

// AsResponses returns an Anthropic attempt converted to the client's Responses
// format. It normalizes supported legacy input, adapts client tools with a
// restoration mapping, and retains the input model/stream setting while sending
// the selected upstream model in streaming mode.
//
// OAuth preparation and cache enforcement precede credentials and exchange.
// Successful Send provides usable response handles and Close; this function
// closes them on every later return. HTTP health chooses generic output before
// failover, while successful output retains usage and separate model/effort
// observations from its streaming or buffered adapter.
func AsResponses(ctx context.Context, p ConversionPorts, in ConversionInput, body []byte) (*Result, error) {
	startTime := time.Now()

	normalizedBody, normalized, err := p.NormalizeResponses(body)
	if err != nil {
		return nil, err
	}
	if normalized {
		body = normalizedBody
	}

	adaptedBody, clientToolMapping, err := AdaptResponsesClientToolsForAnthropic(body)
	if err != nil {
		return nil, fmt.Errorf("adapt responses client tools: %w", err)
	}

	var responsesReq protocolopenai.ResponsesRequest
	if err := json.Unmarshal(adaptedBody, &responsesReq); err != nil {
		return nil, fmt.Errorf("parse responses request: %w", err)
	}
	originalModel := responsesReq.Model
	clientStream := responsesReq.Stream

	anthropicReq, err := bridge.ResponsesToAnthropicRequest(&responsesReq)
	if err != nil {
		return nil, fmt.Errorf("convert responses to anthropic: %w", err)
	}

	// Both downstream modes consume Anthropic events. Buffered conversion joins
	// those events into a complete reply, while streaming conversion restores
	// the client's framing without changing the upstream exchange mode.
	anthropicReq.Stream = true
	reqStream := true

	// Resolve provider/platform selection from the logical input supplied by
	// the entrypoint. An empty mapping keeps that input usable for the request,
	// while output metadata retains its separate logical name.
	mappedModel := p.ResolveModel(ctx, originalModel)
	if mappedModel == "" {
		mappedModel = originalModel
	}
	reasoningEffort := p.Effort(body, false, mappedModel, originalModel)
	// Converted Anthropic output effort overrides the OpenAI recording choice.
	// Its protocol-specific normalization accepts Claude max, preserving that
	// selected level across the intermediate OpenAI request representation.
	if anthropicReq.OutputConfig != nil {
		reasoningEffort = protocolcore.NormalizeClaudeOutputEffort(anthropicReq.OutputConfig.Effort)
	}
	// Fill missing recorded effort for an eligible thinking-enabled model after
	// the converted effort wins. This display observation does not itself rewrite
	// the model's thinking request parameters.
	reasoningEffort = p.ThinkingFallback(reasoningEffort, body, mappedModel)
	anthropicReq.Model = mappedModel

	p.ModelNotice("gateway forward_as_responses: model mapping applied", originalModel, mappedModel, clientStream)

	anthropicBody, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	// OAuth conversion invokes the configured mimic phase for converted requests.
	// Unlike the Messages entrypoint's client-format gate, this path bases the
	// choice directly on OAuth eligibility supplied by the adapter.
	shouldMimicClaudeCode := in.OAuth

	if shouldMimicClaudeCode {
		anthropicBody = p.Mimic(ctx, anthropicBody, anthropicReq.System, mappedModel)
	}

	anthropicBody = p.CacheLimit(anthropicBody)

	if err := p.Credential(ctx); err != nil {
		return nil, fmt.Errorf("get access token: %w", err)
	}
	wireBody, err := p.Build(ctx, anthropicBody, mappedModel, reqStream, shouldMimicClaudeCode)
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}
	resp, err := p.Send(ctx)
	if err != nil {
		return nil, err
	}
	defer resp.Close()

	if resp.StatusCode >= 400 {
		return nil, handleConversionHTTPError(ctx, p, resp.StatusCode, mappedModel)
	}

	var result *Result
	var handleErr error
	if clientStream {
		result, handleErr = ResponsesStreaming(resp, p.Output(), originalModel, mappedModel, reasoningEffort, startTime, clientToolMapping)
	} else {
		result, handleErr = ResponsesBuffered(resp, p.Output(), originalModel, mappedModel, reasoningEffort, startTime, clientToolMapping)
	}

	// A missing response speed can use the selected wire request's fast hint.
	// This fills result metadata after conversion and leaves an observed response
	// speed unchanged, including when output handling returns partial observations.
	if result != nil && strings.TrimSpace(result.Usage.Speed) == "" &&
		strings.EqualFold(strings.TrimSpace(gjson.GetBytes(wireBody, "speed").String()), "fast") {
		result.Usage.Speed = "fast"
	}
	return result, handleErr
}

// AsChat returns an Anthropic attempt converted to the client's Chat format,
// using a Responses request as the intermediate representation. It retains the
// logical model, client streaming choice, and optional usage-chunk request while
// forcing the Anthropic exchange to stream for either downstream mode.
//
// OAuth preparation and cache enforcement precede credentials, Build, and Send.
// A successful Send requires usable response handles and Close, which is
// deferred through error and output handling. Source HTTP health chooses generic
// output before recovery; converted Anthropic effort and missing wire-speed
// metadata accompany the returned output observations.
func AsChat(ctx context.Context, p ConversionPorts, in ConversionInput, body []byte) (*Result, error) {
	startTime := time.Now()

	var ccReq protocolopenai.ChatCompletionsRequest
	if err := json.Unmarshal(body, &ccReq); err != nil {
		return nil, fmt.Errorf("parse chat completions request: %w", err)
	}
	originalModel := ccReq.Model
	clientStream := ccReq.Stream
	includeUsage := ccReq.StreamOptions != nil && ccReq.StreamOptions.IncludeUsage

	responsesReq, err := bridge.ChatCompletionsToResponses(&ccReq, bridge.RequestOptions{SupportsMaxEffort: true})
	if err != nil {
		return nil, fmt.Errorf("convert chat completions to responses: %w", err)
	}

	anthropicReq, err := bridge.ResponsesToAnthropicRequest(responsesReq)
	if err != nil {
		return nil, fmt.Errorf("convert responses to anthropic: %w", err)
	}

	// Both downstream modes consume Anthropic events. Buffered conversion joins
	// those events into a complete reply, while streaming conversion restores
	// the client's framing without changing the upstream exchange mode.
	anthropicReq.Stream = true
	reqStream := true

	// Resolve provider/platform selection from the logical input supplied by
	// the entrypoint. An empty mapping keeps that input usable for the request,
	// while output metadata retains its separate logical name.
	mappedModel := p.ResolveModel(ctx, originalModel)
	if mappedModel == "" {
		mappedModel = originalModel
	}
	anthropicReq.Model = mappedModel

	p.ModelNotice("gateway forward_as_chat_completions: model mapping applied", originalModel, mappedModel, clientStream)

	anthropicBody, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	// OAuth conversion invokes the configured mimic phase for converted requests.
	// Unlike the Messages entrypoint's client-format gate, this path bases the
	// choice directly on OAuth eligibility supplied by the adapter.
	shouldMimicClaudeCode := in.OAuth

	if shouldMimicClaudeCode {
		anthropicBody = p.Mimic(ctx, anthropicBody, anthropicReq.System, mappedModel)
	}

	anthropicBody = p.CacheLimit(anthropicBody)

	if err := p.Credential(ctx); err != nil {
		return nil, fmt.Errorf("get access token: %w", err)
	}
	wireBody, err := p.Build(ctx, anthropicBody, mappedModel, reqStream, shouldMimicClaudeCode)
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}
	resp, err := p.Send(ctx)
	if err != nil {
		return nil, err
	}
	defer resp.Close()

	if resp.StatusCode >= 400 {
		return nil, handleConversionHTTPError(ctx, p, resp.StatusCode, mappedModel)
	}

	reasoningEffort := p.Effort(body, true, mappedModel, originalModel)
	// Converted Anthropic output effort overrides the OpenAI recording choice.
	// Its protocol-specific normalization accepts Claude max, preserving that
	// selected level across the intermediate OpenAI request representation.
	if anthropicReq.OutputConfig != nil {
		reasoningEffort = protocolcore.NormalizeClaudeOutputEffort(anthropicReq.OutputConfig.Effort)
	}
	// Fill missing recorded effort for an eligible thinking-enabled model after
	// the converted effort wins. This display observation does not itself rewrite
	// the model's thinking request parameters.
	reasoningEffort = p.ThinkingFallback(reasoningEffort, body, mappedModel)

	var result *Result
	var handleErr error
	if clientStream {
		result, handleErr = ChatStreaming(resp, p.Output(), originalModel, mappedModel, reasoningEffort, startTime, includeUsage)
	} else {
		result, handleErr = ChatBuffered(resp, p.Output(), originalModel, mappedModel, reasoningEffort, startTime)
	}

	// A missing response speed can use the selected wire request's fast hint.
	// This fills result metadata after conversion and leaves an observed response
	// speed unchanged, including when output handling returns partial observations.
	if result != nil && strings.TrimSpace(result.Usage.Speed) == "" &&
		strings.EqualFold(strings.TrimSpace(gjson.GetBytes(wireBody, "speed").String()), "fast") {
		result.Usage.Speed = "fast"
	}
	return result, handleErr
}

// handleConversionHTTPError returns the selected error after an upstream HTTP
// failure. The caller supplies source status and the selected upstream model,
// and keeps responsibility for closing the response. Available body bytes
// remain the evidence for summary extraction, health classification, and
// recovery even when reading reports an error.
//
// Generic output takes precedence when health also permits failover. A recovery
// decision records its notice before constructing the error with the
// same-provider retry flag; otherwise client output receives the mapped status
// and extracted summary.
func handleConversionHTTPError(ctx context.Context, p ConversionPorts, status int, mappedModel string) error {
	// Keep available body evidence even when reading fails. Health uses source
	// status and selected model before generic output, recovery, or final
	// client-status mapping consumes that captured failure information.
	respBody, _ := p.ReadErrorBody()
	upstreamMsg := p.ErrorMessage(respBody)
	decision := p.Health(ctx, status, respBody, mappedModel)

	if decision.Generic {
		p.Output().Error(500, "server_error", "Upstream gateway error")
		return fmt.Errorf("upstream error: %d (not in custom error codes)", status)
	}

	if decision.Failover {
		// Report the failure before returning its recovery signal, because the outer
		// request loop can immediately begin the next provider attempt.
		p.FailoverNotice(status, upstreamMsg)
		return p.FailoverError(status, respBody, decision.RetrySameProvider)
	}

	p.Output().Error(MapStatus(status), "server_error", upstreamMsg)
	return fmt.Errorf("upstream error: %d %s", status, upstreamMsg)
}

// AdaptResponsesClientToolsForAnthropic returns request bytes and the mapping
// needed to restore Responses client tools after Anthropic conversion. It
// decodes one JSON object using json.Number, lifts additional_tools declarations,
// then delegates reversible tool adaptation. A successful changed request is
// re-encoded; an unchanged request retains the supplied bytes and storage.
//
// Errors return the supplied body and an empty mapping. Decode does not check
// for trailing data after its first value, so unchanged bytes can still require
// validation by the typed request reader. The decoded map is local, while its
// internal mutation and tool mapping follow the bridge implementation.
func AdaptResponsesClientToolsForAnthropic(body []byte) ([]byte, bridge.ResponsesClientToolMapping, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	// Retain numeric literals while tool adaptation rebuilds the generic request.
	// Decoding through float64 could otherwise round unrelated numeric fields
	// before the later typed Anthropic conversion reads their selected values.
	decoder.UseNumber()
	var requestBody map[string]any
	if err := decoder.Decode(&requestBody); err != nil {
		return body, bridge.ResponsesClientToolMapping{}, err
	}

	additionalToolsChanged, err := LiftResponsesAdditionalTools(requestBody)
	if err != nil {
		return body, bridge.ResponsesClientToolMapping{}, err
	}
	mapping, changed, err := bridge.AdaptResponsesClientTools(requestBody)
	if err != nil {
		return body, bridge.ResponsesClientToolMapping{}, err
	}
	changed = changed || additionalToolsChanged
	if !changed {
		return body, mapping, nil
	}

	rebuilt, err := json.Marshal(requestBody)
	if err != nil {
		return body, bridge.ResponsesClientToolMapping{}, err
	}
	return rebuilt, mapping, nil
}

// LiftResponsesAdditionalTools moves additional_tools input items into the
// request's top-level tool array, retaining other input items in their order.
// Nonarray input makes no change; each recognized item needs an array-valued
// tools field, and a nonarray existing tools value supplies no initial tools.
//
// The map's tools/input entries are published after the scan finds a change.
// Element maps remain shared, and appending can reuse an existing tool slice's
// backing array before a later invalid item returns an error. Callers needing
// independent or transactional in-memory edits must copy their request first.
func LiftResponsesAdditionalTools(requestBody map[string]any) (bool, error) {
	input, ok := requestBody["input"].([]any)
	if !ok {
		return false, nil
	}

	tools, _ := requestBody["tools"].([]any)
	kept := make([]any, 0, len(input))
	changed := false
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok || strings.TrimSpace(fmt.Sprint(item["type"])) != "additional_tools" {
			kept = append(kept, raw)
			continue
		}
		additional, ok := item["tools"].([]any)
		if !ok {
			return false, fmt.Errorf("additional_tools.tools must be an array")
		}
		// Append declarations in encounter order and omit their wrapper from input.
		// Existing tool storage may be reused; the final map assignment happens
		// only after all encountered additional-tools arrays have been accepted.
		tools = append(tools, additional...)
		changed = true
	}
	if !changed {
		return false, nil
	}
	requestBody["tools"] = tools
	requestBody["input"] = kept
	return true, nil
}

// ConversionOptionsForModel returns request options that retain max effort
// through intermediate conversion. Its model argument is intentionally unused;
// final target capability resolution belongs to execution after provider model
// mapping, so an input alias alone cannot trim that effort here.
func ConversionOptionsForModel(_ string) bridge.RequestOptions {
	return bridge.RequestOptions{
		SupportsMaxEffort: true,
	}
}

// ExtractEffort returns normalized recorded effort or nil when no usable value
// exists. Nested reasoning.effort wins; Chat input alone can use reasoning_effort
// when the nested value is blank. A nonnil normalizer is required once a raw
// effort is available.
//
// The first trimmed nonempty model candidate wins over the request's model,
// allowing mapped identity to govern capability-sensitive normalization. Blank
// normalized output becomes nil even when input effort was present. The result
// points to a local string value, and gjson extraction does not validate the
// entire request body.
func ExtractEffort(body []byte, chat bool, normalize func(string, string) string, models ...string) *string {
	raw := strings.TrimSpace(gjson.GetBytes(body, "reasoning.effort").String())
	// A nonempty nested value blocks the Chat alias even if normalization later
	// rejects it. This makes field precedence a syntax decision before model
	// capability filtering decides whether the selected effort is usable.
	if raw == "" && chat {
		raw = strings.TrimSpace(gjson.GetBytes(body, "reasoning_effort").String())
	}
	if raw == "" {
		return nil
	}
	model := ""
	for _, candidate := range models {
		if value := strings.TrimSpace(candidate); value != "" {
			model = value
			break
		}
	}
	// Supplied candidates establish execution identity; request model is the
	// final default when none is usable. The normalizer can therefore interpret
	// client effort using a mapped model instead of an unresolved input alias.
	if model == "" {
		model = strings.TrimSpace(gjson.GetBytes(body, "model").String())
	}
	value := normalize(raw, model)
	if value == "" {
		return nil
	}
	return &value
}
