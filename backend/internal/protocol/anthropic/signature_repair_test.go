package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStripSignatureSensitiveBlocksFromClaudeRequest supplies reasoning,
// a tool call, its result, and redacted history. The broader repair must disable
// thinking, turn readable blocks into text, and remove redacted reasoning.
func TestStripSignatureSensitiveBlocksFromClaudeRequest(t *testing.T) {
	req := &ClaudeRequest{
		Model: "claude-sonnet-4-5",

		Thinking: &ThinkingConfig{
			Type:         "enabled",
			BudgetTokens: 1024,
		},

		Messages: []ClaudeMessage{
			{
				Role: "assistant",

				Content: json.RawMessage(`[
					{"type":"thinking","thinking":"secret plan","signature":""},
					{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}
				]`),
			},

			{
				Role: "user",

				Content: json.RawMessage(`[
					{"type":"tool_result","tool_use_id":"t1","content":"ok","is_error":false},
					{"type":"redacted_thinking","data":"..."}
				]`),
			},
		},
	}

	changed, err := StripSignatureSensitiveBlocksFromClaudeRequest(req)
	require.NoError(t, err)
	require.True(t, changed)
	require.Nil(t, req.Thinking)

	require.Len(t, req.Messages, 2)

	var blocks0 []map[string]any
	require.NoError(t, json.Unmarshal(req.Messages[0].Content, &blocks0))
	require.Len(t, blocks0, 2)
	require.Equal(t, "text", blocks0[0]["type"])
	require.Equal(t, "secret plan", blocks0[0]["text"])
	require.Equal(t, "text", blocks0[1]["type"])

	var blocks1 []map[string]any
	require.NoError(t, json.Unmarshal(req.Messages[1].Content, &blocks1))
	require.Len(t, blocks1, 1)
	require.Equal(t, "text", blocks1[0]["type"])
	require.NotEmpty(t, blocks1[0]["text"])
}

// TestStripThinkingFromClaudeRequest_DoesNotDowngradeTools places reasoning
// and a tool call in one assistant message. The narrower repair must disable
// thinking and convert its text while retaining the structured tool call.
func TestStripThinkingFromClaudeRequest_DoesNotDowngradeTools(t *testing.T) {
	req := &ClaudeRequest{
		Model: "claude-sonnet-4-5",

		Thinking: &ThinkingConfig{
			Type:         "enabled",
			BudgetTokens: 1024,
		},

		Messages: []ClaudeMessage{
			{
				Role: "assistant",

				Content: json.RawMessage(`[{"type":"thinking","thinking":"secret plan"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]`),
			},
		},
	}

	changed, err := StripThinkingFromClaudeRequest(req)
	require.NoError(t, err)
	require.True(t, changed)
	require.Nil(t, req.Thinking)

	var blocks []map[string]any
	require.NoError(t, json.Unmarshal(req.Messages[0].Content, &blocks))
	require.Len(t, blocks, 2)
	require.Equal(t, "text", blocks[0]["type"])
	require.Equal(t, "secret plan", blocks[0]["text"])
	require.Equal(t, "tool_use", blocks[1]["type"])
}
