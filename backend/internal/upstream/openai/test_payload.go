package openai

import (
	"strings"
)

// TestResponsesPayload builds the Responses request shared by provider tests and usage probes.
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

	// The internal ChatGPT API requires server-side storage to be disabled for OAuth.
	if isOAuth {
		payload["store"] = false
	}

	// Both OAuth and API key probes use the shared instructions.
	payload["instructions"] = DefaultInstructions

	return payload
}

// TestChatCompletionsPayload builds a streaming Chat Completions provider probe.
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
