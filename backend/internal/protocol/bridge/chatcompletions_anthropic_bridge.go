package bridge

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ChatCompletionsToAnthropicStreamState tracks direct conversion of Chat SSE
// chunks into Anthropic SSE events.
type ChatCompletionsToAnthropicStreamState struct {
	MessageStartSent bool
	MessageStopSent  bool

	// Lifecycle of the current text or thinking block.
	ContentBlockIndex int
	ContentBlockOpen  bool
	CurrentBlockType  string // text or thinking
	HasToolCall       bool

	// Buffer tool calls by upstream index because their argument fragments can interleave.
	// Emit complete blocks in index order at finalization so every delta precedes its stop.
	toolCalls             map[int]*ChatToolCall
	toolArgumentFragments map[int][]string

	// Sequential reasoning and text blocks share ContentBlockIndex.

	FinishReason string

	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int

	ResponseID string
	Model      string
	Created    int64
}

// AnthropicToChatCompletionsRequest converts Messages requests directly to Chat Completions.
// It maps messages, tools, and reasoning options without serializing an
// intermediate ResponsesRequest.
func AnthropicToChatCompletionsRequest(req *AnthropicRequest, options RequestOptions) (*ChatCompletionsRequest, error) {
	if req == nil {
		return nil, fmt.Errorf("anthropic request is nil")
	}

	messages, err := anthropicToChatMessages(req.System, req.Messages)
	if err != nil {
		return nil, err
	}

	out := &ChatCompletionsRequest{
		Model:    req.Model,
		Messages: messages,
		Stream:   req.Stream,
	}

	if !options.DropSampling && !options.DropTemperature {
		out.Temperature = req.Temperature
	}
	if !options.DropSampling && !options.DropTopP {
		out.TopP = req.TopP
	}

	if req.MaxTokens > 0 {
		v := max(req.MaxTokens, minMaxOutputTokens)
		out.MaxCompletionTokens = &v
	}

	// Anthropic input_schema is JSON Schema and can serve as function parameters.
	// Drop web_search tools, which have no corresponding Chat function definition.
	if len(req.Tools) > 0 {
		tools := anthropicToolsToChatTools(req.Tools)
		if len(tools) > 0 {
			out.Tools = tools
		}
	}

	parallelToolCalls := true
	// Forward tool_choice only for retained tools; a named choice must resolve to a
	// declared tool or strict upstreams reject it. Map disable_parallel_tool_use
	// to the top-level Chat parallel_tool_calls flag too.
	if len(out.Tools) > 0 && len(req.ToolChoice) > 0 {
		declared := make(map[string]bool, len(out.Tools))
		for _, tool := range out.Tools {
			if tool.Function != nil {
				declared[tool.Function.Name] = true
			}
		}
		tc, allowParallel, err := convertAnthropicToolChoiceToChat(req.ToolChoice, declared)
		if err != nil {
			return nil, fmt.Errorf("convert tool_choice: %w", err)
		}
		parallelToolCalls = allowParallel
		if len(tc) > 0 {
			out.ToolChoice = tc
		}
	}

	// Map output_config.effort using the target model's supported reasoning levels.
	effort := "medium"
	if req.OutputConfig != nil && req.OutputConfig.Effort != "" {
		effort = req.OutputConfig.Effort
	}
	if isUltraReasoningEffort(effort) {
		return nil, fmt.Errorf("reasoning effort %q is not supported", strings.TrimSpace(effort))
	}
	out.ReasoningEffort = mapAnthropicEffortToResponses(effort, options.SupportsMaxEffort)

	out.ParallelToolCalls = &parallelToolCalls

	return out, nil
}

// anthropicToChatMessages converts the system prompt and messages to Chat messages.
func anthropicToChatMessages(system json.RawMessage, msgs []AnthropicMessage) ([]ChatMessage, error) {
	var messages []ChatMessage

	// The shared parser accepts string/block prompts and removes billing headers.
	if len(system) > 0 {
		sysParts, err := parseAnthropicSystemContentParts(system)
		if err != nil {
			return nil, err
		}
		if len(sysParts) > 0 {
			text := joinResponsesContentPartText(sysParts)
			if text != "" {
				content, _ := json.Marshal(text)
				messages = append(messages, ChatMessage{Role: "system", Content: content})
			}
		}
	}

	for _, m := range msgs {
		converted, err := anthropicMsgToChatMessages(m)
		if err != nil {
			return nil, err
		}
		messages = append(messages, converted...)
	}

	return normalizeChatMessages(messages), nil
}

// anthropicMsgToChatMessages converts one Anthropic message into one or more Chat messages.
// Tool results become tool messages; user text/images and assistant tool calls keep their roles.
func anthropicMsgToChatMessages(m AnthropicMessage) ([]ChatMessage, error) {
	switch m.Role {
	case "assistant":
		return anthropicAssistantToChatMessages(m.Content)
	default: // Unknown roles are treated as user messages.
		return anthropicUserToChatMessages(m.Content)
	}
}

// anthropicUserToChatMessages accepts string or block-array user content.
// Tool results become separate tool messages; their images move into a subsequent
// user message so tool content can remain a string.
func anthropicUserToChatMessages(raw json.RawMessage) ([]ChatMessage, error) {
	// A string becomes a single user message.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		content, _ := json.Marshal(s)
		return []ChatMessage{{Role: "user", Content: content}}, nil
	}

	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}

	var out []ChatMessage
	var toolResultImageParts []ChatContentPart

	// Emit result text as tool messages and collect images for the later user message.
	for _, b := range blocks {
		if b.Type != "tool_result" {
			continue
		}
		text, imageParts := convertToolResultOutput(b)
		content, _ := json.Marshal(text)
		out = append(out, ChatMessage{
			Role:       "tool",
			Content:    content,
			ToolCallID: b.ToolUseID,
		})
		for _, ip := range imageParts {
			toolResultImageParts = append(toolResultImageParts, ChatContentPart{
				Type:     "image_url",
				ImageURL: &ChatImageURL{URL: ip.ImageURL},
			})
		}
	}

	// Join text-only content with blank lines; use a parts array when images are present.
	// Some Chat upstreams reject arrays containing only text.
	var textParts []string
	var parts []ChatContentPart
	hasImage := false
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				textParts = append(textParts, b.Text)
				parts = append(parts, ChatContentPart{Type: "text", Text: b.Text})
			}
		case "image":
			if uri := anthropicImageToDataURI(b.Source); uri != "" {
				hasImage = true
				parts = append(parts, ChatContentPart{
					Type:     "image_url",
					ImageURL: &ChatImageURL{URL: uri},
				})
			}
		}
	}
	if len(toolResultImageParts) > 0 {
		hasImage = true
		parts = append(parts, toolResultImageParts...)
	}

	if !hasImage {
		if len(textParts) > 0 {
			content, _ := json.Marshal(strings.Join(textParts, "\n\n"))
			out = append(out, ChatMessage{Role: "user", Content: content})
		}
		return out, nil
	}

	content, err := json.Marshal(parts)
	if err != nil {
		return nil, err
	}
	out = append(out, ChatMessage{Role: "user", Content: content})

	return out, nil
}

// anthropicAssistantToChatMessages combines text and tool_use into content and tool_calls.
// When tool calls are present, thinking blocks become reasoning_content.
func anthropicAssistantToChatMessages(raw json.RawMessage) ([]ChatMessage, error) {
	// A string becomes a single assistant message.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		content, _ := json.Marshal(s)
		return []ChatMessage{{Role: "assistant", Content: content}}, nil
	}

	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}

	msg := ChatMessage{Role: "assistant"}
	text := extractAnthropicTextFromBlocks(blocks)
	if text != "" {
		content, _ := json.Marshal(text)
		msg.Content = content
	}

	for _, b := range blocks {
		if b.Type != "tool_use" {
			continue
		}
		args := "{}"
		if len(b.Input) > 0 {
			args = string(b.Input)
		}
		msg.ToolCalls = append(msg.ToolCalls, ChatToolCall{
			ID:   b.ID,
			Type: "function",
			Function: ChatFunctionCall{
				Name:      b.Name,
				Arguments: args,
			},
		})
	}

	msg.ReasoningContent = anthropicThinkingToReasoningContent(blocks, len(msg.ToolCalls) > 0)

	return []ChatMessage{msg}, nil
}

// anthropicThinkingToReasoningContent restores Chat reasoning_content from thinking blocks.
//
// chatMessageToAnthropicBlocks emits reasoning as thinking blocks that clients
// replay in later turns. DeepSeek thinking mode requires reasoning_content on
// assistant messages with tool calls or rejects the request with 400.
// hasToolCalls limits replay to those messages, as pendingReasoning does in the Responses bridge.
//
// Redacted and signature-only blocks have no plaintext to join. Multiple blocks
// are joined with newlines, matching extractResponsesReasoningText.
func anthropicThinkingToReasoningContent(blocks []AnthropicContentBlock, hasToolCalls bool) string {
	if !hasToolCalls {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "thinking" && b.Thinking != "" {
			parts = append(parts, b.Thinking)
		}
	}
	return strings.Join(parts, "\n")
}

// anthropicToolsToChatTools maps Anthropic tools to Chat function definitions
// and drops web_search tools.
func anthropicToolsToChatTools(tools []AnthropicTool) []ChatTool {
	var out []ChatTool
	for _, t := range tools {
		if strings.HasPrefix(t.Type, "web_search") {
			continue
		}
		out = append(out, ChatTool{
			Type: "function",
			Function: &ChatFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  normalizeToolParameters(t.InputSchema),
				Strict:      boolPtr(false),
			},
		})
	}
	return out
}

// convertAnthropicToolChoiceToChat returns the Chat choice and whether parallel calls are allowed.
// It returns a nil choice for unknown types or names absent from declared.
//
//	{"type":"auto"}            → "auto"
//	{"type":"any"}             → "required"
//	{"type":"none"}            → "none"
//	{"type":"tool","name":"X"} -> {"type":"function","function":{"name":"X"}} when X is declared
func convertAnthropicToolChoiceToChat(raw json.RawMessage, declared map[string]bool) (json.RawMessage, bool, error) {
	var tc struct {
		Type                   string `json:"type"`
		Name                   string `json:"name"`
		DisableParallelToolUse bool   `json:"disable_parallel_tool_use"`
	}
	if err := json.Unmarshal(raw, &tc); err != nil {
		return nil, true, err
	}
	allowParallel := !tc.DisableParallelToolUse

	switch tc.Type {
	case "auto":
		converted, err := json.Marshal("auto")
		return converted, allowParallel, err
	case "any":
		converted, err := json.Marshal("required")
		return converted, allowParallel, err
	case "none":
		converted, err := json.Marshal("none")
		return converted, allowParallel, err
	case "tool":
		if tc.Name == "" || !declared[tc.Name] {
			return nil, allowParallel, nil
		}
		converted, err := json.Marshal(map[string]any{
			"type":     "function",
			"function": map[string]string{"name": tc.Name},
		})
		return converted, allowParallel, err
	default:
		return nil, allowParallel, nil
	}
}

// joinResponsesContentPartText joins input_text parts with blank lines.
func joinResponsesContentPartText(parts []ResponsesContentPart) string {
	var texts []string
	for _, p := range parts {
		if p.Type == "input_text" && p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n\n")
}

// ChatCompletionsResponseToAnthropic converts the first Chat choice to a Messages response.
// The supplied model takes precedence over the upstream response model.
func ChatCompletionsResponseToAnthropic(runtime Runtime, resp *ChatCompletionsResponse, model string) *AnthropicResponse {
	out := &AnthropicResponse{
		Type:  "message",
		Role:  "assistant",
		Model: model,
	}

	if resp != nil {
		out.ID = resp.ID
		if out.Model == "" {
			out.Model = resp.Model
		}

		if len(resp.Choices) > 0 {
			choice := resp.Choices[0]
			out.Content = chatMessageToAnthropicBlocks(choice.Message)
			out.StopReason = AnthropicStopReasonPtr(chatFinishReasonToAnthropicStopReason(choice.FinishReason, out.Content))
			// Represent length as max_tokens in stop_reason.
		}
		if resp.Usage != nil {
			out.Usage = chatUsageToAnthropicUsage(resp.Usage)
		}
	}

	if len(out.Content) == 0 {
		out.Content = []AnthropicContentBlock{{Type: "text", Text: ""}}
	}
	// A nil response or empty choices still needs a final stop reason.
	if AnthropicStopReasonString(out.StopReason) == "" {
		out.StopReason = AnthropicStopReasonPtr(chatFinishReasonToAnthropicStopReason("", out.Content))
	}
	// Supply the response ID required by clients when the upstream omits it.
	if out.ID == "" {
		out.ID = generateResponsesID(runtime)
	}

	return out
}

// chatMessageToAnthropicBlocks converts reasoning, text, and tool calls into
// thinking, text, and tool_use blocks.
func chatMessageToAnthropicBlocks(message ChatMessage) []AnthropicContentBlock {
	var blocks []AnthropicContentBlock
	reasoning := message.ReasoningText()

	if reasoning != "" {
		blocks = append(blocks, AnthropicContentBlock{
			Type:     "thinking",
			Thinking: reasoning,
		})
	}

	text := chatMessageContentText(message.Content)
	// Expose reasoning as text when DeepSeek returns no text or tool calls.
	if text == "" && strings.TrimSpace(reasoning) != "" && len(message.ToolCalls) == 0 {
		text = reasoning
	}
	if text != "" || len(message.ToolCalls) == 0 {
		blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: text})
	}

	for _, toolCall := range message.ToolCalls {
		arguments := toolCall.Function.Arguments
		if strings.TrimSpace(arguments) == "" {
			arguments = "{}"
		}
		blocks = append(blocks, AnthropicContentBlock{
			Type:  "tool_use",
			ID:    fromResponsesCallID(toolCall.ID),
			Name:  toolCall.Function.Name,
			Input: sanitizeAnthropicToolUseInput(toolCall.Function.Name, arguments),
		})
	}

	return blocks
}

// chatFinishReasonToAnthropicStopReason maps Chat finish reasons to Anthropic stop reasons.
//
//	"length"     → "max_tokens"
//	"tool_calls" → "tool_use"
//	other values -> "tool_use" when a tool block exists, otherwise "end_turn"
//
// For stop, content_filter, and unknown reasons, infer completion from the blocks.
func chatFinishReasonToAnthropicStopReason(reason string, blocks []AnthropicContentBlock) string {
	switch reason {
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	default:
		if containsAnthropicToolUseBlock(blocks) {
			return "tool_use"
		}
		return "end_turn"
	}
}

// chatUsageToAnthropicUsage converts Chat token counts to Anthropic usage buckets.
func chatUsageToAnthropicUsage(usage *ChatUsage) AnthropicUsage {
	if usage == nil {
		return AnthropicUsage{}
	}

	cachedTokens := 0
	cacheCreationTokens := 0
	if usage.PromptTokensDetails != nil {
		cachedTokens = usage.PromptTokensDetails.CachedTokens
		// cache_write_tokens and cache_creation_tokens are aliases, so do not add them.
		// Prefer a positive write count, otherwise use creation.
		if usage.PromptTokensDetails.CacheWriteTokens > 0 {
			cacheCreationTokens = usage.PromptTokensDetails.CacheWriteTokens
		} else {
			cacheCreationTokens = usage.PromptTokensDetails.CacheCreationTokens
		}
	}

	inputTokens := max(usage.PromptTokens-cachedTokens-cacheCreationTokens, 0)

	return AnthropicUsage{
		InputTokens:              inputTokens,
		OutputTokens:             usage.CompletionTokens,
		CacheReadInputTokens:     cachedTokens,
		CacheCreationInputTokens: cacheCreationTokens,
	}
}

// NewChatCompletionsToAnthropicStreamState initializes a direct stream converter.
func NewChatCompletionsToAnthropicStreamState(runtime Runtime, model string) *ChatCompletionsToAnthropicStreamState {
	return &ChatCompletionsToAnthropicStreamState{
		ResponseID:            generateResponsesID(runtime),
		Model:                 model,
		Created:               runtime.Now().Unix(),
		toolCalls:             make(map[int]*ChatToolCall),
		toolArgumentFragments: make(map[int][]string),
	}
}

// ChatCompletionsChunkToAnthropicEvents converts a Chat chunk and updates state, returning zero or more events.
func ChatCompletionsChunkToAnthropicEvents(runtime Runtime,
	chunk *ChatCompletionsChunk,
	state *ChatCompletionsToAnthropicStreamState,
) []AnthropicStreamEvent {
	if chunk == nil || state == nil {
		return nil
	}
	if chunk.ID != "" {
		state.ResponseID = chunk.ID
	}
	if state.Model == "" && chunk.Model != "" {
		state.Model = chunk.Model
	}

	// Save usage before examining choices; include_usage often arrives in an empty-choice chunk.
	if chunk.Usage != nil {
		u := chatUsageToAnthropicUsage(chunk.Usage)
		state.InputTokens = u.InputTokens
		state.OutputTokens = u.OutputTokens
		state.CacheReadInputTokens = u.CacheReadInputTokens
		state.CacheCreationInputTokens = u.CacheCreationInputTokens
	}

	var events []AnthropicStreamEvent
	events = append(events, ensureCCAnthropicMessageStart(state)...)

	for _, choice := range chunk.Choices {
		// Emit reasoning content as thinking deltas.
		reasoning := choice.Delta.ReasoningText()
		if reasoning != nil && *reasoning != "" {
			events = append(events, ensureCCAnthropicThinkingBlock(state)...)
			events = append(events, ccAnthropicDelta(state, &AnthropicDelta{
				Type:     "thinking_delta",
				Thinking: *reasoning,
			})...)
		}

		// Close thinking before opening the text block.
		if choice.Delta.Content != nil && *choice.Delta.Content != "" {
			events = append(events, closeCCAnthropicBlockIfOpen(state, "thinking")...)
			events = append(events, ensureCCAnthropicTextBlock(state)...)
			events = append(events, ccAnthropicDelta(state, &AnthropicDelta{
				Type: "text_delta",
				Text: *choice.Delta.Content,
			})...)
		}

		// Buffer interleaved calls by index and emit complete tool blocks at finalization.
		for _, toolCall := range choice.Delta.ToolCalls {
			bufferCCAnthropicToolCall(runtime, state, &toolCall)
		}

		if choice.FinishReason != nil && *choice.FinishReason != "" {
			state.FinishReason = *choice.FinishReason
		}
	}

	return events
}

// FinalizeChatCompletionsAnthropicStream closes blocks and emits message_delta and message_stop once.
func FinalizeChatCompletionsAnthropicStream(runtime Runtime, state *ChatCompletionsToAnthropicStreamState) []AnthropicStreamEvent {
	if state == nil || state.MessageStopSent {
		return nil
	}

	var events []AnthropicStreamEvent
	if !state.MessageStartSent {
		events = append(events, ensureCCAnthropicMessageStart(state)...)
	}

	events = append(events, closeCCAnthropicBlock(state)...)
	events = append(events, finalizeCCAnthropicToolCalls(runtime, state)...)

	stopReason := ccFinishReasonToAnthropicStopReason(state.FinishReason, state.HasToolCall)

	events = append(events,
		AnthropicStreamEvent{
			Type: "message_delta",
			Delta: &AnthropicDelta{
				StopReason: stopReason,
			},
			Usage: &AnthropicUsage{
				InputTokens:              state.InputTokens,
				OutputTokens:             state.OutputTokens,
				CacheReadInputTokens:     state.CacheReadInputTokens,
				CacheCreationInputTokens: state.CacheCreationInputTokens,
			},
		},
		AnthropicStreamEvent{Type: "message_stop"},
	)
	state.MessageStopSent = true
	return events
}

// ensureCCAnthropicMessageStart emits message_start once before content events.
func ensureCCAnthropicMessageStart(state *ChatCompletionsToAnthropicStreamState) []AnthropicStreamEvent {
	if state.MessageStartSent {
		return nil
	}
	state.MessageStartSent = true
	return []AnthropicStreamEvent{{
		Type: "message_start",
		Message: &AnthropicResponse{
			ID:         state.ResponseID,
			Type:       "message",
			Role:       "assistant",
			Content:    []AnthropicContentBlock{},
			Model:      state.Model,
			StopReason: nil, // JSON null is required here; an empty string is invalid.
			Usage:      AnthropicUsage{InputTokens: 0, OutputTokens: 0},
		},
	}}
}

// ensureCCAnthropicThinkingBlock opens thinking, closing a different block if needed.
func ensureCCAnthropicThinkingBlock(state *ChatCompletionsToAnthropicStreamState) []AnthropicStreamEvent {
	if state.ContentBlockOpen && state.CurrentBlockType == "thinking" {
		return nil
	}
	events := closeCCAnthropicBlock(state)
	idx := state.ContentBlockIndex
	state.ContentBlockOpen = true
	state.CurrentBlockType = "thinking"
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_start",
		Index: &idx,
		ContentBlock: &AnthropicContentBlock{
			Type:     "thinking",
			Thinking: "",
		},
	})
	return events
}

// ensureCCAnthropicTextBlock opens text, closing a different block if needed.
func ensureCCAnthropicTextBlock(state *ChatCompletionsToAnthropicStreamState) []AnthropicStreamEvent {
	if state.ContentBlockOpen && state.CurrentBlockType == "text" {
		return nil
	}
	events := closeCCAnthropicBlock(state)
	idx := state.ContentBlockIndex
	state.ContentBlockOpen = true
	state.CurrentBlockType = "text"
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_start",
		Index: &idx,
		ContentBlock: &AnthropicContentBlock{
			Type: "text",
			Text: "",
		},
	})
	return events
}

// bufferCCAnthropicToolCall collects IDs, names, and argument fragments by upstream index.
// Arguments may precede the name or ID, or all fields may arrive in the first chunk.
func bufferCCAnthropicToolCall(runtime Runtime, state *ChatCompletionsToAnthropicStreamState, toolCall *ChatToolCall) {
	if state == nil || toolCall == nil {
		return
	}
	idx := 0
	if toolCall.Index != nil {
		idx = *toolCall.Index
	}

	stored, ok := state.toolCalls[idx]
	if !ok {
		copyCall := *toolCall
		if copyCall.ID == "" {
			copyCall.ID = generateItemID(runtime)
		}
		if copyCall.Type == "" {
			copyCall.Type = "function"
		}
		// Accumulate first-chunk arguments in the same path as subsequent fragments.
		copyCall.Function.Arguments = ""
		state.toolCalls[idx] = &copyCall
		stored = &copyCall
	} else {
		if toolCall.ID != "" {
			stored.ID = toolCall.ID
		}
		if toolCall.Type != "" {
			stored.Type = toolCall.Type
		}
		if toolCall.Function.Name != "" {
			stored.Function.Name = toolCall.Function.Name
		}
	}

	if toolCall.Function.Arguments != "" {
		// Join fragments once at finalization to avoid repeatedly copying large arguments.
		state.toolArgumentFragments[idx] = append(state.toolArgumentFragments[idx], toolCall.Function.Arguments)
	}
	state.HasToolCall = true
}

// finalizeCCAnthropicToolCalls emits complete tool_use blocks in upstream index order.
// Each block receives one complete JSON argument delta before its stop event.
func finalizeCCAnthropicToolCalls(runtime Runtime, state *ChatCompletionsToAnthropicStreamState) []AnthropicStreamEvent {
	if state == nil || len(state.toolCalls) == 0 {
		return nil
	}

	idxs := make([]int, 0, len(state.toolCalls))
	for idx := range state.toolCalls {
		idxs = append(idxs, idx)
	}
	sort.Ints(idxs)

	var events []AnthropicStreamEvent
	for _, idx := range idxs {
		toolCall := state.toolCalls[idx]
		if toolCall == nil {
			continue
		}
		if toolCall.ID == "" {
			toolCall.ID = generateItemID(runtime)
		}
		arguments := strings.Join(state.toolArgumentFragments[idx], "")
		if strings.TrimSpace(arguments) == "" {
			arguments = "{}"
		}
		arguments = string(sanitizeAnthropicToolUseInput(toolCall.Function.Name, arguments))

		blockIdx := state.ContentBlockIndex
		events = append(events,
			AnthropicStreamEvent{
				Type:  "content_block_start",
				Index: &blockIdx,
				ContentBlock: &AnthropicContentBlock{
					Type:  "tool_use",
					ID:    fromResponsesCallID(toolCall.ID),
					Name:  toolCall.Function.Name,
					Input: json.RawMessage("{}"),
				},
			},
			AnthropicStreamEvent{
				Type:  "content_block_delta",
				Index: &blockIdx,
				Delta: &AnthropicDelta{
					Type:        "input_json_delta",
					PartialJSON: arguments,
				},
			},
			AnthropicStreamEvent{
				Type:  "content_block_stop",
				Index: &blockIdx,
			},
		)
		state.ContentBlockIndex++
	}
	return events
}

// ccAnthropicDelta emits a delta for the open content block.
func ccAnthropicDelta(state *ChatCompletionsToAnthropicStreamState, delta *AnthropicDelta) []AnthropicStreamEvent {
	if !state.ContentBlockOpen {
		return nil
	}
	idx := state.ContentBlockIndex
	return []AnthropicStreamEvent{{
		Type:  "content_block_delta",
		Index: &idx,
		Delta: delta,
	}}
}

// closeCCAnthropicBlockIfOpen closes the current block when its type matches blockType.
func closeCCAnthropicBlockIfOpen(state *ChatCompletionsToAnthropicStreamState, blockType string) []AnthropicStreamEvent {
	if !state.ContentBlockOpen || state.CurrentBlockType != blockType {
		return nil
	}
	return closeCCAnthropicBlock(state)
}

// closeCCAnthropicBlock closes the current text or thinking block and advances its index.
func closeCCAnthropicBlock(state *ChatCompletionsToAnthropicStreamState) []AnthropicStreamEvent {
	if !state.ContentBlockOpen {
		return nil
	}
	idx := state.ContentBlockIndex
	state.ContentBlockOpen = false
	state.ContentBlockIndex++
	state.CurrentBlockType = ""
	return []AnthropicStreamEvent{{
		Type:  "content_block_stop",
		Index: &idx,
	}}
}

// ccFinishReasonToAnthropicStopReason maps the stream finish reason to the final Anthropic stop reason.
func ccFinishReasonToAnthropicStopReason(reason string, hasToolCall bool) string {
	switch reason {
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "stop":
		if hasToolCall {
			return "tool_use"
		}
		return "end_turn"
	default:
		if hasToolCall {
			return "tool_use"
		}
		return "end_turn"
	}
}
