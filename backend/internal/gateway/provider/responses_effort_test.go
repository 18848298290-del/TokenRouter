package provider

import (
	"testing"

	"github.com/stretchr/testify/require"

	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// TestModelIdentityPreservesEffortSuffix passes version spellings, effort
// suffixes, vendor prefixes, and an empty ID through model normalization. For
// these provider-free inputs, the returned model text remains unchanged.
func TestModelIdentityPreservesEffortSuffix(t *testing.T) {
	for _, model := range []string{"gpt-5.4-xhigh", "gpt-5.6-sol-max", "gpt-5.1-codex-max", "gpt5.6sol", "vendor/gpt-5.4", "claude-opus-5.5", "claude-opus-5-5", ""} {
		require.Equal(t, model, (ModelPolicy{}).NormalizeOpenAI(model))
	}
}

// TestExplicitMessagesEffortPreserved requests max effort with no final-model
// attribute information, then supplies a nil request. Explicit max survives
// unknown capability data, while nil requests retain the already converted
// effort.
func TestExplicitMessagesEffortPreserved(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{Model: "client-alias", OutputConfig: &protocolanthropic.AnthropicOutputConfig{Effort: "max"}}
	require.Equal(t, "max", OpenAICompatAnthropicReasoningEffort(req, "gpt-5.6-sol", "xhigh"))
	require.Equal(t, "xhigh", OpenAICompatAnthropicReasoningEffort(nil, "gpt-5.4", "xhigh"))
}

// TestSparkIdentityIsExact supplies a padded uppercase Spark identifier
// alongside effort-suffixed and vendor-prefixed variants. Exact normalized
// identity matches, while the additional suffix and prefix prevent
// recognition.
func TestSparkIdentityIsExact(t *testing.T) {
	require.True(t, IsCodexSparkModel(" GPT-5.3-CODEX-SPARK "))
	require.False(t, IsCodexSparkModel("gpt-5.3-codex-spark-high"))
	require.False(t, IsCodexSparkModel("vendor/gpt-5.3-codex-spark"))
}
