package bridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	toolOutputMediaMarker      = "[Tool output media moved to the following user message]"
	toolOutputMediaAttribution = "[Tool output media for call %s]"

	// customToolInputSchema wraps a custom/freeform tool as a function tool.
	// Chat cannot express freeform input or its grammar constraint, so the schema
	// uses one input string. extractCustomToolCallInput unwraps that field
	// when converting the upstream call back to the client protocol.
	customToolInputSchema = `{"type":"object","properties":{"input":{"type":"string","description":"The raw input for this tool, passed through verbatim."}},"required":["input"]}`

	// toolSearchProxyName is the function name used for tool_search adaptation.
	// Return calls as tool_search_call items so Codex dispatches them correctly.
	toolSearchProxyName = "tool_search"

	toolSearchProxySchema = `{"type":"object","properties":{"query":{"type":"string","description":"Search query for tools or connectors to load."},"limit":{"type":"integer","description":"Maximum number of tool groups to return."}},"required":["query"]}`

	// chatToolNameMaxLen limits flattened Chat function names in bytes.
	chatToolNameMaxLen = 64
)

type toolOutputMediaByCallID map[string][]ChatContentPart

// ResponsesToChatOptions supplies an optional reasoning-history lookup.
// Nil options or fields use the default conversion.
type ResponsesToChatOptions struct {
	// ReasoningContentByID returns cached plaintext for a reasoning item ID.
	// This restores reasoning_content for DeepSeek when a client replays encrypted-only items.
	// Return an empty string on a miss; conversion continues without cached reasoning.
	ReasoningContentByID func(itemID string) string
}

// NamespacedToolName identifies a child tool by namespace and unqualified name.
type NamespacedToolName struct {
	Namespace string
	Name      string
}

// ChatCompletionsToResponsesStreamState tracks conversion of Chat SSE chunks
// into Responses SSE events.
type ChatCompletionsToResponsesStreamState struct {
	ResponseID     string
	Model          string
	Created        int64
	ServiceTier    string // upstream Chat chunk service_tier, echoed on response events
	SequenceNumber int
	CreatedSent    bool
	CompletedSent  bool

	// nextOutputIndex allocates stream indexes in item-opening order.
	// Each item's delta and completion events reuse its allocated index.
	nextOutputIndex int

	// DeepSeek-style streams send reasoning before text. Open a separate reasoning
	// item and summary part before its first delta, then close them before
	// opening a message or tool item.
	ReasoningItemID string
	ReasoningIndex  int
	ReasoningOpen   bool
	ReasoningDone   bool

	// Message-item and output_text-part lifecycle.
	MessageItemID string
	MessageIndex  int
	TextPartOpen  bool

	Text      strings.Builder
	Reasoning strings.Builder

	// Tool-call lifecycle keyed by upstream tool_call index.
	ToolCalls       map[int]*ChatToolCall
	ToolItemIDs     map[int]string
	ToolOutputIndex map[int]int

	// CustomTools contains client custom/freeform tool names from CustomToolNames.
	// Matching calls use custom_tool_call events so Codex dispatches them to
	// the registered custom tool.
	CustomTools map[string]bool

	// FunctionTools contains declared top-level function names.
	FunctionTools map[string]bool

	// ToolSearchDeclared records a tool_search declaration from HasToolSearchTool.
	// Matching proxy calls become tool_search_call items; Codex executes that type
	// with execution=client.
	ToolSearchDeclared bool

	// NamespaceTools maps flattened child names to their original identities.
	// NamespaceToolNames supplies the map; restored calls carry separate namespace
	// and name fields for Codex dispatch.
	NamespaceTools map[string]NamespacedToolName

	// toolIsCustom freezes the classification at announcement so added/done items
	// use the same type.
	toolIsCustom map[int]bool

	// toolIsToolSearch records classification as a tool_search proxy call.
	toolIsToolSearch map[int]bool

	// toolNamespace records the namespace resolved when the call is announced.
	toolNamespace map[int]NamespacedToolName

	// toolAnnounced records whether output_item.added was emitted. Declarations
	// requiring name-based classification delay announcement until the name arrives.
	toolAnnounced map[int]bool

	FinishReason string
	Usage        *ResponsesUsage
}

// ResponsesToChatCompletionsRequestWithOptions converts a request with optional reasoning lookup.
func ResponsesToChatCompletionsRequestWithOptions(req *ResponsesRequest, opts *ResponsesToChatOptions) (*ChatCompletionsRequest, error) {
	if req == nil {
		return nil, fmt.Errorf("responses request is nil")
	}

	messages, err := responsesInputToChatMessagesWithOptions(req.Instructions, req.Input, opts)
	if err != nil {
		return nil, err
	}

	out := &ChatCompletionsRequest{
		Model:               req.Model,
		Messages:            messages,
		MaxCompletionTokens: req.MaxOutputTokens,
		Temperature:         req.Temperature,
		TopP:                req.TopP,
		Stream:              req.Stream,
		ServiceTier:         req.ServiceTier,
		ParallelToolCalls:   req.ParallelToolCalls,
	}
	if req.Reasoning != nil {
		out.ReasoningEffort = req.Reasoning.Effort
	}
	effectiveTools, err := EffectiveResponsesTools(req)
	if err != nil {
		return nil, err
	}
	if len(effectiveTools) > 0 {
		tools, err := responsesToolsToChatTools(effectiveTools)
		if err != nil {
			return nil, err
		}
		out.Tools = tools
	}
	// Omit tool_choice when no tools survive; upstreams reject a choice with no tools.
	// The choice converter also drops selections of omitted tools.
	if len(out.Tools) > 0 && len(req.ToolChoice) > 0 {
		declared := make(map[string]bool, len(out.Tools))
		for _, tool := range out.Tools {
			if tool.Function != nil {
				declared[tool.Function.Name] = true
			}
			if strings.EqualFold(strings.TrimSpace(tool.Type), "x_search") {
				declared["x_search"] = true
			}
		}
		if tc := responsesToolChoiceToChatToolChoice(req.ToolChoice, declared); len(tc) > 0 {
			out.ToolChoice = tc
		}
	}
	if req.Text != nil {
		out.ResponseFormat = responsesTextFormatToChatResponseFormat(req.Text.Format)
	}

	return out, nil
}

// EffectiveResponsesTools combines top-level tools, input additional_tools,
// and completed client tool-search discoveries. Chat upstreams need those
// runtime declarations in the outgoing tool list.
func EffectiveResponsesTools(req *ResponsesRequest) ([]ResponsesTool, error) {
	if req == nil {
		return nil, nil
	}

	tools := append([]ResponsesTool(nil), req.Tools...)
	inputRaw := bytesTrimSpace(req.Input)
	if len(inputRaw) == 0 || string(inputRaw) == "null" || inputRaw[0] != '[' {
		return tools, nil
	}

	var items []json.RawMessage
	if err := json.Unmarshal(inputRaw, &items); err != nil {
		return nil, fmt.Errorf("parse responses input for additional tools: %w", err)
	}
	for _, raw := range items {
		raw = bytesTrimSpace(raw)
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}
		var discriminator struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &discriminator); err != nil {
			return nil, fmt.Errorf("parse responses additional tools item: %w", err)
		}
		if discriminator.Type != "additional_tools" {
			continue
		}
		var item struct {
			Tools []ResponsesTool `json:"tools"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, fmt.Errorf("parse responses additional tools item: %w", err)
		}
		tools = append(tools, item.Tools...)
	}

	// Promote discoveries from completed client searches before converting to Chat.
	// Reuse the shared adapter's duplicate, conflict, namespace, and custom-tool
	// rules when importing those declarations.
	toolsRaw, err := json.Marshal(tools)
	if err != nil {
		return nil, fmt.Errorf("encode responses tools for discovery promotion: %w", err)
	}
	var rawTools, rawInput []any
	if err := json.Unmarshal(toolsRaw, &rawTools); err != nil {
		return nil, fmt.Errorf("decode responses tools for discovery promotion: %w", err)
	}
	if err := json.Unmarshal(inputRaw, &rawInput); err != nil {
		return nil, fmt.Errorf("parse responses input for discovery promotion: %w", err)
	}
	promoted, err := promotedResponsesToolSearchDiscoveries(rawTools, rawInput)
	if err != nil {
		return nil, err
	}
	if len(promoted) > 0 {
		promotedRaw, err := json.Marshal(promoted)
		if err != nil {
			return nil, fmt.Errorf("encode promoted responses tools: %w", err)
		}
		var discovered []ResponsesTool
		if err := json.Unmarshal(promotedRaw, &discovered); err != nil {
			return nil, fmt.Errorf("decode promoted responses tools: %w", err)
		}
		tools = append(tools, discovered...)
	}
	return tools, nil
}

// CustomToolNames collects custom/freeform tool names for restoring calls as
// custom_tool_call items, which Codex requires for custom-tool dispatch.
func CustomToolNames(tools []ResponsesTool) map[string]bool {
	var out map[string]bool
	for _, tool := range tools {
		if tool.Type == "custom" && tool.Name != "" {
			if out == nil {
				out = make(map[string]bool)
			}
			out[tool.Name] = true
		}
	}
	return out
}

// FunctionToolNames collects declared top-level function names.
func FunctionToolNames(tools []ResponsesTool) map[string]bool {
	var out map[string]bool
	for _, tool := range tools {
		if tool.Type == "function" && tool.Name != "" {
			if out == nil {
				out = make(map[string]bool)
			}
			out[tool.Name] = true
		}
	}
	return out
}

// NamespaceToolNames maps flattened names back to namespace and child names.
// Long names include a truncated hash and cannot be recovered by splitting.
// Callers must first validate declarations through namespaceChildrenToChatTools
// so conflicting flattened names have already been rejected.
func NamespaceToolNames(tools []ResponsesTool) map[string]NamespacedToolName {
	var out map[string]NamespacedToolName
	for _, tool := range tools {
		if tool.Type != "namespace" || tool.Name == "" {
			continue
		}
		children := tool.Tools
		if len(children) == 0 {
			children = tool.Children
		}
		for _, child := range children {
			if child.Type != "function" || child.Name == "" {
				continue
			}
			if out == nil {
				out = make(map[string]NamespacedToolName)
			}
			out[flattenNamespaceToolName(tool.Name, child.Name)] = NamespacedToolName{
				Namespace: tool.Name,
				Name:      child.Name,
			}
		}
	}
	return out
}

// customToolCallName recognizes custom names and namespace aliases inferred
// from neighboring tools, such as functions__exec beside functions__wait.
// Declared function/namespace names and ambiguous aliases keep function-call handling.
func customToolCallName(name string, customTools, functionTools map[string]bool, namespaceTools map[string]NamespacedToolName) (string, bool) {
	if functionTools[name] {
		return "", false
	}
	if customTools[name] {
		return name, true
	}
	if _, ok := namespaceTools[name]; ok {
		return "", false
	}
	match := ""
	for customName := range customTools {
		for _, namespaceTool := range namespaceTools {
			if flattenNamespaceToolName(namespaceTool.Namespace, customName) != name {
				continue
			}
			if match != "" && match != customName {
				return "", false
			}
			match = customName
		}
	}
	return match, match != ""
}

func customNameForStreamTool(state *ChatCompletionsToResponsesStreamState, name string) string {
	if customName, ok := customToolCallName(name, state.CustomTools, state.FunctionTools, state.NamespaceTools); ok {
		return customName
	}
	return name
}

// HasToolSearchTool reports whether tools declares tool_search. Response conversion
// uses it to restore tool_search_call with execution=client; Codex rejects the
// same call under function_call because its payload has the wrong type.
func HasToolSearchTool(tools []ResponsesTool) bool {
	for _, tool := range tools {
		if tool.Type == "tool_search" {
			return true
		}
	}
	return false
}

func responsesInputToChatMessagesWithOptions(instructions string, inputRaw json.RawMessage, opts *ResponsesToChatOptions) ([]ChatMessage, error) {
	var messages []ChatMessage
	if strings.TrimSpace(instructions) != "" {
		content, _ := json.Marshal(instructions)
		messages = append(messages, ChatMessage{Role: "system", Content: content})
	}

	inputRaw = bytesTrimSpace(inputRaw)
	if len(inputRaw) == 0 || string(inputRaw) == "null" {
		return messages, nil
	}

	// String input represents a single user turn.
	var inputText string
	if err := json.Unmarshal(inputRaw, &inputText); err == nil {
		content, _ := json.Marshal(inputText)
		messages = append(messages, ChatMessage{Role: "user", Content: content})
		return messages, nil
	}

	var rawItems []json.RawMessage
	if err := json.Unmarshal(inputRaw, &rawItems); err != nil {
		return nil, fmt.Errorf("parse responses input: %w", err)
	}

	built, mediaByCallID, err := buildChatMessagesFromItems(messages, rawItems, opts)
	if err != nil {
		return nil, err
	}
	return normalizeChatMessagesWithToolOutputMedia(built, mediaByCallID), nil
}

// buildChatMessagesFromItems appends Chat messages for Responses input items.
func buildChatMessagesFromItems(messages []ChatMessage, rawItems []json.RawMessage, opts *ResponsesToChatOptions) ([]ChatMessage, toolOutputMediaByCallID, error) {
	// pendingReasoning holds plaintext for the next assistant message or tool call.
	// DeepSeek thinking mode requires reasoning_content alongside assistant tool_calls
	// or returns 400. Consuming a message/call or crossing a non-assistant item
	// clears this pending value; lastTurnReasoning handles later calls in the turn.
	var pendingReasoning string
	// lastTurnReasoning survives tool output for chained calls that omit a repeated
	// reasoning item. New user input starts a fresh turn and clears it.
	var lastTurnReasoning string
	invalidFunctionCallIDs := make(map[string]struct{})
	invalidEmptyFunctionCallOutputs := 0
	reasoningForAssistant := func() string {
		if pendingReasoning != "" {
			return pendingReasoning
		}
		return lastTurnReasoning
	}
	mediaByCallID := make(toolOutputMediaByCallID)

	for _, raw := range rawItems {
		raw = bytesTrimSpace(raw)
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}

		var item map[string]json.RawMessage
		if err := json.Unmarshal(raw, &item); err != nil {
			var text string
			if textErr := json.Unmarshal(raw, &text); textErr == nil {
				content, _ := json.Marshal(text)
				messages = append(messages, ChatMessage{Role: "user", Content: content})
				pendingReasoning = ""
				lastTurnReasoning = ""
				continue
			}
			return nil, nil, fmt.Errorf("parse responses input item: %w", err)
		}

		role := chatCompletionsBridgeRole(rawString(item["role"]))
		itemType := rawString(item["type"])
		switch itemType {
		case "reasoning":
			if txt := extractResponsesReasoningText(item); txt != "" {
				pendingReasoning = txt
			} else if opts != nil && opts.ReasoningContentByID != nil {
				// Compaction may retain only opaque encrypted_content; recover plaintext by
				// item ID from the gateway cache for subsequent tool calls.
				if id := rawString(item["id"]); id != "" {
					if cached := opts.ReasoningContentByID(id); cached != "" {
						pendingReasoning = cached
					}
				}
			}
			if pendingReasoning != "" {
				lastTurnReasoning = pendingReasoning
			}
			continue
		case "function_call":
			arguments := rawString(item["arguments"])
			if strings.TrimSpace(arguments) == "" {
				arguments = "{}"
			}
			callID := rawString(item["call_id"])
			if !json.Valid([]byte(arguments)) {
				// Interrupted streams can leave truncated calls in client history. Drop both
				// the invalid call and its output so Chat upstreams do not repeatedly reject
				// the replayed history on later turns.
				if callID != "" {
					invalidFunctionCallIDs[callID] = struct{}{}
				} else {
					invalidEmptyFunctionCallOutputs++
				}
				pendingReasoning = ""
				continue
			}
			name := rawString(item["name"])
			// Flatten historical namespace calls with the same naming rule used for
			// declarations in namespaceChildrenToChatTools.
			if ns := rawString(item["namespace"]); ns != "" {
				name = flattenNamespaceToolName(ns, name)
			}
			toolCall := ChatToolCall{
				ID:   callID,
				Type: "function",
				Function: ChatFunctionCall{
					Name:      name,
					Arguments: arguments,
				},
			}
			messages = appendAssistantToolCall(messages, toolCall, reasoningForAssistant())
			pendingReasoning = ""
			continue
		case "tool_search_call":
			// tool_search arguments are a JSON object; its serialized form becomes the
			// adapted function call's arguments string.
			arguments := strings.TrimSpace(string(bytesTrimSpace(item["arguments"])))
			if s := rawString(item["arguments"]); s != "" {
				arguments = s
			}
			if arguments == "" || arguments == "null" {
				arguments = "{}"
			}
			toolCall := ChatToolCall{
				ID:   rawString(item["call_id"]),
				Type: "function",
				Function: ChatFunctionCall{
					Name:      toolSearchProxyName,
					Arguments: arguments,
				},
			}
			messages = appendAssistantToolCall(messages, toolCall, reasoningForAssistant())
			pendingReasoning = ""
			continue
		case "custom_tool_call":
			// Wrap historical freeform input in {"input": ...}, matching customToolInputSchema.
			// Using the same shape for history and declarations lets the model relate
			// past calls to the currently available function tool.
			arguments, _ := json.Marshal(map[string]string{"input": rawString(item["input"])})
			toolCall := ChatToolCall{
				ID:   rawString(item["call_id"]),
				Type: "function",
				Function: ChatFunctionCall{
					Name:      rawString(item["name"]),
					Arguments: string(arguments),
				},
			}
			messages = appendAssistantToolCall(messages, toolCall, reasoningForAssistant())
			pendingReasoning = ""
			continue
		case "function_call_output", "custom_tool_call_output", "tool_search_output":
			outputRaw := bytesTrimSpace(item["output"])
			if itemType == "tool_search_output" && (len(outputRaw) == 0 || string(outputRaw) == "null") {
				// Use tools[] as history when the client stores discoveries there.
				outputRaw = bytesTrimSpace(item["tools"])
			}
			callID := rawString(item["call_id"])
			if callID == "" && invalidEmptyFunctionCallOutputs > 0 {
				invalidEmptyFunctionCallOutputs--
				pendingReasoning = ""
				continue
			}
			if _, skipped := invalidFunctionCallIDs[callID]; skipped {
				pendingReasoning = ""
				continue
			}
			delete(mediaByCallID, callID)

			outputText, media, rewritten := extractToolOutputMedia(outputRaw)
			if rewritten {
				if callID != "" {
					mediaByCallID[callID] = media
				}
			} else {
				outputText = rawString(outputRaw)
				if outputText == "" && len(outputRaw) > 0 && string(outputRaw) != "null" && string(outputRaw) != `""` {
					// Serialize object/array outputs, including tool-search result lists, as text.
					outputText = string(outputRaw)
				}
			}
			content, _ := json.Marshal(outputText)
			messages = append(messages, ChatMessage{
				Role:       "tool",
				ToolCallID: callID,
				Content:    content,
			})
			pendingReasoning = ""
			continue
		case "input_text", "text":
			content, _ := json.Marshal(rawString(item["text"]))
			messages = append(messages, ChatMessage{Role: "user", Content: content})
			pendingReasoning = ""
			lastTurnReasoning = ""
			continue
		case "input_image":
			content, err := chatContentFromSingleResponsesPart(itemType, item)
			if err != nil {
				return nil, nil, err
			}
			messages = append(messages, ChatMessage{Role: "user", Content: content})
			pendingReasoning = ""
			lastTurnReasoning = ""
			continue
		}

		// Convert remaining items only when they are messages. Unsupported Responses
		// items such as web_search_call, local_shell_call, and file_search_call have
		// no Chat message equivalent and would break the adjacency of assistant
		// tool_calls and replies, causing upstream rejection.
		if itemType != "" && itemType != "message" {
			pendingReasoning = ""
			continue
		}

		content := item["content"]
		if len(bytesTrimSpace(content)) == 0 {
			if text := rawString(item["text"]); text != "" {
				content, _ = json.Marshal(text)
			}
		}
		chatContent, err := responsesContentToChatContent(content, role)
		if err != nil {
			return nil, nil, err
		}
		message := ChatMessage{Role: role, Content: chatContent}
		if role == "assistant" {
			message.ReasoningContent = reasoningForAssistant()
			pendingReasoning = ""
		} else {
			pendingReasoning = ""
			lastTurnReasoning = ""
		}
		messages = append(messages, message)
	}

	return messages, mediaByCallID, nil
}

// extractToolOutputMedia replaces recognized images with markers and returns their parts.
// It reports false when no media is rewritten so callers can retain the input bytes and cache prefix.
func extractToolOutputMedia(outputRaw json.RawMessage) (string, []ChatContentPart, bool) {
	outputRaw = bytesTrimSpace(outputRaw)
	if len(outputRaw) == 0 || string(outputRaw) == "null" {
		return "", nil, false
	}

	var outputString string
	if err := json.Unmarshal(outputRaw, &outputString); err == nil {
		if isToolOutputImageDataURL(outputString) {
			return toolOutputMediaMarker, []ChatContentPart{toolOutputImagePart(outputString)}, true
		}

		nested, ok := decodeToolOutputJSON([]byte(outputString))
		if !ok {
			return "", nil, false
		}
		rewritten, media, changed := rewriteToolOutputMediaValue(nested)
		if !changed {
			return "", nil, false
		}
		encoded, err := json.Marshal(rewritten)
		if err != nil {
			return "", nil, false
		}
		return string(encoded), media, true
	}

	value, ok := decodeToolOutputJSON(outputRaw)
	if !ok {
		return "", nil, false
	}
	rewritten, media, changed := rewriteToolOutputMediaValue(value)
	if !changed {
		return "", nil, false
	}
	encoded, err := json.Marshal(rewritten)
	if err != nil {
		return "", nil, false
	}
	return string(encoded), media, true
}

func decodeToolOutputJSON(raw []byte) (any, bool) {
	if !json.Valid(raw) {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	return value, true
}

func rewriteToolOutputMediaValue(value any) (any, []ChatContentPart, bool) {
	switch typed := value.(type) {
	case []any:
		var media []ChatContentPart
		changed := false
		for i, item := range typed {
			rewritten, itemMedia, itemChanged := rewriteToolOutputMediaValue(item)
			if !itemChanged {
				continue
			}
			typed[i] = rewritten
			media = append(media, itemMedia...)
			changed = true
		}
		return typed, media, changed
	case map[string]any:
		if imageURL, ok := recognizedToolOutputImageURL(typed); ok {
			return map[string]any{
				"type": "input_text",
				"text": toolOutputMediaMarker,
			}, []ChatContentPart{toolOutputImagePart(imageURL)}, true
		}

		content, ok := typed["content"]
		if !ok {
			return typed, nil, false
		}
		rewritten, media, changed := rewriteToolOutputMediaValue(content)
		if !changed {
			return typed, nil, false
		}
		typed["content"] = rewritten
		return typed, media, true
	default:
		return value, nil, false
	}
}

func recognizedToolOutputImageURL(value map[string]any) (string, bool) {
	partType, _ := value["type"].(string)
	if partType != "input_image" && partType != "image_url" {
		return "", false
	}

	switch imageURL := value["image_url"].(type) {
	case string:
		return imageURL, strings.TrimSpace(imageURL) != ""
	case map[string]any:
		url, _ := imageURL["url"].(string)
		return url, strings.TrimSpace(url) != ""
	default:
		return "", false
	}
}

func isToolOutputImageDataURL(value string) bool {
	const prefix = "data:image/"
	const separator = ";base64,"
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	separatorIndex := strings.Index(value[len(prefix):], separator)
	if separatorIndex <= 0 {
		return false
	}
	payloadIndex := len(prefix) + separatorIndex + len(separator)
	return payloadIndex < len(value)
}

func toolOutputImagePart(imageURL string) ChatContentPart {
	return ChatContentPart{
		Type:     "image_url",
		ImageURL: &ChatImageURL{URL: imageURL},
	}
}

// appendAssistantToolCall merges a call into the trailing assistant message,
// or creates one. Adjacent calls share that message before their tool replies.
func appendAssistantToolCall(messages []ChatMessage, toolCall ChatToolCall, pendingReasoning string) []ChatMessage {
	if n := len(messages); n > 0 && messages[n-1].Role == "assistant" {
		messages[n-1].ToolCalls = append(messages[n-1].ToolCalls, toolCall)
		if messages[n-1].ReasoningContent == "" {
			messages[n-1].ReasoningContent = pendingReasoning
		}
		return messages
	}
	return append(messages, ChatMessage{
		Role:             "assistant",
		ToolCalls:        []ChatToolCall{toolCall},
		ReasoningContent: pendingReasoning,
	})
}

// normalizeChatMessages places each retained assistant tool_calls message
// immediately before its matching tool replies, in call order, as required
// by DeepSeek and OpenAI Chat upstreams.
//
// It repairs these history shapes:
//   - Other messages between a tool-call message and its replies.
//   - Missing replies for some parallel calls or interrupted calls.
//   - Replies whose call is absent.
//
// Unanswered calls and emptied assistant messages are dropped. Tool messages
// with no call ID remain in place for passthrough compatibility.
func normalizeChatMessages(messages []ChatMessage) []ChatMessage {
	return normalizeChatMessagesWithToolOutputMedia(messages, nil)
}

func normalizeChatMessagesWithToolOutputMedia(messages []ChatMessage, mediaByCallID toolOutputMediaByCallID) []ChatMessage {
	// Index replies by call ID, keeping the last reply for duplicates.
	replies := make(map[string]ChatMessage)
	for _, m := range messages {
		if m.Role == "tool" && m.ToolCallID != "" {
			replies[m.ToolCallID] = m
		}
	}

	out := make([]ChatMessage, 0, len(messages))
	for _, m := range messages {
		switch {
		case m.Role == "tool":
			// Keep ID-less passthrough tool messages in place. Matching replies are emitted
			// beside their assistant call message; replies with an ID but no matching
			// call are discarded.
			if m.ToolCallID == "" {
				out = append(out, m)
			}
			continue
		case len(m.ToolCalls) > 0:
			kept := make([]ChatToolCall, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				if tc.ID == "" {
					continue
				}
				if _, ok := replies[tc.ID]; ok {
					kept = append(kept, tc)
				}
			}
			if len(kept) == 0 {
				// Without answered calls, retain text as an ordinary message or drop empty content.
				if isBlankChatContent(m.Content) {
					continue
				}
				m.ToolCalls = nil
				out = append(out, m)
				continue
			}
			m.ToolCalls = kept
			out = append(out, m)
			for _, tc := range kept {
				out = append(out, replies[tc.ID])
			}

			var mediaParts []ChatContentPart
			for _, tc := range kept {
				media := mediaByCallID[tc.ID]
				if len(media) == 0 {
					continue
				}
				mediaParts = append(mediaParts, ChatContentPart{
					Type: "text",
					Text: fmt.Sprintf(toolOutputMediaAttribution, tc.ID),
				})
				mediaParts = append(mediaParts, media...)
			}
			if len(mediaParts) > 0 {
				content, _ := json.Marshal(mediaParts)
				out = append(out, ChatMessage{Role: "user", Content: content})
			}
		default:
			out = append(out, m)
		}
	}
	return out
}

// isBlankChatContent reports whether content contains no extractable text.
func isBlankChatContent(raw json.RawMessage) bool {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" || string(raw) == `""` {
		return true
	}
	return chatMessageContentText(raw) == ""
}

// extractResponsesReasoningText reads summary text first, then content.
// The Chat-to-Responses converter stores reasoning_content in summary_text,
// so this order recovers the plaintext it emitted.
func extractResponsesReasoningText(item map[string]json.RawMessage) string {
	var parts []string
	collect := func(raw json.RawMessage) {
		raw = bytesTrimSpace(raw)
		if len(raw) == 0 || string(raw) == "null" {
			return
		}
		var arr []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &arr); err == nil {
			for _, p := range arr {
				if t := rawString(p["text"]); t != "" {
					parts = append(parts, t)
				}
			}
			return
		}
		if t := rawString(raw); t != "" {
			parts = append(parts, t)
		}
	}
	collect(item["summary"])
	if len(parts) == 0 {
		collect(item["content"])
	}
	return strings.Join(parts, "\n")
}

// ExtractResponsesReasoningItem returns a reasoning item's ID and available
// plaintext for the gateway history cache. Invalid or non-reasoning items
// return ok=false; a valid item can have empty ID or text.
func ExtractResponsesReasoningItem(raw json.RawMessage) (id string, text string, ok bool) {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", "", false
	}
	var item map[string]json.RawMessage
	if err := json.Unmarshal(raw, &item); err != nil || rawString(item["type"]) != "reasoning" {
		return "", "", false
	}
	return rawString(item["id"]), extractResponsesReasoningText(item), true
}

func chatCompletionsBridgeRole(role string) string {
	trimmed := strings.TrimSpace(role)
	if trimmed == "" {
		return "user"
	}
	if strings.EqualFold(trimmed, "developer") {
		return "system"
	}
	return role
}

func responsesContentToChatContent(raw json.RawMessage, role string) (json.RawMessage, error) {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		empty, _ := json.Marshal("")
		return empty, nil
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return raw, nil
	}

	var rawParts []json.RawMessage
	if err := json.Unmarshal(raw, &rawParts); err == nil {
		return responsesContentPartsToChatContent(rawParts, role)
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err == nil {
		return chatContentFromSingleResponsesPart(rawString(obj["type"]), obj)
	}

	return raw, nil
}

func responsesContentPartsToChatContent(rawParts []json.RawMessage, role string) (json.RawMessage, error) {
	var textParts []string
	var chatParts []ChatContentPart
	hasNonText := false

	for _, rawPart := range rawParts {
		var part map[string]json.RawMessage
		if err := json.Unmarshal(rawPart, &part); err != nil {
			continue
		}
		partType := rawString(part["type"])
		switch partType {
		case "input_text", "output_text", "text", "":
			text := rawString(part["text"])
			if text == "" {
				continue
			}
			textParts = append(textParts, text)
			chatParts = append(chatParts, ChatContentPart{Type: "text", Text: text})
		case "input_image", "image_url":
			imageURL := rawString(part["image_url"])
			if imageURL == "" {
				imageURL = rawNestedString(part["image_url"], "url")
			}
			if imageURL == "" {
				continue
			}
			hasNonText = true
			chatParts = append(chatParts, ChatContentPart{
				Type:     "image_url",
				ImageURL: &ChatImageURL{URL: imageURL},
			})
		}
	}

	if !hasNonText {
		joined, _ := json.Marshal(strings.Join(textParts, "\n\n"))
		return joined, nil
	}
	if role != "user" {
		joined, _ := json.Marshal(strings.Join(textParts, "\n\n"))
		return joined, nil
	}
	if len(chatParts) == 0 {
		empty, _ := json.Marshal("")
		return empty, nil
	}
	return json.Marshal(chatParts)
}

func chatContentFromSingleResponsesPart(partType string, part map[string]json.RawMessage) (json.RawMessage, error) {
	switch partType {
	case "input_image", "image_url":
		imageURL := rawString(part["image_url"])
		if imageURL == "" {
			imageURL = rawNestedString(part["image_url"], "url")
		}
		return json.Marshal([]ChatContentPart{{
			Type:     "image_url",
			ImageURL: &ChatImageURL{URL: imageURL},
		}})
	default:
		return json.Marshal(rawString(part["text"]))
	}
}

func responsesToolsToChatTools(tools []ResponsesTool) ([]ChatTool, error) {
	// Reject name collisions after flattening namespace children.
	// Responses distinguishes namespace and name; Chat dispatches by name alone.
	topLevel := make(map[string]bool)
	for _, tool := range tools {
		if (tool.Type == "function" || tool.Type == "custom") && tool.Name != "" {
			if topLevel[tool.Name] {
				return nil, fmt.Errorf("duplicate top-level executable tool name %q; this upstream cannot disambiguate duplicate names, rename one of the tools", tool.Name)
			}
			topLevel[tool.Name] = true
		}
	}
	flatOwner := make(map[string]NamespacedToolName)
	toolSearchDeclared := false
	out := make([]ChatTool, 0, len(tools))
	for _, tool := range tools {
		switch tool.Type {
		case "function":
			out = append(out, ChatTool{
				Type: "function",
				Function: &ChatFunction{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  tool.Parameters,
					Strict:      tool.Strict,
				},
			})
		case "custom":
			// Adapt custom tools such as Codex exec to a function the Chat model can call.
			out = append(out, ChatTool{
				Type: "function",
				Function: &ChatFunction{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  json.RawMessage(customToolInputSchema),
				},
			})
		case "tool_search":
			// Reject a normal tool named tool_search, which would be misclassified on return.
			// Repeated tool_search declarations share one proxy.
			if topLevel[toolSearchProxyName] {
				return nil, fmt.Errorf("built-in tool_search conflicts with a declared tool named %q; this upstream cannot disambiguate them, rename the tool", toolSearchProxyName)
			}
			if toolSearchDeclared {
				continue
			}
			toolSearchDeclared = true
			out = append(out, toolSearchProxyChatTool())
		case "namespace":
			flattened, err := namespaceChildrenToChatTools(tool, topLevel, flatOwner)
			if err != nil {
				return nil, err
			}
			out = append(out, flattened...)
		case "x_search":
			out = append(out, ChatTool{
				Type:                     "x_search",
				AllowedXHandles:          tool.AllowedXHandles,
				ExcludedXHandles:         tool.ExcludedXHandles,
				FromDate:                 tool.FromDate,
				ToDate:                   tool.ToDate,
				EnableImageUnderstanding: tool.EnableImageUnderstanding,
				EnableVideoUnderstanding: tool.EnableVideoUnderstanding,
			})
		}
		// Omit other types such as web_search and image_generation, for which this
		// Chat conversion has no tool representation.
	}
	return out, nil
}

func toolSearchProxyChatTool() ChatTool {
	return ChatTool{
		Type: "function",
		Function: &ChatFunction{
			Name:        toolSearchProxyName,
			Description: "Search and load Codex tools, plugins, connectors, and MCP namespaces for the current task.",
			Parameters:  json.RawMessage(toolSearchProxySchema),
		},
	}
}

// namespaceChildrenToChatTools flattens child functions with a namespace__ prefix.
// It rejects conflicting owners and deduplicates repeated namespace/name pairs.
func namespaceChildrenToChatTools(tool ResponsesTool, topLevel map[string]bool, flatOwner map[string]NamespacedToolName) ([]ChatTool, error) {
	if tool.Name == "" {
		return nil, nil
	}
	children := tool.Tools
	if len(children) == 0 {
		children = tool.Children
	}
	var out []ChatTool
	for _, child := range children {
		if child.Type != "function" || child.Name == "" {
			continue
		}
		flat := flattenNamespaceToolName(tool.Name, child.Name)
		entry := NamespacedToolName{Namespace: tool.Name, Name: child.Name}
		if topLevel[flat] {
			return nil, fmt.Errorf("namespace tool %q/%q flattens to %q which conflicts with a top-level tool of the same name; this upstream cannot disambiguate them, rename one of the tools", tool.Name, child.Name, flat)
		}
		if prev, ok := flatOwner[flat]; ok {
			if prev == entry {
				continue
			}
			return nil, fmt.Errorf("namespace tools %q/%q and %q/%q both flatten to %q; this upstream cannot disambiguate them, rename one of the tools", prev.Namespace, prev.Name, tool.Name, child.Name, flat)
		}
		flatOwner[flat] = entry
		out = append(out, ChatTool{
			Type: "function",
			Function: &ChatFunction{
				Name:        flat,
				Description: child.Description,
				Parameters:  child.Parameters,
				Strict:      child.Strict,
			},
		})
	}
	return out, nil
}

// flattenNamespaceToolName joins namespace and name; oversized names are shortened
// and suffixed with a SHA-256 prefix. Declaration validation checks collisions.
func flattenNamespaceToolName(namespace, name string) string {
	full := namespace + "__" + name
	if len(full) <= chatToolNameMaxLen {
		return full
	}
	sum := sha256.Sum256([]byte(full))
	suffix := "__" + hex.EncodeToString(sum[:4])
	prefixLen := chatToolNameMaxLen - len(suffix)
	var prefix strings.Builder
	for _, ch := range full {
		if prefix.Len()+len(string(ch)) > prefixLen {
			break
		}
		_, _ = prefix.WriteRune(ch)
	}
	return prefix.String() + suffix
}

// responsesToolChoiceToChatToolChoice converts a Responses choice to Chat format.
// Named choices are forwarded when their target remains in declared.
// Selections of dropped tools are removed to prevent unknown-tool upstream errors.
// A nil result means the caller should omit tool_choice.
func responsesToolChoiceToChatToolChoice(raw json.RawMessage, declared map[string]bool) json.RawMessage {
	var choice map[string]json.RawMessage
	if err := json.Unmarshal(raw, &choice); err != nil {
		// Pass through string choices such as auto, none, and required.
		return raw
	}
	var name string
	switch rawString(choice["type"]) {
	case "x_search":
		if !declared["x_search"] {
			return nil
		}
		out, err := json.Marshal(map[string]any{"type": "x_search"})
		if err != nil {
			return raw
		}
		return out
	case "tool_search":
		// responsesToolsToChatTools adapts tool_search to a function proxy with the
		// same name. Adapt the forced choice too so a required search does not
		// become optional model selection.
		name = toolSearchProxyName
	case "function", "custom":
		// Custom tools use function definitions here, so their choices must use that type too.
		name = rawString(choice["name"])
		if name == "" {
			name = rawNestedString(choice["function"], "name")
		}
		if name == "" {
			return raw
		}
	default:
		return nil
	}
	if !declared[name] {
		return nil
	}
	out, err := json.Marshal(map[string]any{
		"type": "function",
		"function": map[string]string{
			"name": name,
		},
	})
	if err != nil {
		return raw
	}
	return out
}

// extractCustomToolCallInput unwraps the input string from adapted arguments.
// Empty input or an empty object returns an empty string. Other malformed
// shapes return trimmed arguments for client validation and model retry.
func extractCustomToolCallInput(arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return ""
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		return trimmed
	}
	if raw, ok := obj["input"]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
		return trimmed
	}
	if len(obj) == 0 {
		return ""
	}
	return trimmed
}

// ChatCompletionsResponseToResponses converts the first non-streaming Chat choice.
// customTools and functionTools identify declared top-level tools for restoring
// custom_tool_call items. toolSearch enables tool_search_call restoration;
// namespaceTools restores child tools' namespace and unqualified name.
// The supplied model takes precedence over the upstream model.
func ChatCompletionsResponseToResponses(runtime Runtime, resp *ChatCompletionsResponse, model string, customTools, functionTools map[string]bool, toolSearch bool, namespaceTools map[string]NamespacedToolName) *ResponsesResponse {
	id := ""
	if resp != nil {
		id = resp.ID
	}
	if id == "" {
		id = generateResponsesID(runtime)
	}

	// Retain a positive upstream creation time; otherwise use the current time.
	createdAt := int64(0)
	if resp != nil {
		createdAt = resp.Created
	}
	if createdAt <= 0 {
		createdAt = runtime.Now().Unix()
	}

	out := &ResponsesResponse{
		ID:          id,
		Object:      "response",
		CreatedAt:   createdAt,
		Model:       model,
		Status:      "completed",
		ServiceTier: chatServiceTier(resp),
	}
	if resp == nil {
		out.Output = []ResponsesOutput{emptyResponsesMessageOutput(runtime)}
		return out
	}
	if out.Model == "" {
		out.Model = resp.Model
	}

	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		out.Output = chatMessageToResponsesOutput(runtime, choice.Message, customTools, functionTools, toolSearch, namespaceTools)
		if choice.FinishReason == "length" {
			out.Status = "incomplete"
			out.IncompleteDetails = &ResponsesIncompleteDetails{Reason: "max_output_tokens"}
		}
	}
	if len(out.Output) == 0 {
		out.Output = []ResponsesOutput{emptyResponsesMessageOutput(runtime)}
	}
	if resp.Usage != nil {
		out.Usage = ChatUsageToResponsesUsage(resp.Usage)
	}
	return out
}

func chatServiceTier(resp *ChatCompletionsResponse) string {
	if resp == nil {
		return ""
	}
	return resp.ServiceTier
}

func chatMessageToResponsesOutput(runtime Runtime, message ChatMessage, customTools, functionTools map[string]bool, toolSearch bool, namespaceTools map[string]NamespacedToolName) []ResponsesOutput {
	var outputs []ResponsesOutput
	reasoning := message.ReasoningText()
	if reasoning != "" {
		outputs = append(outputs, ResponsesOutput{
			Type: "reasoning",
			ID:   generateItemID(runtime),
			Summary: []ResponsesSummary{{
				Type: "summary_text",
				Text: reasoning,
			}},
		})
	}

	text := chatMessageContentText(message.Content)
	if text == "" && strings.TrimSpace(reasoning) != "" && len(message.ToolCalls) == 0 {
		text = reasoning
	}
	if text != "" || len(message.ToolCalls) == 0 {
		outputs = append(outputs, ResponsesOutput{
			Type: "message",
			ID:   generateItemID(runtime),
			Role: "assistant",
			Content: []ResponsesContentPart{{
				Type: "output_text",
				Text: text,
			}},
			Status: "completed",
		})
	}

	for _, toolCall := range message.ToolCalls {
		arguments := toolCall.Function.Arguments
		if strings.TrimSpace(arguments) == "" {
			arguments = "{}"
		}
		if customName, ok := customToolCallName(toolCall.Function.Name, customTools, functionTools, namespaceTools); ok {
			outputs = append(outputs, ResponsesOutput{
				Type:   "custom_tool_call",
				ID:     generateItemID(runtime),
				CallID: toolCall.ID,
				Name:   customName,
				Input:  extractCustomToolCallInput(arguments),
				Status: "completed",
			})
			continue
		}
		if toolSearch && toolCall.Function.Name == toolSearchProxyName {
			outputs = append(outputs, ResponsesOutput{
				Type:      "tool_search_call",
				ID:        generateItemID(runtime),
				CallID:    toolCall.ID,
				Arguments: arguments,
				Status:    "completed",
			})
			continue
		}
		// Skip function calls with invalid JSON arguments so truncated non-streaming
		// calls do not enter the next turn's history as completed calls.
		if !json.Valid([]byte(arguments)) {
			continue
		}
		if ns, ok := namespaceTools[toolCall.Function.Name]; ok {
			outputs = append(outputs, ResponsesOutput{
				Type:      "function_call",
				ID:        generateItemID(runtime),
				CallID:    toolCall.ID,
				Name:      ns.Name,
				Namespace: ns.Namespace,
				Arguments: arguments,
				Status:    "completed",
			})
			continue
		}
		outputs = append(outputs, ResponsesOutput{
			Type:      "function_call",
			ID:        generateItemID(runtime),
			CallID:    toolCall.ID,
			Name:      toolCall.Function.Name,
			Arguments: arguments,
			Status:    "completed",
		})
	}

	return outputs
}

func emptyResponsesMessageOutput(runtime Runtime) ResponsesOutput {
	return ResponsesOutput{
		Type:    "message",
		ID:      generateItemID(runtime),
		Role:    "assistant",
		Content: []ResponsesContentPart{{Type: "output_text", Text: ""}},
		Status:  "completed",
	}
}

func chatMessageContentText(raw json.RawMessage) string {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var parts []ChatContentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		var texts []string
		for _, part := range parts {
			if part.Type == "text" && part.Text != "" {
				texts = append(texts, part.Text)
			}
		}
		return strings.Join(texts, "\n\n")
	}
	return ""
}

// ChatUsageToResponsesUsage converts Chat token counts and cache details to Responses usage.
// It returns nil for nil input and derives the total when the upstream total is zero.
func ChatUsageToResponsesUsage(usage *ChatUsage) *ResponsesUsage {
	if usage == nil {
		return nil
	}
	out := &ResponsesUsage{
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		TotalTokens:  usage.TotalTokens,
	}
	if out.TotalTokens == 0 {
		out.TotalTokens = out.InputTokens + out.OutputTokens
	}
	if usage.PromptTokensDetails != nil && (usage.PromptTokensDetails.CachedTokens > 0 ||
		usage.PromptTokensDetails.CacheCreationTokens > 0 || usage.PromptTokensDetails.CacheWriteTokens > 0) {
		out.InputTokensDetails = &ResponsesInputTokensDetails{
			CachedTokens:        usage.PromptTokensDetails.CachedTokens,
			CacheCreationTokens: usage.PromptTokensDetails.CacheCreationTokens,
			CacheWriteTokens:    usage.PromptTokensDetails.CacheWriteTokens,
		}
		if usage.PromptTokensDetails.CacheWriteTokens > 0 {
			out.CacheCreationInputTokens = usage.PromptTokensDetails.CacheWriteTokens
		} else {
			out.CacheCreationInputTokens = usage.PromptTokensDetails.CacheCreationTokens
		}
	}
	return out
}

// NewChatCompletionsToResponsesStreamState initializes the stream converter.
func NewChatCompletionsToResponsesStreamState(runtime Runtime, model string) *ChatCompletionsToResponsesStreamState {
	return &ChatCompletionsToResponsesStreamState{
		ResponseID:       generateResponsesID(runtime),
		Model:            model,
		Created:          runtime.Now().Unix(),
		ToolCalls:        make(map[int]*ChatToolCall),
		ToolItemIDs:      make(map[int]string),
		ToolOutputIndex:  make(map[int]int),
		toolIsCustom:     make(map[int]bool),
		toolIsToolSearch: make(map[int]bool),
		toolNamespace:    make(map[int]NamespacedToolName),
		toolAnnounced:    make(map[int]bool),
	}
}

// ValidateToolCallArguments rejects nonempty invalid JSON for ordinary function calls.
// Empty arguments and custom/tool-search calls are accepted; callers validate before finalizing history.
func (state *ChatCompletionsToResponsesStreamState) ValidateToolCallArguments() error {
	if state == nil {
		return nil
	}
	for idx, toolCall := range state.ToolCalls {
		if toolCall == nil {
			continue
		}
		if state.toolIsCustom[idx] || state.toolIsToolSearch[idx] {
			continue
		}
		arguments := strings.TrimSpace(toolCall.Function.Arguments)
		if arguments == "" {
			continue
		}
		if !json.Valid([]byte(arguments)) {
			return fmt.Errorf("tool call %q (%s) arguments are invalid JSON", toolCall.ID, toolCall.Function.Name)
		}
	}
	return nil
}

func (state *ChatCompletionsToResponsesStreamState) allocOutputIndex() int {
	idx := state.nextOutputIndex
	state.nextOutputIndex++
	return idx
}

// ChatCompletionsChunkToResponsesEvents converts one Chat chunk into zero or
// more Responses events and updates the accumulated state.
func ChatCompletionsChunkToResponsesEvents(runtime Runtime,
	chunk *ChatCompletionsChunk,
	state *ChatCompletionsToResponsesStreamState,
) []ResponsesStreamEvent {
	if chunk == nil || state == nil {
		return nil
	}
	if chunk.ID != "" {
		state.ResponseID = chunk.ID
	}
	if state.Model == "" && chunk.Model != "" {
		state.Model = chunk.Model
	}
	if chunk.ServiceTier != "" {
		state.ServiceTier = chunk.ServiceTier
	}
	if chunk.Usage != nil {
		state.Usage = ChatUsageToResponsesUsage(chunk.Usage)
	}

	var events []ResponsesStreamEvent
	events = append(events, ensureChatToResponsesCreated(state)...)

	for _, choice := range chunk.Choices {
		// Open the reasoning item and summary part before its first delta.
		// Ignore empty initial deltas commonly emitted by upstreams.
		reasoning := choice.Delta.ReasoningText()
		if reasoning != nil && *reasoning != "" {
			events = append(events, ensureChatReasoningItem(runtime, state)...)
			_, _ = state.Reasoning.WriteString(*reasoning)
			events = append(events, chatToResponsesEvent(state, "response.reasoning_summary_text.delta", &ResponsesStreamEvent{
				OutputIndex:  state.ReasoningIndex,
				SummaryIndex: 0,
				Delta:        *reasoning,
				ItemID:       state.ReasoningItemID,
			}))
		}
		if choice.Delta.Content != nil && *choice.Delta.Content != "" {
			// Close reasoning before opening the message and output_text part
			// for the first text delta.
			events = append(events, closeChatReasoningItem(state)...)
			events = append(events, ensureChatToResponsesMessageItem(runtime, state)...)
			events = append(events, ensureChatToResponsesTextPart(state)...)
			_, _ = state.Text.WriteString(*choice.Delta.Content)
			events = append(events, chatToResponsesEvent(state, "response.output_text.delta", &ResponsesStreamEvent{
				OutputIndex:  state.MessageIndex,
				ContentIndex: 0,
				Delta:        *choice.Delta.Content,
				ItemID:       state.MessageItemID,
			}))
		}
		for _, toolCall := range choice.Delta.ToolCalls {
			idx := 0
			if toolCall.Index != nil {
				idx = *toolCall.Index
			}
			stored, ok := state.ToolCalls[idx]
			if !ok {
				// Close any open reasoning item before starting a tool call.
				events = append(events, closeChatReasoningItem(state)...)
				copyCall := toolCall
				if copyCall.ID == "" {
					copyCall.ID = generateItemID(runtime)
				}
				copyCall.Type = "function"
				// Accumulate first-chunk arguments through the same path as later fragments.
				// GLM/Zhipu may include ID, name, and arguments in the first chunk.
				copyCall.Function.Arguments = ""
				state.ToolCalls[idx] = &copyCall
				stored = &copyCall
				state.ToolItemIDs[idx] = generateItemID(runtime)
				state.ToolOutputIndex[idx] = state.allocOutputIndex()
			} else {
				if toolCall.ID != "" {
					stored.ID = toolCall.ID
				}
				if toolCall.Function.Name != "" {
					stored.Function.Name = toolCall.Function.Name
				}
			}
			events = append(events, announceChatToolItem(state, idx, stored, false)...)
			if toolCall.Function.Arguments != "" {
				stored.Function.Arguments += toolCall.Function.Arguments
				// Buffer until announcement when the name has not arrived. Custom-call JSON
				// wraps freeform input, so decode the complete arguments at finalization in
				// closeChatToolItems. Tool-search arguments are also delivered whole with
				// output_item.done.
				if state.toolAnnounced[idx] && !state.toolIsCustom[idx] && !state.toolIsToolSearch[idx] {
					events = append(events, chatToResponsesEvent(state, "response.function_call_arguments.delta", &ResponsesStreamEvent{
						OutputIndex: state.ToolOutputIndex[idx],
						ItemID:      state.ToolItemIDs[idx],
						Delta:       toolCall.Function.Arguments,
						CallID:      stored.ID,
						Name:        stored.Function.Name,
					}))
				}
			}
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			state.FinishReason = *choice.FinishReason
		}
	}

	return events
}

// FinalizeChatCompletionsResponsesStream closes open items and emits the terminal response once.
func FinalizeChatCompletionsResponsesStream(runtime Runtime, state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if state == nil || state.CompletedSent {
		return nil
	}
	var events []ResponsesStreamEvent
	events = append(events, ensureChatToResponsesCreated(state)...)

	// Close reasoning even when no text followed it.
	events = append(events, closeChatReasoningItem(state)...)
	events = append(events, synthesizeChatReasoningFallbackMessage(runtime, state)...)

	if state.MessageItemID != "" {
		if state.TextPartOpen {
			events = append(events, chatToResponsesEvent(state, "response.output_text.done", &ResponsesStreamEvent{
				OutputIndex:  state.MessageIndex,
				ContentIndex: 0,
				Text:         state.Text.String(),
				ItemID:       state.MessageItemID,
			}))
			events = append(events, chatToResponsesEvent(state, "response.content_part.done", &ResponsesStreamEvent{
				OutputIndex:  state.MessageIndex,
				ContentIndex: 0,
				ItemID:       state.MessageItemID,
				Part:         &ResponsesContentPart{Type: "output_text", Text: state.Text.String()},
			}))
		}
		events = append(events, chatToResponsesEvent(state, "response.output_item.done", &ResponsesStreamEvent{
			OutputIndex: state.MessageIndex,
			Item: &ResponsesOutput{
				Type:    "message",
				ID:      state.MessageItemID,
				Role:    "assistant",
				Content: []ResponsesContentPart{{Type: "output_text", Text: state.Text.String()}},
				Status:  "completed",
			},
		}))
	}

	// Complete function calls with both function_call_arguments.done and
	// output_item.done so Codex can execute them.
	events = append(events, closeChatToolItems(state)...)

	status := "completed"
	var incompleteDetails *ResponsesIncompleteDetails
	if state.FinishReason == "length" {
		status = "incomplete"
		incompleteDetails = &ResponsesIncompleteDetails{Reason: "max_output_tokens"}
	}

	state.CompletedSent = true
	events = append(events, chatToResponsesEvent(state, "response.completed", &ResponsesStreamEvent{
		Response: &ResponsesResponse{
			ID:                state.ResponseID,
			Object:            "response",
			CreatedAt:         state.Created,
			Model:             state.Model,
			Status:            status,
			ServiceTier:       state.ServiceTier,
			Output:            state.chatOutput(runtime),
			Usage:             state.Usage,
			IncompleteDetails: incompleteDetails,
		},
	}))
	return events
}

func ensureChatToResponsesCreated(state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if state.CreatedSent {
		return nil
	}
	state.CreatedSent = true
	return []ResponsesStreamEvent{chatToResponsesEvent(state, "response.created", &ResponsesStreamEvent{
		Response: &ResponsesResponse{
			ID:          state.ResponseID,
			Object:      "response",
			CreatedAt:   state.Created,
			Model:       state.Model,
			Status:      "in_progress",
			ServiceTier: state.ServiceTier,
			Output:      []ResponsesOutput{},
		},
	})}
}

// ensureChatReasoningItem opens the reasoning item and summary part before
// the first delta, providing the lifecycle Codex needs to display thinking.
func ensureChatReasoningItem(runtime Runtime, state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if state.ReasoningOpen || state.ReasoningDone {
		return nil
	}
	state.ReasoningOpen = true
	state.ReasoningItemID = generateItemID(runtime)
	state.ReasoningIndex = state.allocOutputIndex()
	return []ResponsesStreamEvent{
		chatToResponsesEvent(state, "response.output_item.added", &ResponsesStreamEvent{
			OutputIndex: state.ReasoningIndex,
			Item:        &ResponsesOutput{Type: "reasoning", ID: state.ReasoningItemID, Status: "in_progress"},
		}),
		chatToResponsesEvent(state, "response.reasoning_summary_part.added", &ResponsesStreamEvent{
			OutputIndex:  state.ReasoningIndex,
			SummaryIndex: 0,
			ItemID:       state.ReasoningItemID,
			Part:         &ResponsesContentPart{Type: "summary_text"},
		}),
	}
}

// closeChatReasoningItem emits reasoning_summary_text.done,
// reasoning_summary_part.done, and output_item.done for the open reasoning item.
func closeChatReasoningItem(state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if !state.ReasoningOpen {
		return nil
	}
	state.ReasoningOpen = false
	state.ReasoningDone = true
	reasoning := state.Reasoning.String()
	return []ResponsesStreamEvent{
		chatToResponsesEvent(state, "response.reasoning_summary_text.done", &ResponsesStreamEvent{
			OutputIndex:  state.ReasoningIndex,
			SummaryIndex: 0,
			Text:         reasoning,
			ItemID:       state.ReasoningItemID,
		}),
		chatToResponsesEvent(state, "response.reasoning_summary_part.done", &ResponsesStreamEvent{
			OutputIndex:  state.ReasoningIndex,
			SummaryIndex: 0,
			ItemID:       state.ReasoningItemID,
			Part:         &ResponsesContentPart{Type: "summary_text", Text: reasoning},
		}),
		chatToResponsesEvent(state, "response.output_item.done", &ResponsesStreamEvent{
			OutputIndex: state.ReasoningIndex,
			Item: &ResponsesOutput{
				Type:    "reasoning",
				ID:      state.ReasoningItemID,
				Status:  "completed",
				Summary: []ResponsesSummary{{Type: "summary_text", Text: reasoning}},
			},
		}),
	}
}

func synthesizeChatReasoningFallbackMessage(runtime Runtime, state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if state == nil ||
		state.MessageItemID != "" ||
		state.Text.Len() > 0 ||
		state.Reasoning.Len() == 0 ||
		len(state.ToolCalls) > 0 {
		return nil
	}

	text := state.Reasoning.String()
	if strings.TrimSpace(text) == "" {
		return nil
	}

	var events []ResponsesStreamEvent
	events = append(events, ensureChatToResponsesMessageItem(runtime, state)...)
	events = append(events, ensureChatToResponsesTextPart(state)...)
	_, _ = state.Text.WriteString(text)
	events = append(events, chatToResponsesEvent(state, "response.output_text.delta", &ResponsesStreamEvent{
		OutputIndex:  state.MessageIndex,
		ContentIndex: 0,
		Delta:        text,
		ItemID:       state.MessageItemID,
	}))
	return events
}

func ensureChatToResponsesMessageItem(runtime Runtime, state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if state.MessageItemID != "" {
		return nil
	}
	state.MessageItemID = generateItemID(runtime)
	state.MessageIndex = state.allocOutputIndex()
	return []ResponsesStreamEvent{chatToResponsesEvent(state, "response.output_item.added", &ResponsesStreamEvent{
		OutputIndex: state.MessageIndex,
		Item: &ResponsesOutput{
			Type:    "message",
			ID:      state.MessageItemID,
			Role:    "assistant",
			Status:  "in_progress",
			Content: []ResponsesContentPart{{Type: "output_text"}},
		},
	})}
}

func ensureChatToResponsesTextPart(state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if state.TextPartOpen {
		return nil
	}
	state.TextPartOpen = true
	return []ResponsesStreamEvent{chatToResponsesEvent(state, "response.content_part.added", &ResponsesStreamEvent{
		OutputIndex:  state.MessageIndex,
		ContentIndex: 0,
		ItemID:       state.MessageItemID,
		Part:         &ResponsesContentPart{Type: "output_text", Text: ""},
	})}
}

// announceChatToolItem emits output_item.added once the call can be classified.
// With name-dependent declarations, delay until the name arrives so added/done types agree.
// force permits finalization of unnamed calls as function_call items.
func announceChatToolItem(
	state *ChatCompletionsToResponsesStreamState,
	idx int,
	stored *ChatToolCall,
	force bool,
) []ResponsesStreamEvent {
	if state.toolAnnounced[idx] {
		return nil
	}
	if !force && stored.Function.Name == "" && (len(state.CustomTools) > 0 || len(state.FunctionTools) > 0 || state.ToolSearchDeclared || len(state.NamespaceTools) > 0) {
		return nil
	}
	state.toolAnnounced[idx] = true
	customName, isCustom := customToolCallName(stored.Function.Name, state.CustomTools, state.FunctionTools, state.NamespaceTools)
	isToolSearch := !isCustom && state.ToolSearchDeclared && stored.Function.Name == toolSearchProxyName
	state.toolIsCustom[idx] = isCustom
	state.toolIsToolSearch[idx] = isToolSearch
	itemType := "function_call"
	if isCustom {
		itemType = "custom_tool_call"
	}
	if isToolSearch {
		itemType = "tool_search_call"
	}
	// Namespace children use the function-call lifecycle but need their unqualified
	// name and namespace on added/done items for Codex dispatch.
	itemName, itemNamespace := stored.Function.Name, ""
	if isCustom {
		itemName = customName
	}
	if ns, ok := state.NamespaceTools[stored.Function.Name]; ok && !isCustom && !isToolSearch {
		state.toolNamespace[idx] = ns
		itemName, itemNamespace = ns.Name, ns.Namespace
	}
	events := []ResponsesStreamEvent{chatToResponsesEvent(state, "response.output_item.added", &ResponsesStreamEvent{
		OutputIndex: state.ToolOutputIndex[idx],
		Item: &ResponsesOutput{
			Type:      itemType,
			ID:        state.ToolItemIDs[idx],
			CallID:    stored.ID,
			Name:      itemName,
			Namespace: itemNamespace,
			Status:    "in_progress",
		},
	})}
	// Replay buffered function arguments after late announcement; custom/search inputs wait until finalization.
	if !isCustom && !isToolSearch && stored.Function.Arguments != "" {
		events = append(events, chatToResponsesEvent(state, "response.function_call_arguments.delta", &ResponsesStreamEvent{
			OutputIndex: state.ToolOutputIndex[idx],
			ItemID:      state.ToolItemIDs[idx],
			Delta:       stored.Function.Arguments,
			CallID:      stored.ID,
			Name:        stored.Function.Name,
		}))
	}
	return events
}

// closeChatToolItems emits completion events with the full call identity and input.
// Call indexes are expected to be contiguous from zero, as in Chat streams.
func closeChatToolItems(state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if len(state.ToolCalls) == 0 {
		return nil
	}
	var events []ResponsesStreamEvent
	for i := range len(state.ToolCalls) {
		toolCall, ok := state.ToolCalls[i]
		if !ok || toolCall == nil {
			continue
		}
		itemID, opened := state.ToolItemIDs[i]
		if !opened {
			continue
		}
		// Announce any pending call before closing it; unnamed calls use function_call.
		events = append(events, announceChatToolItem(state, i, toolCall, true)...)
		arguments := toolCall.Function.Arguments
		if strings.TrimSpace(arguments) == "" {
			arguments = "{}"
		}
		outputIndex := state.ToolOutputIndex[i]
		if state.toolIsCustom[i] {
			// Decode and emit the complete custom input here, after all argument fragments
			// have arrived in ChatCompletionsChunkToResponsesEvents.
			input := extractCustomToolCallInput(arguments)
			if input != "" {
				events = append(events, chatToResponsesEvent(state, "response.custom_tool_call_input.delta", &ResponsesStreamEvent{
					OutputIndex: outputIndex,
					ItemID:      itemID,
					Delta:       input,
				}))
			}
			events = append(events,
				chatToResponsesEvent(state, "response.custom_tool_call_input.done", &ResponsesStreamEvent{
					OutputIndex: outputIndex,
					ItemID:      itemID,
					CallID:      toolCall.ID,
					Name:        customNameForStreamTool(state, toolCall.Function.Name),
					Input:       input,
				}),
				chatToResponsesEvent(state, "response.output_item.done", &ResponsesStreamEvent{
					OutputIndex: outputIndex,
					Item: &ResponsesOutput{
						Type:   "custom_tool_call",
						ID:     itemID,
						CallID: toolCall.ID,
						Name:   customNameForStreamTool(state, toolCall.Function.Name),
						Input:  input,
						Status: "completed",
					},
				}),
			)
			continue
		}
		if state.toolIsToolSearch[i] {
			// Codex constructs tool_search calls from output_item.done, with complete
			// arguments on the item and no argument-delta events.
			events = append(events, chatToResponsesEvent(state, "response.output_item.done", &ResponsesStreamEvent{
				OutputIndex: outputIndex,
				Item: &ResponsesOutput{
					Type:      "tool_search_call",
					ID:        itemID,
					CallID:    toolCall.ID,
					Arguments: arguments,
					Status:    "completed",
				},
			}))
			continue
		}
		// Use the namespace identity recorded at announcement for the completed item too.
		name, namespace := toolCall.Function.Name, ""
		if ns, ok := state.toolNamespace[i]; ok {
			name, namespace = ns.Name, ns.Namespace
		}
		events = append(events,
			chatToResponsesEvent(state, "response.function_call_arguments.done", &ResponsesStreamEvent{
				OutputIndex: outputIndex,
				ItemID:      itemID,
				CallID:      toolCall.ID,
				Name:        name,
				Arguments:   arguments,
			}),
			chatToResponsesEvent(state, "response.output_item.done", &ResponsesStreamEvent{
				OutputIndex: outputIndex,
				Item: &ResponsesOutput{
					Type:      "function_call",
					ID:        itemID,
					CallID:    toolCall.ID,
					Name:      name,
					Namespace: namespace,
					Arguments: arguments,
					Status:    "completed",
				},
			}),
		)
	}
	return events
}

func (state *ChatCompletionsToResponsesStreamState) chatOutput(runtime Runtime) []ResponsesOutput {
	var outputs []ResponsesOutput
	if state.Reasoning.Len() > 0 {
		outputs = append(outputs, ResponsesOutput{
			Type: "reasoning",
			ID:   generateItemID(runtime),
			Summary: []ResponsesSummary{{
				Type: "summary_text",
				Text: state.Reasoning.String(),
			}},
		})
	}
	if state.MessageItemID != "" || len(state.ToolCalls) == 0 {
		outputs = append(outputs, ResponsesOutput{
			Type: "message",
			ID:   nonEmpty(state.MessageItemID, generateItemID(runtime)),
			Role: "assistant",
			Content: []ResponsesContentPart{{
				Type: "output_text",
				Text: state.Text.String(),
			}},
			Status: "completed",
		})
	}
	for i := range len(state.ToolCalls) {
		toolCall, ok := state.ToolCalls[i]
		if !ok || toolCall == nil {
			continue
		}
		arguments := toolCall.Function.Arguments
		if strings.TrimSpace(arguments) == "" {
			arguments = "{}"
		}
		if state.toolIsCustom[i] {
			outputs = append(outputs, ResponsesOutput{
				Type:   "custom_tool_call",
				ID:     generateItemID(runtime),
				CallID: toolCall.ID,
				Name:   customNameForStreamTool(state, toolCall.Function.Name),
				Input:  extractCustomToolCallInput(arguments),
				Status: "completed",
			})
			continue
		}
		if state.toolIsToolSearch[i] {
			outputs = append(outputs, ResponsesOutput{
				Type:      "tool_search_call",
				ID:        generateItemID(runtime),
				CallID:    toolCall.ID,
				Arguments: arguments,
				Status:    "completed",
			})
			continue
		}
		name, namespace := toolCall.Function.Name, ""
		if ns, ok := state.toolNamespace[i]; ok {
			name, namespace = ns.Name, ns.Namespace
		}
		outputs = append(outputs, ResponsesOutput{
			Type:      "function_call",
			ID:        generateItemID(runtime),
			CallID:    toolCall.ID,
			Name:      name,
			Namespace: namespace,
			Arguments: arguments,
			Status:    "completed",
		})
	}
	return outputs
}

func chatToResponsesEvent(
	state *ChatCompletionsToResponsesStreamState,
	eventType string,
	template *ResponsesStreamEvent,
) ResponsesStreamEvent {
	seq := state.SequenceNumber
	state.SequenceNumber++
	evt := *template
	evt.Type = eventType
	evt.SequenceNumber = seq
	return evt
}

func rawString(raw json.RawMessage) string {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}

func rawNestedString(raw json.RawMessage, key string) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	return rawString(obj[key])
}

func bytesTrimSpace(raw json.RawMessage) json.RawMessage {
	return json.RawMessage(strings.TrimSpace(string(raw)))
}

func nonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
