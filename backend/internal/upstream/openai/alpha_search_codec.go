package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

// A PAT search fallback sends the Alpha Search command description as a
// Responses user message with a hosted web_search tool. Each embedded JSON
// section has its own byte cap, while supported search settings also populate
// the tool declaration.
//
// Responses SSE can provide text deltas or a completed response with full
// output. Deltas take precedence whenever they produce nonblank text, and
// citations are collected from the same stream with URLs deduplicated across
// events. A completed response supplies both text and citations when deltas are
// absent, keeping the fallback usable for upstreams that send terminal output
// alone. Standalone Alpha Search separately removes Responses-only top-level
// fields.

// openAIAlphaSearchUnsupportedBodyFields names Responses-only fields removed before
// standalone SearchRequest forwarding, where these parameters are unsupported.
var openAIAlphaSearchUnsupportedBodyFields = [...]string{
	// Codex alpha/search uses SearchRequest. Responses fields forwarded by clients
	// or proxies, such as prompt_cache_key, cause Unknown parameter errors here.
	"prompt_cache_key",
	"prompt_cache_retention",
	"store",
}

// BuildOpenAIAlphaSearchResponsesWebSearchBody creates streaming,
// store-disabled hosted-search JSON for model. A blank model returns an error;
// optional search-context and object-shaped user-location settings are copied
// to the tool when usable.
func BuildOpenAIAlphaSearchResponsesWebSearchBody(alphaBody []byte, model string) ([]byte, error) {
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("model is required")
	}
	tool := map[string]any{"type": "web_search"}
	if contextSize := strings.TrimSpace(gjson.GetBytes(alphaBody, "settings.search_context_size").String()); contextSize != "" {
		tool["search_context_size"] = contextSize
	}
	if userLocation := gjson.GetBytes(alphaBody, "settings.user_location"); userLocation.IsObject() {
		var loc map[string]any
		// Ignore unusable optional location data so hosted-search conversion can
		// still carry commands and other settings from an otherwise usable request.
		if err := json.Unmarshal([]byte(userLocation.Raw), &loc); err == nil && len(loc) > 0 {
			tool["user_location"] = loc
		}
	}
	// Hosted search requires a Responses message and web_search declaration;
	// carry supported settings into that tool while the command text describes the request.
	payload := map[string]any{
		"model":  model,
		"stream": true,
		"store":  false,
		"input": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "input_text",
						"text": OpenAIAlphaSearchResponsesWebSearchPrompt(alphaBody),
					},
				},
			},
		},
		"tools": []any{tool},
	}
	return json.Marshal(payload)
}

// OpenAIAlphaSearchResponsesWebSearchPrompt describes the standalone commands
// for the hosted-search model. Commands, settings, and recent input are
// appended as raw JSON text with separate byte caps to bound each part of the
// prompt.
func OpenAIAlphaSearchResponsesWebSearchPrompt(alphaBody []byte) string {
	var b strings.Builder
	_, _ = b.WriteString("Execute this Codex standalone web.run request for another model.\n")
	_, _ = b.WriteString("Use the hosted web_search tool when web/current information is needed.\n")
	_, _ = b.WriteString("Return concise source-backed results. Include titles, URLs, dates, and direct answers when available.\n")
	if commands := strings.TrimSpace(gjson.GetBytes(alphaBody, "commands").Raw); commands != "" {
		_, _ = b.WriteString("\nCommands JSON:\n")
		_, _ = b.WriteString(TruncateOpenAIAlphaSearchPromptJSON(commands, 12000))
	}
	if settings := strings.TrimSpace(gjson.GetBytes(alphaBody, "settings").Raw); settings != "" {
		_, _ = b.WriteString("\n\nSearch settings JSON:\n")
		_, _ = b.WriteString(TruncateOpenAIAlphaSearchPromptJSON(settings, 4000))
	}
	if input := strings.TrimSpace(gjson.GetBytes(alphaBody, "input").Raw); input != "" {
		_, _ = b.WriteString("\n\nRecent conversation/input JSON:\n")
		_, _ = b.WriteString(TruncateOpenAIAlphaSearchPromptJSON(input, 8000))
	}
	if b.Len() == 0 {
		return "Execute the requested web search and return concise source-backed results."
	}
	return b.String()
}

// TruncateOpenAIAlphaSearchPromptJSON trims and bounds an embedded prompt
// section by bytes. Nonpositive limits disable truncation; a truncated value
// gains a textual suffix and may end inside a multibyte character or JSON
// token.
func TruncateOpenAIAlphaSearchPromptJSON(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	// The cap bounds prompt bytes, while the suffix signals that the embedded
	// JSON section is partial and should be treated as descriptive input.
	return value[:limit] + "\n...<truncated>"
}

// SanitizeOpenAIAlphaSearchBody removes unsupported top-level Responses fields
// from an Alpha Search object. Empty, malformed, null, and already-compatible
// inputs return their existing bytes; changed objects are encoded again and may
// change formatting.
func SanitizeOpenAIAlphaSearchBody(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}
	var obj map[string]json.RawMessage
	// Sanitization is best effort: preserve unparsed input for the endpoint to
	// validate rather than rejecting it while removing optional compatibility fields.
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		return body, nil
	}
	changed := false
	for _, field := range openAIAlphaSearchUnsupportedBodyFields {
		if _, ok := obj[field]; ok {
			// Remove only known Responses parameters; remaining RawMessage values keep
			// their JSON payloads for the standalone SearchRequest endpoint to validate.
			delete(obj, field)
			changed = true
		}
	}
	if !changed {
		return body, nil
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// OpenAIAlphaSearchResponseFromResponsesSSE returns Alpha Search JSON built
// from accumulated Responses text and URL citations. The output field is
// present even when empty, and results is omitted when no citation is
// collected.
func OpenAIAlphaSearchResponseFromResponsesSSE(body []byte) ([]byte, error) {
	output, results := ParseOpenAIResponsesSSEForAlphaSearch(body)
	resp := map[string]any{
		"output": output,
	}
	if len(results) > 0 {
		resp["results"] = results
	}
	return json.Marshal(resp)
}

// ParseOpenAIResponsesSSEForAlphaSearch returns streamed text and deduplicated
// URL citation results. It ignores empty blocks, DONE markers, and malformed
// event JSON so usable events can still contribute to a fallback response. CRLF
// line endings are normalized before SSE blocks are separated.
//
// Nonempty output_text deltas form the primary text. Only when their combined
// text is blank does the last completed response supply full message text and
// additional citations. Citation collection shares one URL set across those
// phases, preventing a terminal replay of an already observed citation from
// creating another result identifier.
func ParseOpenAIResponsesSSEForAlphaSearch(body []byte) (string, []any) {
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	var output strings.Builder
	var completedResponse any
	results := make([]any, 0)
	seenURLs := make(map[string]struct{})

	for block := range strings.SplitSeq(text, "\n\n") {
		data := OpenAIAlphaSearchSSEData(block)
		if data == "" || data == "[DONE]" {
			continue
		}
		var event map[string]any
		// Skip a malformed event while retaining text and citations already read;
		// one unusable block should not discard other usable search results.
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}
		if delta, _ := event["delta"].(string); delta != "" && event["type"] == "response.output_text.delta" {
			_, _ = output.WriteString(delta)
		}
		if event["type"] == "response.completed" {
			completedResponse = event["response"]
		}
		CollectOpenAIAlphaSearchURLCitations(event, &results, seenURLs)
	}

	out := output.String()
	// Terminal-only upstreams can omit text deltas. Read full output in that
	// case, and share the URL set so repeated citations remain one result.
	if strings.TrimSpace(out) == "" && completedResponse != nil {
		out = ExtractOpenAIResponsesCompletedText(completedResponse)
		CollectOpenAIAlphaSearchURLCitations(completedResponse, &results, seenURLs)
	}
	return out, results
}

// OpenAIAlphaSearchSSEData joins the data lines of one SSE block. It ignores
// other line types, trims individual data prefixes and surrounding whitespace,
// and returns empty text for blocks containing no data.
func OpenAIAlphaSearchSSEData(block string) string {
	var lines []string
	for line := range strings.SplitSeq(block, "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		lines = append(lines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// ExtractOpenAIResponsesCompletedText concatenates output_text content from
// completed response messages. Unsupported object shapes and non-message output
// items contribute no text, letting terminal fallback tolerate mixed tool and
// message output.
func ExtractOpenAIResponsesCompletedText(response any) string {
	resp, ok := response.(map[string]any)
	if !ok {
		return ""
	}
	outputItems, _ := resp["output"].([]any)
	var b strings.Builder
	for _, item := range outputItems {
		itemMap, ok := item.(map[string]any)
		if !ok || itemMap["type"] != "message" {
			continue
		}
		contentItems, _ := itemMap["content"].([]any)
		for _, content := range contentItems {
			contentMap, ok := content.(map[string]any)
			if !ok {
				continue
			}
			if contentMap["type"] == "output_text" {
				if text, _ := contentMap["text"].(string); text != "" {
					_, _ = b.WriteString(text)
				}
			}
		}
	}
	return b.String()
}

// CollectOpenAIAlphaSearchURLCitations appends text_result entries for URL
// citations nested in value. Callers provide a writable results slice pointer
// and initialized seen map shared across stream events. Blank URLs are skipped
// and URLs are trimmed before deduplication.
//
// Each first-seen URL receives an identifier based on the current result count,
// with a trimmed title when supplied. Map traversal order follows Go iteration
// order, while slice traversal follows input order; callers should match
// entries by URL rather than assume a stable ordering for citations discovered
// in different map children.
func CollectOpenAIAlphaSearchURLCitations(value any, results *[]any, seen map[string]struct{}) {
	switch typed := value.(type) {
	case map[string]any:
		if typed["type"] == "url_citation" {
			if urlValue, _ := typed["url"].(string); strings.TrimSpace(urlValue) != "" {
				urlValue = strings.TrimSpace(urlValue)
				if _, exists := seen[urlValue]; !exists {
					seen[urlValue] = struct{}{}
					result := map[string]any{
						"type":   "text_result",
						"ref_id": fmt.Sprintf("turn0search%d", len(*results)),
						"url":    urlValue,
					}
					if title, _ := typed["title"].(string); strings.TrimSpace(title) != "" {
						result["title"] = strings.TrimSpace(title)
					}
					*results = append(*results, result)
				}
			}
		}
		for _, child := range typed {
			CollectOpenAIAlphaSearchURLCitations(child, results, seen)
		}
	case []any:
		for _, child := range typed {
			CollectOpenAIAlphaSearchURLCitations(child, results, seen)
		}
	}
}
