package bridge

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// Chat content can be scalar text, multimodal parts, assistant reasoning, or
// tool-result history. Responses input represents user media as typed parts
// and tool calls/results as separate items, while assistant reasoning is
// represented here by thinking tags inside output text. Unsupported user parts
// are filtered, and assistant content that cannot be decoded as either
// supported outer shape contributes no text.
//
// The request always asks the upstream to stream and disables storage, even
// when the original Chat client wanted a buffered reply. Callers retain the
// client mode separately while downstream readers consume Responses events.
// Token-limit precedence depends on field presence, tool declarations precede
// legacy functions, and explicit tool choice outranks legacy selection.
// Retained optional pointers and raw declaration values can still share source
// storage.

// chatMessageContent retains the selected scalar or array shape for message
// conversion.
type chatMessageContent struct {
	// Text takes precedence over Parts when nonnil, including an empty value.
	// Parsers allocate it for scalar content and absent raw bytes.
	Text *string // selected scalar content text
	// Parts is read only when Text is nil. Retained slices can share caller
	// storage, and conversion filters individual parts without mutating them.
	Parts []ChatContentPart // selected typed content parts
}

// ChatCompletionsToResponses builds a Responses request from a nonnil Chat
// request and model conversion options. It forces upstream streaming, disables
// storage, requests encrypted reasoning, and retains supplied
// model/instructions/service tier. The caller keeps the original client mode
// for response delivery. Retained sampling and parallel-call pointers can
// share source storage.
//
// A present max_completion_tokens overrides max_tokens even at zero; only a
// positive selected value receives the output floor. Ultra effort is rejected,
// other nonempty effort is forwarded, and response format uses the typed
// format converter. Modern tools and legacy functions both contribute
// declarations, while a present raw tool_choice outranks function_call. Input
// decode failures and legacy selector decode failures abort the returned
// request.
func ChatCompletionsToResponses(req *ChatCompletionsRequest, options RequestOptions) (*ResponsesRequest, error) {
	if isUltraReasoningEffort(req.ReasoningEffort) {
		return nil, fmt.Errorf("reasoning effort %q is not supported", strings.TrimSpace(req.ReasoningEffort))
	}

	input, err := convertChatMessagesToResponsesInput(req.Messages)
	if err != nil {
		return nil, err
	}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}

	out := &ResponsesRequest{
		Model:             req.Model,
		Instructions:      req.Instructions,
		Input:             inputJSON,
		Stream:            true, // upstream always streams
		Include:           []string{"reasoning.encrypted_content"},
		ServiceTier:       req.ServiceTier,
		ParallelToolCalls: req.ParallelToolCalls,
	}

	// Callers choose which sampling parameters to drop using the model's rules.
	if !options.DropSampling && !options.DropTemperature {
		out.Temperature = req.Temperature
	}
	if !options.DropSampling && !options.DropTopP {
		out.TopP = req.TopP
	}

	storeFalse := false
	out.Store = &storeFalse

	// Field presence decides the output-limit source before applying a floor. An
	// explicit nonpositive completion limit suppresses a positive legacy
	// max_tokens value.
	maxTokens := 0
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}
	if req.MaxCompletionTokens != nil {
		maxTokens = *req.MaxCompletionTokens
	}
	if maxTokens > 0 {
		// Keep positive output limits above the shared Responses token floor.
		v := max(maxTokens, minMaxOutputTokens)
		out.MaxOutputTokens = &v
	}

	// Responses separates effort from requested summary behavior. Retain the
	// supplied effort and request summaries so response conversion can read
	// reasoning text.
	if req.ReasoningEffort != "" {
		out.Reasoning = &ResponsesReasoning{
			Effort:  req.ReasoningEffort,
			Summary: "auto",
		}
	}

	if format := chatResponseFormatToResponsesTextFormat(req.ResponseFormat); len(format) > 0 {
		if out.Text == nil {
			out.Text = &ResponsesText{}
		}
		out.Text.Format = format
	}

	// Both modern and legacy declarations can be supplied together. Append
	// supported modern tools before legacy functions so their original group
	// order is retained.
	if len(req.Tools) > 0 || len(req.Functions) > 0 {
		out.Tools = convertChatToolsToResponses(req.Tools, req.Functions)
	}

	// Modern raw choice takes precedence, including literal null. Only an absent
	// modern value permits legacy name conversion to the Responses function-
	// choice shape.
	if len(req.ToolChoice) > 0 {
		out.ToolChoice = req.ToolChoice
	} else if len(req.FunctionCall) > 0 {
		tc, err := convertChatFunctionCallToToolChoice(req.FunctionCall)
		if err != nil {
			return nil, fmt.Errorf("convert function_call: %w", err)
		}
		out.ToolChoice = tc
	}

	return out, nil
}

// convertChatMessagesToResponsesInput appends each message converter's
// complete result in input order. A conversion error aborts the collected
// slice; successful messages can expand into several call/result items or
// contribute no assistant item.
func convertChatMessagesToResponsesInput(msgs []ChatMessage) ([]ResponsesInputItem, error) {
	var out []ResponsesInputItem
	for _, m := range msgs {
		items, err := chatMessageToResponsesItems(m)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

// chatMessageToResponsesItems selects conversion by exact Chat role, including
// legacy function results. Unknown roles use user conversion, so their
// supported content is emitted as user input; the helper returns the selected
// converter's errors.
func chatMessageToResponsesItems(m ChatMessage) ([]ResponsesInputItem, error) {
	switch m.Role {
	case "system":
		return chatSystemToResponses(m)
	case "user":
		return chatUserToResponses(m)
	case "assistant":
		return chatAssistantToResponses(m)
	case "tool":
		return chatToolToResponses(m)
	case "function":
		return chatFunctionToResponses(m)
	default:
		// Unknown roles can still contain supported input content. User conversion
		// retains those text/media parts under one recognized target role.
		return chatUserToResponses(m)
	}
}

// chatSystemToResponses encodes supported string or multimodal content under
// the system role. Parse and encode errors are returned, and content whose
// parts all disappear becomes an empty string through marshalChatInputContent.
func chatSystemToResponses(m ChatMessage) ([]ResponsesInputItem, error) {
	parsed, err := parseChatMessageContent(m.Content)
	if err != nil {
		return nil, err
	}
	content, err := marshalChatInputContent(parsed)
	if err != nil {
		return nil, err
	}
	return []ResponsesInputItem{{Role: "system", Content: content}}, nil
}

// chatUserToResponses encodes supported string or multimodal content under the
// user role. Parse failures receive user-content context; encode failures
// propagate, and the target parts are limited to supported text, usable image
// URLs, and supplied file identifiers/data.
func chatUserToResponses(m ChatMessage) ([]ResponsesInputItem, error) {
	parsed, err := parseChatMessageContent(m.Content)
	if err != nil {
		return nil, fmt.Errorf("parse user content: %w", err)
	}
	content, err := marshalChatInputContent(parsed)
	if err != nil {
		return nil, err
	}
	return []ResponsesInputItem{{Role: "user", Content: content}}, nil
}

// chatAssistantToResponses emits assistant output text before function-call
// items. The message's selected reasoning text is wrapped in thinking tags,
// then nonempty decoded assistant content follows on a new line. Empty
// combined content creates no message, while tool calls are still emitted.
//
// Each call retains its ID as result correlation and its supplied
// name/argument text. Empty arguments use an object spelling, and no argument
// JSON validation occurs here. Reasoning and content parts therefore become
// one text representation, whereas call identity stays in separate input
// items. Source tool-call order survives after the assistant text item.
func chatAssistantToResponses(m ChatMessage) ([]ResponsesInputItem, error) {
	var items []ResponsesInputItem
	content := ""

	// Chat reasoning lacks an encrypted Responses item here. Thinking tags keep
	// its selected text distinguishable inside the combined assistant output-text
	// representation.
	if reasoning := m.ReasoningText(); reasoning != "" {
		content = "<thinking>" + reasoning + "</thinking>"
	}

	// Scalar and array assistant content share the same textual representation.
	// Append decoded content after tagged reasoning so neither nonempty
	// contribution is overwritten.
	if len(m.Content) > 0 {
		s, err := parseAssistantContent(m.Content)
		if err != nil {
			return nil, err
		}
		if s != "" {
			if content != "" {
				content += "\n"
			}
			content += s
		}
	}

	if content != "" {
		parts := []ResponsesContentPart{{Type: "output_text", Text: content}}
		partsJSON, err := json.Marshal(parts)
		if err != nil {
			return nil, err
		}
		items = append(items, ResponsesInputItem{Role: "assistant", Content: partsJSON})
	}

	// Separate function items retain correlation IDs independently of assistant
	// text. An absent argument string becomes an object value for later replay.
	for _, tc := range m.ToolCalls {
		args := tc.Function.Arguments
		if args == "" {
			args = "{}"
		}
		items = append(items, ResponsesInputItem{
			Type:      "function_call",
			CallID:    tc.ID,
			Name:      tc.Function.Name,
			Arguments: args,
		})
	}

	return items, nil
}

// parseAssistantContent extracts text from a string-decodable value or a
// decoded array of maps. Missing bytes contribute empty text; an unsupported
// or malformed outer array shape also contributes empty text with no error,
// allowing calls elsewhere in the message to remain usable.
//
// Exact thinking/reasoning kinds use nonempty thinking before text and wrap
// the selected string in thinking tags. Other kinds contribute any string text
// field, even for unfamiliar labels. Adjacent contributions concatenate
// directly. Nonstring fields disappear through type assertions, and the
// current Builder writes return nil errors, so the error result is unused by
// successful construction.
func parseAssistantContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}

	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err != nil {
		// A failed outer content decode leaves no accepted text. Keep converting
		// the message's separate tool calls rather than aborting their usable history.
		return "", nil
	}

	var b strings.Builder
	write := func(v string) error {
		_, err := b.WriteString(v)
		return err
	}
	for _, p := range parts {
		// Only string fields contribute text. Unsupported field values supply no
		// printable content, while unfamiliar kinds can still carry a usable text string.
		typ, _ := p["type"].(string)
		text, _ := p["text"].(string)
		thinking, _ := p["thinking"].(string)

		switch typ {
		case "thinking", "reasoning":
			// Retain one reasoning contribution: thinking wins when nonempty, with text
			// as its alternate field. Tags distinguish that contribution after
			// concatenation.
			if thinking != "" {
				if err := write("<thinking>"); err != nil {
					return "", err
				}
				if err := write(thinking); err != nil {
					return "", err
				}
				if err := write("</thinking>"); err != nil {
					return "", err
				}
			} else if text != "" {
				if err := write("<thinking>"); err != nil {
					return "", err
				}
				if err := write(text); err != nil {
					return "", err
				}
				if err := write("</thinking>"); err != nil {
					return "", err
				}
			}
		default:
			if text != "" {
				if err := write(text); err != nil {
					return "", err
				}
			}
		}
	}

	return b.String(), nil
}

// chatToolToResponses emits one string-valued function result correlated by
// ToolCallID. Parsed empty text selects the literal (empty); multimodal arrays
// contribute text alone, and a content parse error prevents the item from
// being returned.
func chatToolToResponses(m ChatMessage) ([]ResponsesInputItem, error) {
	output, err := parseChatContent(m.Content)
	if err != nil {
		return nil, err
	}
	// A textless result still needs a scalar history contribution. Use the shared
	// placeholder so media-only or empty content does not erase its correlated
	// output item.
	if output == "" {
		output = "(empty)"
	}
	return []ResponsesInputItem{{
		Type:   "function_call_output",
		CallID: m.ToolCallID,
		Output: output,
	}}, nil
}

// chatFunctionToResponses emits a legacy function result using Name as call
// correlation. Parsed empty text selects the literal (empty); modern
// ToolCallID is ignored on this role, and content parsing errors abort the
// result.
func chatFunctionToResponses(m ChatMessage) ([]ResponsesInputItem, error) {
	output, err := parseChatContent(m.Content)
	if err != nil {
		return nil, err
	}
	// A textless result still needs a scalar history contribution. Use the shared
	// placeholder so media-only or empty content does not erase its correlated
	// output item.
	if output == "" {
		output = "(empty)"
	}
	return []ResponsesInputItem{{
		Type:   "function_call_output",
		CallID: m.Name,
		Output: output,
	}}, nil
}

// parseChatContent returns decoded scalar text or concatenated nonempty text
// parts from a Chat content array. Unsupported outer shapes return an error,
// while media and other part kinds contribute no string result.
func parseChatContent(raw json.RawMessage) (string, error) {
	parsed, err := parseChatMessageContent(raw)
	if err != nil {
		return "", err
	}
	if parsed.Text != nil {
		return *parsed.Text, nil
	}
	return flattenChatContentParts(parsed.Parts), nil
}

// parseChatMessageContent distinguishes scalar text from a typed content
// array. Absent bytes select a pointer to empty text, string decoding takes
// precedence, and array decoding supplies Parts; failure of both shapes
// returns a generic content parse error.
func parseChatMessageContent(raw json.RawMessage) (chatMessageContent, error) {
	if len(raw) == 0 {
		return chatMessageContent{Text: stringPtr("")}, nil
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return chatMessageContent{Text: &s}, nil
	}

	// Scalar decoding can fail for multimodal content. Keep the typed array
	// alternative so usable image/file parts reach the owning message converter.
	var parts []ChatContentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		return chatMessageContent{Parts: parts}, nil
	}

	return chatMessageContent{}, fmt.Errorf("parse content as string or parts array")
}

// marshalChatInputContent prefers a nonnil Text pointer, including empty text,
// over Parts. Otherwise it converts supported parts and encodes an empty
// string when none survive; output encoding errors are returned to the owning
// message converter.
func marshalChatInputContent(content chatMessageContent) (json.RawMessage, error) {
	if content.Text != nil {
		return json.Marshal(*content.Text)
	}
	parts := convertChatContentPartsToResponses(content.Parts)
	if len(parts) == 0 {
		// An empty converted slice would encode as null. Emit scalar empty text so
		// this history message retains the string-or-content-array representation
		// used by Responses.
		return json.Marshal("")
	}
	return json.Marshal(parts)
}

// convertChatContentPartsToResponses retains exact text, image_url, and file
// kinds in order. Empty text, unusable image pointers/URLs, known empty base64
// URLs, and files without data or IDs are omitted; other source metadata is
// outside the copied target fields.
func convertChatContentPartsToResponses(parts []ChatContentPart) []ResponsesContentPart {
	var responseParts []ResponsesContentPart
	// Target content uses different type labels and a narrower field set. Keep
	// usable text/media locations, including file ID/data, while omitting
	// unsupported parts.
	for _, p := range parts {
		switch p.Type {
		case "text":
			if p.Text != "" {
				responseParts = append(responseParts, ResponsesContentPart{
					Type: "input_text",
					Text: p.Text,
				})
			}
		case "image_url":
			if p.ImageURL != nil && p.ImageURL.URL != "" && !openai.IsEmptyBase64DataURI(p.ImageURL.URL) {
				responseParts = append(responseParts, ResponsesContentPart{
					Type:     "input_image",
					ImageURL: p.ImageURL.URL,
				})
			}
		case "file":
			if p.File != nil && (p.File.FileData != "" || p.File.FileID != "") {
				responseParts = append(responseParts, ResponsesContentPart{
					Type:     "input_file",
					Filename: p.File.Filename,
					FileData: p.File.FileData,
					FileID:   p.File.FileID,
				})
			}
		}
	}
	return responseParts
}

// flattenChatContentParts concatenates nonempty exact text parts with no
// separator.
func flattenChatContentParts(parts []ChatContentPart) string {
	var textParts []string
	for _, p := range parts {
		if p.Type == "text" && p.Text != "" {
			textParts = append(textParts, p.Text)
		}
	}
	return strings.Join(textParts, "")
}

// stringPtr allocates a string value for optional content representation.
func stringPtr(s string) *string {
	return &s
}

// convertChatToolsToResponses appends recognized modern tools before legacy
// function declarations. Hosted search/execution labels use trimmed lowercase
// matching; ordinary function tools require exact type and a nonnil function.
// Unknown tools are skipped, strictness defaults false, and retained
// schema/pointer fields may share source storage.
func convertChatToolsToResponses(tools []ChatTool, functions []ChatFunction) []ResponsesTool {
	var out []ResponsesTool

	for _, t := range tools {
		toolType := strings.ToLower(strings.TrimSpace(t.Type))
		// Hosted tools stay hosted declarations, with x_search carrying its
		// filter/media options. Ordinary function declarations use their nested
		// function fields.
		if toolType == "x_search" {
			out = append(out, ResponsesTool{
				Type:                     "x_search",
				AllowedXHandles:          t.AllowedXHandles,
				ExcludedXHandles:         t.ExcludedXHandles,
				FromDate:                 t.FromDate,
				ToDate:                   t.ToDate,
				EnableImageUnderstanding: t.EnableImageUnderstanding,
				EnableVideoUnderstanding: t.EnableVideoUnderstanding,
			})
			continue
		}
		if toolType == "web_search" || toolType == "code_execution" {
			out = append(out, ResponsesTool{Type: toolType})
			continue
		}
		if t.Type != "function" || t.Function == nil {
			continue
		}
		rt := ResponsesTool{
			Type:        "function",
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
			Strict:      defaultStrictFalse(t.Function.Strict),
		}
		out = append(out, rt)
	}

	// Legacy functions have no tool wrapper. Give them the same function shape
	// and strictness selection as ordinary modern declarations while retaining
	// their order.
	for _, f := range functions {
		rt := ResponsesTool{
			Type:        "function",
			Name:        f.Name,
			Description: f.Description,
			Parameters:  f.Parameters,
			Strict:      defaultStrictFalse(f.Strict),
		}
		out = append(out, rt)
	}

	return out
}

// defaultStrictFalse returns a supplied strictness pointer or allocates false
// when absent. Returning an existing pointer preserves explicit true/false and
// shares the caller's storage, so the converter does not produce independent
// optional values.
func defaultStrictFalse(src *bool) *bool {
	if src == nil {
		value := false
		return &value
	}
	return src
}

// convertChatFunctionCallToToolChoice forwards string-decodable legacy
// selectors or constructs a function/name choice from object decoding. Object
// decode errors propagate. Selected strings and names are not validated, and
// absent names become an empty named selector.
func convertChatFunctionCallToToolChoice(raw json.RawMessage) (json.RawMessage, error) {
	// Legacy strings already use the target scalar shape. Attempt that decode
	// first so named objects can follow their separate function-choice representation.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return json.Marshal(s)
	}

	// A named legacy object needs the target function type alongside its name.
	// Unknown object members are outside this narrow selection decoder.
	var obj struct {
		Name string `json:"name"` // selected legacy function name
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"type": "function",
		"name": obj.Name,
	})
}
