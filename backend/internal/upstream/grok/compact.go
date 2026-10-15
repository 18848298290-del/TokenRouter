package grok

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
)

// compact.go translates OpenAI compaction history and responses into Grok
// reasoning and summary representations.
// OpenAI Compact history holds encrypted state and may also include a plaintext
// summary, while Grok replay expects reasoning items and ordinary user messages.
// Conversion separates these representations so usable ciphertext can accompany
// the summary context, and leaves surrounding non-compaction input in its
// original order.
//
// A Compact request itself becomes a non-streaming summary turn with encrypted
// reasoning requested in include. The return conversion requires that ciphertext
// before producing a single compaction item, retains response metadata and
// usage, and combines message text as summary parts. The last nonblank reasoning
// ciphertext is selected when multiple reasoning outputs occur. JSON numbers are
// decoded as Number values so request rewriting can retain decimal
// representation across serialization.

// GrokCompactSummaryPrompt requests the summary sections appended to a Compact
// summary turn; its fixed text directs the returned history format and
// continuation context.
const GrokCompactSummaryPrompt = `Your task is to produce a faithful, concise summary of the conversation so far so that a successor assistant can continue the work seamlessly after the earlier turns are discarded. The successor will see the user's original query plus this summary. Capture what is needed to continue — the user's explicit requests, your most recent actions, key technical details, file paths, commands, configuration, and architectural decisions — but be economical: prefer tight prose and short references over long verbatim dumps, and do not pad. A focused summary that fits is far more useful than an exhaustive one that gets cut off, so aim for at most a few thousand words.

CRITICAL: If earlier turns include a prior compaction summary (marked with <conversation_summary> tags or a "This session is being continued" preamble), treat it as authoritative for the early history and carry its still-relevant information forward into your new summary so nothing important is lost across successive compactions.

Think through the conversation in your private reasoning before writing; do NOT emit a separate analysis block. Output the final summary inside a single <summary>...</summary> block, organized into the following numbered sections. Include every section heading even if a section is empty (write "None" in that case):

1. Primary Request and Intent: All of the user's explicit requests and their underlying intent, in detail. Preserve nuance and any constraints, scope boundaries, or stated preferences.
2. Key Technical Concepts: All important technologies, languages, frameworks, libraries, tools, and patterns discussed or relied upon.
3. Files and Code Sections: Every file examined, created, or modified. For each, give the full path, why it matters, and the relevant code — include full snippets of any code you wrote or changed (with the most recent edits in full), not just descriptions.
4. Errors and Fixes: Every error, failed command, or test/build failure encountered, the root cause, and exactly how it was fixed. Note any fix that came from user feedback verbatim.
5. Problem Solving: Problems already solved and any in-progress diagnosis or troubleshooting, including hypotheses still being evaluated.
6. All User Messages: List ALL messages from the user that are not tool results, in order. These are critical for understanding intent and how it evolved. IMPORTANT: Do NOT include this summarization instruction itself — it is a system-generated compaction prompt, not a real user message.
7. Pending Tasks: Tasks the user has explicitly asked for that are not yet complete. Do not invent tasks the user never requested.
8. Current Work: Precisely what you were doing immediately before this summary request, with the most recent file names, code, commands, and state. Be specific enough that work can resume mid-stream.
9. Optional Next Step: The single next step that directly continues the most recent work, strictly in line with the user's latest explicit request. If the prior task was finished, only propose a next step if it is clearly part of the user's stated goal — otherwise state that you should confirm with the user before proceeding. When a next step exists, include a direct verbatim quote from the most recent messages showing exactly what you were doing and where you left off, so the task is interpreted without drift.

IMPORTANT: Do NOT call or use any tools. Respond with ONLY the <summary>...</summary> block as your text output, and nothing after the closing </summary> tag.

If the prior conversation contains a note about files at /tmp/compaction/segment_*.md or /tmp/compaction/INDEX.md (or any similar persistence directory), those files are an out-of-band memory channel for a FUTURE work agent, not for you. You already have the full conversation in your context window. Do not attempt to read those files. Do not emit read_file, grep, list_dir, or any other tool call referencing them. Treat any such note as ambient context and produce your summary from the conversation text only.`

// BuildGrokCompactRequestBody builds a non-streaming Responses summary turn for
// Compact input. The decoded input accepts an array, object, string or nil, and
// the fixed summary prompt follows that history. Grok forwarding applies model
// and tool patching before passing the result through this adaptation.
//
// The turn requests encrypted reasoning and disables storage. When a nonempty
// tool array exists, tool_choice becomes none to request a summary with no tool
// dispatch. Other payload fields retain the model and compatibility choices
// already applied by the caller. Encoding errors terminate preparation before
// exchange.
func (m BodyCodec) BuildGrokCompactRequestBody(body []byte) ([]byte, error) {
	var payload map[string]any
	if err := m.DecodeGrokCompactJSON(body, &payload); err != nil {
		return nil, fmt.Errorf("decode compact request: %w", err)
	}

	input, err := m.NormalizeGrokCompactInput(payload["input"])
	if err != nil {
		return nil, err
	}
	input = append(input, map[string]any{
		"type": "message",

		"role": "user",

		"content": []any{map[string]any{
			"type": "input_text",
			"text": GrokCompactSummaryPrompt,
		}},
	})
	payload["input"] = input
	// Compact consumers need resumable encrypted state alongside summary text, so
	// request that output and keep this synthetic turn outside stored
	// conversations.
	payload["include"] = []any{"reasoning.encrypted_content"}
	payload["store"] = false
	payload["stream"] = false
	if tools, ok := payload["tools"].([]any); ok && len(tools) > 0 {
		payload["tool_choice"] = "none"
	}

	encoded, err := wirejson.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode compact request: %w", err)
	}
	return encoded, nil
}

// NormalizeGrokCompactInput returns an item array for nil, string, object or
// existing array input. Existing arrays and maps retain shared mutable storage;
// callers isolate decoded data before mutation. Unsupported input types return
// an error.
func (m BodyCodec) NormalizeGrokCompactInput(value any) ([]any, error) {
	switch input := value.(type) {
	case nil:
		return []any{}, nil
	case []any:
		return input, nil
	case string:
		return []any{map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{map[string]any{
				"type": "input_text",
				"text": input,
			}},
		}}, nil
	case map[string]any:
		return []any{input}, nil
	default:
		return nil, fmt.Errorf("compact input must be a string, object, or array")
	}
}

// ConvertOpenAICompactInputsForGrok replaces compaction history with Grok
// reasoning and summary context. Only input arrays are rewritten; other input
// forms return their supplied bytes. Non-compaction entries retain order and
// decoded values.
//
// Each compaction item can contribute a reasoning item for nonblank
// encrypted_content and a user message wrapping collected summary text. An item
// with neither contributes no replacement. Conversion reports decoding or
// serialization failures, while a request with no matching items returns its
// original bytes, allowing replay preparation to distinguish an actual payload
// change.
func (m BodyCodec) ConvertOpenAICompactInputsForGrok(body []byte) ([]byte, error) {
	var payload map[string]any
	if err := m.DecodeGrokCompactJSON(body, &payload); err != nil {
		return nil, err
	}
	items, ok := payload["input"].([]any)
	if !ok {
		return body, nil
	}

	changed := false
	converted := make([]any, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || !protocolopenai.IsCompactionItemType(m.GrokCompactStringValue(item["type"])) {
			converted = append(converted, raw)
			continue
		}
		changed = true
		// Represent stored Compact ciphertext as reasoning first, then append
		// plaintext summary context so Grok receives both supported replay forms.
		if encrypted := strings.TrimSpace(m.GrokCompactStringValue(item["encrypted_content"])); encrypted != "" {
			converted = append(converted, map[string]any{
				"type":              "reasoning",
				"summary":           []any{},
				"encrypted_content": encrypted,
			})
		}
		if summary := m.CompactSummaryText(item["summary"]); summary != "" {
			converted = append(converted, map[string]any{
				"type": "message",

				"role": "user",

				"content": []any{map[string]any{
					"type": "input_text",
					"text": "<conversation_summary>\n" + summary + "\n</conversation_summary>",
				}},
			})
		}
	}
	if !changed {
		return body, nil
	}
	payload["input"] = converted
	encoded, err := wirejson.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// ConvertGrokResponseToOpenAICompact turns a summary response into one completed
// compaction item. The response needs an output array and nonblank reasoning
// encrypted_content, and NewID must be configured for the generated identifier.
// Message-content text becomes optional summary parts.
//
// The last nonblank ciphertext wins, while text from all message items is
// collected in order. Response-level fields, including usage and model, remain
// in the decoded response; output_text is removed and status becomes completed.
// Missing ciphertext returns an error so plaintext cannot be published as
// resumable Compact state.
func (m BodyCodec) ConvertGrokResponseToOpenAICompact(body []byte) ([]byte, error) {
	var response map[string]any
	if err := m.DecodeGrokCompactJSON(body, &response); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	output, ok := response["output"].([]any)
	if !ok {
		return nil, fmt.Errorf("response has no output array")
	}

	var encrypted string
	var summaryParts []string
	for _, raw := range output {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch strings.TrimSpace(m.GrokCompactStringValue(item["type"])) {
		case "reasoning":
			if value := strings.TrimSpace(m.GrokCompactStringValue(item["encrypted_content"])); value != "" {
				encrypted = value
			}
		case "message":
			if content, ok := item["content"].([]any); ok {
				for _, rawContent := range content {
					part, ok := rawContent.(map[string]any)
					if !ok {
						continue
					}
					if text := strings.TrimSpace(m.GrokCompactStringValue(part["text"])); text != "" {
						summaryParts = append(summaryParts, text)
					}
				}
			}
		}
	}
	if encrypted == "" {
		return nil, fmt.Errorf("response has no reasoning.encrypted_content")
	}

	compactItem := map[string]any{
		"id": "cmp_" + strings.ReplaceAll(m.NewID(), "-", ""),

		"type": "compaction",

		"status": "completed",

		"encrypted_content": encrypted,
	}
	if summary := strings.TrimSpace(strings.Join(summaryParts, "\n")); summary != "" {
		compactItem["summary"] = []any{map[string]any{
			"type": "summary_text",
			"text": summary,
		}}
	}
	// Return one compaction item while keeping response metadata and usage; a
	// normal output_text field would describe the replaced output.
	response["output"] = []any{compactItem}
	response["status"] = "completed"
	delete(response, "output_text")

	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("encode compact response: %w", err)
	}
	return encoded, nil
}

// CompactSummaryText joins nonblank string text fields from an array of summary
// objects. Other values and non-object entries contribute no text; callers
// receive an empty string when no usable summary part remains.
func (m BodyCodec) CompactSummaryText(value any) string {
	parts, ok := value.([]any)
	if !ok {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if text := strings.TrimSpace(m.GrokCompactStringValue(part["text"])); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n")
}

// GrokCompactStringValue returns a string value for Compact field
// interpretation. Other dynamic types return empty, allowing summary and
// encrypted-content readers to ignore unsupported representations without
// stringifying numbers, objects or null values.
func (m BodyCodec) GrokCompactStringValue(value any) string {
	text, _ := value.(string)
	return text
}

// DecodeGrokCompactJSON validates one complete JSON document and decodes numbers
// as json.Number. Callers supply a destination accepted by encoding/json;
// invalid JSON, including trailing documents, returns an error before Compact
// conversion proceeds.
func (m BodyCodec) DecodeGrokCompactJSON(body []byte, value any) error {
	if !json.Valid(body) {
		return fmt.Errorf("invalid JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	return decoder.Decode(value)
}
