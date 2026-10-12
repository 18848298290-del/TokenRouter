package provider

import (
	"testing"

	"github.com/stretchr/testify/require"

	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// TestModelIdentityPreservesEffortSuffix checks that spelling, suffixes, and unknown model IDs pass through unchanged.
func TestModelIdentityPreservesEffortSuffix(t *testing.T) {
	for _, model := range []string{"gpt-5.4-xhigh", "gpt-5.6-sol-max", "gpt-5.1-codex-max", "gpt5.6sol", "vendor/gpt-5.4", "claude-opus-5.5", "claude-opus-5-5", ""} {
		require.Equal(t, model, (ModelPolicy{}).NormalizeOpenAI(model))
	}
}

// TestExplicitMessagesEffortPreserved checks explicit effort handling when final-model attributes are unavailable.
func TestExplicitMessagesEffortPreserved(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{Model: "client-alias", OutputConfig: &protocolanthropic.AnthropicOutputConfig{Effort: "max"}}
	require.Equal(t, "max", OpenAICompatAnthropicReasoningEffort(req, "gpt-5.6-sol", "xhigh"))
	require.Equal(t, "xhigh", OpenAICompatAnthropicReasoningEffort(nil, "gpt-5.4", "xhigh"))
}

// TestSparkIdentityIsExact checks case-insensitive identity while rejecting suffixes and vendor prefixes.
func TestSparkIdentityIsExact(t *testing.T) {
	require.True(t, IsCodexSparkModel(" GPT-5.3-CODEX-SPARK "))
	require.False(t, IsCodexSparkModel("gpt-5.3-codex-spark-high"))
	require.False(t, IsCodexSparkModel("vendor/gpt-5.3-codex-spark"))
}
