package provider

import (
	"testing"

	"github.com/stretchr/testify/require"

	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// TestModelIdentityPreservesEffortSuffix 验证转发不修正拼写、不拆解档位、不替换未知型号。
func TestModelIdentityPreservesEffortSuffix(t *testing.T) {
	for _, model := range []string{"gpt-5.4-xhigh", "gpt-5.6-sol-max", "gpt-5.1-codex-max", "gpt5.6sol", "vendor/gpt-5.4", "claude-opus-5.5", "claude-opus-5-5", ""} {
		require.Equal(t, model, (ModelPolicy{}).NormalizeOpenAI(model))
	}
}

// TestExplicitMessagesEffortPreserved 检查客户端指定的档位是否按最终型号能力转换。
func TestExplicitMessagesEffortPreserved(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{Model: "client-alias", OutputConfig: &protocolanthropic.AnthropicOutputConfig{Effort: "max"}}
	require.Equal(t, "max", OpenAICompatAnthropicReasoningEffort(req, "gpt-5.6-sol", "xhigh"))
	require.Equal(t, "xhigh", OpenAICompatAnthropicReasoningEffort(nil, "gpt-5.4", "xhigh"))
}

// TestSparkIdentityIsExact 验证大小写比较不开放后缀或供应商别名。
func TestSparkIdentityIsExact(t *testing.T) {
	require.True(t, IsCodexSparkModel(" GPT-5.3-CODEX-SPARK "))
	require.False(t, IsCodexSparkModel("gpt-5.3-codex-spark-high"))
	require.False(t, IsCodexSparkModel("vendor/gpt-5.3-codex-spark"))
}
