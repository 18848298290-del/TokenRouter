package requeststate

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

func TestParseGatewayRequest(t *testing.T) {
	body := []byte(`{"model":"claude-3-7-sonnet","stream":true,"metadata":{"user_id":"session_123e4567-e89b-12d3-a456-426614174000"},"system":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}],"messages":[{"content":"hi"}]}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), "")
	require.NoError(t, err)
	require.Equal(t, "claude-3-7-sonnet", parsed.Model)
	require.True(t, parsed.Stream)
	require.Equal(t, "session_123e4567-e89b-12d3-a456-426614174000", parsed.MetadataUserID)
	require.True(t, parsed.HasSystem)
	require.NotEmpty(t, parsed.SystemRaw())
	require.NotEmpty(t, parsed.MessagesRaw())
	require.False(t, parsed.ThinkingEnabled)
}

func TestParseGatewayRequest_ThinkingEnabled(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-5","thinking":{"type":"enabled"},"messages":[{"content":"hi"}]}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), "")
	require.NoError(t, err)
	require.Equal(t, "claude-sonnet-4-5", parsed.Model)
	require.True(t, parsed.ThinkingEnabled)
}

func TestParseGatewayRequest_ThinkingAdaptiveEnabled(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-5","thinking":{"type":"adaptive"},"messages":[{"content":"hi"}]}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), "")
	require.NoError(t, err)
	require.Equal(t, "claude-sonnet-4-5", parsed.Model)
	require.True(t, parsed.ThinkingEnabled)
}

func TestParseGatewayRequest_MaxTokens(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","max_tokens":1}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), "")
	require.NoError(t, err)
	require.Equal(t, 1, parsed.MaxTokens)
}

func TestParseGatewayRequest_MaxTokensNonIntegralIgnored(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","max_tokens":1.5}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), "")
	require.NoError(t, err)
	require.Equal(t, 0, parsed.MaxTokens)
}

func TestParseGatewayRequest_SystemNull(t *testing.T) {
	body := []byte(`{"model":"claude-3","system":null}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), "")
	require.NoError(t, err)
	// system:null counts as present, suppressing default system injection.
	require.True(t, parsed.HasSystem)
	require.Equal(t, []byte("null"), parsed.SystemRaw())
}

func TestParseGatewayRequest_InvalidModelType(t *testing.T) {
	body := []byte(`{"model":123}`)
	_, err := ParseGatewayRequest(NewRequestBodyRef(body), "")
	require.Error(t, err)
}

func TestParseGatewayRequest_InvalidStreamType(t *testing.T) {
	body := []byte(`{"stream":"true"}`)
	_, err := ParseGatewayRequest(NewRequestBodyRef(body), "")
	require.Error(t, err)
}

func TestParseGatewayRequest_AnthropicNormalizesClaudeCodeLongContextModelSuffix(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  string
	}{
		{name: "lowercase suffix", model: "claude-opus-4-8[1m]", want: "claude-opus-4-8"},
		{name: "uppercase suffix", model: "claude-opus-4-8[1M]", want: "claude-opus-4-8"},
		{name: "duplicated suffix", model: "claude-opus-4-8[1M][1m]", want: "claude-opus-4-8"},
		{name: "suffix in middle", model: "claude-opus-4-8[1m]-preview", want: "claude-opus-4-8[1m]-preview"},
		{name: "suffix only", model: "[1m]", want: "[1m]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":%q,"system":"test","messages":[{"role":"user","content":"hi"}]}`, tt.model))
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), capability.PlatformAnthropic)
			require.NoError(t, err)
			require.Equal(t, tt.want, parsed.Model)
			require.Equal(t, tt.want, gjson.GetBytes(parsed.Body.Bytes(), "model").String())
			require.Equal(t, `"test"`, string(parsed.SystemRaw()))
			require.NotEmpty(t, parsed.MessagesRaw())
		})
	}
}

func TestParseGatewayRequest_NonAnthropicPreservesClaudeCodeLongContextModelSuffix(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-8[1m]","input":"hi"}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), "responses")
	require.NoError(t, err)
	require.Equal(t, "claude-opus-4-8[1m]", parsed.Model)
	require.Equal(t, "claude-opus-4-8[1m]", gjson.GetBytes(parsed.Body.Bytes(), "model").String())
}

func TestParseGatewayRequest_ResponsesInput(t *testing.T) {
	body := []byte(`{"model":"gpt-5.1","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), "responses")
	require.NoError(t, err)
	require.NotEmpty(t, parsed.InputRaw())
	require.Nil(t, parsed.MessagesRaw())
	require.Equal(t, "hello", gjson.ParseBytes(parsed.InputRaw()).Get("0.content.0.text").String())
}

func TestParseGatewayRequest_GeminiContents(t *testing.T) {
	body := []byte(`{
		"contents": [
			{"role": "user", "parts": [{"text": "Hello"}]},
			{"role": "model", "parts": [{"text": "Hi there"}]},
			{"role": "user", "parts": [{"text": "How are you?"}]}
		]
	}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), capability.PlatformGemini)
	require.NoError(t, err)
	require.Len(t, gjson.ParseBytes(parsed.MessagesRaw()).Array(), 3, "should parse contents as Messages")
	require.False(t, parsed.HasSystem, "Gemini format should not set HasSystem")
	require.Nil(t, parsed.SystemRaw(), "no systemInstruction means nil System")
}

func TestParseGatewayRequest_GeminiSystemInstruction(t *testing.T) {
	body := []byte(`{
		"systemInstruction": {
			"parts": [{"text": "You are a helpful assistant."}]
		},
		"contents": [
			{"role": "user", "parts": [{"text": "Hello"}]}
		]
	}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), capability.PlatformGemini)
	require.NoError(t, err)
	system := gjson.ParseBytes(parsed.SystemRaw())
	require.True(t, system.IsArray(), "should parse systemInstruction.parts as System")
	require.Len(t, system.Array(), 1)
	require.Equal(t, "You are a helpful assistant.", system.Get("0.text").String())
	require.Len(t, gjson.ParseBytes(parsed.MessagesRaw()).Array(), 1)
}

func TestParseGatewayRequest_GeminiWithModel(t *testing.T) {
	body := []byte(`{
		"model": "gemini-2.5-pro",
		"contents": [{"role": "user", "parts": [{"text": "test"}]}]
	}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), capability.PlatformGemini)
	require.NoError(t, err)
	require.Equal(t, "gemini-2.5-pro", parsed.Model)
	require.Len(t, gjson.ParseBytes(parsed.MessagesRaw()).Array(), 1)
}

func TestParseGatewayRequest_GeminiIgnoresAnthropicFields(t *testing.T) {
	// Gemini parsing ignores Anthropic system and messages fields.
	body := []byte(`{
		"system": "should be ignored",
		"messages": [{"role": "user", "content": "ignored"}],
		"contents": [{"role": "user", "parts": [{"text": "real content"}]}]
	}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), capability.PlatformGemini)
	require.NoError(t, err)
	require.False(t, parsed.HasSystem, "Gemini protocol should not parse Anthropic system field")
	require.Nil(t, parsed.SystemRaw(), "no systemInstruction = nil System")
	require.Len(t, gjson.ParseBytes(parsed.MessagesRaw()).Array(), 1, "should use contents, not messages")
}

func TestParseGatewayRequest_GeminiEmptyContents(t *testing.T) {
	body := []byte(`{"contents": []}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), capability.PlatformGemini)
	require.NoError(t, err)
	require.Empty(t, gjson.ParseBytes(parsed.MessagesRaw()).Array())
}

func TestParseGatewayRequest_GeminiNoContents(t *testing.T) {
	body := []byte(`{"model": "gemini-2.5-flash"}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), capability.PlatformGemini)
	require.NoError(t, err)
	require.Nil(t, parsed.MessagesRaw())
	require.Equal(t, "gemini-2.5-flash", parsed.Model)
}

func TestParseGatewayRequest_AnthropicIgnoresGeminiFields(t *testing.T) {
	// Anthropic parsing ignores Gemini contents and systemInstruction fields.
	body := []byte(`{
		"system": "real system",
		"messages": [{"role": "user", "content": "real content"}],
		"contents": [{"role": "user", "parts": [{"text": "ignored"}]}],
		"systemInstruction": {"parts": [{"text": "ignored"}]}
	}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), capability.PlatformAnthropic)
	require.NoError(t, err)
	require.True(t, parsed.HasSystem)
	require.Equal(t, "real system", gjson.ParseBytes(parsed.SystemRaw()).String())
	messages := gjson.ParseBytes(parsed.MessagesRaw()).Array()
	require.Len(t, messages, 1)
	require.Equal(t, "real content", messages[0].Get("content").String())
}

// TestParseGatewayRequest_TypeValidation checks rejection of incorrectly typed fields.
func TestParseGatewayRequest_TypeValidation(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantErr   bool
		errSubstr string // Expected error substring; empty skips the check.
	}{
		{
			name:      "model 为 int",
			body:      `{"model":123}`,
			wantErr:   true,
			errSubstr: "invalid model field type",
		},
		{
			name:      "model 为 array",
			body:      `{"model":[]}`,
			wantErr:   true,
			errSubstr: "invalid model field type",
		},
		{
			name:      "model 为 bool",
			body:      `{"model":true}`,
			wantErr:   true,
			errSubstr: "invalid model field type",
		},
		{
			name:      "model 为 null — gjson Null 类型触发类型校验错误",
			body:      `{"model":null}`,
			wantErr:   true, // gjson considers null present, but it is not a string.
			errSubstr: "invalid model field type",
		},
		{
			name:      "stream 为 string",
			body:      `{"stream":"true"}`,
			wantErr:   true,
			errSubstr: "invalid stream field type",
		},
		{
			name:      "stream 为 int",
			body:      `{"stream":1}`,
			wantErr:   true,
			errSubstr: "invalid stream field type",
		},
		{
			name:      "stream 为 null — gjson Null 类型触发类型校验错误",
			body:      `{"stream":null}`,
			wantErr:   true, // gjson considers null present, but it is not a boolean.
			errSubstr: "invalid stream field type",
		},
		{
			name:      "model 为 object",
			body:      `{"model":{}}`,
			wantErr:   true,
			errSubstr: "invalid model field type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseGatewayRequest(NewRequestBodyRef([]byte(tt.body)), "")
			if tt.wantErr {
				require.Error(t, err)
				if tt.errSubstr != "" {
					require.Contains(t, err.Error(), tt.errSubstr)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestParseGatewayRequest_OptionalFieldsMissing checks defaults for absent optional fields.
func TestParseGatewayRequest_OptionalFieldsMissing(t *testing.T) {
	tests := []struct {
		name            string
		body            string
		wantModel       string
		wantStream      bool
		wantMetadataUID string
		wantHasSystem   bool
		wantThinking    bool
		wantMaxTokens   int
		wantMessagesNil bool
		wantMessagesLen int
	}{
		{
			name:            "完全空 JSON — 所有字段零值",
			body:            `{}`,
			wantModel:       "",
			wantStream:      false,
			wantMetadataUID: "",
			wantHasSystem:   false,
			wantThinking:    false,
			wantMaxTokens:   0,
			wantMessagesNil: true,
		},
		{
			name:            "metadata 无 user_id",
			body:            `{"model":"test"}`,
			wantModel:       "test",
			wantMetadataUID: "",
			wantHasSystem:   false,
			wantThinking:    false,
		},
		{
			name:         "thinking 非 enabled（type=disabled）",
			body:         `{"model":"test","thinking":{"type":"disabled"}}`,
			wantModel:    "test",
			wantThinking: false,
		},
		{
			name:         "thinking 字段缺失",
			body:         `{"model":"test"}`,
			wantModel:    "test",
			wantThinking: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(tt.body)), "")
			require.NoError(t, err)

			require.Equal(t, tt.wantModel, parsed.Model)
			require.Equal(t, tt.wantStream, parsed.Stream)
			require.Equal(t, tt.wantMetadataUID, parsed.MetadataUserID)
			require.Equal(t, tt.wantHasSystem, parsed.HasSystem)
			require.Equal(t, tt.wantThinking, parsed.ThinkingEnabled)
			require.Equal(t, tt.wantMaxTokens, parsed.MaxTokens)

			if tt.wantMessagesNil {
				require.Nil(t, parsed.MessagesRaw())
			}
			if tt.wantMessagesLen > 0 {
				require.Len(t, gjson.ParseBytes(parsed.MessagesRaw()).Array(), tt.wantMessagesLen)
			}
		})
	}
}

// TestParseGatewayRequest_MaxTokensBoundary checks numeric max_tokens handling.
func TestParseGatewayRequest_MaxTokensBoundary(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantMaxTokens int
		wantErr       bool
	}{
		{
			name:          "正常整数",
			body:          `{"max_tokens":1024}`,
			wantMaxTokens: 1024,
		},
		{
			name:          "浮点数（非整数）被忽略",
			body:          `{"max_tokens":10.5}`,
			wantMaxTokens: 0,
		},
		{
			name:          "负整数可以通过",
			body:          `{"max_tokens":-1}`,
			wantMaxTokens: -1,
		},
		{
			name:          "超大值不 panic",
			body:          `{"max_tokens":9999999999999999}`,
			wantMaxTokens: 10000000000000000, // float64 rounds 9999999999999999 to 1e16.
		},
		{
			name:          "null 值被忽略",
			body:          `{"max_tokens":null}`,
			wantMaxTokens: 0, // Null is not a number, so max_tokens is ignored.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(tt.body)), "")
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantMaxTokens, parsed.MaxTokens)
		})
	}
}

func TestParseGatewayRequest_OutputEffort(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantEffort string
	}{
		{
			name:       "output_config.effort present",
			body:       `{"model":"claude-opus-4-6","output_config":{"effort":"medium"},"messages":[]}`,
			wantEffort: "medium",
		},
		{
			name:       "output_config.effort max",
			body:       `{"model":"claude-opus-4-6","output_config":{"effort":"max"},"messages":[]}`,
			wantEffort: "max",
		},
		{
			name:       "output_config.effort xhigh",
			body:       `{"model":"claude-opus-4-7","output_config":{"effort":"xhigh"},"messages":[]}`,
			wantEffort: "xhigh",
		},
		{
			name:       "output_config without effort",
			body:       `{"model":"claude-opus-4-6","output_config":{},"messages":[]}`,
			wantEffort: "",
		},
		{
			name:       "no output_config",
			body:       `{"model":"claude-opus-4-6","messages":[]}`,
			wantEffort: "",
		},
		{
			name:       "effort with whitespace trimmed",
			body:       `{"model":"claude-opus-4-6","output_config":{"effort":" high "},"messages":[]}`,
			wantEffort: "high",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(tt.body)), "")
			require.NoError(t, err)
			require.Equal(t, tt.wantEffort, parsed.OutputEffort)
		})
	}
}

func TestDescribeInvalidJSON_TruncatedBody(t *testing.T) {
	// Model a body truncated during transport or partially consumed by middleware.
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi`)

	err := DescribeInvalidJSON(body)

	require.Error(t, err)
	require.Contains(t, err.Error(), fmt.Sprintf("len=%d", len(body)))
	require.Contains(t, err.Error(), "unexpected end of JSON input")
}

func TestDescribeInvalidJSON_InvalidCharacterWithOffset(t *testing.T) {
	body := []byte(`{"model": bad}`)

	err := DescribeInvalidJSON(body)

	require.Error(t, err)
	require.Contains(t, err.Error(), "offset=11")
	require.Contains(t, err.Error(), "invalid character")
}

func TestDescribeInvalidJSON_DoesNotLeakBodyContent(t *testing.T) {
	secret := "sk-super-secret-value"
	body := []byte(`{"api_key":"` + secret + `","broken":`)

	err := DescribeInvalidJSON(body)

	require.Error(t, err)
	require.NotContains(t, err.Error(), secret)
}

func TestParseGatewayRequest_InvalidJSONErrorIsDiagnostic(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[`)

	_, err := ParseGatewayRequest(NewRequestBodyRef(body), capability.PlatformAnthropic)

	require.Error(t, err)
	require.True(t, strings.HasPrefix(err.Error(), "invalid json (len="), "error should carry diagnostics, got: %s", err.Error())
}

func BenchmarkParseGatewayRequest_Old_Small(b *testing.B) {
	data := buildSmallJSON()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for range b.N {
		_, _ = parseGatewayRequestOld(data, "")
	}
}

func BenchmarkParseGatewayRequest_New_Small(b *testing.B) {
	data := buildSmallJSON()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for range b.N {
		_, _ = ParseGatewayRequest(NewRequestBodyRef(data), "")
	}
}

func BenchmarkParseGatewayRequest_Old_Large(b *testing.B) {
	data := buildLargeJSON()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for range b.N {
		_, _ = parseGatewayRequestOld(data, "")
	}
}

func BenchmarkParseGatewayRequest_New_Large(b *testing.B) {
	data := buildLargeJSON()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for range b.N {
		_, _ = ParseGatewayRequest(NewRequestBodyRef(data), "")
	}
}

// parseIntegralNumber converts decoded integral values to int, rejecting fractions, NaN, infinity, and out-of-range values.
// The benchmark uses it to read max_tokens from json.Unmarshal output.
func parseIntegralNumber(raw any) (int, bool) {
	switch v := raw.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) {
			return 0, false
		}
		if v > float64(math.MaxInt) || v < float64(math.MinInt) {
			return 0, false
		}
		return int(v), true
	case int:
		return v, true
	case int8:
		return int(v), true
	case int16:
		return int(v), true
	case int32:
		return int(v), true
	case int64:
		if v > int64(math.MaxInt) || v < int64(math.MinInt) {
			return 0, false
		}
		return int(v), true
	case json.Number:
		i64, err := v.Int64()
		if err != nil {
			return 0, false
		}
		if i64 > int64(math.MaxInt) || i64 < int64(math.MinInt) {
			return 0, false
		}
		return int(i64), true
	default:
		return 0, false
	}
}

// parseGatewayRequestOld extracts fields with json.Unmarshal before calling ParseGatewayRequest to measure duplicate parsing.
func parseGatewayRequestOld(body []byte, protocol string) (*ParsedRequest, error) {
	parsed := &ParsedRequest{
		Body: NewRequestBodyRef(body),
	}

	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}

	if raw, ok := req["model"]; ok {
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("invalid model field type")
		}
		parsed.Model = s
	}

	if raw, ok := req["stream"]; ok {
		b, ok := raw.(bool)
		if !ok {
			return nil, fmt.Errorf("invalid stream field type")
		}
		parsed.Stream = b
	}

	if meta, ok := req["metadata"].(map[string]any); ok {
		if uid, ok := meta["user_id"].(string); ok {
			parsed.MetadataUserID = uid
		}
	}

	if thinking, ok := req["thinking"].(map[string]any); ok {
		if thinkType, ok := thinking["type"].(string); ok && thinkType == "enabled" {
			parsed.ThinkingEnabled = true
		}
	}

	if raw, ok := req["max_tokens"]; ok {
		if n, ok := parseIntegralNumber(raw); ok {
			parsed.MaxTokens = n
		}
	}

	return ParseGatewayRequest(parsed.Body, protocol)
}

// buildSmallJSON builds a compact multi-message benchmark request.
func buildSmallJSON() []byte {
	return []byte(`{"model":"claude-sonnet-4-5","stream":true,"max_tokens":4096,"metadata":{"user_id":"user-abc123"},"thinking":{"type":"enabled","budget_tokens":2048},"system":"You are a helpful assistant.","messages":[{"role":"user","content":"What is the meaning of life?"},{"role":"assistant","content":"The meaning of life is a philosophical question."},{"role":"user","content":"Can you elaborate?"}]}`)
}

// buildLargeJSON builds a benchmark request with 200 messages.
func buildLargeJSON() []byte {
	b := []byte(`{"model":"claude-sonnet-4-5","stream":true,"max_tokens":8192,"metadata":{"user_id":"user-xyz789"},"system":[{"type":"text","text":"You are a detailed assistant.","cache_control":{"type":"ephemeral"}}],"messages":[`)

	msgCount := 200
	for i := range msgCount {
		if i > 0 {
			b = append(b, ',')
		}
		if i%2 == 0 {
			b = fmt.Appendf(b, `{"role":"user","content":"This is user message number %d with some extra padding text to make the message reasonably long for benchmarking purposes. Lorem ipsum dolor sit amet."}`, i)
		} else {
			b = fmt.Appendf(b, `{"role":"assistant","content":[{"type":"text","text":"This is assistant response number %d. I will provide a detailed answer with multiple sentences to simulate real conversation content for benchmark testing."}]}`, i)
		}
	}

	return append(b, ']', '}')
}

func BenchmarkParseGatewayRequest_LargeAnthropicMessages(b *testing.B) {
	for _, size := range benchmarkBodySizes() {
		b.Run(size.name, func(b *testing.B) {
			body := buildLargeAnthropicMessagesBody(size.bytes, false)

			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), capability.PlatformAnthropic)
				if err != nil {
					b.Fatalf("解析 Anthropic 请求失败: %v", err)
				}
				benchmarkIntSink = len(parsed.MessagesRaw())
			}
		})
	}
}

func BenchmarkParseGatewayRequest_LargeGeminiContents(b *testing.B) {
	for _, size := range benchmarkBodySizes() {
		b.Run(size.name, func(b *testing.B) {
			body := buildLargeGeminiContentsBody(size.bytes)

			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), capability.PlatformGemini)
				if err != nil {
					b.Fatalf("解析 Gemini 请求失败: %v", err)
				}
				benchmarkIntSink = len(parsed.MessagesRaw())
			}
		})
	}
}

// buildLargeAnthropicMessagesBody repeats Messages text to a target size and optionally adds cache markers.
func buildLargeAnthropicMessagesBody(targetBytes int, includeCacheControl bool) []byte {
	var builder strings.Builder
	builder.Grow(targetBytes + 1024)
	_, _ = builder.WriteString(`{"model":"claude-sonnet-4-5","stream":true,"system":[{"type":"text","text":"system seed"}],"messages":[`)
	for i := 0; builder.Len() < targetBytes; i++ {
		if i > 0 {
			_ = builder.WriteByte(',')
		}
		_, _ = builder.WriteString(`{"role":"user","content":[{"type":"text","text":"`)
		_, _ = builder.WriteString(strings.Repeat("anthropic payload ", 64))
		_, _ = builder.WriteString(strconv.Itoa(i))
		_ = builder.WriteByte('"')
		if includeCacheControl && i%32 == 0 {
			_, _ = builder.WriteString(`,"cache_control":{"type":"ephemeral"}`)
		}
		_, _ = builder.WriteString(`}]}`)
	}
	_, _ = builder.WriteString(`]}`)
	return []byte(builder.String())
}

// buildLargeGeminiContentsBody repeats Gemini contents to a target size.
func buildLargeGeminiContentsBody(targetBytes int) []byte {
	var builder strings.Builder
	builder.Grow(targetBytes + 1024)
	_, _ = builder.WriteString(`{"model":"gemini-2.5-pro","systemInstruction":{"parts":[{"text":"system seed"}]},"contents":[`)
	for i := 0; builder.Len() < targetBytes; i++ {
		if i > 0 {
			_ = builder.WriteByte(',')
		}
		_, _ = builder.WriteString(`{"role":"user","parts":[{"text":"`)
		_, _ = builder.WriteString(strings.Repeat("gemini payload ", 64))
		_, _ = builder.WriteString(strconv.Itoa(i))
		_, _ = builder.WriteString(`"}]}`)
	}
	_, _ = builder.WriteString(`]}`)
	return []byte(builder.String())
}
