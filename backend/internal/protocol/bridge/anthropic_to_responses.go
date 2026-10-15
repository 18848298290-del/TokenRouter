package bridge

import (
	"encoding/json"
	"fmt"
	"strings"
)

// anthropic_to_responses.go converts Anthropic requests and conversation
// blocks into Responses input.
// Anthropic history combines text, tool results, images, and signed thinking
// inside message blocks. Responses stores calls and results as separate input
// items, so conversion preserves call IDs while grouping supported text and
// media into role-bearing messages. System text becomes a developer item at
// the front of input, and billing-header text is filtered from that system
// content.
//
// User block conversion emits results first and appends extracted result
// images to the remaining user content. Assistant conversion emits accepted
// encrypted reasoning before joined text and tool calls. These choices change
// block ordering, and unsupported blocks are omitted. Request options govern
// sampling and max-effort support, while token floors, explicit storage
// settings, and tool-schema repairs provide the target representation.

// AnthropicToResponses builds a Responses request from a nonnil Messages
// request and conversion options. It copies model/stream values, filters
// system and history content through the block converters, requests encrypted
// reasoning, disables storage, enables parallel calls, and supplies medium
// text verbosity. Sampling pointers remain shared with the source when
// retained.
//
// Positive token limits receive the Responses floor; absent output effort
// selects medium, and ultra returns an error. Max support controls only effort
// mapping. Tool-choice errors abort the returned request, while source history
// remains untouched; this conversion does not validate every retained tool
// argument or schema.
func AnthropicToResponses(req *AnthropicRequest, options RequestOptions) (*ResponsesRequest, error) {
	input, err := convertAnthropicToResponsesInput(req.System, req.Messages)
	if err != nil {
		return nil, err
	}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}

	out := &ResponsesRequest{
		Model:   req.Model,
		Input:   inputJSON,
		Stream:  req.Stream,
		Include: []string{"reasoning.encrypted_content"},
	}
	// Fast spelling selects the request-level priority hint; provider policy later
	// decides the final outbound service tier and supplies any transport headers.
	if strings.EqualFold(strings.TrimSpace(req.Speed), "fast") {
		out.ServiceTier = "priority"
	}

	if !options.DropSampling && !options.DropTemperature {
		out.Temperature = req.Temperature
	}
	if !options.DropSampling && !options.DropTopP {
		out.TopP = req.TopP
	}

	storeFalse := false
	out.Store = &storeFalse
	parallelToolCalls := true
	out.ParallelToolCalls = &parallelToolCalls
	out.Text = &ResponsesText{Verbosity: "medium"}

	if req.MaxTokens > 0 {
		// Apply the shared Responses floor to positive supplied token limits.
		v := max(req.MaxTokens, minMaxOutputTokens)
		out.MaxOutputTokens = &v
	}

	if len(req.Tools) > 0 {
		out.Tools = convertAnthropicToolsToResponses(req.Tools)
	}

	// output_config carries effort independently of the thinking budget. Map its
	// selected value for Responses, and request summaries alongside encrypted history.
	effort := "medium"
	if req.OutputConfig != nil && req.OutputConfig.Effort != "" {
		effort = req.OutputConfig.Effort
	}
	if isUltraReasoningEffort(effort) {
		return nil, fmt.Errorf("reasoning effort %q is not supported", strings.TrimSpace(effort))
	}
	out.Reasoning = &ResponsesReasoning{
		Effort:  mapAnthropicEffortToResponses(effort, options.SupportsMaxEffort),
		Summary: "auto",
	}

	// Tool selectors use different mandatory/named spellings across protocols.
	// Convert the selected object while letting unknown forms reach later validation.
	if len(req.ToolChoice) > 0 {
		tc, err := convertAnthropicToolChoiceToResponses(req.ToolChoice)
		if err != nil {
			return nil, fmt.Errorf("convert tool_choice: %w", err)
		}
		out.ToolChoice = tc
	}

	return out, nil
}

// convertAnthropicToolChoiceToResponses maps auto, any, none, and named tool
// objects to Responses selectors. Object decode errors are returned; unknown
// or missing types retain the supplied raw bytes, and selected names are
// forwarded without trimming or validation.
func convertAnthropicToolChoiceToResponses(raw json.RawMessage) (json.RawMessage, error) {
	var tc struct {
		// Type controls selector translation; Name is used only for the tool case.
		// Other types retain raw bytes, including their unmodeled extension fields.
		Type string `json:"type"` // Anthropic selection kind label
		Name string `json:"name"` // selected tool name spelling
	}
	if err := json.Unmarshal(raw, &tc); err != nil {
		return nil, err
	}

	switch tc.Type {
	case "auto":
		return json.Marshal("auto")
	case "any":
		return json.Marshal("required")
	case "none":
		return json.Marshal("none")
	case "tool":
		return json.Marshal(map[string]any{
			"type": "function",
			"name": tc.Name,
		})
	default:
		// Unknown selector kinds can carry extensions absent from this decoder.
		// Retain raw bytes so later validation receives the supplied form.
		return raw, nil
	}
}

// convertAnthropicToResponsesInput assembles developer system content before
// the converted messages. String or block system decode failures and message
// decode failures abort the result; a system value that contributes no
// accepted text creates no developer item.
//
// Keeping system content in input retains its position relative to the
// supplied conversation. Each message can produce multiple Responses items, so
// the helper appends the entire per-message result in original message order.
// The user and assistant converters own ordering inside each such result,
// including extracted tool media and signed reasoning.
func convertAnthropicToResponsesInput(system json.RawMessage, msgs []AnthropicMessage) ([]ResponsesInputItem, error) {
	var out []ResponsesInputItem

	// System text remains a leading conversation item under the developer role.
	// Keeping its content in input preserves that location when message items are
	// appended.
	if len(system) > 0 {
		sysParts, err := parseAnthropicSystemContentParts(system)
		if err != nil {
			return nil, err
		}
		if len(sysParts) > 0 {
			// These parts contain constructed text fields and no retained raw JSON.
			// Their encoding cannot fail through an unsupported value or malformed raw
			// fragment.
			content, _ := json.Marshal(sysParts)
			out = append(out, ResponsesInputItem{
				Type:    "message",
				Role:    "developer",
				Content: content,
			})
		}
	}

	for _, m := range msgs {
		items, err := anthropicMsgToResponsesItems(m)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

// parseAnthropicSystemContentParts accepts string-decodable JSON or an array
// of Anthropic blocks and returns nonempty text parts. Exact billing-header
// prefixes are omitted. Unsupported block kinds are skipped, whitespace-only
// text survives, and array decode failures are returned.
func parseAnthropicSystemContentParts(raw json.RawMessage) ([]ResponsesContentPart, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if isAnthropicBillingHeaderText(s) || s == "" {
			return nil, nil
		}
		return []ResponsesContentPart{{Type: "input_text", Text: s}}, nil
	}
	// A value that is not string-decodable can use the block-array system form.
	// Keep accepted text and discard the client billing marker before
	// constructing developer input.
	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}
	var parts []ResponsesContentPart
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" && !isAnthropicBillingHeaderText(b.Text) {
			parts = append(parts, ResponsesContentPart{Type: "input_text", Text: b.Text})
		}
	}
	return parts, nil
}

// isAnthropicBillingHeaderText reports the exact billing-header prefix used by
// system filtering.
func isAnthropicBillingHeaderText(text string) bool {
	return strings.HasPrefix(text, "x-anthropic-billing-header: ")
}

// anthropicMsgToResponsesItems dispatches assistant history to its specialized
// converter and user history to the user converter. Every other exact role
// also follows user conversion, so the returned message role can differ from
// unsupported source roles.
func anthropicMsgToResponsesItems(m AnthropicMessage) ([]ResponsesInputItem, error) {
	switch m.Role {
	case "user":
		return anthropicUserToResponses(m.Content)
	case "assistant":
		return anthropicAssistantToResponses(m.Content)
	default:
		// Unsupported roles still have reusable user content. Use that converter so
		// supported text, images, and results follow one consistent target
		// representation.
		return anthropicUserToResponses(m.Content)
	}
}

// anthropicUserToResponses converts string-decodable content to one user
// message or splits an array into result items and supported user parts.
// Block-array decode errors are returned. Result call IDs retain their source
// spelling, and unusable image sources contribute no part.
//
// All tool results precede the combined user message, even when text was
// interleaved with results in the source. Each result supplies text separately
// from its images; extracted images follow ordinary text/image parts so they
// remain visible in history. Unsupported blocks and empty text blocks
// disappear, and no user message is emitted when no parts remain.
func anthropicUserToResponses(raw json.RawMessage) ([]ResponsesInputItem, error) {
	// Scalar text becomes one role-bearing message. Attempt it before splitting
	// block arrays into distinct tool, media, and reasoning representations.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		parts := []ResponsesContentPart{{Type: "input_text", Text: s}}
		partsJSON, err := json.Marshal(parts)
		if err != nil {
			return nil, err
		}
		return []ResponsesInputItem{{Type: "message", Role: "user", Content: partsJSON}}, nil
	}

	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}

	var out []ResponsesInputItem
	var toolResultImageParts []ResponsesContentPart

	// This conversion emits string tool output and places result images in user
	// content. Gather every result first so its call ID and extracted media
	// survive separately.
	for _, b := range blocks {
		if b.Type != "tool_result" {
			continue
		}
		outputText, imageParts := convertToolResultOutput(b)
		out = append(out, ResponsesInputItem{
			Type:   "function_call_output",
			CallID: toResponsesCallID(b.ToolUseID),
			Output: outputText,
		})
		toolResultImageParts = append(toolResultImageParts, imageParts...)
	}

	// Preserve ordinary supported user parts, then append images extracted from
	// results. The target message carries media that the chosen string-output
	// representation omitted.
	var parts []ResponsesContentPart
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				parts = append(parts, ResponsesContentPart{Type: "input_text", Text: b.Text})
			}
		case "image":
			if uri := anthropicImageToDataURI(b.Source); uri != "" {
				parts = append(parts, ResponsesContentPart{Type: "input_image", ImageURL: uri})
			}
		}
	}
	parts = append(parts, toolResultImageParts...)

	if len(parts) > 0 {
		content, err := json.Marshal(parts)
		if err != nil {
			return nil, err
		}
		out = append(out, ResponsesInputItem{Type: "message", Role: "user", Content: content})
	}

	return out, nil
}

// anthropicAssistantToResponses converts string-decodable content to one
// assistant message or emits selected reasoning, joined text, and tool calls
// from a block array. Decode errors abort the result. Tool arguments remain
// supplied JSON text, with an empty raw input represented by an empty object.
//
// Accepted signatures become encrypted reasoning items before text and calls.
// Empty signatures and the gAAAA-prefixed family are omitted, and thinking
// text is not emitted as ordinary content. Text blocks join with paragraph
// separators, then tool_use blocks retain their call IDs in source call order.
// Other blocks contribute nothing, so source interleaving is not retained
// within the emitted groups.
func anthropicAssistantToResponses(raw json.RawMessage) ([]ResponsesInputItem, error) {
	// Scalar text becomes one role-bearing message. Attempt it before splitting
	// block arrays into distinct tool, media, and reasoning representations.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		parts := []ResponsesContentPart{{Type: "output_text", Text: s}}
		partsJSON, err := json.Marshal(parts)
		if err != nil {
			return nil, err
		}
		return []ResponsesInputItem{{Type: "message", Role: "assistant", Content: partsJSON}}, nil
	}

	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}

	var items []ResponsesInputItem

	// Group accepted reasoning before text and calls so replay input contains
	// the encrypted reasoning prefix before the assistant work it accompanied.
	for _, b := range blocks {
		if b.Type != "thinking" {
			continue
		}
		sig := strings.TrimSpace(b.Signature)
		// This replay path excludes empty signatures and the gAAAA-prefixed family.
		// Other signatures retain trimmed spelling in an encrypted-content reasoning item.
		if sig == "" || strings.HasPrefix(sig, "gAAAA") {
			continue
		}
		items = append(items, ResponsesInputItem{
			Type:             "reasoning",
			EncryptedContent: sig,
		})
	}

	// Joined text uses assistant output_text, while encrypted thinking remains a
	// separate item. This prevents thinking text from becoming ordinary replayed
	// assistant content.
	text := extractAnthropicTextFromBlocks(blocks)
	if text != "" {
		parts := []ResponsesContentPart{{Type: "output_text", Text: text}}
		partsJSON, err := json.Marshal(parts)
		if err != nil {
			return nil, err
		}
		items = append(items, ResponsesInputItem{Type: "message", Role: "assistant", Content: partsJSON})
	}

	// Function items retain tool-use IDs as call correlation and encode raw
	// input as argument text; absent input needs an object representation for replay.
	for _, b := range blocks {
		if b.Type != "tool_use" {
			continue
		}
		args := "{}"
		if len(b.Input) > 0 {
			args = string(b.Input)
		}
		fcID := toResponsesCallID(b.ID)
		items = append(items, ResponsesInputItem{
			Type:      "function_call",
			CallID:    fcID,
			Name:      b.Name,
			Arguments: args,
		})
	}

	return items, nil
}

// toResponsesCallID returns the source tool ID verbatim for call/result
// correlation.
func toResponsesCallID(id string) string {
	return id
}

// fromResponsesCallID removes an fc_ wrapper only when its suffix begins with
// toolu_ or call_. All other spelling passes through, including empty IDs and
// unfamiliar item-like prefixes; inputs are not trimmed.
//
// Messages response and stream converters apply this legacy reversal to tool-
// call correlation. Current Anthropic-to-Responses request conversion retains
// IDs verbatim, so most IDs already need no change. Restricting wrapper
// removal to recognizable correlation families prevents stripping a new or
// vendor-specific ID whose prefix happens to resemble a legacy wrapper. The
// helper does not generate missing IDs or validate uniqueness.
func fromResponsesCallID(id string) string {
	if after, ok := strings.CutPrefix(id, "fc_"); ok {
		// Known legacy wrappers can be reversed without guessing the source
		// identity. Strip fc_ for toolu_/call_ suffixes while leaving other vendor
		// ID families intact.
		if strings.HasPrefix(after, "toolu_") || strings.HasPrefix(after, "call_") {
			return after
		}
	}
	return id
}

// anthropicImageToDataURI constructs a base64 data URL from a nonnil source
// with nonempty Data. An absent MediaType selects image/png. Source Type, URL,
// payload encoding, and MIME validity are not checked by this construction
// helper.
func anthropicImageToDataURI(src *AnthropicImageSource) string {
	if src == nil || src.Data == "" {
		return ""
	}
	mediaType := src.MediaType
	if mediaType == "" {
		mediaType = "image/png"
	}
	return "data:" + mediaType + ";base64," + src.Data
}

// convertToolResultOutput separates a tool-result block into text and inline
// image parts. Missing, empty string-decodable, or undecodable array content
// selects the literal (empty) text. Nonempty text blocks join with paragraph
// separators; source error flags and unsupported blocks do not enter the
// result.
//
// The caller emits the text as function output and moves the returned images
// into user content. This split retains usable image data while choosing a
// string representation for the result item. An image-only array therefore
// returns (empty) alongside images, and image-source validation is limited to
// the data URL constructor.
func convertToolResultOutput(b AnthropicContentBlock) (string, []ResponsesContentPart) {
	if len(b.Content) == 0 {
		return "(empty)", nil
	}

	// The scalar content form precedes block parsing. Its empty text needs a
	// printable placeholder so a result item remains available for call/result pairing.
	var s string
	if err := json.Unmarshal(b.Content, &s); err == nil {
		if s == "" {
			s = "(empty)"
		}
		return s, nil
	}

	// Content arrays can contain both text and images.
	var inner []AnthropicContentBlock
	// A failed block decode supplies no usable text or image fields. Keep the
	// result item through the empty placeholder rather than aborting conversation
	// conversion.
	if err := json.Unmarshal(b.Content, &inner); err != nil {
		return "(empty)", nil
	}

	// Split recognized result blocks by their target representation: text stays
	// in function output, and images are returned for the following user message.
	var textParts []string
	var imageParts []ResponsesContentPart
	for _, ib := range inner {
		switch ib.Type {
		case "text":
			if ib.Text != "" {
				textParts = append(textParts, ib.Text)
			}
		case "image":
			if uri := anthropicImageToDataURI(ib.Source); uri != "" {
				imageParts = append(imageParts, ResponsesContentPart{Type: "input_image", ImageURL: uri})
			}
		}
	}

	text := strings.Join(textParts, "\n\n")
	if text == "" {
		text = "(empty)"
	}
	return text, imageParts
}

// extractAnthropicTextFromBlocks joins nonempty exact text blocks with
// paragraph separators.
func extractAnthropicTextFromBlocks(blocks []AnthropicContentBlock) string {
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// mapAnthropicEffortToResponses trims and lowercases effort, mapping max to
// xhigh when max support is false. Supported max remains max; every other
// normalized value passes through for later policy or upstream validation.
func mapAnthropicEffortToResponses(effort string, supportsMax bool) string {
	normalized := strings.ToLower(strings.TrimSpace(effort))
	switch normalized {
	case "max":
		if supportsMax {
			return "max"
		}
		// An upstream without max support uses xhigh for the same selected maximum
		// effort request; callers supply support policy from the final model.
		return "xhigh"
	default:
		return normalized
	}
}

// convertAnthropicToolsToResponses converts every supplied definition in
// order. Types beginning with web_search become the hosted web_search label;
// every other definition becomes a nonstrict function with a normalized
// parameter schema, even when its name or source type is unfamiliar.
func convertAnthropicToolsToResponses(tools []AnthropicTool) []ResponsesTool {
	var out []ResponsesTool
	for _, t := range tools {
		// Hosted search uses one target label across dated Anthropic variants. Keep
		// that hosted form instead of exposing it as a caller-executed named
		// function.
		if strings.HasPrefix(t.Type, "web_search") {
			out = append(out, ResponsesTool{Type: "web_search"})
			continue
		}
		// Ordinary declarations use function parameters and a nonstrict flag. The
		// schema helper repairs missing object properties while retaining
		// unrecognized schema bytes.
		out = append(out, ResponsesTool{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  normalizeToolParameters(t.InputSchema),
			Strict:      boolPtr(false),
		})
	}
	return out
}

// boolPtr allocates a boolean value for optional wire fields.
func boolPtr(v bool) *bool {
	return &v
}

// normalizeToolParameters supplies an object/properties schema for absent
// bytes or the literal null and adds missing properties to exact object types.
// Decode failures, other types, existing properties, and rebuild failures
// retain source bytes. The helper performs these repairs rather than complete
// schema validation.
//
// Retained members use RawMessage during object repair, so numeric tokens and
// unrelated constraints survive rebuilding without float64 conversion.
// Rebuilding can change layout and key order; passthrough results share the
// supplied raw slice.
func normalizeToolParameters(schema json.RawMessage) json.RawMessage {
	if len(schema) == 0 || string(schema) == "null" {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}

	var m map[string]json.RawMessage
	// An undecodable schema cannot be repaired by adding object members. Return
	// its bytes for downstream validation instead of replacing its intended
	// constraints.
	if err := json.Unmarshal(schema, &m); err != nil {
		return schema
	}

	typ := m["type"]
	if string(typ) != `"object"` {
		return schema
	}

	if _, ok := m["properties"]; ok {
		return schema
	}

	// Missing properties is repaired only after selecting the exact object type.
	// Existing properties, including null, retain their supplied representation
	// for downstream validation.
	m["properties"] = json.RawMessage(`{}`)
	out, err := json.Marshal(m)
	if err != nil {
		// Failed rebuilding supplies no replacement schema. Keep source constraints
		// available to the later validator instead of publishing an empty candidate.
		return schema
	}
	return out
}

// isUltraReasoningEffort reports trimmed, case-insensitive spelling of the
// unsupported ultra effort.
func isUltraReasoningEffort(effort string) bool {
	return strings.EqualFold(strings.TrimSpace(effort), "ultra")
}
