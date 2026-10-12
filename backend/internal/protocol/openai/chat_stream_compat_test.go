package openai

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestStripEmptyChatToolCallIdentity_FirstChunkIdentityUntouched supplies the
// first delta with a nonempty ID and name. Bytes, identities, and the present
// empty argument string remain unchanged.
func TestStripEmptyChatToolCallIdentity_FirstChunkIdentityUntouched(t *testing.T) {
	payload := []byte(`{"id":"chatcmpl_tool","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_example","type":"function","function":{"name":"web_search","arguments":""}}]}}]}`)
	rewritten, changed := StripEmptyChatToolCallIdentity(payload)
	require.False(t, changed)
	require.Equal(t, string(payload), string(rewritten))
	require.Equal(t, "call_example", gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.0.id").String())
	require.Equal(t, "web_search", gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.0.function.name").String())
	// An empty arguments string is still part of the first tool delta.
	require.True(t, gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.0.function.arguments").Exists())
	require.Equal(t, "", gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.0.function.arguments").String())
}

// TestStripEmptyChatToolCallIdentity_FollowingDelta supplies empty identities
// beside an argument fragment. Cleanup removes both keys while retaining the
// fragment, call index, and function type.
func TestStripEmptyChatToolCallIdentity_FollowingDelta(t *testing.T) {
	payload := []byte(`{"id":"chatcmpl_tool","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"","type":"function","function":{"name":"","arguments":"{\"query\":"}}]}}]}`)
	rewritten, changed := StripEmptyChatToolCallIdentity(payload)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.0.id").Exists())
	require.False(t, gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.0.function.name").Exists())
	require.Equal(t, `{"query":`, gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.0.function.arguments").String())
	require.Equal(t, int64(0), gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.0.index").Int())
	require.Equal(t, "function", gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.0.type").String())
	require.NotContains(t, string(rewritten), `"id":""`)
	require.NotContains(t, string(rewritten), `"name":""`)
}

// TestStripEmptyChatToolCallIdentity_OnlyEmptyName supplies a valid ID and an
// empty function name. Cleanup removes the name key while keeping the ID and
// exact argument text.
func TestStripEmptyChatToolCallIdentity_OnlyEmptyName(t *testing.T) {
	payload := []byte(`{"id":"chatcmpl_tool","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"","arguments":"{}"}}]}}]}`)
	rewritten, changed := StripEmptyChatToolCallIdentity(payload)
	require.True(t, changed)
	require.Equal(t, "call_1", gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.0.id").String())
	require.False(t, gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.0.function.name").Exists())
	require.Equal(t, "{}", gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.0.function.arguments").String())
}

// TestStripEmptyChatToolCallIdentity_OnlyEmptyID includes whitespace around an
// empty ID key/value. Cleanup removes that ID and retains the nonempty
// function name and its argument text.
func TestStripEmptyChatToolCallIdentity_OnlyEmptyID(t *testing.T) {
	payload := []byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id" : "" , "type" : "function","function":{"name":"get_weather","arguments":"{}"}}]}}]}`)
	rewritten, changed := StripEmptyChatToolCallIdentity(payload)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.0.id").Exists())
	require.Equal(t, "get_weather", gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.0.function.name").String())
	require.Equal(t, "{}", gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.0.function.arguments").String())
}

// TestStripEmptyChatToolCallIdentity_EmptyArgumentsKept supplies empty
// identities and empty arguments in the same delta. Identity keys disappear,
// while the argument key remains with its empty string.
func TestStripEmptyChatToolCallIdentity_EmptyArgumentsKept(t *testing.T) {
	payload := []byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"","type":"function","function":{"name":"","arguments":""}}]}}]}`)
	rewritten, changed := StripEmptyChatToolCallIdentity(payload)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.0.id").Exists())
	require.False(t, gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.0.function.name").Exists())
	require.True(t, gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.0.function.arguments").Exists())
	require.Equal(t, "", gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.0.function.arguments").String())
}

// TestStripEmptyChatToolCallIdentity_TwoParallelToolCalls mixes a named call
// with an empty-identity call in one array. Cleanup keeps the first identity
// and both argument fragments while removing the second identity.
func TestStripEmptyChatToolCallIdentity_TwoParallelToolCalls(t *testing.T) {
	payload := []byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"tool_a","arguments":"{\"x\":"}},{"index":1,"id":"","type":"function","function":{"name":"","arguments":"{\"y\":"}}]}}]}`)
	rewritten, changed := StripEmptyChatToolCallIdentity(payload)
	require.True(t, changed)
	require.Equal(t, "call_a", gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.0.id").String())
	require.Equal(t, "tool_a", gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.0.function.name").String())
	require.Equal(t, `{"x":`, gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.0.function.arguments").String())
	require.False(t, gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.1.id").Exists())
	require.False(t, gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.1.function.name").Exists())
	require.Equal(t, `{"y":`, gjson.GetBytes(rewritten, "choices.0.delta.tool_calls.1.function.arguments").String())
}

// TestStripEmptyChatToolCallIdentity_Passthrough supplies unrelated,
// malformed, empty, and unsupported call-array fixtures. Every case returns
// equal source text and reports that no cleanup occurred.
func TestStripEmptyChatToolCallIdentity_Passthrough(t *testing.T) {
	tests := []struct {
		name    string // stream cleanup fixture label
		payload string // raw Chat chunk text
	}{
		{"no tool_calls", `{"choices":[{"index":0,"delta":{"content":"hi"}}]}`},
		{"no choices", `{"id":"chatcmpl_x"}`},
		{"empty choices", `{"choices":[]}`},
		{"tool_calls not array", `{"choices":[{"index":0,"delta":{"tool_calls":{"foo":1}}}]}`},
		{"invalid JSON", `{"choices":[{`},
		{"empty string", ``},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rewritten, changed := StripEmptyChatToolCallIdentity([]byte(tt.payload))
			require.False(t, changed)
			require.Equal(t, tt.payload, string(rewritten))
		})
	}
}

// TestStripEmptyChatToolCallIdentityFromSSELine_Passthrough supplies the done
// sentinel, comment/blank lines, and an ordinary content chunk. Each line
// remains exactly equal after delegated cleanup.
func TestStripEmptyChatToolCallIdentityFromSSELine_Passthrough(t *testing.T) {
	tests := []struct {
		name string // stream cleanup fixture label
		line string // raw SSE line fixture
	}{
		{"done", "data: [DONE]"},
		{"non-data line", ": keep-alive"},
		{"empty line", ""},
		{"comment line", ":"},
		{"content chunk", `data: {"choices":[{"index":0,"delta":{"content":"hi"}}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.line, StripEmptyChatToolCallIdentityFromSSELine(tt.line))
		})
	}
}

// TestStripEmptyChatToolCallIdentityFromSSELine_KeepsDataPrefix wraps an
// empty-identity tool delta in a data line. The prefix remains, identity keys
// disappear, and the argument string stays readable.
func TestStripEmptyChatToolCallIdentityFromSSELine_KeepsDataPrefix(t *testing.T) {
	line := `data: {"id":"chatcmpl_tool","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"","type":"function","function":{"name":"","arguments":"{}"}}]}}]}`
	got := StripEmptyChatToolCallIdentityFromSSELine(line)
	require.True(t, strings.HasPrefix(got, "data: "))
	payload, ok := ExtractSSEDataLine(got)
	require.True(t, ok)
	require.False(t, gjson.Get(payload, "choices.0.delta.tool_calls.0.id").Exists())
	require.False(t, gjson.Get(payload, "choices.0.delta.tool_calls.0.function.name").Exists())
	require.Equal(t, "{}", gjson.Get(payload, "choices.0.delta.tool_calls.0.function.arguments").String())
}

// TestStripEmptyChatToolCallIdentity_DshClientMerge models a client that
// overwrites present identity fields and appends argument fragments. Sanitized
// deltas keep the initial ID/name and produce the joined JSON argument text.
func TestStripEmptyChatToolCallIdentity_DshClientMerge(t *testing.T) {
	lines := []string{
		`data: {"id":"chatcmpl_tool","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_example","type":"function","function":{"name":"web_search","arguments":""}}]}}]}`,
		`data: {"id":"chatcmpl_tool","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"","type":"function","function":{"name":"","arguments":"{\"query\":"}}]}}]}`,
		`data: {"id":"chatcmpl_tool","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"","type":"function","function":{"name":"","arguments":"\"example\"}"}}]}}]}`,
	}

	// Overwrite identity only when its key exists, while appending arguments.
	// Removed empty keys must therefore leave the initial call identity available.
	var mergedID, mergedName, mergedArgs string
	for _, line := range lines {
		sanitized := StripEmptyChatToolCallIdentityFromSSELine(line)
		payload, ok := ExtractSSEDataLine(sanitized)
		require.True(t, ok)
		for _, tc := range gjson.Get(payload, "choices.0.delta.tool_calls").Array() {
			if v := tc.Get("id"); v.Exists() {
				mergedID = v.String()
			}
			if v := tc.Get("function.name"); v.Exists() {
				mergedName = v.String()
			}
			if v := tc.Get("function.arguments"); v.Exists() {
				mergedArgs += v.String()
			}
		}
	}

	require.Equal(t, "call_example", mergedID)
	require.Equal(t, "web_search", mergedName)
	require.Equal(t, `{"query":"example"}`, mergedArgs)
}

// TestOpenAIRawStreamTerminalState combines terminal signals, null fields,
// missing data, and client-output flags in fresh states. It checks accumulated
// termination evidence and the corresponding truncation result for each
// fixture.
func TestOpenAIRawStreamTerminalState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string   // stream cleanup fixture label
		payloads      []string // ordered observed data payloads
		clientStarted bool     // client output already started
		// wantTerminated and wantTruncated describe different observations from
		// the same payload sequence. The latter also includes clientStarted, so
		// a started non-SSE fixture can be unterminated without being truncated.
		wantTerminated bool // expected terminal evidence flag
		wantTruncated  bool // expected stream truncation result
	}{
		{
			name:           "done sentinel",
			payloads:       []string{`{"choices":[{"delta":{"content":"a"}}]}`, "[DONE]"},
			clientStarted:  true,
			wantTerminated: true,
		},
		{
			name:           "usage chunk",
			payloads:       []string{`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`},
			clientStarted:  true,
			wantTerminated: true,
		},
		{
			name:           "finish reason",
			payloads:       []string{`{"choices":[{"delta":{},"finish_reason":"length"}]}`},
			clientStarted:  true,
			wantTerminated: true,
		},
		{
			name:          "null finish reason is not terminal",
			payloads:      []string{`{"choices":[{"delta":{"content":"a"},"finish_reason":null}]}`},
			clientStarted: true,
			wantTruncated: true,
		},
		{
			name:          "usage null is not terminal",
			payloads:      []string{`{"choices":[{"delta":{"content":"a"}}],"usage":null}`},
			clientStarted: true,
			wantTruncated: true,
		},
		{
			// Model a raw body already delivered with no SSE data observations. The
			// client-started flag makes this distinct from an entirely empty read.
			name:          "non-sse body already forwarded",
			payloads:      nil,
			clientStarted: true,
		},
		{
			name:          "no bytes at all",
			payloads:      nil,
			clientStarted: false,
			wantTruncated: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var state RawStreamTerminalState
			for _, payload := range tt.payloads {
				state.ObserveDataLine(payload)
			}
			require.Equal(t, tt.wantTerminated, state.Terminated())
			require.Equal(t, tt.wantTruncated, state.IsTruncated(tt.clientStarted))
		})
	}
}

// TestIsOpenAIChatUsageOnlyStreamChunk compares usage with empty choices,
// nonempty choices, missing usage, and blank input. The predicate accepts the
// empty-choice usage fixture and rejects the other supplied cases.
func TestIsOpenAIChatUsageOnlyStreamChunk(t *testing.T) {
	t.Parallel()

	require.True(t, IsOpenAIChatUsageOnlyStreamChunk(`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2}}`))
	require.False(t, IsOpenAIChatUsageOnlyStreamChunk(`{"choices":[{"index":0}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`))
	require.False(t, IsOpenAIChatUsageOnlyStreamChunk(`{"choices":[]}`))
	require.False(t, IsOpenAIChatUsageOnlyStreamChunk(``))
}

// TestEnsureOpenAIChatStreamUsage supplies absent and explicitly false
// usage-stream flags. Both patched request bodies must decode with
// include_usage set to true and return no error.
func TestEnsureOpenAIChatStreamUsage(t *testing.T) {
	t.Parallel()

	body, err := EnsureOpenAIChatStreamUsage([]byte(`{"model":"gpt-5.4"}`))
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(body, "stream_options.include_usage").Bool())

	body, err = EnsureOpenAIChatStreamUsage([]byte(`{"model":"gpt-5.4","stream_options":{"include_usage":false}}`))
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(body, "stream_options.include_usage").Bool())
}
