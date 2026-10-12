package bridge

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DummyThoughtSignature is Gemini 3's signature-validation bypass marker.
// See https://ai.google.dev/gemini-api/docs/thought-signatures.
// Cross-provider signature repair also uses this marker.
const DummyThoughtSignature = "skip_thought_signature_validator"

// BuildContents converts Claude messages to Gemini content and separate system parts, reporting downgraded thinking.
func BuildContents(messages []ClaudeMessage, toolIDToName map[string]string, isThinkingEnabled, allowDummyThought bool) ([]GeminiContent, []GeminiPart, bool, error) {
	var contents []GeminiContent
	var systemParts []GeminiPart
	strippedThinking := false

	for i, msg := range messages {
		role := msg.Role
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

		// Move system messages embedded by Claude Code into Gemini systemInstruction.
		if role == "system" {
			systemParts = append(systemParts, parts...)
			continue
		}

		// The dummy-thought workaround applies to Gemini assistant prefill only.
		// Restrict insertion to the final assistant message; historical assistant
		// messages must retain their own signatures.
		if allowDummyThought && role == "model" && isThinkingEnabled && i == len(messages)-1 {
			hasThoughtPart := false
			for _, p := range parts {
				if p.Thought {
					hasThoughtPart = true
					break
				}
			}
			if !hasThoughtPart && len(parts) > 0 {
				// Prepend the dummy thought before prefill content.
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

// BuildParts converts content and updates toolIDToName, which callers must initialize for tool calls.
// It reports downgraded thinking; enable allowDummyThought only for models accepting Gemini's marker.
func BuildParts(content json.RawMessage, toolIDToName map[string]string, allowDummyThought bool) ([]GeminiPart, bool, error) {
	var parts []GeminiPart
	strippedThinking := false

	// Accept plain-string content as well as block arrays.
	var textContent string
	if err := json.Unmarshal(content, &textContent); err == nil {
		if textContent != "(no content)" && strings.TrimSpace(textContent) != "" {
			parts = append(parts, GeminiPart{Text: strings.TrimSpace(textContent)})
		}
		return parts, false, nil
	}

	// Decode the array form after the string form fails.
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
			// Signature handling:
			//   - Claude uses the upstream signature and treats the dummy marker as missing.
			//   - Gemini uses the upstream signature when present, otherwise the dummy marker.
			if block.Signature != "" && (allowDummyThought || block.Signature != DummyThoughtSignature) {
				part.ThoughtSignature = block.Signature
			} else if !allowDummyThought {
				// Downgrade unsigned Claude thinking to text and signal the caller to disable thinking.
				if strings.TrimSpace(block.Thinking) != "" {
					parts = append(parts, GeminiPart{Text: block.Thinking})
				}
				strippedThinking = true
				continue
			} else {
				// Gemini accepts the dummy marker when the signature is missing.
				part.ThoughtSignature = DummyThoughtSignature
			}
			parts = append(parts, part)

		case "image":
			if block.Source != nil && block.Source.Type == "base64" {
				parts = append(parts, GeminiPart{
					InlineData: &GeminiInlineData{
						MimeType: block.Source.MediaType,
						Data:     block.Source.Data,
					},
				})
			}

		case "tool_use":
			// Record the call name for later tool results that provide only an ID.
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
			// Tool-call signature handling:
			//   - Claude retains upstream signatures and treats the dummy marker as missing.
			//   - Gemini fills missing signatures with the dummy marker.
			if block.Signature != "" && (allowDummyThought || block.Signature != DummyThoughtSignature) {
				part.ThoughtSignature = block.Signature
			} else if allowDummyThought {
				part.ThoughtSignature = DummyThoughtSignature
			}
			parts = append(parts, part)

		case "tool_result":
			// Resolve the result name from the block, prior call, or call ID.
			funcName := block.Name
			if funcName == "" {
				if name, ok := toolIDToName[block.ToolUseID]; ok {
					funcName = name
				} else {
					funcName = block.ToolUseID
				}
			}

			// Normalize the result content to text.
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

// ParseToolResultContent extracts tool-result text, with success/error placeholders for empty output.
func ParseToolResultContent(content json.RawMessage, isError bool) string {
	if len(content) == 0 {
		if isError {
			return "Tool execution failed with no output."
		}
		return "Command executed successfully."
	}

	// Accept string results before trying arrays of text objects.
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

	// Join text fields from array results.
	var arr []map[string]any
	if err := json.Unmarshal(content, &arr); err == nil {
		var texts []string
		for _, item := range arr {
			if text, ok := item["text"].(string); ok {
				texts = append(texts, text)
			}
		}
		result := strings.Join(texts, "\n")
		if strings.TrimSpace(result) == "" {
			if isError {
				return "Tool execution failed with no output."
			}
			return "Command executed successfully."
		}
		return result
	}

	// Keep other JSON shapes as serialized text.
	return string(content)
}
