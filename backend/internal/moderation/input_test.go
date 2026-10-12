package moderation

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestContentModerationAuditInputAppliesSourceLimitsAndToggles(t *testing.T) {
	input := ContentModerationInput{
		Text: "abcdef\n123456",
		Items: []ContentModerationInputItem{
			{Index: 0, Source: ContentModerationSourceUser, Type: ContentModerationItemTypeText, Text: "abcdef"},
			{Index: 1, Source: ContentModerationSourceTool, Type: ContentModerationItemTypeText, Text: "123456"},
			{Index: 2, Source: ContentModerationSourceUser, Type: ContentModerationItemTypeImage, ImageRef: "https://example.com/user.png"},
			{Index: 3, Source: ContentModerationSourceTool, Type: ContentModerationItemTypeImage, ImageRef: "https://example.com/tool.png"},
		},
		Images: []string{"https://example.com/user.png", "https://example.com/tool.png"},
		ImageItems: []ContentModerationImage{
			{SourceIndex: 2, Source: ContentModerationSourceUser, Reference: "https://example.com/user.png"},
			{SourceIndex: 3, Source: ContentModerationSourceTool, Reference: "https://example.com/tool.png"},
		},
	}
	cfg := defaultContentModerationConfig()
	cfg.AuditUserTextMaxChars = 3
	cfg.AuditToolOutputMaxChars = 2

	audit := contentModerationAuditInput(input, cfg)

	require.Equal(t, "abc\n12", audit.Text)
	require.Equal(t, input.Images, audit.Images)
	require.Equal(t, []int{2, 3}, []int{audit.ImageItems[0].SourceIndex, audit.ImageItems[1].SourceIndex})

	cfg.AuditToolOutputs = false
	audit = contentModerationAuditInput(input, cfg)
	require.Equal(t, "abc", audit.Text)
	require.Equal(t, []string{"https://example.com/user.png"}, audit.Images)

	cfg.AuditImages = false
	audit = contentModerationAuditInput(input, cfg)
	require.Empty(t, audit.Images)
	require.Empty(t, audit.ImageItems)
}

// TestExtractContentModerationInput_AnthropicAgentToolLoopExtractsToolResult checks extraction of the
// current tool-loop result.
func TestExtractContentModerationInput_AnthropicAgentToolLoopExtractsToolResult(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"user","content":"调用一下天气工具"},
			{"role":"assistant","content":[{"type":"tool_use","id":"tool_1","name":"weather","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_1","content":"晴 25 度"}]}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolAnthropicMessages, body)

	require.Equal(t, "晴 25 度", input.Text)
	require.Empty(t, input.Images)
	require.Equal(t, ContentModerationSourceTool, input.Source)
}

func TestExtractContentModerationInput_AnthropicFirstTurnExtractsUser(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"user","content":"Q1"}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolAnthropicMessages, body)

	require.Equal(t, "Q1", input.Text)
}

func TestExtractContentModerationInput_AnthropicMultiTurnExtractsLatestUser(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"user","content":"Q1"},
			{"role":"assistant","content":"A1"},
			{"role":"user","content":"Q2"}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolAnthropicMessages, body)

	require.Equal(t, "Q2", input.Text)
}

func TestExtractContentModerationInput_AnthropicStreamResendExtractsResend(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"user","content":"原问题"},
			{"role":"assistant","content":"部分回答……"},
			{"role":"user","content":"重发"}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolAnthropicMessages, body)

	require.Equal(t, "重发", input.Text)
}

func TestExtractContentModerationInput_OpenAIChatAgentToolLoopExtractsToolResult(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"system","content":"sys"},
			{"role":"user","content":"列出我的订单"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"orders","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"[]"}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIChat, body)

	require.Equal(t, "[]", input.Text)
	require.Empty(t, input.Images)
	require.Equal(t, ContentModerationSourceTool, input.Source)
}

func TestExtractContentModerationInput_OpenAIChatMultiTurnExtractsLatestUser(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"user","content":"Q1"},
			{"role":"assistant","content":"A1"},
			{"role":"user","content":"Q2"}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIChat, body)

	require.Equal(t, "Q2", input.Text)
}

func TestExtractContentModerationInput_GeminiAgentToolLoopExtractsFunctionResponse(t *testing.T) {
	body := []byte(`{
		"contents": [
			{"role":"user","parts":[{"text":"查询天气"}]},
			{"role":"model","parts":[{"functionCall":{"name":"weather","args":{}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"weather","response":{"temp":25}}}]}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolGemini, body)

	require.JSONEq(t, `{"temp":25}`, input.Text)
	require.Empty(t, input.Images)
	require.Equal(t, ContentModerationSourceTool, input.Source)
}

func TestExtractContentModerationInput_GeminiFirstTurnExtractsUser(t *testing.T) {
	body := []byte(`{
		"contents": [
			{"role":"user","parts":[{"text":"你好"}]}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolGemini, body)

	require.Equal(t, "你好", input.Text)
}

func TestExtractContentModerationInput_GeminiMultiTurnExtractsLatestUser(t *testing.T) {
	body := []byte(`{
		"contents": [
			{"role":"user","parts":[{"text":"Q1"}]},
			{"role":"model","parts":[{"text":"A1"}]},
			{"role":"user","parts":[{"text":"Q2"}]}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolGemini, body)

	require.Equal(t, "Q2", input.Text)
}

func TestExtractContentModerationInput_ResponsesAgentToolLoopExtractsToolOutput(t *testing.T) {
	body := []byte(`{
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"运行测试"}]},
			{"type":"function_call","call_id":"call_1","name":"run_tests","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"all passed"}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIResponses, body)

	require.Equal(t, "all passed", input.Text)
	require.Empty(t, input.Images)
	require.Equal(t, ContentModerationSourceTool, input.Source)
}

func TestExtractContentModerationPromptExcerpt_ResponsesUsesCurrentToolOutput(t *testing.T) {
	body := []byte(`{
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"检查这段高风险请求 sk-proj-1234567890abcdef"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"我先看一下"}]},
			{"type":"function_call","call_id":"call_1","name":"run_tests","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"all passed"}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIResponses, body)
	excerpt := ExtractContentModerationPromptExcerptFromInput(ExtractContentModerationInput(ContentModerationProtocolOpenAIResponses, body))

	require.Equal(t, "all passed", input.Text)
	require.Equal(t, "all passed", excerpt)
}

func TestExtractContentModerationInput_ResponsesLastUserMessageExtracted(t *testing.T) {
	body := []byte(`{
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"latest"}]}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIResponses, body)

	require.Equal(t, "latest", input.Text)
}

func TestExtractContentModerationInput_ResponsesLastIsAssistantSkipped(t *testing.T) {
	body := []byte(`{
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"q1"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a1"}]}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIResponses, body)

	require.Empty(t, input.Text)
	require.Empty(t, input.Images)
}

func TestExtractContentModerationInput_ChatCollectsParallelToolResultsAndDeduplicatesImages(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"user","content":"old request"},
			{"role":"assistant","tool_calls":[{"id":"one"},{"id":"two"}]},
			{"role":"tool","tool_call_id":"one","content":{"result":"first","image_url":"https://example.com/result.png"}},
			{"role":"tool","tool_call_id":"two","content":[{"type":"text","text":"second"},{"type":"image_url","image_url":{"url":"https://example.com/result.png"}}]}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIChat, body)

	require.Contains(t, input.Text, `"result":"first"`)
	require.Contains(t, input.Text, `"text":"second"`)
	require.NotContains(t, input.Text, "old request")
	require.Equal(t, []string{"https://example.com/result.png"}, input.Images)
	require.Equal(t, ContentModerationSourceTool, input.Source)
}

func TestExtractContentModerationInput_ResponsesCollectsAllSupportedToolOutputs(t *testing.T) {
	body := []byte(`{
		"input":[
			{"type":"function_call","call_id":"call_1","name":"run","arguments":"{}"},
			{"type":"function_call_output","output":{"one":1}},
			{"type":"custom_tool_call_output","output":"two"},
			{"type":"mcp_tool_call_output","output":"three"},
			{"type":"tool_search_output","output":"four"},
			{"type":"computer_call_output","output":"five"}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIResponses, body)

	require.Contains(t, input.Text, `"one":1`)
	for _, value := range []string{"two", "three", "four", "five"} {
		require.Contains(t, input.Text, value)
	}
	require.Equal(t, ContentModerationSourceTool, input.Source)
}

func TestExtractContentModerationInput_AnthropicKeepsSystemRemindersAndEphemeralUserText(t *testing.T) {
	body := []byte(`{
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "<system-reminder>工具说明</system-reminder>"},
					{"type": "text", "text": "<system-reminder>Ainder>\n\n"},
					{"type": "text", "text": "hid", "cache_control": {"type": "ephemeral"}}
				]
			}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolAnthropicMessages, body)

	require.Contains(t, input.Text, "<system-reminder>工具说明</system-reminder>")
	require.Contains(t, input.Text, "<system-reminder>Ainder>")
	require.Contains(t, input.Text, "hid")
	require.Empty(t, input.Images)
}

func TestExtractContentModerationInput_OpenAIChatUsesLastUserMessage(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.5",
		"messages":[
			{"role":"system","content":"system prompt"},
			{"role":"user","content":"old user"},
			{"role":"assistant","content":"ok"},
			{"role":"user","content":[{"type":"text","text":"latest user"},{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIChat, body)

	require.Equal(t, "latest user", input.Text)
	require.Equal(t, []string{"https://example.com/a.png"}, input.Images)
	require.NotContains(t, input.Text, "old user")
	require.NotContains(t, input.Text, "system prompt")
}

func TestExtractContentModerationInput_OpenAIImagesIncludesPromptAndImages(t *testing.T) {
	body := []byte(`{
		"prompt":"replace background",
		"images":[
			{"image_url":"https://example.com/source.png"},
			{"image_url":"data:image/png;base64,aGVsbG8="}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIImages, body)

	require.Equal(t, "replace background", input.Text)
	require.Equal(t, []string{"https://example.com/source.png", "data:image/png;base64,aGVsbG8="}, input.Images)
}

func TestExtractContentModerationInput_OpenAIResponsesCodexPayloadUsesLastUserMessage(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.5",
		"instructions":"instructions.....",
		"input":[
			{"type":"message","role":"developer","content":[{"type":"input_text","text":"developer permissions sk-proj-1234567890abcdef"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"first user prompt"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"last user prompt"}]}
		],
		"prompt_cache_key":"cache-key"
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIResponses, body)

	require.Equal(t, "first user prompt\nlast user prompt", input.Text)
	require.Empty(t, input.Images)
	require.NotContains(t, input.Text, "developer permissions")
}
