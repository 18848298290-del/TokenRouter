package clientmeta

import (
	"encoding/json"
	"strings"
)

const (
	InterceptTypeNone              InterceptType = iota
	InterceptTypeWarmup                          // Warmup request; reply with "New Conversation".
	InterceptTypeSuggestionMode                  // Suggestion mode; reply with an empty string.
	InterceptTypeMaxTokensOneHaiku               // Single-token Haiku probe; reply with "#".
)

// InterceptType identifies requests answered locally.
type InterceptType int

// DetectInterceptType classifies probe, suggestion, and warmup requests for local responses.
// Haiku probe detection requires a validated Claude Code client.
func DetectInterceptType(body []byte, model string, maxTokens int, isClaudeCodeClient bool) InterceptType {
	// Probe detection takes precedence for both streaming and buffered requests.
	if isClaudeCodeClient && IsHaikuProbe(model, maxTokens) {
		return InterceptTypeMaxTokensOneHaiku
	}

	// Skip JSON parsing when no suggestion or warmup marker is present.
	bodyStr := string(body)
	hasSuggestionMode := strings.Contains(bodyStr, "[SUGGESTION MODE:")
	hasWarmupKeyword := strings.Contains(bodyStr, "title") || strings.Contains(bodyStr, "Warmup")

	if !hasSuggestionMode && !hasWarmupKeyword {
		return InterceptTypeNone
	}

	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return InterceptTypeNone
	}

	// Suggestion mode must begin the first text block of the final user message.
	if hasSuggestionMode && len(req.Messages) > 0 {
		lastMsg := req.Messages[len(req.Messages)-1]
		if lastMsg.Role == "user" && len(lastMsg.Content) > 0 &&
			lastMsg.Content[0].Type == "text" &&
			strings.HasPrefix(lastMsg.Content[0].Text, "[SUGGESTION MODE:") {
			return InterceptTypeSuggestionMode
		}
	}

	if hasWarmupKeyword {
		// Recognize conversation-title prompts and the fixed warmup message.
		for _, msg := range req.Messages {
			for _, content := range msg.Content {
				if content.Type == "text" {
					if strings.Contains(content.Text, "Please write a 5-10 word title for the following conversation:") ||
						content.Text == "Warmup" {
						return InterceptTypeWarmup
					}
				}
			}
		}
		// Recognize title extraction in the system prompt.
		for _, sys := range req.System {
			if strings.Contains(sys.Text, "nalyze if this message indicates a new conversation topic. If it does, extract a 2-3 word title") {
				return InterceptTypeWarmup
			}
		}
	}

	return InterceptTypeNone
}
