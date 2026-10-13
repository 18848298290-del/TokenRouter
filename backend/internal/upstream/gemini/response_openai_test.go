package gemini

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGeminiResponseToChatCompletionsPreservesInlineData converts image-only
// and mixed text/image parts to Chat output. It checks data-URL image text,
// preceding text order, one choice, and the final stop reason.
func TestGeminiResponseToChatCompletionsPreservesInlineData(t *testing.T) {
	tests := []struct {
		name  string // case identity label
		parts []any  // ordered source response parts
		want  string // expected converted Chat text
	}{
		{
			name: "image only",

			parts: []any{
				map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": "aW1hZ2U="}},
			},

			want: "![image](data:image/png;base64,aW1hZ2U=)",
		},

		{
			name: "text and image",

			parts: []any{
				map[string]any{"text": "rendered image:\n"},

				map[string]any{"inlineData": map[string]any{"mimeType": "image/webp", "data": "d2VicA=="}},
			},

			want: "rendered image:\n![image](data:image/webp;base64,d2VicA==)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			geminiResp := map[string]any{
				"candidates": []any{map[string]any{
					"content":      map[string]any{"parts": tt.parts},
					"finishReason": "STOP",
				}},
			}
			rawData, err := json.Marshal(geminiResp)
			require.NoError(t, err)

			got, _, err := GeminiResponseToChatCompletions(geminiResp, "gemini-test", rawData, nil)
			require.NoError(t, err)
			require.Len(t, got.Choices, 1)

			var content string
			require.NoError(t, json.Unmarshal(got.Choices[0].Message.Content, &content))
			require.Equal(t, tt.want, content)
			require.Equal(t, "stop", got.Choices[0].FinishReason)
		})
	}
}

// TestGeminiResponseToChatCompletionsOmitsInvalidInlineData supplies
// unsupported MIME, malformed MIME, and invalid base64 between visible text
// parts. It checks invalid image data is omitted while the surrounding text
// remains together.
func TestGeminiResponseToChatCompletionsOmitsInvalidInlineData(t *testing.T) {
	tests := []struct {
		name       string         // case identity label
		inlineData map[string]any // invalid inline image fixture
	}{
		{
			name:       "unsupported MIME type",
			inlineData: map[string]any{"mimeType": "image/svg+xml", "data": "PHN2Zz48L3N2Zz4="},
		},

		{
			name:       "malformed MIME type",
			inlineData: map[string]any{"mimeType": "image/png; charset=utf-8", "data": "aW1hZ2U="},
		},

		{
			name:       "malformed base64",
			inlineData: map[string]any{"mimeType": "image/png", "data": "not-valid-base64!!!"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			geminiResp := map[string]any{
				"candidates": []any{map[string]any{
					"content": map[string]any{"parts": []any{map[string]any{"text": "before"}, map[string]any{"inlineData": tt.inlineData}, map[string]any{"text": "after"}}},

					"finishReason": "STOP",
				}},
			}
			rawData, err := json.Marshal(geminiResp)
			require.NoError(t, err)

			got, _, err := GeminiResponseToChatCompletions(geminiResp, "gemini-test", rawData, nil)
			require.NoError(t, err)

			var content string
			require.NoError(t, json.Unmarshal(got.Choices[0].Message.Content, &content))
			require.Equal(t, "beforeafter", content)
		})
	}
}

// TestGeminiResponseToChatCompletionsRetainsTextAndToolBehavior combines
// visible text with one function call. It checks text output, tool-call finish
// reason, function identity, and serialized arguments in the Chat result.
func TestGeminiResponseToChatCompletionsRetainsTextAndToolBehavior(t *testing.T) {
	geminiResp := map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{
				map[string]any{"text": "checking"},

				map[string]any{"functionCall": map[string]any{
					"name": "get_weather",
					"args": map[string]any{"city": "Paris"},
				}},
			}},

			"finishReason": "STOP",
		}},
	}
	rawData, err := json.Marshal(geminiResp)
	require.NoError(t, err)

	got, _, err := GeminiResponseToChatCompletions(geminiResp, "gemini-test", rawData, nil)
	require.NoError(t, err)
	require.Len(t, got.Choices, 1)

	choice := got.Choices[0]
	var content string
	require.NoError(t, json.Unmarshal(choice.Message.Content, &content))
	require.Equal(t, "checking", content)
	require.Equal(t, "tool_calls", choice.FinishReason)
	require.Len(t, choice.Message.ToolCalls, 1)
	require.Equal(t, "get_weather", choice.Message.ToolCalls[0].Function.Name)
	require.JSONEq(t, `{"city":"Paris"}`, choice.Message.ToolCalls[0].Function.Arguments)
}
