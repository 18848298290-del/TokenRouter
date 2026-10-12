package bridge

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ChatCompletionsToAnthropicStreamState 保存 Chat SSE chunk 直转 Anthropic SSE event 的状态，
// 将原来的两个转换状态机合并为一个。
type ChatCompletionsToAnthropicStreamState struct {
	MessageStartSent bool
	MessageStopSent  bool

	// 当前文本或思考内容块的生命周期状态。
	ContentBlockIndex int
	ContentBlockOpen  bool
	CurrentBlockType  string // 内容块类型：text 或 thinking。
	HasToolCall       bool

	// 工具调用按上游 index 聚合。Chat Completions 允许多个工具的参数分片交错到达，
	// Anthropic 内容块则必须在 stop 前收到全部 delta，因此统一在流收尾时顺序输出。
	toolCalls             map[int]*ChatToolCall
	toolArgumentFragments map[int][]string

	// DeepSeek 风格 reasoning_content 先于正文到达；内容块按顺序生成，因此复用同一个 block index 计数器。

	FinishReason string

	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int

	ResponseID string
	Model      string
	Created    int64
}

// AnthropicToChatCompletionsRequest 直接把 Anthropic Messages 请求转换为 Chat Completions 请求。
// 结果等价于依次调用 AnthropicToResponses 和 ResponsesToChatCompletionsRequest，
// 但不会创建中间 ResponsesRequest，也省去额外的序列化往返。
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

	// Anthropic input_schema 本身就是 JSON Schema，可直接作为 Chat function parameters。
	// web_search_* 等服务端工具在 Chat Completions 中缺少对应表示，转换时丢弃。
	if len(req.Tools) > 0 {
		tools := anthropicToolsToChatTools(req.Tools)
		if len(tools) > 0 {
			out.Tools = tools
		}
	}

	parallelToolCalls := true
	// 只有转换后仍存在 tools 时才转发 tool_choice；具名选择还必须指向已声明工具，
	// 否则严格 Chat 上游会因未知工具返回 400。Anthropic 的并行禁用标记需要
	// 同时映射到 Chat Completions 顶层 parallel_tool_calls。
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

	// 根据模型映射 output_config.effort，推理档位由该字段决定。
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

// anthropicToChatMessages 把 Anthropic system 与消息列表直接转换为 ChatMessage。
func anthropicToChatMessages(system json.RawMessage, msgs []AnthropicMessage) ([]ChatMessage, error) {
	var messages []ChatMessage

	// parseAnthropicSystemContentParts 同时处理字符串和 block 数组，并过滤计费 header。
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

// anthropicMsgToChatMessages 把一条 Anthropic 消息转换为一条或多条 Chat 消息。
// tool_result 独立成为 tool 角色消息，text/image 留在 user 消息，assistant tool_use 转为 tool_calls。
func anthropicMsgToChatMessages(m AnthropicMessage) ([]ChatMessage, error) {
	switch m.Role {
	case "assistant":
		return anthropicAssistantToChatMessages(m.Content)
	default: // user 与未知角色均按 user 处理。
		return anthropicUserToChatMessages(m.Content)
	}
}

// anthropicUserToChatMessages 处理字符串或 block 数组形式的 Anthropic user 消息。
// tool_result 会拆成独立 tool 消息，其中的图片提升到后续 user 消息的 image_url；
// function_call_output 使用字符串，图片单独传递。
func anthropicUserToChatMessages(raw json.RawMessage) ([]ChatMessage, error) {
	// 纯字符串直接生成单条 user 消息。
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

	// tool_result 的文本先生成 tool 消息，图片延后并入 user 消息。
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

	// 剩余 text/image 组成 user 消息。纯文本用空行拼接为字符串，含图片时使用 parts 数组。
	// 部分 Chat 上游会拒绝没有图片的数组 content。
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

// anthropicAssistantToChatMessages 把文本与 tool_use 合并到 assistant 消息的 content/tool_calls；
// 同一消息包含工具调用时，将 thinking 块写入 reasoning_content。
func anthropicAssistantToChatMessages(raw json.RawMessage) ([]ChatMessage, error) {
	// 纯字符串直接生成单条 assistant 消息。
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

// anthropicThinkingToReasoningContent 将 thinking 块回填到 Chat Completions 的 reasoning_content 字段。
//
// chatMessageToAnthropicBlocks 出站时会把上游 reasoning_content 生成 thinking 块，
// 多轮客户端随后会原样回传；此处丢弃会使桥接丢失刚生成的内容。DeepSeek thinking 模式要求
// 产生工具调用的 assistant 消息回传 reasoning_content，否则响应 400；Responses→Chat 桥
// 已通过 pendingReasoning 实现同样行为。hasToolCalls 保证范围一致：reasoning 仅随工具调用传递。
//
// redacted_thinking 与仅含签名的占位块没有明文，不参与拼接；多个块用 "\n" 连接，
// 与 extractResponsesReasoningText 保持一致。
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

// anthropicToolsToChatTools 把 Anthropic 工具映射为 Chat function tools，
// 并丢弃没有 Chat Completions 等价表示的 web_search_* 等 server tool。
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

// convertAnthropicToolChoiceToChat 映射 Anthropic tool_choice；返回 nil 表示丢弃选择。
// 未知类型或指向未声明工具的具名选择返回 nil。
//
//	{"type":"auto"}            → "auto"
//	{"type":"any"}             → "required"
//	{"type":"none"}            → "none"
//	{"type":"tool","name":"X"} → {"type":"function","function":{"name":"X"}}（X 已声明）
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

// joinResponsesContentPartText 拼接 system prompt 中的 input_text parts。
func joinResponsesContentPartText(parts []ResponsesContentPart) string {
	var texts []string
	for _, p := range parts {
		if p.Type == "input_text" && p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n\n")
}

// ChatCompletionsResponseToAnthropic 直接把 Chat Completions 响应转换为 Anthropic Messages 响应，
// 结果等价于依次调用 ChatCompletionsResponseToResponses 和 ResponsesToAnthropic。
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
			// length 由 stop_reason 表达为 max_tokens，不需要 incomplete_details 字段。
		}
		if resp.Usage != nil {
			out.Usage = chatUsageToAnthropicUsage(resp.Usage)
		}
	}

	if len(out.Content) == 0 {
		out.Content = []AnthropicContentBlock{{Type: "text", Text: ""}}
	}
	// choices 为空或响应为 nil 时生成 end_turn，非流式完成响应需要有效的 stop_reason。
	if AnthropicStopReasonString(out.StopReason) == "" {
		out.StopReason = AnthropicStopReasonPtr(chatFinishReasonToAnthropicStopReason("", out.Content))
	}
	// 上游省略响应 ID 时生成一个 ID，客户端需要该字段。
	if out.ID == "" {
		out.ID = generateResponsesID(runtime)
	}

	return out
}

// chatMessageToAnthropicBlocks 把 reasoning、文本和 tool_calls 分别转换为
// Anthropic thinking、text 与 tool_use blocks。
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
	// DeepSeek 仅返回 reasoning 且没有 tool call 时，将 reasoning 同时写入可见文本。
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

// chatFinishReasonToAnthropicStopReason 把 Chat finish_reason 映射为 Anthropic stop_reason。
//
//	"length"     → "max_tokens"
//	"tool_calls" → "tool_use"
//	其它值       → "end_turn"（存在 tool_use block 时为 "tool_use"）
//
// stop、content_filter 和未知原因都按已完成响应处理，再根据 blocks 推导 stop_reason。
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

// chatUsageToAnthropicUsage 把 Chat token 用量转换为 Anthropic usage。
func chatUsageToAnthropicUsage(usage *ChatUsage) AnthropicUsage {
	if usage == nil {
		return AnthropicUsage{}
	}

	cachedTokens := 0
	cacheCreationTokens := 0
	if usage.PromptTokensDetails != nil {
		cachedTokens = usage.PromptTokensDetails.CachedTokens
		// cache_write_tokens 与 cache_creation_tokens 是同一数量的两种字段名，不能相加；
		// 优先读取 write，缺失时读取 creation。
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

// NewChatCompletionsToAnthropicStreamState 返回已初始化的直转流状态。
func NewChatCompletionsToAnthropicStreamState(runtime Runtime, model string) *ChatCompletionsToAnthropicStreamState {
	return &ChatCompletionsToAnthropicStreamState{
		ResponseID:            generateResponsesID(runtime),
		Model:                 model,
		Created:               runtime.Now().Unix(),
		toolCalls:             make(map[int]*ChatToolCall),
		toolArgumentFragments: make(map[int][]string),
	}
}

// ChatCompletionsChunkToAnthropicEvents 把一个 Chat 流式 chunk 转为零个或多个 Anthropic event 并更新状态。
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

	// include_usage 通常以空 choices 的独立 chunk 到达，先保存供 finalize 的 message_delta 使用。
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
		// reasoning content 写入 thinking block。
		reasoning := choice.Delta.ReasoningText()
		if reasoning != nil && *reasoning != "" {
			events = append(events, ensureCCAnthropicThinkingBlock(state)...)
			events = append(events, ccAnthropicDelta(state, &AnthropicDelta{
				Type:     "thinking_delta",
				Thinking: *reasoning,
			})...)
		}

		// 正文写入 text block，开始前先关闭 thinking block。
		if choice.Delta.Content != nil && *choice.Delta.Content != "" {
			events = append(events, closeCCAnthropicBlockIfOpen(state, "thinking")...)
			events = append(events, ensureCCAnthropicTextBlock(state)...)
			events = append(events, ccAnthropicDelta(state, &AnthropicDelta{
				Type: "text_delta",
				Text: *choice.Delta.Content,
			})...)
		}

		// tool calls 仅按 index 聚合；等流结束后再生成完整且不交错的 tool_use blocks。
		for _, toolCall := range choice.Delta.ToolCalls {
			bufferCCAnthropicToolCall(runtime, state, &toolCall)
		}

		if choice.FinishReason != nil && *choice.FinishReason != "" {
			state.FinishReason = *choice.FinishReason
		}
	}

	return events
}

// FinalizeChatCompletionsAnthropicStream 在流结束时关闭内容块并发出 message_delta/message_stop。
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

// ensureCCAnthropicMessageStart 在首次事件前发出 message_start。
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
			StopReason: nil, // 序列化为 JSON null，不能是空字符串。
			Usage:      AnthropicUsage{InputTokens: 0, OutputTokens: 0},
		},
	}}
}

// ensureCCAnthropicThinkingBlock 在需要时打开 thinking block。
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

// ensureCCAnthropicTextBlock 在需要时打开 text block。
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

// bufferCCAnthropicToolCall 按上游 index 聚合工具 ID、名称和参数分片。
// 首帧可能同时携带完整字段，也可能先到参数、后到名称或 ID。
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
		// 首帧参数和后续分片由下方代码一起累加。
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
		// 参数分片暂存到收尾时一次拼接，减少大参数复制。
		state.toolArgumentFragments[idx] = append(state.toolArgumentFragments[idx], toolCall.Function.Arguments)
	}
	state.HasToolCall = true
}

// finalizeCCAnthropicToolCalls 按工具 index 生成连续、闭合的 Anthropic tool_use blocks。
// 每个工具在自己的 block 内发送一个完整 JSON delta。
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

// ccAnthropicDelta 在当前内容块上发出 content_block_delta。
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

// closeCCAnthropicBlockIfOpen 仅在当前块类型匹配时关闭，用于打开 text/tool 前结束 thinking。
func closeCCAnthropicBlockIfOpen(state *ChatCompletionsToAnthropicStreamState, blockType string) []AnthropicStreamEvent {
	if !state.ContentBlockOpen || state.CurrentBlockType != blockType {
		return nil
	}
	return closeCCAnthropicBlock(state)
}

// closeCCAnthropicBlock 关闭当前文本或思考内容块。
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

// ccFinishReasonToAnthropicStopReason 把流中取得的 Chat finish_reason 映射为 message_delta 的 Anthropic stop_reason。
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
