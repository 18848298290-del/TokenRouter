package bridge

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// TestApplyAnthropicCompatFullReplayGuard_TrimsOldMessages supplies fifteen
// ordinary messages to the twelve-message tail guard. Trimming must retain the
// asserted first and last fixture messages with the expected tail length.
func TestApplyAnthropicCompatFullReplayGuard_TrimsOldMessages(t *testing.T) {
	t.Parallel()

	req := &protocolanthropic.AnthropicRequest{Messages: make([]protocolanthropic.AnthropicMessage, 0, openAICompatAnthropicReplayMaxTailMessages+3)}
	for i := range openAICompatAnthropicReplayMaxTailMessages + 3 {
		req.Messages = append(req.Messages, protocolanthropic.AnthropicMessage{
			Role:    "user",
			Content: json.RawMessage(fmt.Sprintf(`"message-%02d"`, i)),
		})
	}

	trimmed := ApplyAnthropicCompatFullReplayGuard(req)

	require.True(t, trimmed)
	require.Len(t, req.Messages, openAICompatAnthropicReplayMaxTailMessages)
	require.JSONEq(t, `"message-03"`, string(req.Messages[0].Content))
	require.JSONEq(t, `"message-14"`, string(req.Messages[len(req.Messages)-1].Content))
}

// TestApplyAnthropicCompatFullReplayGuard_KeepsToolBoundaryIntact places a
// retained result after a call before the nominal tail. Expansion must keep the
// asserted call/result markers and assistant role while retaining fourteen messages.
func TestApplyAnthropicCompatFullReplayGuard_KeepsToolBoundaryIntact(t *testing.T) {
	t.Parallel()

	req := &protocolanthropic.AnthropicRequest{Messages: make([]protocolanthropic.AnthropicMessage, 0, openAICompatAnthropicReplayMaxTailMessages+3)}
	for i := range openAICompatAnthropicReplayMaxTailMessages + 3 {
		role := "user"
		content := json.RawMessage(fmt.Sprintf(`"message-%02d"`, i))
		if i == 1 {
			role = "assistant"
			content = json.RawMessage(`[{"type":"tool_use","id":"toolu_keep","name":"Read","input":{"file_path":"main.go"}}]`)
		}
		if i == 3 {
			content = json.RawMessage(`[{"type":"tool_result","tool_use_id":"toolu_keep","content":"ok"}]`)
		}
		req.Messages = append(req.Messages, protocolanthropic.AnthropicMessage{
			Role:    role,
			Content: content,
		})
	}

	trimmed := ApplyAnthropicCompatFullReplayGuard(req)

	require.True(t, trimmed)
	require.Len(t, req.Messages, openAICompatAnthropicReplayMaxTailMessages+2)
	require.Equal(t, "assistant", req.Messages[0].Role)
	require.Contains(t, string(req.Messages[0].Content), `"toolu_keep"`)
	require.Contains(t, string(req.Messages[2].Content), `"tool_result"`)
}
