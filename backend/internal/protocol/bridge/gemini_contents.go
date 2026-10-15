package bridge

import (
	"encoding/json"
	"fmt"
	"strings"
)

// gemini_contents.go converts Messages history into internal Gemini content
// and system instructions.
// Internal Gemini history uses model roles and function names where Messages
// history uses assistant roles and tool-use IDs. A shared call-ID map connects
// later results to earlier names while system turns become separate
// systemInstruction parts, allowing the Antigravity request assembler to
// combine them with its own instruction text.
//
// Thinking history follows the caller's signature policy. Signed blocks keep
// their signatures; unsigned or dummy-signed blocks are downgraded when dummy
// signatures are disabled, and the returned flag tells the assembler to remove
// thinking controls. Dummy-enabled conversion can add a thought before a final
// assistant prefill. Conversion keeps chronological parts and reports errors
// with message position, while map updates from earlier parts remain visible
// to the caller.

// DummyThoughtSignature is the marker inserted by dummy-enabled BuildParts and
// final assistant prefill handling. Cross-provider signature repair uses the
// same literal. See https://ai.google.dev/gemini-api/docs/thought-signatures.
const DummyThoughtSignature = "skip_thought_signature_validator"

// BuildContents converts Messages history into Gemini contents and separate
// system parts, reporting whether thinking was downgraded. Supply a writable
// toolIDToName map and reuse it across the history so later tool results can
// resolve prior call names.
//
// Assistant turns become model turns, system parts leave ordinary history, and
// other roles retain their spelling. Dummy-enabled thinking inserts a leading
// thought only into a nonempty final model turn lacking a thought part. A
// malformed message returns an indexed error and discards output slices, while
// prior call-map updates remain. The Antigravity assembler uses the downgrade
// flag to disable request thinking controls.
func BuildContents(messages []ClaudeMessage, toolIDToName map[string]string, isThinkingEnabled, allowDummyThought bool) ([]GeminiContent, []GeminiPart, bool, error) {
	var contents []GeminiContent
	var systemParts []GeminiPart
	strippedThinking := false

	for i, msg := range messages {
		role := msg.Role
		// Messages assistant turns use Gemini's model role, while exact system turns
		// are separated later and all other role spellings remain unchanged.
		if role == "assistant" {
			role = "model"
		}

		parts, strippedThisMsg, err := BuildParts(msg.Content, toolIDToName, allowDummyThought)
		if err != nil {
			return nil, nil, false, fmt.Errorf("build parts for message %d: %w", i, err)
		}
		if strippedThisMsg {
			strippedThinking = true
		}

		// Embedded system turns contribute parts to systemInstruction instead of
		// conversational contents; the request assembler merges these parts with its
		// separately supplied system text.
		if role == "system" {
			systemParts = append(systemParts, parts...)
			continue
		}

		// Only a final model turn is treated as assistant prefill for dummy thought
		// insertion. Earlier turns keep the signature handling applied by
		// BuildParts.
		if allowDummyThought && role == "model" && isThinkingEnabled && i == len(messages)-1 {
			hasThoughtPart := false
			for _, p := range parts {
				if p.Thought {
					hasThoughtPart = true
					break
				}
			}
			if !hasThoughtPart && len(parts) > 0 {
				// Place the synthetic thought before nonempty prefill parts so the final
				// model turn begins with a signed thought under the selected policy.
				parts = append([]GeminiPart{{
					Text:             "Thinking...",
					Thought:          true,
					ThoughtSignature: DummyThoughtSignature,
				}}, parts...)
			}
		}

		if len(parts) == 0 {
			continue
		}

		contents = append(contents, GeminiContent{
			Role:  role,
			Parts: parts,
		})
	}

	return contents, systemParts, strippedThinking, nil
}

// BuildParts converts string or block-array Messages content into Gemini parts
// and reports signature-based thinking downgrade. Initialize toolIDToName
// before calls with nonempty IDs and names; the map is updated during
// traversal and belongs to the caller.
//
// String content is trimmed and suppresses blank or no-content placeholders,
// while accepted text blocks keep their whitespace. Signature checks use exact
// strings and leave validation upstream. With dummy signatures disabled,
// unsigned or dummy-signed thinking becomes visible text and sets the
// downgrade flag; tools simply omit that marker. Results prefer a supplied
// name, then the prior call map, then the call ID. Array decoding failure
// returns an error.
func BuildParts(content json.RawMessage, toolIDToName map[string]string, allowDummyThought bool) ([]GeminiPart, bool, error) {
	var parts []GeminiPart
	strippedThinking := false

	// Try scalar content first: trimming suppresses blank and no-content
	// placeholders here, while the array path below keeps accepted text block
	// whitespace.
	var textContent string
	if err := json.Unmarshal(content, &textContent); err == nil {
		if textContent != "(no content)" && strings.TrimSpace(textContent) != "" {
			parts = append(parts, GeminiPart{Text: strings.TrimSpace(textContent)})
		}
		return parts, false, nil
	}

	// A failed scalar decode can still represent Messages block-array content;
	// decode that representation before treating the content as a conversion
	// error.
	var blocks []ContentBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil, false, fmt.Errorf("parse content blocks: %w", err)
	}

	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "(no content)" && strings.TrimSpace(block.Text) != "" {
				parts = append(parts, GeminiPart{Text: block.Text})
			}

		case "thinking":
			part := GeminiPart{
				Text:    block.Thinking,
				Thought: true,
			}
			// The caller's dummy policy distinguishes supplied nonmarker signatures from
			// placeholders; nonempty strings remain attached in either policy unless they
			// exactly equal the disabled dummy marker.
			if block.Signature != "" && (allowDummyThought || block.Signature != DummyThoughtSignature) {
				part.ThoughtSignature = block.Signature
			} else if !allowDummyThought {
				// Unsigned or dummy-signed thinking becomes visible text under this
				// policy; report the downgrade so the assembler removes thinking controls
				// for this history.
				if strings.TrimSpace(block.Thinking) != "" {
					parts = append(parts, GeminiPart{Text: block.Thinking})
				}
				strippedThinking = true
				continue
			} else {
				// Dummy-enabled conversion supplies the configured marker for missing
				// thought signatures, allowing the request assembler to keep the block as
				// thinking.
				part.ThoughtSignature = DummyThoughtSignature
			}
			parts = append(parts, part)

		case "image":
			// Base64 Messages images become inlineData with their supplied MIME type
			// and encoded bytes; URL sources contribute no image part on this path.
			if block.Source != nil && block.Source.Type == "base64" {
				parts = append(parts, GeminiPart{
					InlineData: &GeminiInlineData{
						MimeType: block.Source.MediaType,
						Data:     block.Source.Data,
					},
				})
			}

		case "tool_use":
			// Record nonempty call IDs and names before emitting the call, because a
			// later Messages result can identify the call by ID alone.
			if block.ID != "" && block.Name != "" {
				toolIDToName[block.ID] = block.Name
			}

			part := GeminiPart{
				FunctionCall: &GeminiFunctionCall{
					Name: block.Name,
					Args: block.Input,
					ID:   block.ID,
				},
			}
			// Calls keep nonempty supplied signatures under the same exact marker check.
			// Dummy-enabled conversion fills missing signatures; disabling that policy
			// leaves unsigned or dummy-signed calls without thoughtSignature.
			if block.Signature != "" && (allowDummyThought || block.Signature != DummyThoughtSignature) {
				part.ThoughtSignature = block.Signature
			} else if allowDummyThought {
				part.ThoughtSignature = DummyThoughtSignature
			}
			parts = append(parts, part)

		case "tool_result":
			// Gemini function responses need a name: prefer the result's supplied name,
			// then the matching call's name, and finally its Messages call ID.
			funcName := block.Name
			if funcName == "" {
				if name, ok := toolIDToName[block.ToolUseID]; ok {
					funcName = name
				} else {
					funcName = block.ToolUseID
				}
			}

			// The internal request wraps tool results in a result string, so flatten
			// text blocks and supply success/error placeholders before constructing
			// functionResponse.
			resultContent := ParseToolResultContent(block.Content, block.IsError)

			parts = append(parts, GeminiPart{
				FunctionResponse: &GeminiFunctionResponse{
					Name: funcName,
					Response: map[string]any{
						"result": resultContent,
					},
					ID: block.ToolUseID,
				},
			})
		}
	}

	return parts, strippedThinking, nil
}

// ParseToolResultContent returns tool-result text for an internal Gemini
// function response. Strings retain supplied whitespace when nonblank, arrays
// join string text fields with newlines, and other JSON shapes keep their
// serialized text. Empty or whitespace-only supported content selects an
// isError-dependent placeholder.
func ParseToolResultContent(content json.RawMessage, isError bool) string {
	// Missing result content needs a printable result string; isError chooses a
	// failure placeholder so an empty failure is distinguishable from success.
	if len(content) == 0 {
		if isError {
			return "Tool execution failed with no output."
		}
		return "Command executed successfully."
	}

	// Attempt the scalar representation first, retaining nonblank supplied text
	// and selecting the same status placeholder for blank strings or JSON null.
	var str string
	if err := json.Unmarshal(content, &str); err == nil {
		if strings.TrimSpace(str) == "" {
			if isError {
				return "Tool execution failed with no output."
			}
			return "Command executed successfully."
		}
		return str
	}

	// Array results expose their string text fields in order; joining with
	// newlines gives functionResponse one string while omitting nontext entries.
	var arr []map[string]any
	if err := json.Unmarshal(content, &arr); err == nil {
		var texts []string
		for _, item := range arr {
			if text, ok := item["text"].(string); ok {
				texts = append(texts, text)
			}
		}
		result := strings.Join(texts, "\n")
		// A textless or whitespace-only decoded array still needs the status
		// placeholder; its successful decode does not establish meaningful tool
		// output.
		if strings.TrimSpace(result) == "" {
			if isError {
				return "Tool execution failed with no output."
			}
			return "Command executed successfully."
		}
		return result
	}

	// Keep unrecognized or undecodable result bytes as text so this conversion
	// can deliver the tool's supplied payload instead of discarding it.
	return string(content)
}
