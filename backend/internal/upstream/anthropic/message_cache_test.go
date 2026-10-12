package anthropic

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestStripMessageCacheControl(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`)
	out := StripMessageCacheControl(body)
	require.False(t, gjson.GetBytes(out, "messages.0.content.0.cache_control").Exists())
}

func TestAddMessageCacheBreakpoints_LastMessageOnly(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	out := AddMessageCacheBreakpoints(body)
	require.Equal(t, "ephemeral", gjson.GetBytes(out, "messages.0.content.0.cache_control.type").String())
	require.Equal(t, "5m", gjson.GetBytes(out, "messages.0.content.0.cache_control.ttl").String())
}

func TestAddMessageCacheBreakpoints_SecondToLastUserTurn(t *testing.T) {
	// Four messages enable the second-to-last user breakpoint.
	body := []byte(`{"messages":[
        {"role":"user","content":[{"type":"text","text":"q1"}]},
        {"role":"assistant","content":[{"type":"text","text":"a1"}]},
        {"role":"user","content":[{"type":"text","text":"q2"}]},
        {"role":"assistant","content":[{"type":"text","text":"a2"}]}
    ]}`)
	out := AddMessageCacheBreakpoints(body)
	// The final assistant message receives a breakpoint.
	require.Equal(t, "ephemeral", gjson.GetBytes(out, "messages.3.content.0.cache_control.type").String())
	// The second-to-last user message is at index zero.
	require.Equal(t, "ephemeral", gjson.GetBytes(out, "messages.0.content.0.cache_control.type").String())
	// Intermediate messages have no breakpoint.
	require.False(t, gjson.GetBytes(out, "messages.1.content.0.cache_control").Exists())
	require.False(t, gjson.GetBytes(out, "messages.2.content.0.cache_control").Exists())
}

func TestAddMessageCacheBreakpoints_StringContentPromoted(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	out := AddMessageCacheBreakpoints(body)
	// String content becomes a text-block array.
	require.True(t, gjson.GetBytes(out, "messages.0.content").IsArray())
	require.Equal(t, "text", gjson.GetBytes(out, "messages.0.content.0.type").String())
	require.Equal(t, "hi", gjson.GetBytes(out, "messages.0.content.0.text").String())
	require.Equal(t, "5m", gjson.GetBytes(out, "messages.0.content.0.cache_control.ttl").String())
}
