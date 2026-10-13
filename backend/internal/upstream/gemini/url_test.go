package gemini

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBuildGeminiAIStudioModelActionURL builds both generation actions and
// token counting with a common API base. It checks exact resource paths,
// trimming of model/base text, and the streaming SSE query.
func TestBuildGeminiAIStudioModelActionURL(t *testing.T) {
	const base = "https://generativelanguage.googleapis.com"

	got, err := BuildGeminiAIStudioModelActionURL(base, "gemini-2.5-pro", "generateContent", false)
	require.NoError(t, err)
	require.Equal(t, base+"/v1beta/models/gemini-2.5-pro:generateContent", got)

	got, err = BuildGeminiAIStudioModelActionURL(base+"/", " gemini-2.5-flash ", "streamGenerateContent", true)
	require.NoError(t, err)
	require.Equal(t, base+"/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", got)

	got, err = BuildGeminiAIStudioModelActionURL(base, "gemini-2.5-pro", "countTokens", false)
	require.NoError(t, err)
	require.Equal(t, base+"/v1beta/models/gemini-2.5-pro:countTokens", got)
}

// TestBuildGeminiAIStudioModelActionURLRejectsNonConformingModel supplies
// navigation, encoding, delimiter, whitespace, and unsupported model
// spellings. It checks rejection by both helpers and also rejects an
// unsupported action or empty base URL.
func TestBuildGeminiAIStudioModelActionURLRejectsNonConformingModel(t *testing.T) {
	const base = "https://generativelanguage.googleapis.com"

	for _, model := range []string{
		"../../x/y",
		"..",
		".",
		"gemini-2.5-pro/../../x",
		`..\..\x`,
		"gemini-2.5-pro?a=b",
		"gemini-2.5-pro#frag",
		"gemini-2.5-pro%2f..",
		"gemini 2.5 pro",
		"gemini\x00pro",
		"gemini-2.5-pro@001",
		"gemini~pro",
		"models/gemini-2.5-pro",
		"...",
		"",
		"   ",
	} {
		t.Run("model_"+model, func(t *testing.T) {
			_, err := BuildGeminiAIStudioModelActionURL(base, model, "generateContent", false)
			require.Error(t, err, "model %q must be rejected", model)
			require.False(t, IsSafeGeminiModelPathSegment(model))
		})
	}

	// Reject actions outside the supported operation set.
	_, err := BuildGeminiAIStudioModelActionURL(base, "gemini-2.5-pro", "deleteModel", false)
	require.Error(t, err)

	_, err = BuildGeminiAIStudioModelActionURL("", "gemini-2.5-pro", "generateContent", false)
	require.Error(t, err)
}
