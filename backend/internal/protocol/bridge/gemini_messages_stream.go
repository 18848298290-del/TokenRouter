package bridge

import (
	"encoding/json"
	"iter"
	"strings"
)

// NativeMessageEvent carries an output event, flush request, or first-token marker for the HTTP layer.
type NativeMessageEvent struct {
	Name       string
	Data       any
	Flush      bool
	FirstToken bool
}

// NativeGeminiMessagesStream tracks text, tool blocks, and usage while converting Gemini to Messages.
type NativeGeminiMessagesStream struct {
	runtime        NativeGeminiRuntime
	finishReason   string
	sawToolUse     bool
	nextBlockIndex int
	openBlockIndex int
	openToolIndex  int
	openBlockType  string
	seenText       string
	openToolID     string
	openToolName   string
	seenToolJSON   string
	usage          NativeGeminiUsage
}

// NewNativeGeminiMessagesStream creates stream state with negative indexes for unopened blocks.
func NewNativeGeminiMessagesStream(runtime NativeGeminiRuntime) *NativeGeminiMessagesStream {
	return &NativeGeminiMessagesStream{runtime: runtime, openBlockIndex: -1, openToolIndex: -1}
}

// Usage returns a copy of the observed usage.
func (s *NativeGeminiMessagesStream) Usage() *NativeGeminiUsage {
	value := s.usage
	return &value
}

// Begin emits message_start followed by a flush before upstream events are consumed.
func (s *NativeGeminiMessagesStream) Begin(messageID, originalModel string) iter.Seq[NativeMessageEvent] {
	return func(yield func(NativeMessageEvent) bool) {
		emit := func(name string, data any) bool { return yield(NativeMessageEvent{Name: name, Data: data}) }
		flush := func() bool { return yield(NativeMessageEvent{Flush: true}) }
		messageStart := map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":            messageID,
				"type":          "message",
				"role":          "assistant",
				"model":         originalModel,
				"content":       []any{},
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage": map[string]any{
					"input_tokens":  0,
					"output_tokens": 0,
				},
			},
		}
		if !emit("message_start", messageStart) {
			return
		}
		if !flush() {
			return
		}
	}
}

// Process yields events as the consumer advances. Usage updates after all output is consumed.
func (s *NativeGeminiMessagesStream) Process(geminiResp map[string]any, raw []byte) iter.Seq[NativeMessageEvent] {
	return func(yield func(NativeMessageEvent) bool) {
		emit := func(name string, data any) bool { return yield(NativeMessageEvent{Name: name, Data: data}) }
		flush := func() bool { return yield(NativeMessageEvent{Flush: true}) }
		token := func() bool { return yield(NativeMessageEvent{FirstToken: true}) }
		if fr := NativeExtractGeminiFinishReason(geminiResp); fr != "" {
			s.finishReason = fr
		}

		parts := NativeExtractGeminiParts(geminiResp)
		for _, part := range parts {
			if text, ok := part["text"].(string); ok && text != "" {
				// Close the tool block before opening a text block so Anthropic content blocks do not overlap.
				if s.openToolIndex >= 0 {
					if !emit("content_block_stop", map[string]any{
						"type":  "content_block_stop",
						"index": s.openToolIndex,
					}) {
						return
					}
					s.openToolIndex = -1
					s.openToolName = ""
					s.seenToolJSON = ""
				}

				delta, newSeen := NativeComputeGeminiTextDelta(s.seenText, text)
				s.seenText = newSeen
				if delta == "" {
					continue
				}

				if s.openBlockType != "text" {
					if s.openBlockIndex >= 0 {
						if !emit("content_block_stop", map[string]any{
							"type":  "content_block_stop",
							"index": s.openBlockIndex,
						}) {
							return
						}
					}
					s.openBlockType = "text"
					s.openBlockIndex = s.nextBlockIndex
					s.nextBlockIndex++
					if !emit("content_block_start", map[string]any{
						"type":  "content_block_start",
						"index": s.openBlockIndex,
						"content_block": map[string]any{
							"type": "text",
							"text": "",
						},
					}) {
						return
					}
				}

				if !token() {
					return
				}
				if !emit("content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": s.openBlockIndex,
					"delta": map[string]any{
						"type": "text_delta",
						"text": delta,
					},
				}) {
					return
				}
				if !flush() {
					return
				}
				continue
			}

			if fc, ok := part["functionCall"].(map[string]any); ok && fc != nil {
				name, _ := fc["name"].(string)
				args := fc["args"]
				if strings.TrimSpace(name) == "" {
					name = "tool"
				}

				// Close the text block before opening a tool block.
				if s.openBlockIndex >= 0 {
					if !emit("content_block_stop", map[string]any{
						"type":  "content_block_stop",
						"index": s.openBlockIndex,
					}) {
						return
					}
					s.openBlockIndex = -1
					s.openBlockType = ""
				}

				// Keep one tool block for successive argument fragments with the same tool name.
				if s.openToolIndex >= 0 && s.openToolName != name {
					if !emit("content_block_stop", map[string]any{
						"type":  "content_block_stop",
						"index": s.openToolIndex,
					}) {
						return
					}
					s.openToolIndex = -1
					s.openToolName = ""
					s.seenToolJSON = ""
				}

				if s.openToolIndex < 0 {
					s.openToolID = "toolu_" + s.runtime.RandomHex(8)
					s.openToolIndex = s.nextBlockIndex
					s.openToolName = name
					s.nextBlockIndex++
					s.sawToolUse = true

					if !emit("content_block_start", map[string]any{
						"type":  "content_block_start",
						"index": s.openToolIndex,
						"content_block": map[string]any{
							"type":  "tool_use",
							"id":    s.openToolID,
							"name":  name,
							"input": map[string]any{},
						},
					}) {
						return
					}
				}

				argsJSONText := "{}"
				switch v := args.(type) {
				case nil:
					// Use the default empty JSON object.
				case string:
					if strings.TrimSpace(v) != "" {
						argsJSONText = v
					}
				default:
					if b, err := json.Marshal(args); err == nil && len(b) > 0 {
						argsJSONText = string(b)
					}
				}

				delta, newSeen := NativeComputeGeminiTextDelta(s.seenToolJSON, argsJSONText)
				s.seenToolJSON = newSeen
				if delta != "" {
					if !emit("content_block_delta", map[string]any{
						"type":  "content_block_delta",
						"index": s.openToolIndex,
						"delta": map[string]any{
							"type":         "input_json_delta",
							"partial_json": delta,
						},
					}) {
						return
					}
				}
				if !flush() {
					return
				}
			}
		}

		if u := NativeExtractGeminiUsage(raw); u != nil {
			s.usage = *u
		}
	}
}

// Finish closes open blocks, emits final usage and message_stop, then requests a flush.
func (s *NativeGeminiMessagesStream) Finish() iter.Seq[NativeMessageEvent] {
	return func(yield func(NativeMessageEvent) bool) {
		emit := func(name string, data any) bool { return yield(NativeMessageEvent{Name: name, Data: data}) }
		flush := func() bool { return yield(NativeMessageEvent{Flush: true}) }

		if s.openBlockIndex >= 0 {
			if !emit("content_block_stop", map[string]any{
				"type":  "content_block_stop",
				"index": s.openBlockIndex,
			}) {
				return
			}
		}
		if s.openToolIndex >= 0 {
			if !emit("content_block_stop", map[string]any{
				"type":  "content_block_stop",
				"index": s.openToolIndex,
			}) {
				return
			}
		}

		stopReason := NativeMapGeminiFinishReasonToClaudeStopReason(s.finishReason)
		if s.sawToolUse {
			stopReason = "tool_use"
		}

		usageObj := map[string]any{
			"output_tokens": s.usage.OutputTokens,
		}
		if s.usage.InputTokens > 0 {
			usageObj["input_tokens"] = s.usage.InputTokens
		}
		if !emit("message_delta", map[string]any{
			"type": "message_delta",
			"delta": map[string]any{
				"stop_reason":   stopReason,
				"stop_sequence": nil,
			},
			"usage": usageObj,
		}) {
			return
		}
		if !emit("message_stop", map[string]any{
			"type": "message_stop",
		}) {
			return
		}
		if !flush() {
			return
		}
	}
}
