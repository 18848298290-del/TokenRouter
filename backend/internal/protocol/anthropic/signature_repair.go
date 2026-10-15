package anthropic

import (
	"encoding/json"
)

// signature_repair.go downgrades signature-sensitive Claude history for staged
// recovery attempts.
// Antigravity signature recovery first tries readable conversation history with
// structured tool calls intact, then can downgrade both tool calls and results
// to text when the narrower retry fails. Both stages disable top-level thinking
// and replace reasoning blocks whose signatures cannot be reused.
//
// Message bodies remain raw JSON until a content array needs conversion.
// String content and undecodable arrays pass through, readable reasoning becomes
// text, and encrypted redacted thinking is removed. A placeholder retains
// nonempty content when all blocks disappear. Repairs mutate the supplied
// request and message slice as they progress; callers needing an independent
// retry copy the message slice before invoking either function, as the
// Antigravity recovery executor does for each stage.

// StripThinkingFromClaudeRequest disables thinking and converts readable
// thinking history to text while retaining structured tool blocks. A nil
// request is an unchanged success. The boolean reports completed mutations,
// including removal of top-level thinking before message processing.
//
// Redacted reasoning is removed and an all-removed array receives placeholder
// text. String content and arrays that fail decoding pass through. The request
// and its message slice are mutated in place; an encoding error returns with
// any earlier changes still applied, so callers copy retry state beforehand.
// Modified arrays are reencoded from generic JSON maps, so unrelated numeric
// values inside those arrays follow float64 decoding and can lose precision.
func StripThinkingFromClaudeRequest(req *ClaudeRequest) (bool, error) {
	if req == nil {
		return false, nil
	}

	changed := false
	if req.Thinking != nil {
		req.Thinking = nil
		changed = true
	}

	for i := range req.Messages {
		raw := req.Messages[i].Content
		if len(raw) == 0 {
			continue
		}

		// If content is a string, nothing to strip.
		var str string
		if json.Unmarshal(raw, &str) == nil {
			continue
		}

		// Historical block arrays permit selective reasoning repair. Keep content
		// that fails this decoding intact so unrelated message representations survive the retry.
		var blocks []map[string]any
		if err := json.Unmarshal(raw, &blocks); err != nil {
			continue
		}

		filtered := make([]map[string]any, 0, len(blocks))
		modifiedAny := false
		for _, block := range blocks {
			t, _ := block["type"].(string)
			switch t {
			case "thinking":
				// Readable reasoning can continue as ordinary history after its
				// signature is rejected; converting to text removes the signature-bearing representation.
				thinkingText, _ := block["thinking"].(string)
				if thinkingText != "" {
					filtered = append(filtered, map[string]any{
						"type": "text",
						"text": thinkingText,
					})
				}
				modifiedAny = true
			case "redacted_thinking":
				// Redacted reasoning contains encrypted history rather than readable
				// text, so this downgrade drops the block instead of replaying unusable ciphertext.
				modifiedAny = true
			case "":
				// Some compatibility histories omit the type while retaining thinking.
				// Apply the same text downgrade so the retry does not retain unsigned reasoning.
				if thinkingText, hasThinking := block["thinking"].(string); hasThinking {
					if thinkingText != "" {
						filtered = append(filtered, map[string]any{
							"type": "text",
							"text": thinkingText,
						})
					}
					modifiedAny = true
				} else {
					filtered = append(filtered, block)
				}
			default:
				filtered = append(filtered, block)
			}
		}

		if !modifiedAny {
			continue
		}

		if len(filtered) == 0 {
			// Removing every reasoning block would leave an empty message array.
			// Supply placeholder text so later conversion still receives a content-bearing message.
			filtered = append(filtered, map[string]any{
				"type": "text",
				"text": "(content removed)",
			})
		}

		newRaw, err := json.Marshal(filtered)
		if err != nil {
			return changed, err
		}
		req.Messages[i].Content = newRaw
		changed = true
	}

	return changed, nil
}

// StripSignatureSensitiveBlocksFromClaudeRequest downgrades reasoning, tool
// calls, and tool results to text for the second Antigravity signature retry.
// A nil request is an unchanged success. Callers use this stage when structured
// tool replay can be sacrificed after the thinking-only retry fails.
//
// Tool names, identifiers, arguments, result content, and error markers remain
// as readable text instead of paired protocol blocks. Redacted reasoning is
// removed; an all-removed array receives placeholder text. The request and
// message slice are mutated in place, and an encoding error can follow earlier
// completed mutations, so each independent retry starts from copied state.
// Modified arrays are reencoded from generic JSON maps, so unrelated numeric
// values inside those arrays follow float64 decoding and can lose precision.
func StripSignatureSensitiveBlocksFromClaudeRequest(req *ClaudeRequest) (bool, error) {
	if req == nil {
		return false, nil
	}

	changed := false
	if req.Thinking != nil {
		req.Thinking = nil
		changed = true
	}

	for i := range req.Messages {
		raw := req.Messages[i].Content
		if len(raw) == 0 {
			continue
		}

		// If content is a string, nothing to strip.
		var str string
		if json.Unmarshal(raw, &str) == nil {
			continue
		}

		// The broader recovery operates on block arrays. Content that fails
		// decoding is retained so an unfamiliar representation does not erase conversation history.
		var blocks []map[string]any
		if err := json.Unmarshal(raw, &blocks); err != nil {
			continue
		}

		filtered := make([]map[string]any, 0, len(blocks))
		modifiedAny := false
		for _, block := range blocks {
			t, _ := block["type"].(string)
			switch t {
			case "thinking":
				// Readable reasoning survives as ordinary text after signature failure.
				// Empty reasoning disappears because it contributes no text to the downgraded history.
				thinkingText, _ := block["thinking"].(string)
				if thinkingText != "" {
					filtered = append(filtered, map[string]any{
						"type": "text",
						"text": thinkingText,
					})
				}
				modifiedAny = true
			case "redacted_thinking":
				// Redacted thinking supplies encrypted data rather than readable text.
				// Remove that block because this recovery cannot produce usable textual history from it.
				modifiedAny = true
			case "tool_use":
				// The broader retry gives up structured tool execution after the
				// narrower repair fails, retaining names, identifiers, and arguments as conversation text.
				name, _ := block["name"].(string)
				id, _ := block["id"].(string)
				input := block["input"]
				// If argument encoding fails, the textual call still carries its
				// available name and ID; the unusable argument detail contributes no text.
				inputJSON, _ := json.Marshal(input)
				text := "(tool_use)"
				if name != "" {
					text += " name=" + name
				}
				if id != "" {
					text += " id=" + id
				}
				if len(inputJSON) > 0 && string(inputJSON) != "null" {
					text += " input=" + string(inputJSON)
				}
				filtered = append(filtered, map[string]any{
					"type": "text",
					"text": text,
				})
				modifiedAny = true
			case "tool_result":
				// Tool results follow the same downgrade as their calls, preventing
				// a structured result from referring to a tool_use block already converted to text.
				toolUseID, _ := block["tool_use_id"].(string)
				isError, _ := block["is_error"].(bool)
				content := block["content"]
				// If result encoding fails, the textual result still carries its
				// available call ID and error flag, leaving only its body detail absent.
				contentJSON, _ := json.Marshal(content)
				text := "(tool_result)"
				if toolUseID != "" {
					text += " tool_use_id=" + toolUseID
				}
				if isError {
					text += " is_error=true"
				}
				if len(contentJSON) > 0 && string(contentJSON) != "null" {
					text += "\n" + string(contentJSON)
				}
				filtered = append(filtered, map[string]any{
					"type": "text",
					"text": text,
				})
				modifiedAny = true
			case "":
				// Compatibility histories can omit the block type while retaining
				// thinking text; downgrade that representation too so unsigned reasoning does not survive.
				if thinkingText, hasThinking := block["thinking"].(string); hasThinking {
					if thinkingText != "" {
						filtered = append(filtered, map[string]any{
							"type": "text",
							"text": thinkingText,
						})
					}
					modifiedAny = true
				} else {
					filtered = append(filtered, block)
				}
			default:
				filtered = append(filtered, block)
			}
		}

		if !modifiedAny {
			continue
		}

		if len(filtered) == 0 {
			// An array containing only removed reasoning would lose all content.
			// Keep a placeholder so the retry can still convert a content-bearing conversation turn.
			filtered = append(filtered, map[string]any{
				"type": "text",
				"text": "(content removed)",
			})
		}

		newRaw, err := json.Marshal(filtered)
		if err != nil {
			return changed, err
		}
		req.Messages[i].Content = newRaw
		changed = true
	}

	return changed, nil
}
