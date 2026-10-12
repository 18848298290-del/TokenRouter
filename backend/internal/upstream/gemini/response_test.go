package gemini

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

func TestConvertGeminiToClaudeMessageOmitsInlineDataForAnthropicMessages(t *testing.T) {
	geminiResp := map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{
				map[string]any{"text": "before"},

				map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": "aW1hZ2U="}},

				map[string]any{"functionCall": map[string]any{"name": "get_weather", "args": map[string]any{"city": "Paris"}}},

				map[string]any{"text": "after"},
			}},

			"finishReason": "STOP",
		}},
	}
	rawData, err := json.Marshal(geminiResp)
	require.NoError(t, err)

	withInlineData, _ := ConvertGeminiToClaudeMessage(geminiResp, "gemini-test", rawData, true)
	require.Regexp(t, `^msg_01[0-9A-Za-z]{22}$`, withInlineData["id"])
	contentWithInlineData, ok := withInlineData["content"].([]any)
	require.True(t, ok)
	require.Len(t, contentWithInlineData, 4)
	require.Equal(t, map[string]any{"type": "text", "text": "before"}, contentWithInlineData[0])
	require.Equal(t, map[string]any{"type": "text", "text": "![image](data:image/png;base64,aW1hZ2U=)"}, contentWithInlineData[1])
	toolUse, ok := contentWithInlineData[2].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "tool_use", toolUse["type"])
	require.Equal(t, "get_weather", toolUse["name"])
	require.Equal(t, map[string]any{"type": "text", "text": "after"}, contentWithInlineData[3])

	withoutInlineData, _ := ConvertGeminiToClaudeMessage(geminiResp, "gemini-test", rawData, false)
	contentWithoutInlineData, ok := withoutInlineData["content"].([]any)
	require.True(t, ok)
	require.Len(t, contentWithoutInlineData, 3)
	require.Equal(t, map[string]any{"type": "text", "text": "before"}, contentWithoutInlineData[0])
	toolUseWithoutInlineData, ok := contentWithoutInlineData[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "tool_use", toolUseWithoutInlineData["type"])
	require.Equal(t, "get_weather", toolUseWithoutInlineData["name"])
	require.Equal(t, map[string]any{"type": "text", "text": "after"}, contentWithoutInlineData[2])
}

// TestUnwrapGeminiResponse checks that scalar response fields leave the body intact.
func TestUnwrapGeminiResponse(t *testing.T) {
	// Exercise a response object larger than 50 KiB.
	largePadding := strings.Repeat("x", 50*1024)
	largeInput := []byte(fmt.Sprintf(`{"response":{"id":"big","pad":"%s"}}`, largePadding))
	largeExpected := fmt.Sprintf(`{"id":"big","pad":"%s"}`, largePadding)

	tests := []struct {
		name     string
		input    []byte
		expected string
		wantErr  bool
	}{
		{
			name: "正常 response 包装（JSON 对象）",

			input: []byte(`{"response":{"key":"val"}}`),

			expected: `{"key":"val"}`,
		},

		{
			name:     "无包装直接返回",
			input:    []byte(`{"key":"val"}`),
			expected: `{"key":"val"}`,
		},

		{
			name:     "空 JSON",
			input:    []byte(`{}`),
			expected: `{}`,
		},

		{
			name:     "null response 返回原始 body",
			input:    []byte(`{"response":null}`),
			expected: `{"response":null}`,
		},

		{
			name:     "非法 JSON 返回原始 body",
			input:    []byte(`not json`),
			expected: `not json`,
		},

		{
			name: "response 为基础类型 string 返回原始 body",

			input: []byte(`{"response":"hello"}`),

			expected: `{"response":"hello"}`,
		},

		{
			name: "嵌套 response 只解一层",

			input: []byte(`{"response":{"response":{"inner":true}}}`),

			expected: `{"response":{"inner":true}}`,
		},

		{
			name:     "大型 JSON >50KB",
			input:    largeInput,
			expected: largeExpected,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := UnwrapGeminiResponse(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.expected, strings.TrimSpace(string(got)))
		})
	}
}

func TestExtractGeminiUsage(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantNil   bool
		wantUsage *upstream.TokenUsage
	}{
		{
			name: "完整 usageMetadata",

			input: `{"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":50,"cachedContentTokenCount":20}}`,

			wantNil: false,

			wantUsage: &upstream.TokenUsage{
				InputTokens:          80,
				OutputTokens:         50,
				CacheReadInputTokens: 20,
			},
		},

		{
			name: "包含 thoughtsTokenCount",

			input: `{"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"thoughtsTokenCount":50}}`,

			wantNil: false,

			wantUsage: &upstream.TokenUsage{
				InputTokens:          100,
				OutputTokens:         70,
				CacheReadInputTokens: 0,
			},
		},

		{
			name: "包含 thoughtsTokenCount 与缓存",

			input: `{"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"cachedContentTokenCount":30,"thoughtsTokenCount":50}}`,

			wantNil: false,

			wantUsage: &upstream.TokenUsage{
				InputTokens:          70,
				OutputTokens:         70,
				CacheReadInputTokens: 30,
			},
		},

		{
			name: "缺失 cachedContentTokenCount",

			input: `{"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":50}}`,

			wantNil: false,

			wantUsage: &upstream.TokenUsage{
				InputTokens:          100,
				OutputTokens:         50,
				CacheReadInputTokens: 0,
			},
		},

		{
			name:    "无 usageMetadata",
			input:   `{"candidates":[]}`,
			wantNil: true,
		},

		{
			// gjson treats an explicit null as an existing field, so usage is a zero-valued
			// TokenUsage rather than nil.

			name: "null usageMetadata — gjson Exists 为 true",

			input: `{"usageMetadata":null}`,

			wantNil: false,

			wantUsage: &upstream.TokenUsage{
				InputTokens:          0,
				OutputTokens:         0,
				CacheReadInputTokens: 0,
			},
		},

		{
			name: "零值字段",

			input: `{"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0,"cachedContentTokenCount":0}}`,

			wantNil: false,

			wantUsage: &upstream.TokenUsage{
				InputTokens:          0,
				OutputTokens:         0,
				CacheReadInputTokens: 0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractGeminiUsage([]byte(tt.input))
			if tt.wantNil {
				if got != nil {
					t.Fatalf("期望返回 nil，实际返回 %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("期望返回非 nil，实际返回 nil")
				return
			}
			if got.InputTokens != tt.wantUsage.InputTokens {
				t.Errorf("InputTokens: 期望 %d，实际 %d", tt.wantUsage.InputTokens, got.InputTokens)
			}
			if got.OutputTokens != tt.wantUsage.OutputTokens {
				t.Errorf("OutputTokens: 期望 %d，实际 %d", tt.wantUsage.OutputTokens, got.OutputTokens)
			}
			if got.CacheReadInputTokens != tt.wantUsage.CacheReadInputTokens {
				t.Errorf("CacheReadInputTokens: 期望 %d，实际 %d", tt.wantUsage.CacheReadInputTokens, got.CacheReadInputTokens)
			}
		})
	}
}
