package bridge

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Responses replies store reasoning, text, calls, and hosted search in
// separate items. Messages conversion retains supported traversal order,
// transfers encrypted reasoning into signatures, and subtracts cache
// reads/writes from input totals to recover uncached usage. Hosted search
// becomes a query call plus empty result because the modeled item supplies no
// individual results.
//
// Streaming conversion owns one active block and retains function/reasoning
// output-index mappings after closure. Text opens lazily, thinking waits for
// completed-item signatures, and exact Read arguments wait for valid
// accumulated JSON before empty pages removal. Ordinary argument fragments
// pass through immediately. Several handlers do not verify current identity or
// open state, so callers provide consistent ordered lifecycles. Terminal
// usage/status has its own precedence, while EOF recovery uses existing
// counters and any opened client-tool evidence.

// ResponsesEventToAnthropicState tracks state for converting a sequence of
// Responses SSE events directly into Anthropic SSE events.
type ResponsesEventToAnthropicState struct {
	// MessageStartSent and MessageStopSent gate creation/terminal output and
	// EOF recovery. They do not prevent other handlers from mutating later content.
	MessageStartSent bool // message start already produced
	MessageStopSent  bool // message stop already produced

	// ContentBlockIndex and ContentBlockOpen identify the sole active block.
	// Closure emits its index before advancing; historical mappings can retain
	// earlier positions, so ordered association remains the caller's responsibility.
	ContentBlockIndex int  // current or next block position
	ContentBlockOpen  bool // current block is active
	// CurrentBlockType remains the last opened kind after closure. Delta routing
	// can consult it even when ContentBlockOpen is false.
	CurrentBlockType string // text, thinking, or tool_use

	// CurrentToolName and CurrentToolArgs belong to the active tool lifecycle.
	// Exact Read alone accumulates fragments here; ordinary tools stream bytes
	// directly. Closure clears both before another tool opens.
	CurrentToolName string // active exact tool name
	CurrentToolArgs string // accumulated Read argument text
	// CurrentToolHadDelta suppresses packed done arguments. Ordinary tools set
	// it before map lookup, whereas Read sets it after finding a mapping for a
	// valid buffered value. True can precede actual ordinary fragment emission.
	CurrentToolHadDelta bool // argument delta evidence recorded
	// PendingThinkingSignature retains trimmed encrypted content from added or
	// completed reasoning. Thinking closure emits and clears it; another kind's
	// closure leaves this pending value unchanged.
	PendingThinkingSignature string // pending encrypted reasoning signature
	// HasToolCall remains true after any function/custom start, including empty
	// identity. Completed status and EOF recovery use it to select tool_use.
	HasToolCall bool // any client tool block opened

	// OutputIndexToBlockIdx retains function/reasoning start positions after
	// closure. Text/server-search blocks are absent; constructor setup and
	// consistent output indexes are required before consumers rely on matching.
	OutputIndexToBlockIdx map[int]int // retained output-to-block index pairs

	// InputTokens, OutputTokens, CacheReadInputTokens, and CacheCreationInputTokens
	// retain converted terminal usage. Nested response usage replaces event
	// usage; missing observations keep caller/default values for EOF recovery.
	InputTokens              int // uncached input token count
	OutputTokens             int // terminal output token count
	CacheReadInputTokens     int // cached input read count
	CacheCreationInputTokens int // cached input write count

	// ResponseID and Model feed message_start. Each creation updates identity,
	// while selected nonempty model wins; duplicate creation emits no second start.
	ResponseID string // response-derived message identity
	Model      string // selected client message model
	Created    int64  // captured Unix seconds
}

// ResponsesToAnthropic converts a nonnil reply using the supplied model
// verbatim. Supported items expand in traversal order; reasoning summaries
// concatenate without separators, and nonempty summary or trimmed signature
// retains the original encrypted-content spelling in a thinking block.
//
// Functions keep call correlation after legacy reversal and use the narrow
// Read sanitizer. Hosted search emits query and empty-result blocks regardless
// of item status; unsupported kinds, including custom calls, are omitted.
// Empty supported content becomes one empty text block. Stop reason combines
// status/details/tool presence, usage separates cache buckets, and source
// Error is not represented in the result.
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
					// Summary segments concatenate for one thinking block. Builder writes
					// return nil errors, so this discarded result loses no encoding failure.
					_, _ = summaryText.WriteString(s.Text)
				}
			}
			// Expose encrypted_content as thinking.signature for clients to replay on
			// later turns. A signature-only block retains that state when there is no
			// visible summary.
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
			// Correlation IDs identify tool_use, independently of Responses item
			// identity. The Read repair retains other raw arguments for downstream
			// validation.
			blocks = append(blocks, AnthropicContentBlock{
				Type:  "tool_use",
				ID:    fromResponsesCallID(item.CallID),
				Name:  item.Name,
				Input: sanitizeAnthropicToolUseInput(item.Name, item.Arguments),
			})
		case "web_search_call":
			// Hosted items supply query data without individual result payloads.
			// Represent that observation through a server-tool call and correlated
			// empty result.
			toolUseID := "srvtoolu_" + item.ID
			query := ""
			if item.Action != nil {
				query = item.Action.Query
			}
			// The query map and empty typed result have supported values only. Their
			// discarded encoder errors cannot hide unsupported values or invalid raw
			// fragments.
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

	// Filtered output still needs modeled message content. Supply empty text
	// so consumers receive a block shape even when no supported output survives.
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

// anthropicUsageFromResponsesUsage separates Responses total input into
// Messages uncached/read/write buckets. Nil usage returns zeros. Cached input
// comes from InputTokensDetails and creation from the top-level canonical
// field; both are subtracted before uncached input is clamped to zero.
//
// Output/cache counts keep their supplied values, including negatives, and
// TotalTokens is unused. Directly constructed detail-only write counts do not
// contribute unless decoding populated the canonical creation field. The clamp
// prevents cache overcounts from producing negative uncached input, while
// callers validate other counters separately.
func anthropicUsageFromResponsesUsage(usage *ResponsesUsage) AnthropicUsage {
	if usage == nil {
		return AnthropicUsage{}
	}

	cachedTokens := 0
	if usage.InputTokensDetails != nil {
		cachedTokens = usage.InputTokensDetails.CachedTokens
	}

	// Responses input includes cache buckets reported separately in Messages.
	// Subtract both, then clamp inconsistent overcounts so uncached input does
	// not become negative.
	inputTokens := max(usage.InputTokens-cachedTokens-usage.CacheCreationInputTokens, 0)

	return AnthropicUsage{
		InputTokens:              inputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheReadInputTokens:     cachedTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
	}
}

// responsesStatusToAnthropicStopReason combines exact status, incomplete
// details, and converted block kinds. Incomplete with max_output_tokens
// selects max_tokens; other incomplete evidence selects end_turn. Completed
// selects tool_use when any exact tool-use block exists, including one
// preceding later text.
//
// Unknown status selects end_turn, and server-tool blocks do not qualify as
// ordinary tool use. This stop reason describes the modeled completion path
// and does not preserve failure/error payloads. Callers inspect source errors
// when deciding whether response delivery represents success.
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

// containsAnthropicToolUseBlock reports any exact tool_use kind in the
// supplied block list.
func containsAnthropicToolUseBlock(blocks []AnthropicContentBlock) bool {
	for _, block := range blocks {
		if block.Type == "tool_use" {
			return true
		}
	}
	return false
}

// sanitizeAnthropicToolUseInput removes exact empty-string pages from JSON
// object arguments for exact Read. Other names, empty bytes, missing/nonempty
// pages, and object-decode failures return supplied text as RawMessage without
// validating its argument shape.
//
// Successful repair retains other members as RawMessage, keeping their numeric
// values independent of float64 decoding while rebuilding object layout and
// key order. Rebuild failure also retains source text. Only that selected
// empty member is removed; null, whitespace strings, and other pages values
// remain available for downstream validation.
func sanitizeAnthropicToolUseInput(name string, raw string) json.RawMessage {
	if name != "Read" || raw == "" {
		return json.RawMessage(raw)
	}

	var input map[string]json.RawMessage
	// Failed object decoding cannot identify Read's optional pages member. Keep
	// raw bytes so this targeted repair does not invent a replacement argument
	// object.
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return json.RawMessage(raw)
	}

	if pages, ok := input["pages"]; !ok || string(pages) != `""` {
		return json.RawMessage(raw)
	}

	// Remove Read's exact empty-string pages argument after selecting that narrow
	// case. Other raw members retain their value representation through
	// rebuilding.
	delete(input, "pages")
	sanitized, err := json.Marshal(input)
	if err != nil {
		// Rebuilding supplied no accepted repair. Retain source bytes for later
		// validation instead of publishing a partially encoded replacement object.
		return json.RawMessage(raw)
	}
	return sanitized
}

// NewResponsesEventToAnthropicState initializes output lookup and captures
// runtime.Now time. The callback is required although these handlers do not
// emit Created; callers use one constructed state for each stream and
// serialize its mutable fields.
func NewResponsesEventToAnthropicState(runtime Runtime) *ResponsesEventToAnthropicState {
	return &ResponsesEventToAnthropicState{
		OutputIndexToBlockIdx: make(map[int]int),
		Created:               runtime.Now().Unix(),
	}
}

// ResponsesEventToAnthropicEvents dispatches a nonnil event through
// constructed nonnil Messages state. Unknown labels produce nothing. Callers
// serialize access and provide consistent ordered item lifecycles because only
// one block is active and historical mappings remain.
//
// Custom input deltas use raw argument routing without a JSON wrapper.
// Thinking remains open after summary.done until completed-item signature
// arrives. Four terminal labels share usage/status conversion, but only
// terminal/finalizer handlers guard repeated message stop; content handlers
// can still mutate later state. The helper does not fully validate association
// or propagate source error payloads.
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
		// Custom input uses the same Delta field and retained output-index route.
		// Its raw text is passed to argument handling without adding a wrapper object.
		"response.custom_tool_call_input.delta":
		return resToAnthHandleFuncArgsDelta(evt, state)
	case "response.function_call_arguments.done":
		return resToAnthHandleFuncArgsDone(evt, state)
	case "response.output_item.done":
		return resToAnthHandleOutputItemDone(evt, state)
	case "response.reasoning_summary_text.delta",
		// Raw reasoning and summary fragments both carry visible thinking text. Use
		// their shared output mapping so either label addresses the previously
		// opened reasoning block.
		"response.reasoning_text.delta":
		return resToAnthHandleReasoningDelta(evt, state)
	case "response.reasoning_summary_text.done":
		// Completed reasoning can supply encrypted content after summary completion.
		// Leave thinking open so the signature is emitted inside its lifecycle
		// before stop.
		return nil
	// Legacy done and explicit completed/incomplete/failed labels share one
	// terminal selector. Nested status and usage determine the emitted Messages fields.
	case "response.completed", "response.done", "response.incomplete", "response.failed":
		return resToAnthHandleCompleted(evt, state)
	default:
		return nil
	}
}

// FinalizeResponsesAnthropicStream closes the active block and emits
// message_delta/message_stop after creation unless already stopped.
// Unstarted/stopped state produces nothing. HasToolCall selects tool_use,
// otherwise EOF recovery selects end_turn and uses current usage fields.
//
// Unseen terminal status/error/usage is not recovered. Counters usually remain
// zero before terminal handling unless the caller populated them. Ordinary
// closure emits any pending thinking signature, then MessageStopSent is set.
// Repeated finalization returns nothing, while callers decide what an
// interrupted transport means for delivery.
func FinalizeResponsesAnthropicStream(state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if !state.MessageStartSent || state.MessageStopSent {
		return nil
	}

	var events []AnthropicStreamEvent
	// Close active content before message termination so a signature and stop
	// remain inside the same block lifecycle, using its old index before advancing.
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

// ResponsesAnthropicEventToSSE serializes one typed event into a complete
// event/data SSE frame. Codec errors return no frame; the supplied Type
// becomes the label, and the helper performs no transport write or flush.
func ResponsesAnthropicEventToSSE(evt AnthropicStreamEvent) (string, error) {
	data, err := json.Marshal(evt)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", evt.Type, data), nil
}

// resToAnthHandleCreated reads response identity/model before emitting
// message_start once. Nonempty selected Model wins, while each supplied
// response replaces ResponseID, including empty spelling. The start has null
// stop reason, empty content, and zero input/output usage.
//
// Duplicate creation can update metadata without another start. Source
// creation usage is ignored, so final counters come from terminal handling or
// caller state. No open-block check is performed here. Callers provide a
// single creation lifecycle before ordered content events.
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

	// Creation supplies no terminal stop evidence and this path ignores initial
	// usage. Emit null stop_reason and zero counters until terminal selection
	// supplies observations.
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

// resToAnthHandleOutputItemAdded opens function/custom tool or thinking blocks
// after closing an active block. Constructor-initialized lookup stores their
// output-to-block positions; message items open no block because text starts
// lazily. Initial arguments/input are not emitted.
//
// Tool starts use empty-object input and reset Read buffer/delta evidence;
// HasToolCall remains true afterward. Thinking retains trimmed initial
// encrypted content until closure. Reused indexes overwrite mappings, while
// other mappings remain, so distinct ordered item lifecycles and consistent
// later events are caller obligations rather than validated associations.
func resToAnthHandleOutputItemAdded(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Item == nil {
		return nil
	}

	switch evt.Item.Type {
	// Both call kinds use a Messages tool lifecycle. Initial payload is not
	// emitted here; mapped deltas or packed function done later provide raw
	// argument bytes.
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

// resToAnthHandleTextDelta emits nonempty text under a lazily opened text
// block. Empty fragments produce nothing. An existing open text block receives
// the next fragment, while another active kind is closed before assigning text
// kind and opening a new block.
//
// The block-start event precedes its first delta, using the counter after
// previous closure. OutputIndex and content-part identity are not consulted,
// so consecutive fragments can share a block across source items until a done
// or kind transition closes it. Callers provide boundaries consistent with
// their ordered content lifecycle.
func resToAnthHandleTextDelta(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Delta == "" {
		return nil
	}

	var events []AnthropicStreamEvent

	// A kind transition ends the preceding lifecycle before text acquires its
	// index. Otherwise the first text fragment could address a tool or thinking block.
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

// resToAnthHandleFuncArgsDelta routes nonempty argument/custom-input text
// through retained output mappings. Ordinary fragments set tool delta evidence
// before lookup and emit immediately when mapped; the open flag/current index
// are not checked.
//
// Exact Read buffers until json.Valid succeeds, then sanitizes and emits once
// after mapping lookup. Later fragments keep buffering but emit no second
// delta. Missing maps suppress output, and ordinary evidence may still have
// been set. Callers provide ordered current-item events and valid
// representations; arbitrary custom text is not wrapped as JSON.
func resToAnthHandleFuncArgsDelta(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Delta == "" {
		return nil
	}

	// Read needs a whole JSON value to inspect its pages member. Buffer until
	// validation succeeds, then emit once so missing done does not suppress
	// usable content.
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

	// The retained output mapping supplies the announced block identity. Missing
	// association drops this fragment instead of guessing the active content
	// position.
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

// resToAnthHandleFuncArgsDone closes an open tool and emits complete arguments
// when no delta evidence exists. Closed state returns nothing; another active
// kind is closed. Arguments precedes the Read buffer, and empty or already-
// delivered content closes without another delta.
//
// For packed content, Read sanitization precedes index selection. Missing
// mapping selects the current position; stale mapping declines
// emission/closure. The evidence branch closes before identity comparison, so
// association is a caller obligation. This prevents duplicate full arguments
// after streamed fragments but does not replace partial ordinary fragments
// with later done text.
func resToAnthHandleFuncArgsDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	// A closed block gives repeated done no active target. A later different open
	// block still needs correct caller association because this guard has no item
	// identity.
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

	// Packed done content uses the delta output mapping. If no mapping exists,
	// the active position supplies a current-block alternative for this
	// completion path.
	blockIdx, ok := state.OutputIndexToBlockIdx[evt.OutputIndex]
	if !ok {
		blockIdx = state.ContentBlockIndex
	}

	// Historical mappings can identify a closed block. Decline packed arguments
	// unless the selected index addresses the open position, preventing emission
	// to that stale block.
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

// resToAnthHandleReasoningDelta emits nonempty thinking text at a mapped
// output position. Missing mapping returns nothing; open state/current kind
// are unchecked, so callers prevent stale deltas from addressing a retained
// mapping after closure.
func resToAnthHandleReasoningDelta(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Delta == "" {
		return nil
	}

	// The retained output mapping supplies the announced block identity. Missing
	// association drops this fragment instead of guessing the active content
	// position.
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

// resToAnthHandleBlockDone closes an active block or returns nothing when none
// is open. It uses current state without event identity; callers apply it to
// the block whose lifecycle they are completing.
func resToAnthHandleBlockDone(state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if !state.ContentBlockOpen {
		return nil
	}
	return closeCurrentBlock(state)
}

// resToAnthHandleOutputItemDone synthesizes completed hosted-search pairs or
// closes active content for another nonnil item. A nil item produces nothing.
// Completed reasoning replaces pending signature only when its trimmed
// encrypted content is nonempty, then closure can emit it inside a thinking
// block.
//
// The completed item can therefore add metadata absent at summary completion.
// OutputIndex and active kind are not validated; another current kind can
// close while that pending signature remains. Item text/arguments do not
// supplement missing deltas here, so callers preserve matching item order and
// route full arguments through the dedicated done path.
func resToAnthHandleOutputItemDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Item == nil {
		return nil
	}

	// Completed hosted search needs server-tool/query and result blocks rather
	// than client function arguments. Build that pair before considering ordinary
	// block closure.
	if evt.Item.Type == "web_search_call" && evt.Item.Status == "completed" {
		return resToAnthHandleWebSearchDone(evt, state)
	}

	// Reasoning completion can supply a signature missing at start. Capture its
	// nonempty value before thinking closure emits the last signature delta and stop.
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

// resToAnthHandleWebSearchDone closes active content and emits query server-
// tool plus empty-result blocks. It requires a nonnil item from the completed
// hosted-search branch; absent action supplies an empty query.
//
// The pair receives sequential start/stop indexes and shares srvtoolu_
// correlation derived from item ID. Individual search result payloads are
// unavailable, so the result array is empty. The counter advances twice, while
// HasToolCall and function/reasoning index lookup are not updated by these
// server-tool blocks.
func resToAnthHandleWebSearchDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	var events []AnthropicStreamEvent
	events = append(events, closeCurrentBlock(state)...)

	toolUseID := "srvtoolu_" + evt.Item.ID
	query := ""
	if evt.Item.Action != nil {
		query = evt.Item.Action.Query
	}
	// Query input is a string map and results use an empty typed array. Their
	// supported values cannot cause encoding failure or invalid raw-JSON publication.
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

	// Emit a matching result block with no result entries. The converted
	// web_search_call carries the query but no individual result payloads.
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

// resToAnthHandleCompleted closes active content and emits selected usage/stop
// plus message_stop once. Top-level event usage is applied before nested
// response usage, which replaces it when present. Either observation includes
// explicit zero values.
//
// Nested status alone selects max_tokens for recognized incomplete details or
// tool_use for completed responses after client-tool starts.
// Missing/failed/unknown status selects end_turn even with HasToolCall, and
// error payloads are not emitted. MessageStopSent is set after construction;
// earlier creation is unnecessary. Callers determine source failure handling
// outside this conversion.
func resToAnthHandleCompleted(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if state.MessageStopSent {
		return nil
	}

	var events []AnthropicStreamEvent
	// Close active content before message termination so a signature and stop
	// remain inside the same block lifecycle, using its old index before advancing.
	events = append(events, closeCurrentBlock(state)...)

	stopReason := "end_turn"
	// Select whole usage observations in order. Nested response usage replaces
	// event usage so the emitted counters come from one selected observation
	// rather than a mix.
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

// closeCurrentBlock emits pending trimmed thinking signature before stopping
// the active block, advances its index, and clears tool state. Closed state
// returns nothing. Historical mappings and CurrentBlockType remain, so callers
// prevent stale content events from using them.
//
// Completed reasoning can provide a signature after summary text, requiring
// signature-before-stop ordering. Thinking closure alone clears that pending
// field; other kinds leave it intact. The stop retains the old index through a
// local copy, while new blocks use the advanced counter. Closure does not
// flush incomplete Read arguments or synthesize missing text.
func closeCurrentBlock(state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if !state.ContentBlockOpen {
		return nil
	}
	idx := state.ContentBlockIndex
	var events []AnthropicStreamEvent
	// Signature can arrive after visible reasoning as completed-item metadata.
	// Emit it before block stop so the replay value remains inside the thinking
	// lifecycle.
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
