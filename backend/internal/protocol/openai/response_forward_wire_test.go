package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

var benchmarkUsageSink ForwardUsage

func BenchmarkOpenAIUsageExtractLegacy(b *testing.B) {
	body := benchmarkOpenAIUsageJSONBytes()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		usage, ok := legacyExtractOpenAIUsageFromJSONBytes(body)
		if ok {
			benchmarkUsageSink = usage
		}
	}
}

func BenchmarkOpenAIUsageExtractOptimized(b *testing.B) {
	body := benchmarkOpenAIUsageJSONBytes()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		usage, ok := ExtractOpenAIUsageFromJSONBytes(body)
		if ok {
			benchmarkUsageSink = usage
		}
	}
}

func TestExtractOpenAIUsageFromJSONBytes_MergesHostedImageGenToolUsage(t *testing.T) {
	// Streaming terminal events nest both main usage and image tool usage under response.
	body := []byte(`{
		"type": "response.completed",
		"response": {
			"usage": {
				"input_tokens": 43792,
				"output_tokens": 1005,
				"total_tokens": 44797
			},
			"tool_usage": {
				"image_gen": {
					"input_tokens": 7918,
					"input_tokens_details": {"image_tokens": 7620, "text_tokens": 298},
					"output_tokens": 186,
					"output_tokens_details": {"image_tokens": 186, "text_tokens": 0},
					"total_tokens": 8104
				}
			}
		}
	}`)

	usage, ok := ExtractOpenAIUsageFromJSONBytes(body)
	require.True(t, ok)
	require.Equal(t, 43792, usage.InputTokens)
	require.Equal(t, 1005, usage.OutputTokens)
	require.Equal(t, 186, usage.ImageOutputTokens)
	require.Equal(t, 7620, usage.ImageInputTokens)
}

func TestExtractOpenAIUsageFromJSONBytes_NonStreamingMergesImageGen(t *testing.T) {
	// Non-streaming responses put both main usage and image tool usage at the top level.
	body := []byte(`{
		"id": "resp_abc123",
		"object": "response",
		"usage": {
			"input_tokens": 5000,
			"output_tokens": 200
		},
		"tool_usage": {
			"image_gen": {
				"input_tokens": 3000,
				"input_tokens_details": {"image_tokens": 2800, "text_tokens": 200},
				"output_tokens": 150,
				"output_tokens_details": {"image_tokens": 150, "text_tokens": 0},
				"total_tokens": 3150
			}
		}
	}`)

	usage, ok := ExtractOpenAIUsageFromJSONBytes(body)
	require.True(t, ok)
	require.Equal(t, 5000, usage.InputTokens)
	require.Equal(t, 200, usage.OutputTokens)
	require.Equal(t, 150, usage.ImageOutputTokens)
	require.Equal(t, 2800, usage.ImageInputTokens)
}

func TestExtractOpenAIUsageFromJSONBytes_HostedImageGenFallbackRules(t *testing.T) {
	tests := []struct {
		name            string
		body            string
		wantImageInput  int
		wantImageOutput int
	}{
		{
			name:            "without tool usage",
			body:            `{"usage":{"input_tokens":100,"output_tokens":50}}`,
			wantImageInput:  0,
			wantImageOutput: 0,
		},
		{
			name:            "base usage takes precedence",
			body:            `{"usage":{"input_tokens":100,"output_tokens":50,"input_tokens_details":{"image_tokens":20},"output_tokens_details":{"image_tokens":30}},"tool_usage":{"image_gen":{"input_tokens_details":{"image_tokens":90},"output_tokens_details":{"image_tokens":100}}}}`,
			wantImageInput:  20,
			wantImageOutput: 30,
		},
		{
			name:            "malformed tool details ignored",
			body:            `{"usage":{"input_tokens":100,"output_tokens":50},"tool_usage":{"image_gen":{"input_tokens_details":{"image_tokens":1.5},"output_tokens_details":{"image_tokens":-1}}}}`,
			wantImageInput:  0,
			wantImageOutput: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage, ok := ExtractOpenAIUsageFromJSONBytes([]byte(tt.body))
			require.True(t, ok)
			require.Equal(t, tt.wantImageInput, usage.ImageInputTokens)
			require.Equal(t, tt.wantImageOutput, usage.ImageOutputTokens)
		})
	}
}

func TestMergeHostedImageGenToolUsage_EmptyImageGen(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{name: "missing", json: `{}`},
		{name: "null", json: `{"image_gen":null}`},
		{name: "not object", json: `{"image_gen":42}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := ForwardUsage{InputTokens: 100, OutputTokens: 50}
			original := usage
			MergeHostedImageGenToolUsage(gjson.Get(tt.json, "image_gen"), &usage)
			require.Equal(t, original, usage)
		})
	}
}

func TestParseSSEUsageBytes_ResponseCompletedWithImageGen(t *testing.T) {
	data := []byte(`{
		"type": "response.completed",
		"response": {
			"usage": {"input_tokens": 10000, "output_tokens": 500},
			"tool_usage": {
				"image_gen": {
					"input_tokens_details": {"image_tokens": 3800, "text_tokens": 200},
					"output_tokens_details": {"image_tokens": 186, "text_tokens": 0}
				}
			}
		}
	}`)

	usage := &ForwardUsage{}
	ParseSSEUsageBytes(data, usage)

	require.Equal(t, 10000, usage.InputTokens)
	require.Equal(t, 500, usage.OutputTokens)
	require.Equal(t, 186, usage.ImageOutputTokens)
	require.Equal(t, 3800, usage.ImageInputTokens)
}

func TestReplaceModelInResponseBody(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		from     string
		to       string
		expected string
	}{
		{
			name:     "替换顶层 model",
			body:     `{"id":"chatcmpl-123","model":"gpt-4o","choices":[]}`,
			from:     "gpt-4o",
			to:       "alias",
			expected: `{"id":"chatcmpl-123","model":"alias","choices":[]}`,
		},
		{
			name:     "model 不匹配不替换",
			body:     `{"id":"chatcmpl-123","model":"gpt-3.5-turbo","choices":[]}`,
			from:     "gpt-4o",
			to:       "alias",
			expected: `{"id":"chatcmpl-123","model":"gpt-3.5-turbo","choices":[]}`,
		},
		{
			name:     "无 model 字段不替换",
			body:     `{"id":"chatcmpl-123","choices":[]}`,
			from:     "gpt-4o",
			to:       "alias",
			expected: `{"id":"chatcmpl-123","choices":[]}`,
		},
		{
			name:     "非法 JSON 返回原值",
			body:     `not json`,
			from:     "gpt-4o",
			to:       "alias",
			expected: `not json`,
		},
		{
			name:     "空 body 返回原值",
			body:     ``,
			from:     "gpt-4o",
			to:       "alias",
			expected: ``,
		},
		{
			name:     "保持嵌套结构不变",
			body:     `{"model":"gpt-4o","usage":{"prompt_tokens":10,"completion_tokens":20},"choices":[{"message":{"role":"assistant","content":"hello"}}]}`,
			from:     "gpt-4o",
			to:       "alias",
			expected: `{"model":"alias","usage":{"prompt_tokens":10,"completion_tokens":20},"choices":[{"message":{"role":"assistant","content":"hello"}}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ReplaceModelInResponseBody([]byte(tt.body), tt.from, tt.to)
			require.Equal(t, tt.expected, string(got))
		})
	}
}

// TestReplaceModelInSSELine checks model replacement within SSE data lines.
func TestReplaceModelInSSELine(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		from     string
		to       string
		expected string
	}{
		{
			name:     "顶层 model 字段替换",
			line:     `data: {"id":"chatcmpl-123","model":"gpt-4o","choices":[]}`,
			from:     "gpt-4o",
			to:       "my-custom-model",
			expected: `data: {"id":"chatcmpl-123","model":"my-custom-model","choices":[]}`,
		},
		{
			name:     "嵌套 response.model 替换",
			line:     `data: {"type":"response","response":{"id":"resp-1","model":"gpt-4o","output":[]}}`,
			from:     "gpt-4o",
			to:       "my-model",
			expected: `data: {"type":"response","response":{"id":"resp-1","model":"my-model","output":[]}}`,
		},
		{
			name:     "model 不匹配时不替换",
			line:     `data: {"id":"chatcmpl-123","model":"gpt-3.5-turbo","choices":[]}`,
			from:     "gpt-4o",
			to:       "my-model",
			expected: `data: {"id":"chatcmpl-123","model":"gpt-3.5-turbo","choices":[]}`,
		},
		{
			name:     "无 model 字段时不替换",
			line:     `data: {"id":"chatcmpl-123","choices":[]}`,
			from:     "gpt-4o",
			to:       "my-model",
			expected: `data: {"id":"chatcmpl-123","choices":[]}`,
		},
		{
			name:     "空 data 行",
			line:     `data: `,
			from:     "gpt-4o",
			to:       "my-model",
			expected: `data: `,
		},
		{
			name:     "[DONE] 行",
			line:     `data: [DONE]`,
			from:     "gpt-4o",
			to:       "my-model",
			expected: `data: [DONE]`,
		},
		{
			name:     "非 data: 前缀行",
			line:     `event: message`,
			from:     "gpt-4o",
			to:       "my-model",
			expected: `event: message`,
		},
		{
			name:     "非法 JSON 不替换",
			line:     `data: {invalid json}`,
			from:     "gpt-4o",
			to:       "my-model",
			expected: `data: {invalid json}`,
		},
		{
			name:     "无空格 data: 格式",
			line:     `data:{"id":"x","model":"gpt-4o"}`,
			from:     "gpt-4o",
			to:       "my-model",
			expected: `data: {"id":"x","model":"my-model"}`,
		},
		{
			name:     "model 名含特殊字符",
			line:     `data: {"model":"org/model-v2.1-beta"}`,
			from:     "org/model-v2.1-beta",
			to:       "custom/alias",
			expected: `data: {"model":"custom/alias"}`,
		},
		{
			name:     "空行",
			line:     "",
			from:     "gpt-4o",
			to:       "my-model",
			expected: "",
		},
		{
			name:     "保持其他字段不变",
			line:     `data: {"id":"abc","object":"chat.completion.chunk","model":"gpt-4o","created":1234567890,"choices":[{"index":0,"delta":{"content":"hi"}}]}`,
			from:     "gpt-4o",
			to:       "alias",
			expected: `data: {"id":"abc","object":"chat.completion.chunk","model":"alias","created":1234567890,"choices":[{"index":0,"delta":{"content":"hi"}}]}`,
		},
		{
			name:     "顶层优先于嵌套：同时存在两个 model",
			line:     `data: {"model":"gpt-4o","response":{"model":"gpt-4o"}}`,
			from:     "gpt-4o",
			to:       "replaced",
			expected: `data: {"model":"replaced","response":{"model":"gpt-4o"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ReplaceModelInSSELine(tt.line, tt.from, tt.to)
			require.Equal(t, tt.expected, got)
		})
	}
}

func TestReplaceModelInSSEBody(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		from     string
		to       string
		expected string
	}{
		{
			name:     "多行 SSE body 替换",
			body:     "data: {\"model\":\"gpt-4o\",\"choices\":[]}\n\ndata: {\"model\":\"gpt-4o\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n",
			from:     "gpt-4o",
			to:       "alias",
			expected: "data: {\"model\":\"alias\",\"choices\":[]}\n\ndata: {\"model\":\"alias\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n",
		},
		{
			name:     "无需替换的 body",
			body:     "data: {\"model\":\"gpt-3.5-turbo\"}\n\ndata: [DONE]\n",
			from:     "gpt-4o",
			to:       "alias",
			expected: "data: {\"model\":\"gpt-3.5-turbo\"}\n\ndata: [DONE]\n",
		},
		{
			name:     "混合 event 和 data 行",
			body:     "event: message\ndata: {\"model\":\"gpt-4o\"}\n\n",
			from:     "gpt-4o",
			to:       "alias",
			expected: "event: message\ndata: {\"model\":\"alias\"}\n\n",
		},
		{
			name:     "空 body",
			body:     "",
			from:     "gpt-4o",
			to:       "alias",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ReplaceModelInSSEBody(tt.body, tt.from, tt.to)
			require.Equal(t, tt.expected, got)
		})
	}
}

func TestParseSSEUsage_SelectiveParsing(t *testing.T) {
	usage := &ForwardUsage{InputTokens: 9, OutputTokens: 8, CacheReadInputTokens: 7}

	// Merge positive usage fields received before the terminal event.
	ParseSSEUsageBytes([]byte(`{"type":"response.in_progress","response":{"usage":{"input_tokens":1,"output_tokens":2}}}`), usage)
	require.Equal(t, 1, usage.InputTokens)
	require.Equal(t, 2, usage.OutputTokens)
	require.Equal(t, 7, usage.CacheReadInputTokens)

	// Completed events supply terminal usage.
	ParseSSEUsageBytes([]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":5,"input_tokens_details":{"cached_tokens":2}}}}`), usage)
	require.Equal(t, 3, usage.InputTokens)
	require.Equal(t, 5, usage.OutputTokens)
	require.Equal(t, 2, usage.CacheReadInputTokens)

	// Legacy done events can also supply terminal usage.
	ParseSSEUsageBytes([]byte(`{"type":"response.done","response":{"usage":{"input_tokens":13,"output_tokens":15,"input_tokens_details":{"cached_tokens":4}}}}`), usage)
	require.Equal(t, 13, usage.InputTokens)
	require.Equal(t, 15, usage.OutputTokens)
	require.Equal(t, 4, usage.CacheReadInputTokens)

	// Failed events can report consumed tokens, as in WebSocket and passthrough paths.
	ParseSSEUsageBytes([]byte(`{"type":"response.failed","response":{"usage":{"input_tokens":17,"output_tokens":19,"input_tokens_details":{"cached_tokens":6}}}}`), usage)
	require.Equal(t, 17, usage.InputTokens)
	require.Equal(t, 19, usage.OutputTokens)
	require.Equal(t, 6, usage.CacheReadInputTokens)

	ParseSSEUsageBytes([]byte(`{"type":"response.completed","response":{"usage":{"prompt_tokens":21,"completion_tokens":8,"prompt_tokens_details":{"cached_tokens":6}}}}`), usage)
	require.Equal(t, 21, usage.InputTokens)
	require.Equal(t, 8, usage.OutputTokens)
	require.Equal(t, 6, usage.CacheReadInputTokens)
}

func TestParseSSEUsage_NonTerminalUsageMergesNonZeroFields(t *testing.T) {
	usage := &ForwardUsage{}

	ParseSSEUsageBytes([]byte(`{"type":"response.in_progress","usage":{"input_tokens":17,"output_tokens":1,"input_tokens_details":{"cached_tokens":4}}}`), usage)
	ParseSSEUsageBytes([]byte(`{"type":"response.output_text.done","usage":{"input_tokens":0,"output_tokens":5,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":3}}}`), usage)

	require.Equal(t, 17, usage.InputTokens)
	require.Equal(t, 5, usage.OutputTokens)
	require.Equal(t, 4, usage.CacheReadInputTokens)
	require.Equal(t, 3, usage.CacheCreationInputTokens)
}

func TestParseSSEUsage_TerminalUsageReplacesFallback(t *testing.T) {
	usage := &ForwardUsage{}

	ParseSSEUsageBytes([]byte(`{"type":"response.output_text.done","usage":{"input_tokens":17,"output_tokens":5,"input_tokens_details":{"cached_tokens":4}}}`), usage)
	ParseSSEUsageBytes([]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":19,"output_tokens":7}}}`), usage)

	require.Equal(t, 19, usage.InputTokens)
	require.Equal(t, 7, usage.OutputTokens)
	require.Zero(t, usage.CacheReadInputTokens)
}

func TestParseSSEUsage_TerminalWithoutUsageKeepsFallback(t *testing.T) {
	usage := &ForwardUsage{}

	ParseSSEUsageBytes([]byte(`{"type":"response.in_progress","usage":{"input_tokens":17,"output_tokens":5}}`), usage)
	ParseSSEUsageBytes([]byte(`{"type":"response.completed","response":{"id":"resp_1"}}`), usage)
	ParseSSEUsageBytes([]byte("  [DONE]\n"), usage)

	require.Equal(t, 17, usage.InputTokens)
	require.Equal(t, 5, usage.OutputTokens)
}

func TestEffectiveOpenAISSEEventTypePrefersPayload(t *testing.T) {
	t.Parallel()

	require.Equal(t, "response.failed", EffectiveOpenAISSEEventType([]byte(`{"type":"response.failed"}`), "error"))
	require.Equal(t, "error", EffectiveOpenAISSEEventType([]byte(`{"error":{"message":"failed"}}`), " error "))
	require.JSONEq(t, `{"type":"error","error":{"message":"failed"}}`, OpenAICompatPayloadWithEventType(`{"type":"","error":{"message":"failed"}}`, "error"))
}

func TestExtractOpenAISSETerminalEventUsesFinalAuthoritativeTerminal(t *testing.T) {
	t.Parallel()

	body := "data: {\"type\":\"error\",\"error\":{\"message\":\"recovering\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\"}}\n\n"
	eventType, payload, ok := ExtractOpenAISSETerminalEvent(body)
	require.True(t, ok)
	require.Equal(t, "response.completed", eventType)
	require.Equal(t, "resp_1", gjson.GetBytes(payload, "response.id").String())
}

func TestParseSSEUsageEffectiveTerminalRules(t *testing.T) {
	t.Parallel()

	usage := &ForwardUsage{}
	ParseSSEUsageBytesWithType([]byte(`{"usage":{"input_tokens":17,"output_tokens":5,"input_tokens_details":{"cached_tokens":3}}}`), "response.in_progress", usage)
	ParseSSEUsageBytesWithType([]byte(`{"response":{"id":"resp_1"}}`), "response.completed", usage)
	require.Equal(t, ForwardUsage{InputTokens: 17, OutputTokens: 5, CacheReadInputTokens: 3}, *usage)

	ParseSSEUsageBytesWithType([]byte(`{"response":{"usage":{"input_tokens":0,"output_tokens":0,"input_tokens_details":{"cached_tokens":0}}}}`), "response.completed", usage)
	require.Equal(t, ForwardUsage{InputTokens: 17, OutputTokens: 5, CacheReadInputTokens: 3}, *usage)

	ParseSSEUsageBytesWithType([]byte(`{"response":{"usage":{"input_tokens":2,"output_tokens":0,"input_tokens_details":{"cached_tokens":0}}}}`), "response.completed", usage)
	require.Equal(t, ForwardUsage{InputTokens: 2}, *usage)
}

func BenchmarkParseSSEUsageNoUsageDelta(b *testing.B) {
	usage := &ForwardUsage{}
	payload := []byte(`{"type":"response.output_text.delta","delta":"hello"}`)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		ParseSSEUsageBytesWithType(payload, "response.output_text.delta", usage)
	}
}

func TestForEachOpenAISSEFrameDataTypeOverridesEventField(t *testing.T) {
	t.Parallel()

	var types []string
	ForEachOpenAISSEFrame(strings.Join([]string{
		"event: response.in_progress",
		`data: {"type":"response.completed","response":{"id":"resp_1"}}`,
		"",
	}, "\n"), func(eventType string, _ []byte) {
		types = append(types, eventType)
	})
	require.Equal(t, []string{"response.completed"}, types)
}

func benchmarkOpenAIUsageJSONBytes() []byte {
	return []byte(`{"id":"resp_bench","object":"response","model":"gpt-5.3-codex","usage":{"input_tokens":3210,"output_tokens":987,"input_tokens_details":{"cached_tokens":456}}}`)
}

func legacyExtractOpenAIUsageFromJSONBytes(body []byte) (ForwardUsage, bool) {
	var response struct {
		Usage struct {
			InputTokens       int `json:"input_tokens"`
			OutputTokens      int `json:"output_tokens"`
			InputTokenDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return ForwardUsage{}, false
	}
	return ForwardUsage{
		InputTokens:          response.Usage.InputTokens,
		OutputTokens:         response.Usage.OutputTokens,
		CacheReadInputTokens: response.Usage.InputTokenDetails.CachedTokens,
	}, true
}
