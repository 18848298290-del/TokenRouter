package grok

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Compact adaptation needs a non-streaming summary request and resumable
// encrypted state in its response. These examples inspect the synthetic prompt's
// selected sections, retained history and response metadata, then reject a
// plaintext-only result that cannot become the required compaction item.

// TestBuildGrokCompactRequestBodyUsesResponsesCompactionTurn supplies history
// and a function tool on a streaming request. It asserts
// non-streaming/no-storage settings, encrypted include, tool-choice suppression,
// retained history and selected fixed summary-prompt sections.
func TestBuildGrokCompactRequestBodyUsesResponsesCompactionTurn(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],"tools":[{"type":"function","name":"shell"}],"stream":true}`)

	patched, err := (BodyCodec{NewID: uuid.NewString}).BuildGrokCompactRequestBody(body)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(patched, "stream").Bool())
	require.False(t, gjson.GetBytes(patched, "store").Bool())
	require.Equal(t, "none", gjson.GetBytes(patched, "tool_choice").String())
	require.Equal(t, "reasoning.encrypted_content", gjson.GetBytes(patched, "include.0").String())
	require.Equal(t, "hello", gjson.GetBytes(patched, "input.0.content.0.text").String())
	prompt := gjson.GetBytes(patched, "input.1.content.0.text").String()
	require.Contains(t, prompt, "1. Primary Request and Intent")
	require.Contains(t, prompt, "9. Optional Next Step")
	require.Contains(t, prompt, "Respond with ONLY the <summary>...</summary> block")
	require.NotContains(t, prompt, "<summary_request>")
}

// TestConvertGrokResponseToOpenAICompact combines reasoning ciphertext, message
// summary and token usage in one response. Conversion must retain response ID
// and usage while returning one compaction item containing that ciphertext and
// summary.
func TestConvertGrokResponseToOpenAICompact(t *testing.T) {
	body := []byte(`{
		"id":"resp_grok_1",
		"object":"response",
		"status":"completed",
		"model":"grok-4.5",
		"output":[
			{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"grok-encrypted-state"},
			{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"summary text"}]}
		],
		"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14}
	}`)

	converted, err := (BodyCodec{NewID: uuid.NewString}).ConvertGrokResponseToOpenAICompact(body)
	require.NoError(t, err)
	require.Equal(t, "resp_grok_1", gjson.GetBytes(converted, "id").String())
	require.Len(t, gjson.GetBytes(converted, "output").Array(), 1)
	require.Equal(t, "compaction", gjson.GetBytes(converted, "output.0.type").String())
	require.Equal(t, "grok-encrypted-state", gjson.GetBytes(converted, "output.0.encrypted_content").String())
	require.Equal(t, "summary text", gjson.GetBytes(converted, "output.0.summary.0.text").String())
	require.Equal(t, int64(14), gjson.GetBytes(converted, "usage.total_tokens").Int())
}

// TestConvertGrokResponseToOpenAICompactRequiresEncryptedContent supplies a
// response containing summary text without reasoning ciphertext. Conversion must
// report the missing encrypted-content requirement instead of producing a
// compaction item from that plaintext-only response.
func TestConvertGrokResponseToOpenAICompactRequiresEncryptedContent(t *testing.T) {
	_, err := (BodyCodec{NewID: uuid.NewString}).ConvertGrokResponseToOpenAICompact([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"summary"}]}]}`))
	require.ErrorContains(t, err, "reasoning.encrypted_content")
}
