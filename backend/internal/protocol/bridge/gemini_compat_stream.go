package bridge

import (
	"encoding/json"
	"iter"
	"strings"
)

// Gemini payloads can repeat accumulated text or provide new fragments, while
// Responses and Chat consumers receive sequential Anthropic block events. Text
// and thinking therefore keep separate accumulated histories, and successive
// calls with the same tool name share one argument history until a text part
// or different name closes that tool.
//
// The iterator mutates state as its consumer advances. Usage and finish
// metadata are recorded before output, so a disconnected consumer can still
// obtain the current payload's usage. Block starts carry indexes, while deltas
// and stops rely on the active-block convention of
// AnthropicEventToResponsesEvents. Callers serialize Begin, Process, and
// Finish consumption for one response.

// NativeGeminiCompatStream keeps one response's cumulative content and active
// block state for conversion into Responses or Chat events.
type NativeGeminiCompatStream struct {
	// runtime supplies RandomHex when a tool opens; NewNativeGeminiCompatStream
	// retains the supplied callbacks for this response's entire iterator lifetime.
	runtime NativeGeminiRuntime
	// finishReason retains the latest nonempty first-candidate reason until
	// Finish selects the client stop reason, including the tool-use override.
	finishReason string
	// sawToolUse becomes true when a tool opens and makes Finish report
	// tool_use regardless of the recorded Gemini finish reason.
	sawToolUse bool
	// nextBlockIndex allocates starts; openBlockIndex and openToolIndex use -1
	// for absence. openBlockType chooses the current text/thinking lifecycle;
	// transitions close the previous kind before another kind starts.
	nextBlockIndex int    // next announced block index
	openBlockIndex int    // active text/thinking block index
	openToolIndex  int    // active tool block index
	openBlockType  string // active text/thinking block kind
	// seenText and seenThinking accumulate their separate channels across block
	// transitions. Process compares each new payload with its channel's history
	// to suppress repeated prefixes and emit new fragments.
	seenText     string // accumulated visible text history
	seenThinking string // accumulated thinking text history
	// openToolName groups adjacent calls by name; seenToolJSON accumulates that
	// group's argument text. Closing the tool resets both, so a later occurrence
	// of the same name opens a distinct block.
	openToolName string // active tool grouping name
	seenToolJSON string // accumulated tool argument text
	// usage is replaced before output when raw contains usageMetadata; a payload
	// lacking that field leaves the previously observed counts available to Usage.
	usage NativeGeminiUsage
}

// NewNativeGeminiCompatStream creates state for one response with no active
// content or tool block. It copies runtime, so configure RandomHex before
// construction when calls can occur; serialize consumption of each Begin,
// Process and Finish sequence.
func NewNativeGeminiCompatStream(runtime NativeGeminiRuntime) *NativeGeminiCompatStream {
	return &NativeGeminiCompatStream{runtime: runtime, openBlockIndex: -1, openToolIndex: -1}
}

// Usage returns a detached value copy of the latest recorded token counts.
func (s *NativeGeminiCompatStream) Usage() *NativeGeminiUsage {
	value := s.usage
	return &value
}

// Begin yields an empty assistant message with the supplied client-visible
// model and message ID. Consume this sequence once before Process;
// message_start emission has no duplicate guard.
func (s *NativeGeminiCompatStream) Begin(messageID, originalModel string) iter.Seq[*AnthropicStreamEvent] {
	return func(yield func(*AnthropicStreamEvent) bool) {
		emit := func(event *AnthropicStreamEvent) bool { return !yield(event) }
		// Responses state reads identity and model from this start event before any
		// content item is announced; initial usage is filled by later observations.
		if emit(&AnthropicStreamEvent{
			Type: "message_start",
			Message: &AnthropicResponse{
				ID:         messageID,
				Type:       "message",
				Role:       "assistant",
				Model:      originalModel,
				Content:    []AnthropicContentBlock{},
				StopReason: nil, // JSON null
				Usage:      AnthropicUsage{},
			},
		}) {
			return
		}
	}
}

// Process yields Anthropic block events from the first candidate and records
// usage from the accompanying raw payload. Consume each sequence once before
// processing another payload, and keep both supplied payloads unchanged until
// iteration finishes.
//
// Text and thinking histories distinguish cumulative repeats from incremental
// fragments; adjacent same-name function calls use the same argument
// accumulator. Opening or closing a block changes state during iteration, and
// a consumer stop keeps those partial changes. Metadata updates precede all
// yielded output, which lets Usage report this payload even when its first
// block stops the consumer.
func (s *NativeGeminiCompatStream) Process(geminiResp map[string]any, raw []byte) iter.Seq[*AnthropicStreamEvent] {
	return func(yield func(*AnthropicStreamEvent) bool) {
		emit := func(event *AnthropicStreamEvent) bool { return !yield(event) }
		// Record finish and usage before yielding any block so the HTTP caller can
		// report observed counts when output stops early.
		if fr := NativeExtractGeminiFinishReason(geminiResp); fr != "" {
			s.finishReason = fr
		}
		if u := NativeExtractGeminiUsage(raw); u != nil {
			s.usage = *u
		}

		for _, part := range NativeExtractGeminiParts(geminiResp) {
			if text, ok := part["text"].(string); ok && text != "" {
				// A text or thinking part ends the active tool's argument history before
				// another content kind opens in downstream Responses state.
				if s.openToolIndex >= 0 {
					if s.closeOpenTool(emit) {
						return
					}
				}
				thought, _ := part["thought"].(bool)
				blockType := "text"
				deltaType := "text_delta"
				seen := s.seenText
				if thought {
					blockType = "thinking"
					deltaType = "thinking_delta"
					seen = s.seenThinking
				}
				// Compare against the selected channel so repeated thinking does not
				// consume visible text history and cumulative prefixes are emitted once.
				delta, newSeen := NativeComputeGeminiTextDelta(seen, text)
				if thought {
					s.seenThinking = newSeen
				} else {
					s.seenText = newSeen
				}
				if delta == "" {
					continue
				}
				// Close the previous content kind before announcing the next;
				// AnthropicEventToResponsesEvents associates unindexed deltas and stops
				// with its currently active item.
				if s.openBlockType != blockType {
					if s.closeOpenBlock(emit) {
						return
					}
					idx := s.nextBlockIndex
					s.nextBlockIndex++
					s.openBlockIndex = idx
					s.openBlockType = blockType
					contentBlock := &AnthropicContentBlock{Type: "text", Text: ""}
					if thought {
						contentBlock = &AnthropicContentBlock{Type: "thinking", Thinking: ""}
					}
					if emit(&AnthropicStreamEvent{
						Type:         "content_block_start",
						Index:        &idx,
						ContentBlock: contentBlock,
					}) {
						return
					}
				}
				deltaEvent := &AnthropicDelta{Type: deltaType, Text: delta}
				if thought {
					deltaEvent.Text = ""
					deltaEvent.Thinking = delta
				}
				if emit(&AnthropicStreamEvent{
					Type:  "content_block_delta",
					Delta: deltaEvent,
				}) {
					return
				}
				continue
			}

			if fc, ok := part["functionCall"].(map[string]any); ok && fc != nil {
				name, _ := fc["name"].(string)
				// A missing or blank function name uses the generic tool label so a usable
				// tool block can still be announced downstream.
				if strings.TrimSpace(name) == "" {
					name = "tool"
				}
				// Finish text/thinking before opening a function item; same-name adjacent
				// calls remain one tool block until another name or text intervenes.
				if s.closeOpenBlock(emit) {
					return
				}
				if s.openToolIndex >= 0 && s.openToolName != name {
					if s.closeOpenTool(emit) {
						return
					}
				}
				if s.openToolIndex < 0 {
					idx := s.nextBlockIndex
					s.nextBlockIndex++
					s.openToolIndex = idx
					s.openToolName = name
					s.sawToolUse = true
					if emit(&AnthropicStreamEvent{
						Type:  "content_block_start",
						Index: &idx,
						ContentBlock: &AnthropicContentBlock{
							Type:  "tool_use",
							ID:    "toolu_" + s.runtime.RandomHex(8),
							Name:  name,
							Input: json.RawMessage(`{}`),
						},
					}) {
						return
					}
				}

				// Forward string arguments as supplied, serialize other values, and keep
				// an empty object when arguments are absent, blank, or unencodable.
				argsJSONText := "{}"
				switch v := fc["args"].(type) {
				case nil:
				case string:
					if strings.TrimSpace(v) != "" {
						argsJSONText = v
					}
				default:
					if b, err := json.Marshal(v); err == nil && len(b) > 0 {
						argsJSONText = string(b)
					}
				}
				// Apply the same prefix heuristic to serialized arguments so repeated
				// complete objects contribute only their unseen suffix to the active tool.
				delta, newSeen := NativeComputeGeminiTextDelta(s.seenToolJSON, argsJSONText)
				s.seenToolJSON = newSeen
				if delta != "" {
					if emit(&AnthropicStreamEvent{
						Type: "content_block_delta",
						Delta: &AnthropicDelta{
							Type:        "input_json_delta",
							PartialJSON: delta,
						},
					}) {
						return
					}
				}
			}
		}
	}
}

// Finish closes active blocks, emits the latest usage with the selected stop
// reason, and yields message_stop. Consume it once after upstream exhaustion
// because repeated calls can repeat terminal events.
//
// Closing helpers clear their open markers after a stop callback, even when
// the consumer rejects that yield. Tool presence overrides the recorded finish
// reason. Stopping iteration skips later termination events, so the HTTP
// adapter ends response handling on a write failure instead of consuming
// another terminal sequence.
func (s *NativeGeminiCompatStream) Finish() iter.Seq[*AnthropicStreamEvent] {
	return func(yield func(*AnthropicStreamEvent) bool) {
		emit := func(event *AnthropicStreamEvent) bool { return !yield(event) }

		if s.closeOpenBlock(emit) {
			return
		}
		if s.closeOpenTool(emit) {
			return
		}

		// An opened tool requires a client tool response next, so its presence takes
		// precedence over the upstream finish-reason mapping.
		stopReason := NativeMapGeminiFinishReasonToClaudeStopReason(s.finishReason)
		if s.sawToolUse {
			stopReason = "tool_use"
		}
		if emit(&AnthropicStreamEvent{
			Type: "message_delta",
			Delta: &AnthropicDelta{
				Type:       "message_delta",
				StopReason: stopReason,
			},
			Usage: &AnthropicUsage{
				InputTokens:          s.usage.InputTokens,
				OutputTokens:         s.usage.OutputTokens,
				CacheReadInputTokens: s.usage.CacheReadInputTokens,
			},
		}) {
			return
		}
		if emit(&AnthropicStreamEvent{Type: "message_stop"}) {
			return
		}
	}
}

// closeOpenBlock emits a stop for the active text/thinking block and clears
// its open markers. The callback returns true when the consumer stops; marker
// cleanup still occurs after that result.
func (s *NativeGeminiCompatStream) closeOpenBlock(emit func(*AnthropicStreamEvent) bool) bool {
	if s.openBlockIndex < 0 {
		return false
	}
	// The downstream converter closes its active item from this unindexed stop;
	// clear our open marker after emission even when iteration is abandoned.
	disconnected := emit(&AnthropicStreamEvent{Type: "content_block_stop"})
	s.openBlockIndex = -1
	s.openBlockType = ""
	return disconnected
}

// closeOpenTool emits a stop and clears the active tool's name and argument
// history. The callback reports consumer stop with true, and cleanup occurs
// even when that stop is rejected.
func (s *NativeGeminiCompatStream) closeOpenTool(emit func(*AnthropicStreamEvent) bool) bool {
	if s.openToolIndex < 0 {
		return false
	}
	// The downstream converter closes its active item from this unindexed stop;
	// clear our open marker after emission even when iteration is abandoned.
	disconnected := emit(&AnthropicStreamEvent{Type: "content_block_stop"})
	s.openToolIndex = -1
	s.openToolName = ""
	s.seenToolJSON = ""
	return disconnected
}
