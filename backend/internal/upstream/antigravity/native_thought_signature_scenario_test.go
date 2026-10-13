package antigravity

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	protocolgemini "github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
)

// signature_cleaner.go in protocol/gemini replaces nested thought signatures
// with the dummy value exposed by antigravity/request_transformer.go. These
// fixtures check contents and cached parts alongside the unrelated signature
// field and invalid source bytes; they call the cleaner directly rather than
// executing the request transformer.

// TestCleanGeminiNativeThoughtSignatures_ReplacesNestedThoughtSignatures
// supplies thought signatures in text, function-call, and cached-content
// parts. Cleanup must remove the supplied values, emit the shared dummy
// signature, and retain the unrelated signature field.
func TestCleanGeminiNativeThoughtSignatures_ReplacesNestedThoughtSignatures(t *testing.T) {
	input := []byte(`{
		"contents": [
			{
				"role": "user",
				"parts": [{"text": "hello"}]
			},
			{
				"role": "model",
				"parts": [
					{"text": "thinking", "thought": true, "thoughtSignature": "sig_1"},
					{"functionCall": {"name": "toolA", "args": {"k": "v"}}, "thoughtSignature": "sig_2"}
				]
			}
		],
		"cachedContent": {
			"parts": [{"text": "cached", "thoughtSignature": "sig_3"}]
		},
		"signature": "keep_me"
	}`)

	cleaned := protocolgemini.CleanNativeThoughtSignatures(input, bridge.DummyThoughtSignature)

	var got map[string]any
	require.NoError(t, json.Unmarshal(cleaned, &got))

	require.NotContains(t, string(cleaned), `"thoughtSignature":"sig_1"`)
	require.NotContains(t, string(cleaned), `"thoughtSignature":"sig_2"`)
	require.NotContains(t, string(cleaned), `"thoughtSignature":"sig_3"`)
	require.Contains(t, string(cleaned), `"thoughtSignature":"`+DummyThoughtSignature+`"`)
	require.Contains(t, string(cleaned), `"signature":"keep_me"`)
}

// TestCleanGeminiNativeThoughtSignatures_InvalidJSONReturnsOriginal supplies
// malformed Gemini content to signature cleanup. The helper must return the
// exact source bytes, allowing the caller to handle invalid JSON separately.
func TestCleanGeminiNativeThoughtSignatures_InvalidJSONReturnsOriginal(t *testing.T) {
	input := []byte(`{"contents":[invalid-json]}`)

	cleaned := protocolgemini.CleanNativeThoughtSignatures(input, bridge.DummyThoughtSignature)

	require.Equal(t, input, cleaned)
}
