package bridge

import (
	"encoding/json"
	"fmt"
	"strings"
)

// responses_to_anthropic_request.go converts Responses requests into Messages
// history with repaired tool pairing.
// Responses history can separate a function call from its output with
// developer instructions or other turns. Messages history represents calls
// inside assistant blocks and expects their results in adjacent user content,
// so conversion gathers system text separately and repairs tool pairing after
// converting supported message parts. Reasoning items are discarded because
// their encrypted content is not used as an Anthropic replay signature.
//
// Pairing repair keeps the last nonempty-ID result, moves results into call
// order, and drops unanswered calls and orphan results. Same-role merging runs
// before and after that repair so parallel calls can share one assistant turn.
// Content filtering removes unsupported or blank block arrays, while scalar
// input and malformed content take different paths. Tool schema normalization,
// effort budgets, and selector translation prepare request fields; callers
// still validate raw arguments and retained unknown forms.

// ResponsesToAnthropicRequest builds Messages fields from a nonnil Responses
// request. It delegates history filtering/pairing, copies model/stream and
// sampling pointers, maps priority/fast service tiers to speed, and supplies
// 8192 tokens when the output limit is absent or nonpositive. Retained
// sampling values can share source storage.
//
// Nonempty reasoning effort becomes output_config and, except for normalized
// low, enabled thinking with a fixed budget. Ultra returns an error; unknown
// effort retains its spelling and uses the default budget. Tool definitions
// and selector forms receive their respective conversions. The returned
// request is not complete validation: raw argument encoding, scalar blank
// input, retained unknown content, and downstream effort/budget policy remain
// caller concerns.
func ResponsesToAnthropicRequest(req *ResponsesRequest) (*AnthropicRequest, error) {
	system, messages, err := convertResponsesInputToAnthropic(req.Instructions, req.Input)
	if err != nil {
		return nil, err
	}

	out := &AnthropicRequest{
		Model:       req.Model,
		Messages:    messages,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stream:      req.Stream,
	}
	// Priority and fast share the Messages speed hint. Header policy stays with
	// the gateway, so this pure converter changes the request field alone.
	switch strings.ToLower(strings.TrimSpace(req.ServiceTier)) {
	case "priority", "fast":
		out.Speed = "fast"
	}

	if len(system) > 0 {
		out.System = system
	}

	if req.MaxOutputTokens != nil && *req.MaxOutputTokens > 0 {
		out.MaxTokens = *req.MaxOutputTokens
	}
	if out.MaxTokens == 0 {
		// Messages requires an output allowance. An absent or nonpositive Responses
		// limit uses this default, independently of any later effort-budget adjustment.
		out.MaxTokens = 8192
	}

	if len(req.Tools) > 0 {
		out.Tools = convertResponsesToAnthropicTools(req.Tools)
	}

	if len(req.ToolChoice) > 0 {
		tc, err := convertResponsesToAnthropicToolChoice(req.ToolChoice)
		if err != nil {
			return nil, fmt.Errorf("convert tool_choice: %w", err)
		}
		out.ToolChoice = tc
	}

	if req.Reasoning != nil && req.Reasoning.Effort != "" {
		if isUltraReasoningEffort(req.Reasoning.Effort) {
			return nil, fmt.Errorf("reasoning effort %q is not supported", strings.TrimSpace(req.Reasoning.Effort))
		}
		effort := mapResponsesEffortToAnthropic(req.Reasoning.Effort)
		out.OutputConfig = &AnthropicOutputConfig{Effort: effort}
		if effort != "low" {
			out.Thinking = &AnthropicThinking{
				Type:         "enabled",
				BudgetTokens: defaultThinkingBudget(effort),
			}
		}
	}

	return out, nil
}

// defaultThinkingBudget selects fixed token allowances for low, medium, high,
// and xhigh/max. Unrecognized exact effort spelling selects the high
// allowance; these values are independent of the request output limit and can
// exceed it.
func defaultThinkingBudget(effort string) int {
	// Effort selects a fixed token budget independently of the output limit.
	switch effort {
	case "low":
		return 1024
	case "medium":
		return 4096
	case "high":
		return 10240
	case "xhigh", "max":
		return 32768
	default:
		return 10240
	}
}

// mapResponsesEffortToAnthropic trims and lowercases recognized effort labels
// while retaining xhigh and max separately. Unknown values keep the supplied
// spelling, allowing downstream policy to reject or interpret them without an
// invented normalized level.
func mapResponsesEffortToAnthropic(effort string) string {
	switch value := strings.ToLower(strings.TrimSpace(effort)); value {
	case "low", "medium", "high", "xhigh", "max":
		return value
	default:
		return effort
	}
}

// convertResponsesInputToAnthropic separates trimmed instructions/system text
// from supported history and returns JSON system content plus Messages turns.
// String-decodable input takes a direct one-user-message path, including empty
// text. Array decoding errors abort conversion, and system/developer roles
// take precedence over item type.
//
// Function calls/results retain correlated IDs through the Anthropic name
// conversion. Reasoning is dropped, unsupported array parts are filtered, and
// empty/blank converted array messages disappear. The helper merges same-role
// messages, repairs pairing, then merges again. This retains answered calls
// next to results while allowing parallel groups; raw argument encode errors
// can leave nil content because those marshal errors are discarded, so callers
// validate argument JSON before relying on the resulting history.
func convertResponsesInputToAnthropic(instructions string, inputRaw json.RawMessage) (json.RawMessage, []AnthropicMessage, error) {
	var systemParts []string
	if strings.TrimSpace(instructions) != "" {
		systemParts = append(systemParts, strings.TrimSpace(instructions))
	}

	var inputStr string
	if err := json.Unmarshal(inputRaw, &inputStr); err == nil {
		// Scalar content and joined system text contain strings only. Their
		// encoders cannot fail from unsupported values or malformed retained raw JSON.
		content, _ := json.Marshal(inputStr)
		var system json.RawMessage
		if len(systemParts) > 0 {
			system, _ = json.Marshal(strings.Join(systemParts, "\n\n"))
		}
		return system, []AnthropicMessage{{Role: "user", Content: content}}, nil
	}

	var items []ResponsesInputItem
	if err := json.Unmarshal(inputRaw, &items); err != nil {
		return nil, nil, fmt.Errorf("parse responses input: %w", err)
	}

	var messages []AnthropicMessage

	for _, item := range items {
		switch {
		case item.Role == "system" || item.Role == "developer":
			text := extractTextFromContent(item.Content)
			if text != "" {
				systemParts = append(systemParts, text)
			}

		case item.Type == "function_call":
			// Calls occupy assistant tool_use blocks, while results occupy user
			// tool_result blocks. Apply the same ID conversion to both for later
			// pairing repair.
			input := json.RawMessage("{}")
			if item.Arguments != "" {
				input = json.RawMessage(item.Arguments)
			}
			block := AnthropicContentBlock{
				Type:  "tool_use",
				ID:    fromResponsesCallIDToAnthropic(item.CallID),
				Name:  item.Name,
				Input: input,
			}
			// Invalid raw argument JSON can fail this encoding. Its discarded error
			// leaves nil content and processing continues; callers validate arguments
			// before conversion.
			blockJSON, _ := json.Marshal([]AnthropicContentBlock{block})
			messages = append(messages, AnthropicMessage{
				Role:    "assistant",
				Content: blockJSON,
			})

		case item.Type == "function_call_output":
			contentJSON := responsesFunctionOutputToAnthropicContent(item)
			block := AnthropicContentBlock{
				Type:      "tool_result",
				ToolUseID: fromResponsesCallIDToAnthropic(item.CallID),
				Content:   contentJSON,
			}
			// The output helper constructs encoded strings or typed text/image blocks.
			// This wrapper therefore carries valid raw content and no unsupported
			// programmatic values.
			blockJSON, _ := json.Marshal([]AnthropicContentBlock{block})
			messages = append(messages, AnthropicMessage{
				Role:    "user",
				Content: blockJSON,
			})

		case item.Type == "reasoning":
			// This request path has no Anthropic replay signature for Responses
			// reasoning. Discard the whole item, including unsupported reasoning_text,
			// before content fallback can inspect it.

		case item.Role == "user":
			content, err := convertResponsesUserToAnthropicContent(item.Content)
			if err != nil {
				return nil, nil, err
			}
			// Filtering can remove every accepted part or leave only blank text. Omit
			// that message while retaining any separate tool-call/result items for
			// pairing.
			if anthropicContentIsEmpty(content) || anthropicContentIsOnlyBlankText(content) {
				continue
			}
			messages = append(messages, AnthropicMessage{
				Role:    "user",
				Content: content,
			})

		case item.Role == "assistant":
			content, err := convertResponsesAssistantToAnthropicContent(item.Content)
			if err != nil {
				return nil, nil, err
			}
			// Assistant filtering uses an empty text placeholder when no parts survive.
			// Drop it here so this conversion does not append a blank assistant turn.
			if anthropicContentIsEmpty(content) || anthropicContentIsOnlyBlankText(content) {
				continue
			}
			messages = append(messages, AnthropicMessage{
				Role:    "assistant",
				Content: content,
			})

		default:
			// Unknown items can still carry supported user text or inline images.
			// Use the user filter so Responses-only parts do not become Messages block kinds.
			if item.Content == nil {
				continue
			}
			content, err := convertResponsesUserToAnthropicContent(item.Content)
			if err != nil {
				return nil, nil, err
			}
			if anthropicContentIsEmpty(content) || anthropicContentIsOnlyBlankText(content) {
				continue
			}
			messages = append(messages, AnthropicMessage{
				Role:    "user",
				Content: content,
			})
		}
	}

	// First merge parallel calls and results so pairing repair sees each group
	// together. Pairing repair can split user turns around inserted messages;
	// merge again afterward to restore alternating roles.
	messages = mergeConsecutiveMessages(messages)
	messages = normalizeAnthropicToolPairing(messages)
	messages = mergeConsecutiveMessages(messages)

	var system json.RawMessage
	if len(systemParts) > 0 {
		system, _ = json.Marshal(strings.Join(systemParts, "\n\n"))
	}

	return system, messages, nil
}

// responsesFunctionOutputToAnthropicContent returns scalar output text or
// supported text/inline-image blocks from RawOutput. An absent raw value uses
// Output with (empty) for empty text; a decoded empty array also uses that
// placeholder.
//
// Nonempty accepted blocks preserve their array order. If a nonempty raw array
// contributes no accepted blocks, or array decoding fails, the helper string-
// encodes Output instead. RawOutput therefore decides whether structured media
// is inspected, but the scalar fallback can be empty. File parts, remote image
// URLs, and other unsupported parts do not become Messages media.
func responsesFunctionOutputToAnthropicContent(item ResponsesInputItem) json.RawMessage {
	if len(item.RawOutput()) == 0 {
		output := item.Output
		if output == "" {
			output = "(empty)"
		}
		// Scalar output is a string even when it uses the empty placeholder. Its
		// encoding has no unsupported values or retained raw fragments that could
		// fail.
		content, _ := json.Marshal(output)
		return content
	}

	var parts []ResponsesContentPart
	if err := json.Unmarshal(item.RawOutput(), &parts); err == nil {
		blocks := make([]AnthropicContentBlock, 0, len(parts))
		for _, part := range parts {
			switch part.Type {
			case "input_text", "output_text", "text":
				if part.Text != "" {
					blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: part.Text})
				}
			case "input_image":
				if source := dataURIToAnthropicImageSource(part.ImageURL); source != nil {
					blocks = append(blocks, AnthropicContentBlock{Type: "image", Source: source})
				}
			}
		}
		if len(blocks) > 0 {
			// These newly constructed blocks contain text or image source strings.
			// Their encoder has no invalid raw arguments, so its discarded error remains nil.
			content, _ := json.Marshal(blocks)
			return content
		}
		if len(parts) == 0 {
			// Empty structured output contributes the same placeholder as absent
			// scalar text. Encoding this literal string cannot lose a raw-JSON error.
			content, _ := json.Marshal("(empty)")
			return content
		}
	}

	// Unsupported structured output keeps its decoded scalar representation.
	// Output is a string, so encoding it cannot fail through a malformed raw
	// content member.
	content, _ := json.Marshal(item.Output)
	return content
}

// normalizeAnthropicToolPairing moves answered tool results beside their
// assistant calls and drops unanswered calls and orphan results. It indexes
// nonempty result IDs across user messages, retaining the last occurrence,
// then emits results in retained call order. Callers merge consecutive roles
// afterward.
//
// Non-tool assistant blocks are emitted before retained calls, so interleaving
// inside a mixed block list changes. Non-result user blocks stay at their
// original message position. Duplicate calls can reuse the same indexed
// result; the helper does not validate uniqueness or reject malformed content.
// It reconstructs only affected tool messages, while untouched message values
// retain their raw content.
func normalizeAnthropicToolPairing(messages []AnthropicMessage) []AnthropicMessage {
	// Later duplicate results replace earlier values at one ID. Index before
	// emission so each retained call can select that result even across
	// intervening turns.
	results := make(map[string]AnthropicContentBlock)
	for _, m := range messages {
		if m.Role != "user" {
			continue
		}
		for _, b := range parseContentBlocks(m.Content) {
			if b.Type == "tool_result" && b.ToolUseID != "" {
				results[b.ToolUseID] = b
			}
		}
	}

	out := make([]AnthropicMessage, 0, len(messages))
	for _, m := range messages {
		blocks := parseContentBlocks(m.Content)
		switch m.Role {
		case "assistant":
			var toolUses, others []AnthropicContentBlock
			for _, b := range blocks {
				if b.Type == "tool_use" {
					toolUses = append(toolUses, b)
				} else {
					others = append(others, b)
				}
			}
			if len(toolUses) == 0 {
				out = append(out, m)
				continue
			}
			kept := make([]AnthropicContentBlock, 0, len(toolUses))
			for _, tu := range toolUses {
				if _, ok := results[tu.ID]; ok {
					kept = append(kept, tu)
				}
			}
			if len(kept) == 0 {
				// Removing unanswered calls can empty an assistant message. Keep its
				// independent non-tool content so dropping an incomplete tool round does
				// not erase ordinary history.
				if len(others) > 0 {
					out = append(out, anthropicMessageFromBlocks("assistant", others))
				}
				continue
			}
			asstBlocks := make([]AnthropicContentBlock, 0, len(others)+len(kept))
			asstBlocks = append(asstBlocks, others...)
			asstBlocks = append(asstBlocks, kept...)
			out = append(out, anthropicMessageFromBlocks("assistant", asstBlocks))

			resBlocks := make([]AnthropicContentBlock, 0, len(kept))
			for _, tu := range kept {
				resBlocks = append(resBlocks, results[tu.ID])
			}
			out = append(out, anthropicMessageFromBlocks("user", resBlocks))

		case "user":
			var nonResult []AnthropicContentBlock
			hasResult := false
			for _, b := range blocks {
				if b.Type == "tool_result" {
					hasResult = true
					continue
				}
				nonResult = append(nonResult, b)
			}
			if !hasResult {
				out = append(out, m)
				continue
			}
			// Results have already been emitted beside retained calls. Remove their old
			// positions while keeping ordinary user blocks available at this point in
			// history.
			if len(nonResult) > 0 {
				out = append(out, anthropicMessageFromBlocks("user", nonResult))
			}

		default:
			out = append(out, m)
		}
	}
	return out
}

// anthropicMessageFromBlocks encodes blocks under the supplied role. Encoding
// errors are discarded and leave nil Content, so callers provide valid raw
// JSON members inside blocks; this helper does not validate roles or recover
// invalid argument bytes.
func anthropicMessageFromBlocks(role string, blocks []AnthropicContentBlock) AnthropicMessage {
	// Blocks can contain raw argument/content fragments. A failed encoding leaves
	// nil content because this helper discards its error; callers supply valid
	// fragments.
	content, _ := json.Marshal(blocks)
	return AnthropicMessage{Role: role, Content: content}
}

// extractTextFromContent returns string-decodable content or joins recognized
// nonempty text parts with paragraph separators. Absent bytes, unsupported
// parts, and failed array decoding contribute empty text; the helper extracts
// system text without returning validation errors.
func extractTextFromContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []ResponsesContentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		var texts []string
		for _, p := range parts {
			if (p.Type == "input_text" || p.Type == "output_text" || p.Type == "text") && p.Text != "" {
				texts = append(texts, p.Text)
			}
		}
		return strings.Join(texts, "\n\n")
	}
	// Neither accepted representation supplied system text. Omit that
	// contribution so an unrecognized shape does not become a guessed instruction string.
	return ""
}

// anthropicContentIsEmpty recognizes absent bytes, null, empty arrays, and
// string-decodable blank text. Nonempty block arrays are outside this
// predicate, including blank text blocks; callers combine it with
// anthropicContentIsOnlyBlankText when filtering converted history.
func anthropicContentIsEmpty(content json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(content))
	switch trimmed {
	case "", "null", `""`, "[]":
		return true
	}
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return strings.TrimSpace(text) == ""
	}
	return false
}

// anthropicContentIsOnlyBlankText reports a nonempty parsed block list
// consisting entirely of blank exact text blocks. Empty or unparseable content
// returns false, and any other kind prevents the result even when that block's
// own fields are empty.
func anthropicContentIsOnlyBlankText(content json.RawMessage) bool {
	blocks := parseContentBlocks(content)
	if len(blocks) == 0 {
		return false
	}
	for _, b := range blocks {
		if b.Type != "text" || strings.TrimSpace(b.Text) != "" {
			return false
		}
	}
	return true
}

// convertResponsesUserToAnthropicContent retains scalar text or converts
// nonblank input_text/text and data-URL input_image parts. Empty bytes or a
// fully filtered array become scalar empty text. Failed array decoding returns
// raw bytes for downstream validation; image payload encoding is not verified
// here.
func convertResponsesUserToAnthropicContent(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.Marshal("")
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return json.Marshal(s)
	}

	var parts []ResponsesContentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		// A failed typed-part decode can represent an unfamiliar content shape.
		// Retain raw bytes for downstream validation rather than publishing guessed
		// block fields.
		return raw, nil
	}

	var blocks []AnthropicContentBlock
	for _, p := range parts {
		switch p.Type {
		case "input_text", "text":
			if strings.TrimSpace(p.Text) != "" {
				blocks = append(blocks, AnthropicContentBlock{
					Type: "text",
					Text: p.Text,
				})
			}
		case "input_image":
			src := dataURIToAnthropicImageSource(p.ImageURL)
			if src != nil {
				blocks = append(blocks, AnthropicContentBlock{
					Type:   "image",
					Source: src,
				})
			}
		}
	}

	if len(blocks) == 0 {
		return json.Marshal("")
	}
	return json.Marshal(blocks)
}

// convertResponsesAssistantToAnthropicContent wraps scalar text and converts
// nonblank output_text/text array parts. Empty bytes or a fully filtered array
// become one empty text block, which the owning history converter filters.
// Failed array decoding retains raw bytes for downstream validation.
func convertResponsesAssistantToAnthropicContent(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.Marshal([]AnthropicContentBlock{{Type: "text", Text: ""}})
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return json.Marshal([]AnthropicContentBlock{{Type: "text", Text: s}})
	}

	var parts []ResponsesContentPart
	// An unfamiliar assistant content shape cannot be filtered as typed parts.
	// Retain its raw bytes so the caller or upstream can validate that supplied
	// representation.
	if err := json.Unmarshal(raw, &parts); err != nil {
		return raw, nil
	}

	var blocks []AnthropicContentBlock
	for _, p := range parts {
		switch p.Type {
		case "output_text", "text":
			if strings.TrimSpace(p.Text) != "" {
				blocks = append(blocks, AnthropicContentBlock{
					Type: "text",
					Text: p.Text,
				})
			}
		}
	}

	// A fully filtered assistant array needs a recognizable empty placeholder.
	// The owning history converter removes that block instead of sending an empty
	// array.
	if len(blocks) == 0 {
		blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: ""})
	}
	return json.Marshal(blocks)
}

// fromResponsesCallIDToAnthropic removes fc_ only from known toolu_/call_
// suffixes, otherwise retaining recognized correlation families or adding
// toolu_ to the supplied ID. Input is not trimmed or validated, and empty
// input becomes the prefix alone.
//
// Calls and results apply the same transformation so their correlation remains
// equal after history conversion. A new Responses item-like fc_ spelling is
// not treated as a reversible legacy wrapper unless its suffix identifies the
// recognized source family. This prevents unconditional stripping from
// changing unfamiliar IDs while supplying the Messages-side prefix for
// otherwise unrecognized correlation text.
func fromResponsesCallIDToAnthropic(id string) string {
	if after, ok := strings.CutPrefix(id, "fc_"); ok {
		if strings.HasPrefix(after, "toolu_") || strings.HasPrefix(after, "call_") {
			return after
		}
	}
	// Unrecognized correlation text receives the Messages tool-use prefix. Calls
	// and results apply this identical spelling change so their pairing keys
	// remain equal.
	if !strings.HasPrefix(id, "toolu_") && !strings.HasPrefix(id, "call_") {
		return "toolu_" + id
	}
	return id
}

// dataURIToAnthropicImageSource accepts an exact data: prefix followed by a
// first semicolon and base64, marker. Other spellings return nil. It retains
// media/payload text, including empty values, and performs no base64 or MIME
// validation; remote URLs are not converted.
func dataURIToAnthropicImageSource(dataURI string) *AnthropicImageSource {
	if !strings.HasPrefix(dataURI, "data:") {
		return nil
	}
	// Only inline base64 marker syntax becomes a source block. Retain payload
	// spelling for the target while leaving base64 and MIME validation to later
	// processing.
	rest := strings.TrimPrefix(dataURI, "data:")
	semicolonIdx := strings.Index(rest, ";")
	if semicolonIdx < 0 {
		return nil
	}
	mediaType := rest[:semicolonIdx]
	rest = rest[semicolonIdx+1:]
	if !strings.HasPrefix(rest, "base64,") {
		return nil
	}
	data := strings.TrimPrefix(rest, "base64,")
	return &AnthropicImageSource{
		Type:      "base64",
		MediaType: mediaType,
		Data:      data,
	}
}

// mergeConsecutiveMessages combines adjacent exact same-role messages by
// parsing and concatenating their blocks. Zero/one input message returns the
// supplied slice; larger input produces a new slice whose untouched raw
// content can still share source storage.
//
// Combining content changes scalar strings into text-block arrays and retains
// decoded block order. Unsupported content shapes parse to no blocks and can
// disappear during a merge; marshal failures leave nil content. The helper
// groups parallel call/result turns before pairing repair and joins roles
// introduced by that repair afterward, but does not independently establish
// valid tool correlation or accepted roles.
func mergeConsecutiveMessages(messages []AnthropicMessage) []AnthropicMessage {
	if len(messages) <= 1 {
		return messages
	}

	var merged []AnthropicMessage
	for _, msg := range messages {
		if len(merged) == 0 || merged[len(merged)-1].Role != msg.Role {
			merged = append(merged, msg)
			continue
		}

		// Same-role neighbors need one block list for grouped tool pairing. Scalar
		// content becomes text blocks, while unparseable shapes contribute no blocks
		// to the merge.
		last := &merged[len(merged)-1]
		lastBlocks := parseContentBlocks(last.Content)
		newBlocks := parseContentBlocks(msg.Content)
		combined := append(lastBlocks, newBlocks...)
		// Decoded blocks can still contain invalid programmatic raw fragments. A
		// discarded encoding error replaces this merged content with nil rather than
		// stopping the pass.
		last.Content, _ = json.Marshal(combined)
	}
	return merged
}

// parseContentBlocks decodes a block array or wraps string-decodable text in
// one block. Array decoding takes precedence, including null, and failed
// decoding of both forms returns nil; callers decide how lost or empty content
// affects history.
func parseContentBlocks(raw json.RawMessage) []AnthropicContentBlock {
	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		return blocks
	}
	// Scalar messages need a block shape for merging and pairing inspection.
	// When array decoding fails, wrap accepted string content in one text block.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []AnthropicContentBlock{{Type: "text", Text: s}}
	}
	// Neither accepted shape yielded content. Return no blocks so callers can
	// decide whether an empty contribution is filtered or retained in their
	// message flow.
	return nil
}

// convertResponsesToAnthropicTools preserves declaration order while mapping
// three search labels to the dated hosted search type. Function/custom
// declarations become unnamed-type tools with normalized object schemas;
// unknown types retain their type/name/description while receiving the same
// schema normalization.
func convertResponsesToAnthropicTools(tools []ResponsesTool) []AnthropicTool {
	var out []AnthropicTool
	for _, t := range tools {
		switch t.Type {
		case "web_search", "google_search", "web_search_20250305":
			// These hosted search spellings use one dated Messages declaration.
			// Supply its reserved name and omit the caller-executed function input schema.
			out = append(out, AnthropicTool{
				Type: "web_search_20250305",
				Name: "web_search",
			})
		case "function":
			out = append(out, AnthropicTool{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: normalizeAnthropicInputSchema(t.Parameters),
			})
		case "custom":
			// This declaration becomes an ordinary Messages tool with object input.
			// Custom grammar is absent from the emitted fields; callers lower freeform
			// history separately.
			out = append(out, AnthropicTool{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: normalizeAnthropicInputSchema(t.Parameters),
			})
		default:
			// Unknown kinds retain their declared identity fields for downstream
			// policy. Supply the same object-schema normalization used by ordinary
			// functions to prepare input_schema.
			out = append(out, AnthropicTool{
				Type:        t.Type,
				Name:        t.Name,
				Description: t.Description,
				InputSchema: normalizeAnthropicInputSchema(t.Parameters),
			})
		}
	}
	return out
}

// normalizeAnthropicInputSchema produces an object schema with a properties
// member. Absent/null, undecodable, or nonobject-type input uses the empty
// template; missing/null type is inserted, and missing properties is added
// while existing values remain.
//
// Retained object members use RawMessage so unrelated constraints and numeric
// text survive a successful rebuild. The helper validates the type label, not
// every schema member: supplied null or malformed properties can remain.
// Rebuild failure selects the empty template, and callers validate complete
// tool schemas if required by their upstream.
func normalizeAnthropicInputSchema(schema json.RawMessage) json.RawMessage {
	const emptyObjectSchema = `{"type":"object","properties":{}}`

	trimmed := strings.TrimSpace(string(schema))
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage(emptyObjectSchema)
	}

	var m map[string]json.RawMessage
	// Failed object decoding cannot retain schema members for repair. Select the
	// empty object template so the emitted declaration still has a prepared
	// input_schema.
	if err := json.Unmarshal(schema, &m); err != nil {
		return json.RawMessage(emptyObjectSchema)
	}

	typeRaw, ok := m["type"]
	if !ok || strings.TrimSpace(string(typeRaw)) == "" || string(typeRaw) == "null" {
		m["type"] = json.RawMessage(`"object"`)
	} else {
		var typ string
		// A supplied incompatible type cannot receive object properties safely.
		// Replace the whole schema with the object template rather than retaining
		// conflicting members.
		if err := json.Unmarshal(typeRaw, &typ); err != nil || typ != "object" {
			return json.RawMessage(emptyObjectSchema)
		}
	}

	if _, ok := m["properties"]; !ok {
		m["properties"] = json.RawMessage(`{}`)
	}

	out, err := json.Marshal(m)
	if err != nil {
		// Rebuilding failed after local repairs. Publish the known object template
		// so callers receive one prepared schema instead of partially encoded
		// replacement bytes.
		return json.RawMessage(emptyObjectSchema)
	}
	return out
}

// convertResponsesToAnthropicToolChoice maps known scalar selectors and
// function objects to Messages choices. Top-level trimmed name precedes the
// legacy nested name; blank selected names, unknown strings/types, and decode
// failures return source bytes with no error, leaving validation to the
// caller.
func convertResponsesToAnthropicToolChoice(raw json.RawMessage) (json.RawMessage, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch s {
		case "auto":
			return json.Marshal(map[string]string{"type": "auto"})
		case "required":
			return json.Marshal(map[string]string{"type": "any"})
		case "none":
			return json.Marshal(map[string]string{"type": "none"})
		default:
			return raw, nil
		}
	}

	var tc struct {
		Type string `json:"type"` // selected Responses tool kind
		// Name wins when its trimmed spelling is nonempty. Function.Name is
		// consulted only when this top-level selection supplies no usable name.
		Name string `json:"name"` // top-level selected function name
		// Function supplies the legacy name alternative for exact function type.
		// Other nested fields do not enter the emitted Messages tool choice.
		Function struct {
			Name string `json:"name"` // legacy nested function name
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &tc); err == nil && tc.Type == "function" {
		name := strings.TrimSpace(tc.Name)
		// Older selectors nest the selected function name. Consult that member
		// only when the preferred top-level name is blank after trimming.
		if name == "" {
			name = strings.TrimSpace(tc.Function.Name)
		}
		if name == "" {
			return raw, nil
		}
		return json.Marshal(map[string]string{
			"type": "tool",
			"name": name,
		})
	}

	// Unknown or undecodable selectors can carry forms this decoder does not
	// model. Retain their bytes so downstream validation receives the supplied selection.
	return raw, nil
}
