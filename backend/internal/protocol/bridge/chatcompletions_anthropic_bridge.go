package bridge

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Messages history mixes text, thinking, media, and tool blocks inside roles,
// while Chat carries reasoning and function calls in separate message fields.
// Direct request conversion splits tool results from user media, replays
// plaintext thinking only on assistant tool messages, and normalizes answered
// call/result adjacency. Tool choices are limited to retained declarations,
// with sampling and max-effort behavior supplied through conversion options.
//
// Chat response streams can deliver argument fragments before their call names
// or IDs. Text/thinking blocks emit as they arrive, but calls are buffered by
// tool index and emitted in sorted order during finalization. Each retained
// call then gets start, joined-argument delta, and stop before message
// termination. Joining does not validate JSON or required call metadata. All
// choices share this state, usage observations replace whole counters, and
// finish evidence plus any tool observation determines the final stop reason.

// ChatCompletionsToAnthropicStreamState tracks direct conversion of Chat SSE
// chunks into Anthropic SSE events.
type ChatCompletionsToAnthropicStreamState struct {
	// MessageStartSent and MessageStopSent govern one-time start/finalization.
	// Chunk processing still mutates state after stop, so callers end input
	// before finalization and do not treat these flags as a global dispatch guard.
	MessageStartSent bool // message start already produced
	MessageStopSent  bool // message termination already produced

	// ContentBlockIndex, ContentBlockOpen, and CurrentBlockType describe one
	// immediate text/thinking block. Closing retains the old event index before
	// advancing; buffered tool blocks use subsequent positions at finalization.
	ContentBlockIndex int    // current or next block position
	ContentBlockOpen  bool   // immediate content block active
	CurrentBlockType  string // text or thinking
	// HasToolCall remains true after any accepted buffer observation, even when
	// name/arguments are absent. Stop selection consults it independently of
	// record validation.
	HasToolCall bool // any tool observation buffered

	// toolCalls and toolArgumentFragments share source tool-index keys across
	// all choices; absent index selects zero. Metadata can arrive late while
	// separate immutable strings retain argument fragments until final joining.
	toolCalls             map[int]*ChatToolCall // accumulated tool metadata records
	toolArgumentFragments map[int][]string      // ordered raw fragments per tool

	// FinishReason retains the last nonempty label across chunks and choices.
	// Finalization combines it with HasToolCall, giving exact length precedence.
	FinishReason string // retained nonempty finish evidence

	// InputTokens, OutputTokens, CacheReadInputTokens, and CacheCreationInputTokens
	// are replaced together from each present chunk usage. Zero observations
	// therefore replace earlier counts, including empty-choice usage chunks.
	InputTokens              int // uncached input token count
	OutputTokens             int // selected output token count
	CacheReadInputTokens     int // cached input read count
	CacheCreationInputTokens int // cached input write count

	// ResponseID and Model feed message_start. Nonempty chunk identity replaces
	// generated identity, while a nonempty chosen model wins over later source models.
	ResponseID string // current message response identity
	Model      string // selected client message model
	Created    int64  // captured Unix seconds
}

// AnthropicToChatCompletionsRequest converts Messages history directly to Chat
// fields, returning an error for nil input. Sampling pointers are retained
// unless options drop them; positive token limits receive the shared
// completion floor. System and message decode errors abort the request.
//
// Hosted web-search declarations are removed, while other tools become
// nonstrict functions with repaired parameters. Choice conversion runs only
// when tools remain, and unnamed/undeclared selections can be omitted while
// parallelism still follows a decoded disable flag. Effort defaults medium,
// max support controls max-to-xhigh mapping, and ultra is rejected. The
// request does not retain every source option or perform complete
// schema/history validation.
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
		// Positive completion limits use the shared Chat/Responses token floor.
		v := max(req.MaxTokens, minMaxOutputTokens)
		out.MaxCompletionTokens = &v
	}

	// Hosted search has no retained function declaration on this direct route.
	// Other tool schemas use the same parameter repair as Responses function
	// conversion.
	if len(req.Tools) > 0 {
		tools := anthropicToolsToChatTools(req.Tools)
		if len(tools) > 0 {
			out.Tools = tools
		}
	}

	parallelToolCalls := true
	// A named selector needs a retained function identity after tool filtering.
	// Decode selection only when tools remain, and carry its disable flag into
	// the separate parallel field.
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

	// output_config selects effort independently of thinking budgets. Normalize
	// it with supplied max support and reject ultra before producing the outbound
	// Chat fields.
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

// anthropicToChatMessages appends accepted system text before per-message
// conversion, then normalizes call/result history. Billing-header system text
// is filtered by the shared parser; invalid system or message decoding returns
// an error.
//
// Per-message expansion can put tool results before ordinary user content.
// Normalization then indexes replies, keeps the last duplicate, and places
// answered replies beside calls in call order. Unanswered calls and orphan
// replies with IDs are removed; ID-less tool messages remain. This helper
// delegates that repair rather than promising untouched source ordering or
// complete upstream request validation.
func anthropicToChatMessages(system json.RawMessage, msgs []AnthropicMessage) ([]ChatMessage, error) {
	var messages []ChatMessage

	// System text stays before conversation history. Reuse the billing-aware
	// parser so private system markers do not become ordinary Chat instruction text.
	if len(system) > 0 {
		sysParts, err := parseAnthropicSystemContentParts(system)
		if err != nil {
			return nil, err
		}
		if len(sysParts) > 0 {
			text := joinResponsesContentPartText(sysParts)
			if text != "" {
				// Joined system content contains only a constructed string. Its encoder
				// cannot fail from unsupported values or malformed raw argument members.
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

// anthropicMsgToChatMessages sends exact assistant roles through assistant
// conversion and every other role through user conversion. The selected
// converter's errors propagate, and unsupported source roles therefore become
// user/tool target messages.
func anthropicMsgToChatMessages(m AnthropicMessage) ([]ChatMessage, error) {
	switch m.Role {
	case "assistant":
		return anthropicAssistantToChatMessages(m.Content)
	default:
		// An unrecognized role can still contain accepted text, media, or results.
		// User conversion gives those parts a recognized target role and the same
		// result-splitting rules.
		return anthropicUserToChatMessages(m.Content)
	}
}

// anthropicUserToChatMessages converts string-decodable content to one user
// message, or separates block-array results from user text/images. Decode
// errors abort the result. Tool output text uses its source correlation ID,
// while extracted result images are held for later user content.
//
// Results are emitted first. Without usable images, ordinary text blocks join
// with blank lines into scalar content; with images, their part order is
// retained and result images follow ordinary parts. Unsupported or empty
// blocks can contribute no user message. The owning history normalizer can
// subsequently move or discard tool replies according to answered-call
// pairing.
func anthropicUserToChatMessages(raw json.RawMessage) ([]ChatMessage, error) {
	// A string becomes a single user message.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		// Scalar content is a decoded string, including null's empty value. Encoding
		// that string cannot fail through unsupported values or retained raw JSON.
		content, _ := json.Marshal(s)
		return []ChatMessage{{Role: "user", Content: content}}, nil
	}

	// When scalar decoding fails, block arrays retain distinct text/tool/media
	// fields. Decode that alternative before mapping supported fields to their
	// target representation.
	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}

	var out []ChatMessage
	var toolResultImageParts []ChatContentPart

	// Tool output stays scalar text with its correlation key. Hold extracted
	// images separately so the later user message still carries their visual content.
	for _, b := range blocks {
		if b.Type != "tool_result" {
			continue
		}
		text, imageParts := convertToolResultOutput(b)
		// Result/text extraction supplies a string, including its empty placeholder.
		// The discarded encoding error is nil for this supported scalar value.
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

	// Scalar text and multimodal arrays use distinct target shapes here. Keep
	// text-only content scalar; when usable images exist, retain ordered parts
	// and append extracted result images.
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
			// Paragraph joining produces one string for text-only content. Its encoding
			// cannot fail because no unsupported or raw-JSON value enters this scalar.
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

// anthropicAssistantToChatMessages returns one assistant message from scalar
// or block content. Scalar decoding wins, including null as empty text; block
// decode errors propagate. Nonempty text joins with paragraph separators, and
// tool uses retain IDs/names with empty-object argument text when input is
// absent.
//
// Plaintext thinking is copied into reasoning_content only when the converted
// message has tool calls. Signatures and redacted thinking do not enter that
// field, and supplied raw argument JSON is not validated here. Empty block
// content can still produce an empty assistant value for the owning history
// pass to handle.
func anthropicAssistantToChatMessages(raw json.RawMessage) ([]ChatMessage, error) {
	// A string becomes a single assistant message.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		// Scalar content is a decoded string, including null's empty value. Encoding
		// that string cannot fail through unsupported values or retained raw JSON.
		content, _ := json.Marshal(s)
		return []ChatMessage{{Role: "assistant", Content: content}}, nil
	}

	// When scalar decoding fails, block arrays retain distinct text/tool/media
	// fields. Decode that alternative before mapping supported fields to their
	// target representation.
	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}

	msg := ChatMessage{Role: "assistant"}
	text := extractAnthropicTextFromBlocks(blocks)
	if text != "" {
		// Accepted text blocks have already become one nonempty string. Its encoding
		// cannot fail through unsupported values or retained raw argument JSON.
		content, _ := json.Marshal(text)
		msg.Content = content
	}

	for _, b := range blocks {
		if b.Type != "tool_use" {
			continue
		}
		// Chat stores tool input as argument text. An absent input still needs an
		// object-shaped string, while supplied raw bytes remain available for
		// downstream validation.
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

// anthropicThinkingToReasoningContent joins nonempty exact thinking plaintext
// with newlines when hasToolCalls is true. False returns empty text regardless
// of block content; redacted/signature-only blocks have no selected plaintext,
// and whitespace-only nonempty thinking is retained.
//
// The outbound Chat-to-Messages block converter represents reasoning as
// thinking, so this selection restores its plaintext on replayed assistant
// tool messages. Ordinary assistant messages omit that replay field. It does
// not decode signatures, select a model, or validate upstream reasoning
// requirements; the caller derives hasToolCalls from its converted message.
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

// anthropicToolsToChatTools skips exact web_search-prefixed source types and
// converts every other declaration to a nonstrict Chat function.
// Names/descriptions remain supplied, parameters receive partial
// normalization, and unfamiliar kinds or blank names are not independently
// rejected. Unchanged schema bytes can share source storage, so callers
// preserve them during serialization.
func anthropicToolsToChatTools(tools []AnthropicTool) []ChatTool {
	var out []ChatTool
	// Hosted search is not retained as a client function on this direct route.
	// Convert other definitions with repaired parameters and leave their
	// required-field validation to the caller.
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

// convertAnthropicToolChoiceToChat translates decoded auto/any/none or
// declared named selectors and returns the inverse disable-parallel flag.
// Decode errors return an error with parallel allowed. Unknown kinds and
// empty/undeclared exact names return nil choice while retaining decoded
// parallelism; strings and names are not trimmed.
func convertAnthropicToolChoiceToChat(raw json.RawMessage, declared map[string]bool) (json.RawMessage, bool, error) {
	var tc struct {
		// Type selects scalar or named conversion, while Name is used only for
		// exact tool kind. It must match a retained declaration literally; unknown
		// kinds or unmatched names omit choice while decoded parallelism still applies.
		Type string `json:"type"` // source tool selection kind
		Name string `json:"name"` // exact selected function name
		// DisableParallelToolUse is inverted even when decoded choice is omitted.
		// Missing/false allows parallel calls; the caller invokes this decoder only
		// when at least one retained declaration exists.
		DisableParallelToolUse bool `json:"disable_parallel_tool_use"` // source parallel-disable flag
	}
	if err := json.Unmarshal(raw, &tc); err != nil {
		return nil, true, err
	}
	allowParallel := !tc.DisableParallelToolUse

	// Scalar selectors use target vocabulary, and named selectors need a
	// surviving declaration. Return the separate parallel decision even when
	// selection is omitted.
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
		// Filtered tools have no matching function identity. Decline this named
		// selection so an unavailable declaration is not recreated by tool choice alone.
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

// joinResponsesContentPartText joins nonempty exact input_text parts with
// paragraph separators.
func joinResponsesContentPartText(parts []ResponsesContentPart) string {
	var texts []string
	for _, p := range parts {
		if p.Type == "input_text" && p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n\n")
}

// ChatCompletionsResponseToAnthropic converts only the first choice and its
// usage into a Messages reply. Nil or choice-free responses receive empty text
// and end-turn evidence. A supplied nonempty model wins; otherwise a nonnil
// response supplies model. Missing identity uses the runtime random callback.
//
// Reasoning/text/tool fields become ordered blocks through the message
// converter, including its reasoning-only text fallback and Read argument
// repair. Finish length outranks tool evidence, while other reasons can infer
// tool use from blocks. Source IDs and usage are retained through their
// respective conversions; later choices do not contribute, and argument/schema
// validity is not guaranteed by this response conversion.
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
		}
		if resp.Usage != nil {
			out.Usage = chatUsageToAnthropicUsage(resp.Usage)
		}
	}

	if len(out.Content) == 0 {
		out.Content = []AnthropicContentBlock{{Type: "text", Text: ""}}
	}
	// Nil or choice-free replies still use the final Messages stop field. Select
	// its modeled default from content rather than returning an empty stop
	// string.
	if AnthropicStopReasonString(out.StopReason) == "" {
		out.StopReason = AnthropicStopReasonPtr(chatFinishReasonToAnthropicStopReason("", out.Content))
	}
	// A source without identity still needs reply metadata. Generate its label
	// through the injected callback while retaining supplied IDs when they exist.
	if out.ID == "" {
		out.ID = generateResponsesID(runtime)
	}

	return out
}

// chatMessageToAnthropicBlocks emits selected reasoning first, visible text
// next, then tool uses in call order. With no tool calls, absent text receives
// nonblank reasoning as visible text; otherwise an empty text block still
// represents a textless ordinary reply.
//
// Calls preserve correlation after known legacy reversal, use an empty object
// for whitespace-only arguments, and apply the narrow Read pages repair. Their
// supplied types, names, and nonempty argument text are not validated.
// Reasoning becomes plaintext thinking with no replay signature synthesized,
// and unsupported content parts contribute only what the shared text extractor
// recognizes.
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
	// A nonblank reasoning-only ordinary reply otherwise has no visible text.
	// Reuse its reasoning in the text block while retaining the separate thinking
	// block.
	if text == "" && strings.TrimSpace(reasoning) != "" && len(message.ToolCalls) == 0 {
		text = reasoning
	}
	if text != "" || len(message.ToolCalls) == 0 {
		blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: text})
	}

	// Tool input stays a raw argument value apart from visible text. Convert
	// correlation/name together for each block so their pairing survives
	// independent content grouping.
	for _, toolCall := range message.ToolCalls {
		arguments := toolCall.Function.Arguments
		// Argument-free calls still need an object value inside tool_use input. Use
		// the placeholder for blank text; other raw forms remain available for
		// downstream validation.
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

// chatFinishReasonToAnthropicStopReason selects max_tokens for exact length
// and tool_use for exact tool_calls. Other reasons inspect every converted
// block for ordinary tool use; its presence selects tool_use even before later
// text.
//
// With no such block, stop/content_filter/unknown reasons use end_turn. The
// helper does not preserve Chat's content_filter vocabulary or verify
// successful source completion. This combination keeps first-choice finish
// evidence separate from the converted block order, and callers retain
// responsibility for source error classification.
func chatFinishReasonToAnthropicStopReason(reason string, blocks []AnthropicContentBlock) string {
	// Messages uses a different stop vocabulary. Exact length outranks tool
	// observations, while other labels can still describe a retained tool continuation.
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

// chatUsageToAnthropicUsage separates Chat prompt input into
// uncached/read/write Messages counters, returning zeros for nil usage. Prompt
// details supply cached input and positive cache_write_tokens before the
// creation alias; the aliases are not added together.
//
// Selected cached/creation values are subtracted from PromptTokens and the
// remainder is clamped to zero. Output preserves CompletionTokens, TotalTokens
// is unused, and negative cache/output values are not otherwise validated.
// This avoids double-counting overlapping write aliases while allowing callers
// to inspect retained cache buckets independently of the uncached floor.
func chatUsageToAnthropicUsage(usage *ChatUsage) AnthropicUsage {
	if usage == nil {
		return AnthropicUsage{}
	}

	cachedTokens := 0
	cacheCreationTokens := 0
	if usage.PromptTokensDetails != nil {
		cachedTokens = usage.PromptTokensDetails.CachedTokens
		// Write and creation describe overlapping cache input. Select positive write
		// first, otherwise creation, so subtraction uses one cache-write
		// observation.
		if usage.PromptTokensDetails.CacheWriteTokens > 0 {
			cacheCreationTokens = usage.PromptTokensDetails.CacheWriteTokens
		} else {
			cacheCreationTokens = usage.PromptTokensDetails.CacheCreationTokens
		}
	}

	// Prompt totals include cache input counted separately by Messages. Remove
	// selected read/write buckets once, clamping inconsistent excess at zero
	// uncached input.
	inputTokens := max(usage.PromptTokens-cachedTokens-cacheCreationTokens, 0)

	return AnthropicUsage{
		InputTokens:              inputTokens,
		OutputTokens:             usage.CompletionTokens,
		CacheReadInputTokens:     cachedTokens,
		CacheCreationInputTokens: cacheCreationTokens,
	}
}

// NewChatCompletionsToAnthropicStreamState generates initial identity,
// captures runtime time, and initializes call/fragment maps. Runtime callbacks
// are required. The supplied model wins over later chunks when nonempty;
// callers serialize one constructed state for each stream.
func NewChatCompletionsToAnthropicStreamState(runtime Runtime, model string) *ChatCompletionsToAnthropicStreamState {
	return &ChatCompletionsToAnthropicStreamState{
		ResponseID:            generateResponsesID(runtime),
		Model:                 model,
		Created:               runtime.Now().Unix(),
		toolCalls:             make(map[int]*ChatToolCall),
		toolArgumentFragments: make(map[int][]string),
	}
}

// ChatCompletionsChunkToAnthropicEvents consumes all choices into one
// constructed stream state and returns immediate text/thinking events. Nil
// chunk/state returns nothing. Nonempty chunk ID replaces identity, empty
// selected model takes a source model, and each present usage replaces all
// converted counters before choice processing.
//
// Tool fragments are buffered rather than emitted, and finish reason retains
// the last nonempty choice observation. Choice indexes do not separate
// records: equal tool indexes across choices share one call buffer.
// MessageStopSent is not checked here, so callers finish input before
// finalization and stop feeding a terminated stream. The runtime supplies any
// missing call identity during buffering.
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

	// Usage can arrive in a chunk with no choices. Replace counters before the
	// choice loop so finalization still retains that complete observed usage selection.
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
		// Selected reasoning text opens/reuses thinking before its delta is emitted.
		// Tool fragments remain buffered separately, so this visible block does not
		// consume tool identities.
		reasoning := choice.Delta.ReasoningText()
		if reasoning != nil && *reasoning != "" {
			events = append(events, ensureCCAnthropicThinkingBlock(state)...)
			events = append(events, ccAnthropicDelta(state, &AnthropicDelta{
				Type:     "thinking_delta",
				Thinking: *reasoning,
			})...)
		}

		// Text needs its own block kind/index. Stop any open thinking first so the
		// following visible delta cannot be attached to that reasoning lifecycle.
		if choice.Delta.Content != nil && *choice.Delta.Content != "" {
			events = append(events, closeCCAnthropicBlockIfOpen(state, "thinking")...)
			events = append(events, ensureCCAnthropicTextBlock(state)...)
			events = append(events, ccAnthropicDelta(state, &AnthropicDelta{
				Type: "text_delta",
				Text: *choice.Delta.Content,
			})...)
		}

		// Tool fragments can interleave independently of visible blocks. Keep their
		// index association until finalization emits joined arguments with each
		// stored identity.
		for _, toolCall := range choice.Delta.ToolCalls {
			bufferCCAnthropicToolCall(runtime, state, &toolCall)
		}

		if choice.FinishReason != nil && *choice.FinishReason != "" {
			state.FinishReason = *choice.FinishReason
		}
	}

	return events
}

// FinalizeChatCompletionsAnthropicStream emits message start if needed, closes
// text/thinking, emits buffered tools, then sends final usage/stop once. Nil
// or stopped state returns nothing; even a stream with no chunks receives
// start and termination.
//
// Tool emission uses sorted source indexes and joined raw fragments, while
// final stop reason combines retained finish evidence with HasToolCall.
// MessageStopSent is set after constructing the result, so transport delivery
// remains external. Buffered records remain in state, but the public finalizer
// prevents their repeat emission. This helper does not recover source errors
// or validate the completed argument JSON.
func FinalizeChatCompletionsAnthropicStream(runtime Runtime, state *ChatCompletionsToAnthropicStreamState) []AnthropicStreamEvent {
	if state == nil || state.MessageStopSent {
		return nil
	}

	var events []AnthropicStreamEvent
	if !state.MessageStartSent {
		events = append(events, ensureCCAnthropicMessageStart(state)...)
	}

	// Complete the active lifecycle before assigning another block position.
	// The stop retains its old index and the following start uses the advanced counter.
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

// ensureCCAnthropicMessageStart emits the current identity/model with empty
// content, null stop reason, and zero usage once. Nonnil state is required;
// stored final usage is deliberately deferred to message_delta rather than
// exposed in this start snapshot.
func ensureCCAnthropicMessageStart(state *ChatCompletionsToAnthropicStreamState) []AnthropicStreamEvent {
	if state.MessageStartSent {
		return nil
	}
	state.MessageStartSent = true
	return []AnthropicStreamEvent{{
		Type: "message_start",
		Message: &AnthropicResponse{
			ID:      state.ResponseID,
			Type:    "message",
			Role:    "assistant",
			Content: []AnthropicContentBlock{},
			Model:   state.Model,
			// A nil stop pointer encodes as JSON null, representing unset stop evidence
			// at message start before finalization supplies the terminal reason.
			StopReason: nil,
			Usage:      AnthropicUsage{InputTokens: 0, OutputTokens: 0},
		},
	}}
}

// ensureCCAnthropicThinkingBlock reuses an open thinking block or closes
// another kind before opening empty thinking. Consecutive reasoning fragments
// therefore share an index, while a transition from text emits its stop before
// the thinking start receives the advanced position.
//
// Nonnil state is required. The helper emits a start only and retains no
// reasoning text or signature; the owning chunk converter sends the visible
// delta afterward. Keeping that order prevents a fragment from addressing the
// preceding text lifecycle while avoiding another start for each continued
// thinking fragment.
func ensureCCAnthropicThinkingBlock(state *ChatCompletionsToAnthropicStreamState) []AnthropicStreamEvent {
	if state.ContentBlockOpen && state.CurrentBlockType == "thinking" {
		return nil
	}
	// Complete the active lifecycle before assigning another block position.
	// The stop retains its old index and the following start uses the advanced counter.
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

// ensureCCAnthropicTextBlock reuses open text or closes another active kind
// before opening empty text. Consecutive visible fragments therefore share one
// index, while a transition from thinking emits its stop before text acquires
// the advanced position.
//
// Nonnil state is required. The helper emits only the start, and the owning
// chunk converter emits the supplied visible delta afterward. No whole-text
// buffer or part identity is maintained here, so the block continues until a
// later kind transition or finalization closes it.
func ensureCCAnthropicTextBlock(state *ChatCompletionsToAnthropicStreamState) []AnthropicStreamEvent {
	if state.ContentBlockOpen && state.CurrentBlockType == "text" {
		return nil
	}
	// Complete the active lifecycle before assigning another block position.
	// The stop retains its old index and the following start uses the advanced counter.
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

// bufferCCAnthropicToolCall stores observations by exact supplied index, with
// absent index selecting zero. Nil state/call is ignored; accepted
// observations require initialized maps. New records shallow-copy metadata and
// generate missing ID; empty type selects function. Later nonempty
// ID/type/name replaces stored values.
//
// First and later arguments enter the same fragment slice rather than
// Function.Arguments, retaining early bytes until late metadata arrives.
// Finalization joins them and ignores stored Type when producing tool_use.
// Negative indexes are accepted and choice index is absent from the key.
// HasToolCall becomes true even for unnamed/argument-free observations, and
// metadata or JSON validity is not checked.
func bufferCCAnthropicToolCall(runtime Runtime, state *ChatCompletionsToAnthropicStreamState, toolCall *ChatToolCall) {
	if state == nil || toolCall == nil {
		return
	}
	// Missing indexes select one shared zero slot, including later observations.
	// Read the value as the key now; a retained Index pointer is only copied metadata.
	idx := 0
	if toolCall.Index != nil {
		idx = *toolCall.Index
	}

	stored, ok := state.toolCalls[idx]
	if !ok {
		copyCall := *toolCall
		// A new index can precede upstream identity. Supply a temporary generated
		// ID, then allow later nonempty ID/name observations to replace that record
		// metadata.
		if copyCall.ID == "" {
			copyCall.ID = generateItemID(runtime)
		}
		if copyCall.Type == "" {
			copyCall.Type = "function"
		}
		// First arguments belong in the fragment log too. Clear the copied string so
		// late metadata and later arguments share one deferred-join path at
		// finalization.
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
		// Preserve each arriving argument string as a fragment until finalization.
		// Deferred joining avoids constructing a full accumulated prefix at every
		// new fragment.
		state.toolArgumentFragments[idx] = append(state.toolArgumentFragments[idx], toolCall.Function.Arguments)
	}
	state.HasToolCall = true
}

// finalizeCCAnthropicToolCalls emits retained calls in sorted integer source-
// index order, assigning successive Messages block indexes. Nil/no-call state
// yields nothing and nil records are skipped. Missing record IDs are
// generated, whitespace-only joined arguments become an empty object, and Read
// receives its targeted repair.
//
// Each emitted block starts with empty-object input, receives one joined raw
// argument delta, and stops at that same index before the counter advances.
// Joining does not establish valid JSON or a usable name. Records are not
// cleared, so invoking this private helper again emits them again; the public
// finalizer owns one-time termination.
func finalizeCCAnthropicToolCalls(runtime Runtime, state *ChatCompletionsToAnthropicStreamState) []AnthropicStreamEvent {
	if state == nil || len(state.toolCalls) == 0 {
		return nil
	}

	idxs := make([]int, 0, len(state.toolCalls))
	for idx := range state.toolCalls {
		idxs = append(idxs, idx)
	}
	// Source indexes determine final tool order independently of fragment
	// arrival. Sorting keeps each complete start/delta/stop group together before
	// moving to another buffered call.
	sort.Ints(idxs)

	var events []AnthropicStreamEvent
	for _, idx := range idxs {
		toolCall := state.toolCalls[idx]
		if toolCall == nil {
			continue
		}
		// A retained record can still lack source correlation at emission. Generate
		// a label before constructing its tool block so the client receives an
		// identity field.
		if toolCall.ID == "" {
			toolCall.ID = generateItemID(runtime)
		}
		// Join one call's stored fragments only after all input has arrived.
		// Empty/blank text uses an object placeholder; nonempty JSON validity
		// remains a caller concern.
		arguments := strings.Join(state.toolArgumentFragments[idx], "")
		// Argument-free calls still need an object value inside tool_use input. Use
		// the placeholder for blank text; other raw forms remain available for
		// downstream validation.
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

// ccAnthropicDelta emits the supplied shared delta pointer at the current open
// block index. Nonnil state is required; closed state returns no events, and
// the helper does not validate delta kind or content.
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

// closeCCAnthropicBlockIfOpen delegates closure only for an open exact
// matching kind. Nonnil state is required; mismatched or closed blocks stay
// unchanged, letting the caller limit a transition to its selected active
// kind.
func closeCCAnthropicBlockIfOpen(state *ChatCompletionsToAnthropicStreamState, blockType string) []AnthropicStreamEvent {
	if !state.ContentBlockOpen || state.CurrentBlockType != blockType {
		return nil
	}
	return closeCCAnthropicBlock(state)
}

// closeCCAnthropicBlock emits stop at the current index, clears open/kind
// state, and advances the index. Nonnil state is required; already-closed
// state produces no event, and buffered tool records are unaffected.
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

// ccFinishReasonToAnthropicStopReason combines the final exact finish label
// with any tool observation. Length selects max_tokens and tool_calls selects
// tool_use independently of hasToolCall; stop and other labels consult that
// flag.
//
// The flag records accepted buffering, including an unnamed call or an
// externally nil record, so it does not prove valid emitted tool arguments.
// Without it, unrecognized/absent finish evidence uses end_turn. The helper
// chooses Messages vocabulary rather than preserving Chat content-filter or
// failure detail, which callers classify separately.
func ccFinishReasonToAnthropicStopReason(reason string, hasToolCall bool) string {
	// Messages uses a different stop vocabulary. Exact length outranks tool
	// observations, while other labels can still describe a retained tool continuation.
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
