package anthropic

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestBuildDynamicToolMap_BelowThreshold(t *testing.T) {
	// At most five names use static rewriting only.
	names := []string{"bash", "edit", "read", "write", "search"}
	require.Nil(t, BuildDynamicToolMap(names))
}

func TestBuildDynamicToolMap_AboveThresholdIsStable(t *testing.T) {
	// An identical ordered name list must produce identical aliases for caching.
	names := []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta"}
	a := BuildDynamicToolMap(names)
	b := BuildDynamicToolMap(names)
	require.NotNil(t, a)
	require.Equal(t, a, b, "same input tool_names must yield identical mapping")
	require.Len(t, a, 6)
	for _, name := range names {
		require.Contains(t, a, name)
		require.NotEqual(t, name, a[name])
	}
}

func TestSanitizeToolName_StaticPrefix(t *testing.T) {
	require.Equal(t, "cc_sess_list", SanitizeToolName("sessions_list", nil))
	require.Equal(t, "cc_ses_get", SanitizeToolName("session_get", nil))
	require.Equal(t, "bash", SanitizeToolName("bash", nil))
}

func TestSanitizeToolName_DynamicTakesPrecedence(t *testing.T) {
	dyn := map[string]string{"sessions_list": "analyze_ses00"}
	got := SanitizeToolName("sessions_list", dyn)
	require.Equal(t, "analyze_ses00", got, "dynamic mapping wins over static prefix")
}

func TestRestoreToolNamesInBytes_LongestFirst(t *testing.T) {
	// Replace the longer alias first when one alias contains another.
	rw := &ToolNameRewrite{
		Forward: map[string]string{"foo": "abc_12", "bar": "abc_12_ext"},
		Reverse: map[string]string{"abc_12": "foo", "abc_12_ext": "bar"},
	}
	// Construct the longest-first replacement order.
	rw.ReverseOrdered = [][2]string{
		{"abc_12_ext", "bar"},
		{"abc_12", "foo"},
	}
	data := []byte(`{"tool":"abc_12_ext","other":"abc_12"}`)
	restored := string(RestoreToolNamesInBytes(data, rw))
	require.Equal(t, `{"tool":"bar","other":"foo"}`, restored)
}

func TestRestoreToolNamesInBytes_StaticPrefixRollback(t *testing.T) {
	data := []byte(`{"name":"sessions_list","id":"cc_ses_xyz"}`)
	got := string(RestoreToolNamesInBytes(data, nil))
	require.Equal(t, `{"name":"sessions_list","id":"session_xyz"}`, got)
}

func TestApplyToolNameRewriteToBody_RenamesToolsAndToolChoice(t *testing.T) {
	body := []byte(`{"tools":[{"name":"sessions_list","input_schema":{}},{"name":"session_get","input_schema":{}},{"name":"web_search","type":"web_search_20250305"}],"tool_choice":{"type":"tool","name":"sessions_list"}}`)
	rw := BuildToolNameRewriteFromBody(body)
	require.NotNil(t, rw)
	require.Contains(t, rw.Forward, "sessions_list")
	require.Contains(t, rw.Forward, "session_get")
	// Server tool names remain unchanged.
	require.NotContains(t, rw.Forward, "web_search")

	out := ApplyToolNameRewriteToBody(body, rw)

	// Rewrite the two eligible names while retaining the server tool name.
	require.Equal(t, "cc_sess_list", gjson.GetBytes(out, "tools.0.name").String())
	require.Equal(t, "cc_ses_get", gjson.GetBytes(out, "tools.1.name").String())
	require.Equal(t, "web_search", gjson.GetBytes(out, "tools.2.name").String())

	// The named tool choice follows the declaration alias.
	require.Equal(t, "cc_sess_list", gjson.GetBytes(out, "tool_choice.name").String())
	require.Equal(t, "tool", gjson.GetBytes(out, "tool_choice.type").String())
}

func TestApplyToolNameRewriteToBody_RenamesToolUseInMessages(t *testing.T) {
	// sessions_list uses the static cc_sess_ prefix.
	// web_search is a server tool and retains its name.
	// Historical tool calls must match the rewritten declarations.
	body := []byte(`{"tools":[{"name":"sessions_list","input_schema":{}},{"name":"web_search","type":"web_search_20250305"}],"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]},{"role":"assistant","content":[{"type":"tool_use","id":"tu_01","name":"sessions_list","input":{}},{"type":"text","text":"thinking"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_01","content":"ok"}]}]}`)
	rw := BuildToolNameRewriteFromBody(body)
	require.NotNil(t, rw)
	require.Equal(t, "cc_sess_list", rw.Forward["sessions_list"])

	out := ApplyToolNameRewriteToBody(body, rw)

	// The eligible declaration receives an alias.
	require.Equal(t, "cc_sess_list", gjson.GetBytes(out, "tools.0.name").String())
	// The server tool keeps its name.
	require.Equal(t, "web_search", gjson.GetBytes(out, "tools.1.name").String())
	// The historical tool_use receives the declaration alias.
	require.Equal(t, "cc_sess_list", gjson.GetBytes(out, "messages.1.content.0.name").String())
	// Text blocks remain unchanged.
	require.Equal(t, "thinking", gjson.GetBytes(out, "messages.1.content.1.text").String())
	// Tool results must not gain a name field.
	require.Equal(t, "ok", gjson.GetBytes(out, "messages.2.content.0.content").String())
}

func TestApplyToolsLastCacheBreakpoint_InjectsDefault(t *testing.T) {
	body := []byte(`{"tools":[{"name":"a","input_schema":{}},{"name":"b","input_schema":{}}]}`)
	out := ApplyToolsLastCacheBreakpoint(body)
	require.Equal(t, "ephemeral", gjson.GetBytes(out, "tools.1.cache_control.type").String())
	require.Equal(t, "5m", gjson.GetBytes(out, "tools.1.cache_control.ttl").String())
	// First tool untouched
	require.False(t, gjson.GetBytes(out, "tools.0.cache_control").Exists())
}

func TestApplyToolsLastCacheBreakpoint_PassesThroughClientTTL(t *testing.T) {
	body := []byte(`{"tools":[{"name":"a","input_schema":{},"cache_control":{"type":"ephemeral","ttl":"1h"}}]}`)
	out := ApplyToolsLastCacheBreakpoint(body)
	// User-provided ttl must be preserved.
	require.Equal(t, "1h", gjson.GetBytes(out, "tools.0.cache_control.ttl").String())
}

func TestApplyToolsLastCacheBreakpoint_StripsDeferredToolCacheControl(t *testing.T) {
	body := []byte(`{"tools":[{"name":"custom","custom":{"defer_loading":true},"cache_control":{"type":"ephemeral","ttl":"1h"}},{"name":"top","defer_loading":true},{"name":"ordinary","defer_loading":false,"cache_control":{"type":"ephemeral"}}]}`)
	out := ApplyToolsLastCacheBreakpoint(body)
	require.False(t, gjson.GetBytes(out, "tools.0.cache_control").Exists())
	require.False(t, gjson.GetBytes(out, "tools.1.cache_control").Exists())
	require.Equal(t, "ephemeral", gjson.GetBytes(out, "tools.2.cache_control.type").String())
}

func TestApplyToolsLastCacheBreakpointOnlyLiteralTrueIsDeferred(t *testing.T) {
	body := []byte(`{"tools":[{"name":"string","defer_loading":"true","cache_control":{"type":"ephemeral"}},{"name":"number","defer_loading":1,"cache_control":{"type":"ephemeral"}},{"name":"object","defer_loading":{},"cache_control":{"type":"ephemeral"}}]}`)
	out := StripDeferredToolCacheControl(body)
	for idx := range 3 {
		require.Equal(t, "ephemeral", gjson.GetBytes(out, fmt.Sprintf("tools.%d.cache_control.type", idx)).String())
	}
}

func TestBuildToolNameRewriteFromBody_ReverseOrderedByLengthDesc(t *testing.T) {
	// Above the threshold, dynamic aliases are restored longest-first.
	body := []byte(`{"tools":[
        {"name":"t1","input_schema":{}},
        {"name":"t2","input_schema":{}},
        {"name":"t3","input_schema":{}},
        {"name":"t4","input_schema":{}},
        {"name":"t5","input_schema":{}},
        {"name":"t6","input_schema":{}}
    ]}`)
	rw := BuildToolNameRewriteFromBody(body)
	require.NotNil(t, rw)
	require.NotEmpty(t, rw.ReverseOrdered)
	for i := 1; i < len(rw.ReverseOrdered); i++ {
		require.GreaterOrEqual(t, len(rw.ReverseOrdered[i-1][0]), len(rw.ReverseOrdered[i][0]),
			"ReverseOrdered must be sorted by fake-name length descending")
	}
}

func TestRestoreToolNamesInBytes_NoMapping_NoStaticMatch_IsNoop(t *testing.T) {
	data := []byte("plain text without any tool names")
	require.Equal(t, string(data), string(RestoreToolNamesInBytes(data, nil)))
}

// Ensure the fake name format follows Parrot's "{prefix}{name[:3]}{i:02d}".

func TestBuildDynamicToolMap_FakeNameShape(t *testing.T) {
	names := []string{"alphabet", "bravo", "charlie", "delta", "echo", "foxtrot"}
	m := BuildDynamicToolMap(names)
	require.NotNil(t, m)
	for _, name := range names {
		fake, ok := m[name]
		require.True(t, ok)
		// fake = prefix + head3 + "%02d"
		// ends with two decimal digits
		require.Regexp(t, `^[a-z]+_[a-z0-9]{1,3}\d{2}$`, fake)
		head := name
		if len(head) > 3 {
			head = head[:3]
		}
		require.True(t, strings.Contains(fake, head), "fake %q should contain head3 %q of %q", fake, head, name)
	}
}
