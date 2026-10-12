package bridge

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ResponsesEventToAnthropicState tracks state for converting a sequence of
// Responses SSE events directly into Anthropic SSE events.
type ResponsesEventToAnthropicState struct {
	MessageStartSent bool
	MessageStopSent  bool

	ContentBlockIndex   int
	ContentBlockOpen    bool
	CurrentBlockType    string // "text" | "thinking" | "tool_use"
	CurrentToolName     string
	CurrentToolArgs     string
	CurrentToolHadDelta bool
	// PendingThinkingSignature holds reasoning.encrypted_content until signature_delta
	// is emitted before the thinking block closes.
	PendingThinkingSignature string
	HasToolCall              bool

	// OutputIndexToBlockIdx maps Responses output_index → Anthropic content block index.
	OutputIndexToBlockIdx map[int]int

	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int

	ResponseID string
	Model      string
	Created    int64
}

// ResponsesToAnthropic converts a Responses API response directly into an
// Anthropic Messages response. Reasoning output items are mapped to thinking
// blocks; function_call items become tool_use blocks.
func ResponsesToAnthropic(resp *ResponsesResponse, model string) *AnthropicResponse {
	out := &AnthropicResponse{
		ID:    resp.ID,
		Type:  "message",
		Role:  "assistant",
		Model: model,
	}

	var blocks []AnthropicContentBlock

	for _, item := range resp.Output {
		switch item.Type {
		case "reasoning":
			var summaryText strings.Builder
			for _, s := range item.Summary {
				if s.Type == "summary_text" && s.Text != "" {
					_, _ = summaryText.WriteString(s.Text)
				}
			}
			// Expose encrypted_content as thinking.signature for clients to replay on later turns.
			// A signature-only block retains that state when there is no visible summary.
			if summaryText.String() != "" || strings.TrimSpace(item.EncryptedContent) != "" {
				blocks = append(blocks, AnthropicContentBlock{
					Type:      "thinking",
					Thinking:  summaryText.String(),
					Signature: item.EncryptedContent,
				})
			}
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" && part.Text != "" {
					blocks = append(blocks, AnthropicContentBlock{
						Type: "text",
						Text: part.Text,
					})
				}
			}
		case "function_call":
			blocks = append(blocks, AnthropicContentBlock{
				Type:  "tool_use",
				ID:    fromResponsesCallID(item.CallID),
				Name:  item.Name,
				Input: sanitizeAnthropicToolUseInput(item.Name, item.Arguments),
			})
		case "web_search_call":
			toolUseID := "srvtoolu_" + item.ID
			query := ""
			if item.Action != nil {
				query = item.Action.Query
			}
			inputJSON, _ := json.Marshal(map[string]string{"query": query})
			blocks = append(blocks, AnthropicContentBlock{
				Type:  "server_tool_use",
				ID:    toolUseID,
				Name:  "web_search",
				Input: inputJSON,
			})
			emptyResults, _ := json.Marshal([]struct{}{})
			blocks = append(blocks, AnthropicContentBlock{
				Type:      "web_search_tool_result",
				ToolUseID: toolUseID,
				Content:   emptyResults,
			})
		}
	}

	if len(blocks) == 0 {
		blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: ""})
	}
	out.Content = blocks

	out.StopReason = AnthropicStopReasonPtr(responsesStatusToAnthropicStopReason(resp.Status, resp.IncompleteDetails, blocks))

	if resp.Usage != nil {
		out.Usage = anthropicUsageFromResponsesUsage(resp.Usage)
	}

	return out
}

func anthropicUsageFromResponsesUsage(usage *ResponsesUsage) AnthropicUsage {
	if usage == nil {
		return AnthropicUsage{}
	}

	cachedTokens := 0
	if usage.InputTokensDetails != nil {
		cachedTokens = usage.InputTokensDetails.CachedTokens
	}

	inputTokens := max(usage.InputTokens-cachedTokens-usage.CacheCreationInputTokens, 0)

	return AnthropicUsage{
		InputTokens:              inputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheReadInputTokens:     cachedTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
	}
}

func responsesStatusToAnthropicStopReason(status string, details *ResponsesIncompleteDetails, blocks []AnthropicContentBlock) string {
	switch status {
	case "incomplete":
		if details != nil && details.Reason == "max_output_tokens" {
			return "max_tokens"
		}
		return "end_turn"
	case "completed":
		if containsAnthropicToolUseBlock(blocks) {
			return "tool_use"
		}
		return "end_turn"
	default:
		return "end_turn"
	}
}

func containsAnthropicToolUseBlock(blocks []AnthropicContentBlock) bool {
	for _, block := range blocks {
		if block.Type == "tool_use" {
			return true
		}
	}
	return false
}

func sanitizeAnthropicToolUseInput(name string, raw string) json.RawMessage {
	if name != "Read" || raw == "" {
		return json.RawMessage(raw)
	}

	var input map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return json.RawMessage(raw)
	}

	if pages, ok := input["pages"]; !ok || string(pages) != `""` {
		return json.RawMessage(raw)
	}

	delete(input, "pages")
	sanitized, err := json.Marshal(input)
	if err != nil {
		return json.RawMessage(raw)
	}
	return sanitized
}

// NewResponsesEventToAnthropicState returns an initialised stream state.
func NewResponsesEventToAnthropicState(runtime Runtime) *ResponsesEventToAnthropicState {
	return &ResponsesEventToAnthropicState{
		OutputIndexToBlockIdx: make(map[int]int),
		Created:               runtime.Now().Unix(),
	}
}

// ResponsesEventToAnthropicEvents converts a single Responses SSE event into
// zero or more Anthropic SSE events, updating state as it goes.
func ResponsesEventToAnthropicEvents(
	evt *ResponsesStreamEvent,
	state *ResponsesEventToAnthropicState,
) []AnthropicStreamEvent {
	switch evt.Type {
	case "response.created":
		return resToAnthHandleCreated(evt, state)
	case "response.output_item.added":
		return resToAnthHandleOutputItemAdded(evt, state)
	case "response.output_text.delta":
		return resToAnthHandleTextDelta(evt, state)
	case "response.output_text.done":
		return resToAnthHandleBlockDone(state)
	case "response.function_call_arguments.delta",
		// Custom tool input deltas use the same field as function argument deltas.
		"response.custom_tool_call_input.delta":
		return resToAnthHandleFuncArgsDelta(evt, state)
	case "response.function_call_arguments.done":
		return resToAnthHandleFuncArgsDone(evt, state)
	case "response.output_item.done":
		return resToAnthHandleOutputItemDone(evt, state)
	case "response.reasoning_summary_text.delta",
		// Map raw reasoning text to thinking just like reasoning summaries.
		"response.reasoning_text.delta":
		return resToAnthHandleReasoningDelta(evt, state)
	case "response.reasoning_summary_text.done":
		// Keep thinking open until output_item.done supplies encrypted_content.
		// Closing at summary.done would lose the signature needed for replay and cache reuse.
		return nil
	// Accept response.done from WebSocket and passthrough paths alongside response.completed.
	case "response.completed", "response.done", "response.incomplete", "response.failed":
		return resToAnthHandleCompleted(evt, state)
	default:
		return nil
	}
}

// FinalizeResponsesAnthropicStream emits synthetic termination events if the
// stream ended without a proper completion event.
func FinalizeResponsesAnthropicStream(state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if !state.MessageStartSent || state.MessageStopSent {
		return nil
	}

	var events []AnthropicStreamEvent
	events = append(events, closeCurrentBlock(state)...)

	stopReason := "end_turn"
	if state.HasToolCall {
		stopReason = "tool_use"
	}

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

// ResponsesAnthropicEventToSSE formats an AnthropicStreamEvent as an SSE line pair.
func ResponsesAnthropicEventToSSE(evt AnthropicStreamEvent) (string, error) {
	data, err := json.Marshal(evt)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", evt.Type, data), nil
}

func resToAnthHandleCreated(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Response != nil {
		state.ResponseID = evt.Response.ID
		// A caller-selected model takes precedence over the upstream model.
		if state.Model == "" {
			state.Model = evt.Response.Model
		}
	}

	if state.MessageStartSent {
		return nil
	}
	state.MessageStartSent = true

	// message_start carries a null stop_reason and zero usage; completion supplies final values.
	return []AnthropicStreamEvent{{
		Type: "message_start",
		Message: &AnthropicResponse{
			ID:         state.ResponseID,
			Type:       "message",
			Role:       "assistant",
			Content:    []AnthropicContentBlock{},
			Model:      state.Model,
			StopReason: nil,
			Usage: AnthropicUsage{
				InputTokens:  0,
				OutputTokens: 0,
			},
		},
	}}
}

func resToAnthHandleOutputItemAdded(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Item == nil {
		return nil
	}

	switch evt.Item.Type {
	// Function and custom/freeform calls both become Anthropic tool_use blocks.
	case "function_call", "custom_tool_call":
		var events []AnthropicStreamEvent
		events = append(events, closeCurrentBlock(state)...)

		idx := state.ContentBlockIndex
		state.OutputIndexToBlockIdx[evt.OutputIndex] = idx
		state.ContentBlockOpen = true
		state.CurrentBlockType = "tool_use"
		state.CurrentToolName = evt.Item.Name
		state.CurrentToolArgs = ""
		state.CurrentToolHadDelta = false
		state.HasToolCall = true

		events = append(events, AnthropicStreamEvent{
			Type:  "content_block_start",
			Index: &idx,
			ContentBlock: &AnthropicContentBlock{
				Type:  "tool_use",
				ID:    fromResponsesCallID(evt.Item.CallID),
				Name:  evt.Item.Name,
				Input: json.RawMessage("{}"),
			},
		})
		return events

	case "reasoning":
		var events []AnthropicStreamEvent
		events = append(events, closeCurrentBlock(state)...)

		idx := state.ContentBlockIndex
		state.OutputIndexToBlockIdx[evt.OutputIndex] = idx
		state.ContentBlockOpen = true
		state.CurrentBlockType = "thinking"
		state.PendingThinkingSignature = strings.TrimSpace(evt.Item.EncryptedContent)

		events = append(events, AnthropicStreamEvent{
			Type:  "content_block_start",
			Index: &idx,
			ContentBlock: &AnthropicContentBlock{
				Type:     "thinking",
				Thinking: "",
			},
		})
		return events

	case "message":
		return nil
	}

	return nil
}

func resToAnthHandleTextDelta(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Delta == "" {
		return nil
	}

	var events []AnthropicStreamEvent

	if !state.ContentBlockOpen || state.CurrentBlockType != "text" {
		events = append(events, closeCurrentBlock(state)...)

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
	}

	idx := state.ContentBlockIndex
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_delta",
		Index: &idx,
		Delta: &AnthropicDelta{
			Type: "text_delta",
			Text: evt.Delta,
		},
	})
	return events
}

func resToAnthHandleFuncArgsDelta(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Delta == "" {
		return nil
	}

	// Read pages can be sanitized once the arguments form complete JSON. Emit at that point
	// so an omitted arguments.done event does not prevent delivery.
	if state.CurrentBlockType == "tool_use" && state.CurrentToolName == "Read" {
		state.CurrentToolArgs += evt.Delta
		if state.CurrentToolHadDelta || !json.Valid([]byte(state.CurrentToolArgs)) {
			return nil
		}

		blockIdx, ok := state.OutputIndexToBlockIdx[evt.OutputIndex]
		if !ok {
			return nil
		}
		state.CurrentToolHadDelta = true
		sanitized := sanitizeAnthropicToolUseInput(state.CurrentToolName, state.CurrentToolArgs)
		return []AnthropicStreamEvent{{
			Type:  "content_block_delta",
			Index: &blockIdx,
			Delta: &AnthropicDelta{
				Type:        "input_json_delta",
				PartialJSON: string(sanitized),
			},
		}}
	}

	if state.CurrentBlockType == "tool_use" {
		state.CurrentToolHadDelta = true
	}

	blockIdx, ok := state.OutputIndexToBlockIdx[evt.OutputIndex]
	if !ok {
		return nil
	}

	return []AnthropicStreamEvent{{
		Type:  "content_block_delta",
		Index: &blockIdx,
		Delta: &AnthropicDelta{
			Type:        "input_json_delta",
			PartialJSON: evt.Delta,
		},
	}}
}

func resToAnthHandleFuncArgsDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	// Ignore late or duplicate done events after the block has closed.
	if !state.ContentBlockOpen {
		return nil
	}
	if state.CurrentBlockType != "tool_use" {
		return resToAnthHandleBlockDone(state)
	}

	raw := evt.Arguments
	if raw == "" {
		raw = state.CurrentToolArgs
	}
	if raw == "" || state.CurrentToolHadDelta {
		return closeCurrentBlock(state)
	}
	if state.CurrentToolName == "Read" {
		sanitized := sanitizeAnthropicToolUseInput(state.CurrentToolName, raw)
		if len(sanitized) == 0 {
			return closeCurrentBlock(state)
		}
		raw = string(sanitized)
	}

	// Resolve the same output-to-block mapping used for argument deltas.
	blockIdx, ok := state.OutputIndexToBlockIdx[evt.OutputIndex]
	if !ok {
		blockIdx = state.ContentBlockIndex
	}

	// Do not emit arguments for a block whose index has already been passed.
	if !state.ContentBlockOpen || blockIdx != state.ContentBlockIndex {
		return nil
	}

	events := []AnthropicStreamEvent{{
		Type:  "content_block_delta",
		Index: &blockIdx,
		Delta: &AnthropicDelta{
			Type:        "input_json_delta",
			PartialJSON: raw,
		},
	}}
	events = append(events, closeCurrentBlock(state)...)
	return events
}

func resToAnthHandleReasoningDelta(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Delta == "" {
		return nil
	}

	blockIdx, ok := state.OutputIndexToBlockIdx[evt.OutputIndex]
	if !ok {
		return nil
	}

	return []AnthropicStreamEvent{{
		Type:  "content_block_delta",
		Index: &blockIdx,
		Delta: &AnthropicDelta{
			Type:     "thinking_delta",
			Thinking: evt.Delta,
		},
	}}
}

func resToAnthHandleBlockDone(state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if !state.ContentBlockOpen {
		return nil
	}
	return closeCurrentBlock(state)
}

func resToAnthHandleOutputItemDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Item == nil {
		return nil
	}

	// Handle web_search_call → synthesize server_tool_use + web_search_tool_result blocks.
	if evt.Item.Type == "web_search_call" && evt.Item.Status == "completed" {
		return resToAnthHandleWebSearchDone(evt, state)
	}

	// Capture encrypted_content from the completed reasoning item before closing the block.
	if evt.Item.Type == "reasoning" {
		if sig := strings.TrimSpace(evt.Item.EncryptedContent); sig != "" {
			state.PendingThinkingSignature = sig
		}
	}

	if state.ContentBlockOpen {
		return closeCurrentBlock(state)
	}
	return nil
}

// resToAnthHandleWebSearchDone converts an OpenAI web_search_call output item
// into Anthropic server_tool_use + web_search_tool_result content block pairs.
// This allows Claude Code to count the searches performed.
func resToAnthHandleWebSearchDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	var events []AnthropicStreamEvent
	events = append(events, closeCurrentBlock(state)...)

	toolUseID := "srvtoolu_" + evt.Item.ID
	query := ""
	if evt.Item.Action != nil {
		query = evt.Item.Action.Query
	}
	inputJSON, _ := json.Marshal(map[string]string{"query": query})

	idx1 := state.ContentBlockIndex
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_start",
		Index: &idx1,
		ContentBlock: &AnthropicContentBlock{
			Type:  "server_tool_use",
			ID:    toolUseID,
			Name:  "web_search",
			Input: inputJSON,
		},
	})
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_stop",
		Index: &idx1,
	})
	state.ContentBlockIndex++

	// Emit a matching result block with no result entries.
	// The converted web_search_call carries the query but no individual result payloads.
	emptyResults, _ := json.Marshal([]struct{}{})
	idx2 := state.ContentBlockIndex
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_start",
		Index: &idx2,
		ContentBlock: &AnthropicContentBlock{
			Type:      "web_search_tool_result",
			ToolUseID: toolUseID,
			Content:   emptyResults,
		},
	})
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_stop",
		Index: &idx2,
	})
	state.ContentBlockIndex++

	return events
}

func resToAnthHandleCompleted(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if state.MessageStopSent {
		return nil
	}

	var events []AnthropicStreamEvent
	events = append(events, closeCurrentBlock(state)...)

	stopReason := "end_turn"
	if evt.Usage != nil {
		usage := anthropicUsageFromResponsesUsage(evt.Usage)
		state.InputTokens = usage.InputTokens
		state.OutputTokens = usage.OutputTokens
		state.CacheReadInputTokens = usage.CacheReadInputTokens
		state.CacheCreationInputTokens = usage.CacheCreationInputTokens
	}
	if evt.Response != nil {
		if evt.Response.Usage != nil {
			usage := anthropicUsageFromResponsesUsage(evt.Response.Usage)
			state.InputTokens = usage.InputTokens
			state.OutputTokens = usage.OutputTokens
			state.CacheReadInputTokens = usage.CacheReadInputTokens
			state.CacheCreationInputTokens = usage.CacheCreationInputTokens
		}
		switch evt.Response.Status {
		case "incomplete":
			if evt.Response.IncompleteDetails != nil && evt.Response.IncompleteDetails.Reason == "max_output_tokens" {
				stopReason = "max_tokens"
			}
		case "completed":
			if state.HasToolCall {
				stopReason = "tool_use"
			}
		}
	}

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

func closeCurrentBlock(state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if !state.ContentBlockOpen {
		return nil
	}
	idx := state.ContentBlockIndex
	var events []AnthropicStreamEvent
	// Emit the signature before block_stop so Claude clients can replay encrypted reasoning
	// on later turns, including Grok cache reuse.
	if state.CurrentBlockType == "thinking" {
		if sig := strings.TrimSpace(state.PendingThinkingSignature); sig != "" {
			events = append(events, AnthropicStreamEvent{
				Type:  "content_block_delta",
				Index: &idx,
				Delta: &AnthropicDelta{
					Type:      "signature_delta",
					Signature: sig,
				},
			})
		}
		state.PendingThinkingSignature = ""
	}
	state.ContentBlockOpen = false
	state.ContentBlockIndex++
	state.CurrentToolName = ""
	state.CurrentToolArgs = ""
	state.CurrentToolHadDelta = false
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_stop",
		Index: &idx,
	})
	return events
}
