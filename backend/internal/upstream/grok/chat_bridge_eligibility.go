package grok

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// chat_bridge_eligibility.go checks whether Grok Chat requests can use
// Responses conversion without losing supported request intent.
// A Chat request can use the Grok Responses bridge when the shared converter can
// represent its supplied fields and message history faithfully. Unknown fields,
// non-null stop or effort controls, legacy function controls and token caps
// below the converter's floor keep the request eligible for the caller's native
// Chat path instead of silently changing its intent.
//
// Eligibility combines declaration shape, tool choice and message role: required
// tool choice needs declarations, and an assistant with no ordinary text needs
// tool calls or reasoning content. Content parts accept text and image tags
// while later conversion handles their payload. Runtime selection additionally
// limits the bridge to its fixed model set and a nonblank cache identity,
// and cache-intent preparation clears ineffective choices only after the
// converter has omitted tools.

// These endpoint paths let Chat execution record whether the selected attempt
// uses the Responses bridge or native Chat exchange.
const (
	GrokChatResponsesEndpoint = "/v1/responses"        // Responses bridge endpoint path
	GrokChatRawEndpoint       = "/v1/chat/completions" // native Chat endpoint path
)

// grokChatResponsesBridgeTopLevelFields limits bridge input to fields
// represented by the shared converter and the eligibility checks below.
var grokChatResponsesBridgeTopLevelFields = map[string]struct{}{
	"model": {},

	"messages": {},

	"instructions": {},

	"stream": {},

	"stream_options": {},

	"max_tokens": {},

	"max_completion_tokens": {},

	"temperature": {},

	"top_p": {},

	"stop": {},

	"reasoning_effort": {},

	"prompt_cache_key": {},

	"tools": {},

	"tool_choice": {},

	"functions": {},

	"function_call": {},

	"parallel_tool_calls": {},

	"response_format": {},

	"service_tier": {},
}

// GrokChatResponsesBridgeEligibility reports representability and a rejection
// reason for a Chat body. It validates supported top-level shapes, model
// presence, output caps, declarations and message roles before the caller
// chooses Responses or native Chat. A successful result has an empty reason.
//
// Tool choice and message content are checked together with their supporting
// declarations or history: required needs tools, and absent, null or blank
// string assistant content needs reasoning or calls. An empty content array
// needs reasoning even when tool calls exist. Image tags establish representation
// without URL validation. Unknown map fields produce a corresponding rejection
// reason, whose selection follows map iteration rather than input-field order.
func (m BodyCodec) GrokChatResponsesBridgeEligibility(body []byte) (bool, string) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil || root == nil {
		return false, "invalid_json"
	}

	// Null stop or effort controls contribute no request setting here. Admit that
	// empty representation while populated controls keep the request on the
	// caller's native Chat path.
	for _, field := range []string{"stop", "reasoning_effort"} {
		if raw, exists := root[field]; exists && !m.GrokChatJSONNull(raw) {
			return false, "unsupported_" + field
		}
	}
	if raw, exists := root["instructions"]; exists {
		var instructions string
		if !m.GrokChatJSONNull(raw) && json.Unmarshal(raw, &instructions) != nil {
			return false, "invalid_instructions"
		}
	}
	if raw, exists := root["response_format"]; exists {
		var responseFormat map[string]json.RawMessage
		if !m.GrokChatJSONNull(raw) && (json.Unmarshal(raw, &responseFormat) != nil || responseFormat == nil) {
			return false, "invalid_response_format"
		}
	}
	if raw, exists := root["service_tier"]; exists {
		var serviceTier string
		if !m.GrokChatJSONNull(raw) && json.Unmarshal(raw, &serviceTier) != nil {
			return false, "invalid_service_tier"
		}
	}
	if raw, exists := root["tools"]; exists {
		if ok, reason := m.GrokChatFunctionDeclarationsBridgeable(raw); !ok {
			return false, reason
		}
	}
	if raw, exists := root["functions"]; exists && !m.GrokChatNullOrEmptyArray(raw) {
		return false, "unsupported_functions"
	}
	if raw, exists := root["tool_choice"]; exists {
		if ok, reason := m.GrokChatToolChoiceBridgeable(raw); !ok {
			return false, reason
		}
		var choice string
		// Required choice has meaning only with declared tools. Reject missing
		// declarations before conversion can drop its supporting request controls.
		if json.Unmarshal(raw, &choice) == nil && choice == "required" && !m.GrokChatHasFunctionDeclarations(root) {
			return false, "required_tool_choice_without_tools"
		}
	}
	if raw, exists := root["function_call"]; exists && !m.GrokChatNullOrNone(raw) {
		return false, "unsupported_function_call"
	}
	for field := range root {
		if _, supported := grokChatResponsesBridgeTopLevelFields[field]; !supported {
			return false, "unknown_field_" + field
		}
	}

	var model string
	if raw, ok := root["model"]; !ok || json.Unmarshal(raw, &model) != nil || strings.TrimSpace(model) == "" {
		return false, "invalid_model"
	}

	if raw, ok := root["stream"]; ok {
		var stream *bool
		if json.Unmarshal(raw, &stream) != nil || stream == nil {
			return false, "invalid_stream"
		}
	}
	if raw, ok := root["parallel_tool_calls"]; ok {
		var parallelToolCalls *bool
		if json.Unmarshal(raw, &parallelToolCalls) != nil || parallelToolCalls == nil {
			return false, "invalid_parallel_tool_calls"
		}
	}
	if raw, ok := root["stream_options"]; ok {
		var options map[string]json.RawMessage
		if json.Unmarshal(raw, &options) != nil || options == nil {
			return false, "invalid_stream_options"
		}
		for field, value := range options {
			if field != "include_usage" {
				return false, "unknown_stream_option_" + field
			}
			var includeUsage *bool
			if json.Unmarshal(value, &includeUsage) != nil || includeUsage == nil {
				return false, "invalid_stream_include_usage"
			}
		}
	}

	// The shared converter floors output caps at 128 tokens. Reject smaller
	// supplied caps so bridge selection cannot enlarge the client's limit.
	for _, field := range []string{"max_tokens", "max_completion_tokens"} {
		if raw, ok := root[field]; ok {
			var value *int
			if json.Unmarshal(raw, &value) != nil || value == nil || *value < 128 {
				return false, "unsafe_" + field
			}
		}
	}
	if _, hasMaxTokens := root["max_tokens"]; hasMaxTokens {
		if _, hasMaxCompletionTokens := root["max_completion_tokens"]; hasMaxCompletionTokens {
			return false, "conflicting_max_tokens"
		}
	}
	for _, field := range []string{"temperature", "top_p"} {
		if raw, ok := root[field]; ok {
			var value *float64
			if json.Unmarshal(raw, &value) != nil || value == nil {
				return false, "invalid_" + field
			}
		}
	}
	if raw, ok := root["prompt_cache_key"]; ok {
		var key string
		if json.Unmarshal(raw, &key) != nil {
			return false, "invalid_prompt_cache_key"
		}
	}

	var messages []map[string]json.RawMessage
	rawMessages, ok := root["messages"]
	if !ok || json.Unmarshal(rawMessages, &messages) != nil || len(messages) == 0 {
		return false, "invalid_messages"
	}
	for _, message := range messages {
		var role string
		if raw, exists := message["role"]; !exists || json.Unmarshal(raw, &role) != nil {
			return false, "invalid_message_role"
		}
		switch role {
		case "system", "user":
			if ok, reason := m.GrokChatMessageFieldsBridgeable(message, "role", "content"); !ok {
				return false, reason
			}
			raw, exists := message["content"]
			if !exists {
				return false, "non_text_message_content"
			}
			if ok, reason := m.GrokChatRequiredMessageContentBridgeable(raw); !ok {
				return false, reason
			}
		case "assistant":
			if ok, reason := m.GrokChatMessageFieldsBridgeable(message, "role", "content", "reasoning_content", "tool_calls"); !ok {
				return false, reason
			}
			reasoningContent := ""
			if raw, exists := message["reasoning_content"]; exists {
				if !m.GrokChatJSONNull(raw) && json.Unmarshal(raw, &reasoningContent) != nil {
					return false, "invalid_reasoning_content"
				}
			}
			hasReasoningContent := strings.TrimSpace(reasoningContent) != ""
			toolCallCount := 0
			if raw, exists := message["tool_calls"]; exists {
				var reason string
				toolCallCount, reason = m.GrokChatAssistantToolCallsBridgeable(raw)
				if reason != "" {
					return false, reason
				}
			}
			raw, hasContent := message["content"]
			if !hasContent || strings.TrimSpace(string(raw)) == "null" {
				if toolCallCount == 0 && !hasReasoningContent {
					return false, "non_text_message_content"
				}
				continue
			}
			var content string
			if json.Unmarshal(raw, &content) == nil {
				if strings.TrimSpace(content) == "" && toolCallCount == 0 && !hasReasoningContent {
					return false, "empty_message_content"
				}
				continue
			}
			if ok, reason := m.GrokChatStructuredContentBridgeable(raw); !ok {
				// Reasoning content can carry an otherwise empty assistant turn. Admit that
				// specific empty-array result while keeping unsupported parts rejected.
				if !hasReasoningContent || reason != "empty_message_content" {
					return false, reason
				}
			}
		case "tool":
			if ok, reason := m.GrokChatMessageFieldsBridgeable(message, "role", "content", "tool_call_id"); !ok {
				return false, reason
			}
			var callID string
			if raw, exists := message["tool_call_id"]; !exists || json.Unmarshal(raw, &callID) != nil || strings.TrimSpace(callID) == "" {
				return false, "invalid_tool_call_id"
			}
			var output string
			if raw, exists := message["content"]; !exists || json.Unmarshal(raw, &output) != nil || output == "" {
				return false, "invalid_tool_message_content"
			}
		default:
			return false, "unsupported_message_role_" + role
		}
	}

	return true, ""
}

// GrokChatFunctionDeclarationsBridgeable checks null or arrays of supported Chat
// function declarations. It restricts tool and function keys, requires a
// nonblank name and object parameters, and checks optional description/strict
// decoding. Schema contents remain for later conversion or upstream validation.
func (m BodyCodec) GrokChatFunctionDeclarationsBridgeable(raw json.RawMessage) (bool, string) {
	if strings.TrimSpace(string(raw)) == "null" {
		return true, ""
	}
	var declarations []json.RawMessage
	if json.Unmarshal(raw, &declarations) != nil {
		return false, "invalid_tools"
	}
	for _, declaration := range declarations {
		var tool map[string]json.RawMessage
		if json.Unmarshal(declaration, &tool) != nil || tool == nil {
			return false, "invalid_tool"
		}
		for field := range tool {
			if field != "type" && field != "function" {
				return false, "unsafe_tool_field_" + field
			}
		}
		var toolType string
		if rawType, exists := tool["type"]; !exists || json.Unmarshal(rawType, &toolType) != nil || toolType != "function" {
			return false, "unsupported_tool_type"
		}
		functionRaw, exists := tool["function"]
		if !exists {
			return false, "invalid_tool_function"
		}

		var function map[string]json.RawMessage
		if json.Unmarshal(functionRaw, &function) != nil || function == nil {
			return false, "invalid_tool_function"
		}
		for field := range function {
			switch field {
			case "name", "description", "parameters", "strict":
			default:
				return false, "unsafe_tool_function_field_" + field
			}
		}
		var name string
		if rawName, exists := function["name"]; !exists || json.Unmarshal(rawName, &name) != nil || strings.TrimSpace(name) == "" {
			return false, "invalid_tool_function_name"
		}
		if rawDescription, exists := function["description"]; exists {
			var description string
			if json.Unmarshal(rawDescription, &description) != nil {
				return false, "invalid_tool_function_description"
			}
		}
		var parameters map[string]json.RawMessage
		if rawParameters, exists := function["parameters"]; !exists || json.Unmarshal(rawParameters, &parameters) != nil || parameters == nil {
			return false, "invalid_tool_function_parameters"
		}
		if rawStrict, exists := function["strict"]; exists {
			var strict bool
			if json.Unmarshal(rawStrict, &strict) != nil {
				return false, "invalid_tool_function_strict"
			}
		}
	}
	return true, ""
}

// GrokChatToolChoiceBridgeable accepts JSON null or exact auto, none and
// required strings. Named-object and unknown-string choices return
// unsupported_tool_choice; the caller separately checks whether required has
// supporting function declarations.
func (m BodyCodec) GrokChatToolChoiceBridgeable(raw json.RawMessage) (bool, string) {
	if strings.TrimSpace(string(raw)) == "null" {
		return true, ""
	}
	var choice string
	if json.Unmarshal(raw, &choice) != nil {
		return false, "unsupported_tool_choice"
	}
	switch choice {
	case "auto", "none", "required":
		return true, ""
	default:
		return false, "unsupported_tool_choice"
	}
}

// GrokChatHasFunctionDeclarations reports a populated decodable tools or
// functions array in root. It does not validate individual entries; eligibility
// validates declarations separately before using this count to admit required
// tool choice.
func (m BodyCodec) GrokChatHasFunctionDeclarations(root map[string]json.RawMessage) bool {
	for _, field := range []string{"tools", "functions"} {
		raw, exists := root[field]
		if !exists {
			continue
		}
		var declarations []json.RawMessage
		if json.Unmarshal(raw, &declarations) == nil && len(declarations) > 0 {
			return true
		}
	}
	return false
}

// GrokChatMessageFieldsBridgeable checks every message key against the supplied
// allowedFields set. Unknown keys return a reason naming one rejected field; map
// iteration means multiple unsupported fields have no guaranteed rejection
// order.
func (m BodyCodec) GrokChatMessageFieldsBridgeable(message map[string]json.RawMessage, allowedFields ...string) (bool, string) {
	allowed := make(map[string]struct{}, len(allowedFields))
	for _, field := range allowedFields {
		allowed[field] = struct{}{}
	}
	for field := range message {
		if _, ok := allowed[field]; !ok {
			return false, "unsafe_message_field_" + field
		}
	}
	return true, ""
}

// GrokChatRequiredMessageContentBridgeable admits a nonblank string or supported
// structured content. Whitespace-only strings return empty_message_content.
// Non-string values pass to the structured-content checker, whose result
// distinguishes empty arrays from unsupported parts.
func (m BodyCodec) GrokChatRequiredMessageContentBridgeable(raw json.RawMessage) (bool, string) {
	var content string
	if json.Unmarshal(raw, &content) == nil {
		if strings.TrimSpace(content) == "" {
			return false, "empty_message_content"
		}
		return true, ""
	}
	// Structured content accepts text, image_url, and input_image arrays that the
	// converter can represent as Responses input_text and input_image parts.
	return m.GrokChatStructuredContentBridgeable(raw)
}

// GrokChatAssistantToolCallsBridgeable validates function-call history and
// returns its call count. Null yields zero without a rejection reason. Calls
// need nonblank identifiers and names, JSON argument strings and supported keys;
// supplied indices may be null or nonnegative integers.
func (m BodyCodec) GrokChatAssistantToolCallsBridgeable(raw json.RawMessage) (int, string) {
	if strings.TrimSpace(string(raw)) == "null" {
		return 0, ""
	}
	var calls []map[string]json.RawMessage
	if json.Unmarshal(raw, &calls) != nil {
		return 0, "invalid_tool_calls"
	}
	for _, call := range calls {
		if call == nil {
			return 0, "invalid_tool_call"
		}
		for field := range call {
			switch field {
			case "id", "type", "function", "index":
			default:
				return 0, "unsafe_tool_call_field_" + field
			}
		}
		if rawIndex, exists := call["index"]; exists {
			var index *int
			if json.Unmarshal(rawIndex, &index) != nil || (index != nil && *index < 0) {
				return 0, "invalid_tool_call_index"
			}
		}
		var callID string
		if rawID, exists := call["id"]; !exists || json.Unmarshal(rawID, &callID) != nil || strings.TrimSpace(callID) == "" {
			return 0, "invalid_tool_call_id"
		}
		var callType string
		if rawType, exists := call["type"]; !exists || json.Unmarshal(rawType, &callType) != nil || callType != "function" {
			return 0, "unsupported_tool_call_type"
		}
		var function map[string]json.RawMessage
		if rawFunction, exists := call["function"]; !exists || json.Unmarshal(rawFunction, &function) != nil || function == nil {
			return 0, "invalid_tool_call_function"
		}
		for field := range function {
			if field != "name" && field != "arguments" {
				return 0, "unsafe_tool_call_function_field_" + field
			}
		}
		var name string
		if rawName, exists := function["name"]; !exists || json.Unmarshal(rawName, &name) != nil || strings.TrimSpace(name) == "" {
			return 0, "invalid_tool_call_function_name"
		}
		var arguments string
		if rawArguments, exists := function["arguments"]; !exists || json.Unmarshal(rawArguments, &arguments) != nil || !json.Valid([]byte(arguments)) {
			return 0, "invalid_tool_call_arguments"
		}
	}
	return len(calls), ""
}

// GrokChatStructuredContentBridgeable checks an array for supported text and
// image-tagged parts. At least one nonblank text or image tag is needed;
// unsupported types reject the array. Image payload details are left for
// conversion and upstream validation.
func (m BodyCodec) GrokChatStructuredContentBridgeable(raw json.RawMessage) (bool, string) {
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return false, "non_text_message_content"
	}
	if len(parts) == 0 {
		return false, "empty_message_content"
	}
	hasContent := false
	for _, part := range parts {
		var partType string
		rawType, ok := part["type"]
		if !ok || json.Unmarshal(rawType, &partType) != nil {
			return false, "non_text_message_content"
		}
		switch strings.TrimSpace(partType) {
		case "text":
			var text string
			if raw, ok := part["text"]; ok && json.Unmarshal(raw, &text) == nil {
				if strings.TrimSpace(text) != "" {
					hasContent = true
				}
			}
		case "image_url", "input_image":
			hasContent = true
		default:
			return false, "unsupported_content_part_" + strings.TrimSpace(partType)
		}
	}
	if !hasContent {
		return false, "empty_message_content"
	}
	return true, ""
}

// GrokChatNullOrNone recognizes JSON null or a trimmed case-insensitive none
// string.
func (m BodyCodec) GrokChatNullOrNone(raw json.RawMessage) bool {
	if strings.TrimSpace(string(raw)) == "null" {
		return true
	}
	var value string
	return json.Unmarshal(raw, &value) == nil && strings.EqualFold(strings.TrimSpace(value), "none")
}

// GrokChatJSONNull reports whether trimmed input is the literal JSON null.
func (m BodyCodec) GrokChatJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

// GrokChatNullOrEmptyArray recognizes JSON null or a decodable empty array.
func (m BodyCodec) GrokChatNullOrEmptyArray(raw json.RawMessage) bool {
	if strings.TrimSpace(string(raw)) == "null" {
		return true
	}
	var values []json.RawMessage
	return json.Unmarshal(raw, &values) == nil && len(values) == 0
}

// GrokChatResponsesCacheIntentBody copies body or removes an ineffective
// auto/none choice when tools is absent. A present tools field, including empty
// or null, keeps that choice. The rewritten case decodes and re-encodes JSON;
// unchanged cases return independent byte storage.
func (m BodyCodec) GrokChatResponsesCacheIntentBody(body []byte) ([]byte, error) {
	// Conversion can omit tools entirely. Remove ineffective auto/none choices in
	// that case so the cache-intent payload describes the available request
	// controls.
	if gjson.GetBytes(body, "tools").Exists() {
		return append([]byte(nil), body...), nil
	}
	choice := gjson.GetBytes(body, "tool_choice")
	if !choice.Exists() || choice.Type != gjson.String || (choice.String() != "auto" && choice.String() != "none") {
		return append([]byte(nil), body...), nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, err
	}
	delete(root, "tool_choice")
	return json.Marshal(root)
}

// GrokChatResponsesBridgeModel recognizes the fixed 4.5 and 4.6 Chat bridge
// identities.
func (m BodyCodec) GrokChatResponsesBridgeModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "grok-4.5", "grok-4.6", "grok-4.6-latest":
		return true
	default:
		return false
	}
}

// GrokChatResponsesRuntimeEligible admits a supported bridge model with a
// nonblank cache identity. Both inputs are required by runtime selection; false
// leaves the caller's native Chat execution path available for this dispatch.
func (m BodyCodec) GrokChatResponsesRuntimeEligible(upstreamModel, cacheIdentity string) bool {
	return m.GrokChatResponsesBridgeModel(upstreamModel) && strings.TrimSpace(cacheIdentity) != ""
}
