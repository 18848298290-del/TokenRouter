package openai

import (
	"strings"
)

// test_payload.go builds Responses and Chat Completions payloads for provider
// text probes.
// Provider text probes use one user prompt in the selected Responses or Chat
// wire format. Blank prompts become hi, while Responses probes include the
// shared Codex instructions and OAuth storage policy needed by the internal
// ChatGPT endpoint.

// TestResponsesPayload builds a streaming Responses probe using modelID as
// supplied. Prompt is trimmed and blank input becomes hi; OAuth disables
// storage and both credential forms receive the shared embedded instructions.
func TestResponsesPayload(modelID string, prompt string, isOAuth bool) map[string]any {
	testPrompt := strings.TrimSpace(prompt)
	if testPrompt == "" {
		testPrompt = "hi"
	}
	payload := map[string]any{
		"model": modelID,
		"input": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{
						"type": "input_text",
						"text": testPrompt,
					},
				},
			},
		},
		"stream": true,
	}

	// OAuth probes target internal ChatGPT Responses, which expects disabled
	// storage; API-key probes keep the standard request field selection for that provider.
	if isOAuth {
		payload["store"] = false
	}

	// Both OAuth and API key probes use the shared instructions.
	payload["instructions"] = DefaultInstructions

	return payload
}

// TestChatCompletionsPayload builds a streaming Chat probe with one user
// message and the supplied modelID. Prompt is trimmed and blank input becomes
// hi, matching the ordinary probe content used for Responses.
func TestChatCompletionsPayload(modelID string, prompt string) map[string]any {
	testPrompt := strings.TrimSpace(prompt)
	if testPrompt == "" {
		testPrompt = "hi"
	}

	return map[string]any{
		"model": modelID,
		"messages": []map[string]any{
			{
				"role":    "user",
				"content": testPrompt,
			},
		},
		"stream": true,
	}
}
