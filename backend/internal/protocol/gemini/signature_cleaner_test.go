package gemini

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReplaceThoughtSignaturesRecursive_OnlyReplacesTargetField supplies
// replay and generic signature fields at the root and in nested array objects.
// Replacement must target thoughtSignature at both levels while retaining
// the neighboring signature values.
func TestReplaceThoughtSignaturesRecursive_OnlyReplacesTargetField(t *testing.T) {
	input := map[string]any{
		"thoughtSignature": "sig_root",
		"signature":        "keep_signature",
		"nested": []any{
			map[string]any{
				"thoughtSignature": "sig_nested",
				"signature":        "keep_nested_signature",
			},
		},
	}

	got, ok := replaceThoughtSignaturesRecursive(input, "skip_thought_signature_validator").(map[string]any)
	require.True(t, ok)
	require.Equal(t, "skip_thought_signature_validator", got["thoughtSignature"])
	require.Equal(t, "keep_signature", got["signature"])

	nested, ok := got["nested"].([]any)
	require.True(t, ok)
	nestedMap, ok := nested[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "skip_thought_signature_validator", nestedMap["thoughtSignature"])
	require.Equal(t, "keep_nested_signature", nestedMap["signature"])
}
