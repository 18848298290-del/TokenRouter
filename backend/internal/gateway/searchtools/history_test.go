package searchtools

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	// These later-turn fixtures distinguish reserved gateway identities from
	// upstream-issued search blocks while retaining the same text-summary context.
	// emulatedWebSearchBody represents a later /v1/messages request with gateway-generated
	// server_tool_use, web_search_tool_result, and summary text in its history.
	emulatedWebSearchBody = `{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"search the weather"}]},` +
		`{"role":"assistant","content":[` +
		`{"type":"server_tool_use","id":"srvtoolu_ws_0123456789abcdef","name":"web_search","input":{"query":"weather"}},` +
		`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_ws_0123456789abcdef","content":[{"type":"web_search_result","url":"https://example.com","title":"Weather"}]},` +
		`{"type":"text","text":"Here are the search results for \"weather\":"}]},` +
		`{"role":"user","content":[{"type":"text","text":"thanks, continue"}]}]}`

	// genuineWebSearchBody contains Anthropic search blocks with upstream-issued IDs.
	genuineWebSearchBody = `{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"search"}]},` +
		`{"role":"assistant","content":[` +
		`{"type":"server_tool_use","id":"srvtoolu_01ABCDEF","name":"web_search","input":{"query":"weather"}},` +
		`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_01ABCDEF","content":[{"type":"web_search_result","url":"https://example.com","title":"Weather"}]},` +
		`{"type":"text","text":"summary with citations"}]}]}`
)

// collectContentTypes reads content block types from each message for ordered
// fixture assertions. It marks itself as a helper and returns only observed
// type spellings without validating message completeness.
func collectContentTypes(t *testing.T, body []byte) []string {
	t.Helper()
	var types []string
	for _, msg := range gjson.GetBytes(body, "messages").Array() {
		for _, block := range msg.Get("content").Array() {
			types = append(types, block.Get("type").String())
		}
	}
	return types
}

// TestFilterWebSearchHistoryBlocks_StripsEmulatedBlocksForAnthropicStrict
// supplies reserved-prefix call/results with stripAll false. Filtering must
// remove those synthetic identities while retaining summary text, ordinary
// messages and valid JSON.
func TestFilterWebSearchHistoryBlocks_StripsEmulatedBlocksForAnthropicStrict(t *testing.T) {
	out := FilterWebSearchHistoryBlocks([]byte(emulatedWebSearchBody), false)

	require.Equal(t, []string{"text", "text", "text"}, collectContentTypes(t, out))
	// The synthetic summary retains search context.
	require.Contains(t, string(out), "Here are the search results")
	require.NotContains(t, string(out), "srvtoolu_ws_")
	require.True(t, gjson.ValidBytes(out))
}

// TestFilterWebSearchHistoryBlocks_KeepsGenuineBlocksForAnthropicStrict
// supplies upstream-issued search identities with stripAll false. Filtering
// must return the same body spelling because those blocks do not match the
// reserved synthetic prefix.
func TestFilterWebSearchHistoryBlocks_KeepsGenuineBlocksForAnthropicStrict(t *testing.T) {
	body := []byte(genuineWebSearchBody)
	out := FilterWebSearchHistoryBlocks(body, false)

	require.Equal(t, string(body), string(out))
}

// TestFilterWebSearchHistoryBlocks_StripsAllBlocksForPassbackRequired supplies
// upstream-issued search blocks and selects stripAll true. Both supported
// server-search types must be removed while their readable summary remains
// available.
func TestFilterWebSearchHistoryBlocks_StripsAllBlocksForPassbackRequired(t *testing.T) {
	// Select the caller-owned all-search-block rule explicitly; this helper
	// receives no mapped model and therefore tests the supplied flag alone.
	out := FilterWebSearchHistoryBlocks([]byte(genuineWebSearchBody), true)

	require.Equal(t, []string{"text", "text"}, collectContentTypes(t, out))
	require.NotContains(t, string(out), "server_tool_use")
	require.NotContains(t, string(out), "web_search_tool_result")
	require.Contains(t, string(out), "summary with citations")
}

// TestFilterWebSearchHistoryBlocks_StripsEmulatedBlocksForUnknownModel
// exercises synthetic reserved-prefix history with stripAll false. The helper
// receives no model argument, and the assertion protects prefix-based removal
// for these supplied bytes.
func TestFilterWebSearchHistoryBlocks_StripsEmulatedBlocksForUnknownModel(t *testing.T) {
	out := FilterWebSearchHistoryBlocks([]byte(emulatedWebSearchBody), false)

	require.Equal(t, []string{"text", "text", "text"}, collectContentTypes(t, out))
	require.NotContains(t, string(out), "srvtoolu_ws_")
}

// TestFilterWebSearchHistoryBlocks_KeepsGenuineBlocksForUnknownModel exercises
// upstream-issued history with stripAll false. The helper receives no model
// argument, and the body must remain byte-equal for this prefix decision.
func TestFilterWebSearchHistoryBlocks_KeepsGenuineBlocksForUnknownModel(t *testing.T) {
	body := []byte(genuineWebSearchBody)
	out := FilterWebSearchHistoryBlocks(body, false)

	require.Equal(t, string(body), string(out))
}

// TestFilterWebSearchHistoryBlocks_NoWebSearchBlocksFastPath supplies ordinary
// user text without supported search block types. Filtering must return the
// same body spelling through its unchanged-history path.
func TestFilterWebSearchHistoryBlocks_NoWebSearchBlocksFastPath(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	out := FilterWebSearchHistoryBlocks(body, false)

	require.Equal(t, string(body), string(out))
}

// TestFilterWebSearchHistoryBlocks_EmptiedMessageGetsPlaceholder removes every
// block from an assistant message under stripAll. The message must retain its
// position and role with one assistant-specific placeholder text block.
func TestFilterWebSearchHistoryBlocks_EmptiedMessageGetsPlaceholder(t *testing.T) {
	body := []byte(`{"model":"glm-4.7","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"search"}]},` +
		`{"role":"assistant","content":[` +
		`{"type":"server_tool_use","id":"srvtoolu_01X","name":"web_search","input":{"query":"q"}},` +
		`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_01X","content":[]}]}]}`)

	out := FilterWebSearchHistoryBlocks(body, true)

	msgs := gjson.GetBytes(out, "messages").Array()
	require.Len(t, msgs, 2)
	assistant := msgs[1]
	require.Equal(t, "assistant", assistant.Get("role").String())
	content := assistant.Get("content").Array()
	require.Len(t, content, 1)
	require.Equal(t, "text", content[0].Get("type").String())
	require.Equal(t, "(assistant content removed)", content[0].Get("text").String())
}

// TestFilterWebSearchHistoryBlocks_StringContentUntouched mentions a supported
// type name inside ordinary string content. Filtering must return the same
// supplied bytes because text mention is not a removable structured block.
func TestFilterWebSearchHistoryBlocks_StringContentUntouched(t *testing.T) {
	// Mentioning a block type in text must not rewrite the message.
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[` +
		`{"role":"user","content":"please explain \"server_tool_use\" blocks"}]}`)

	out := FilterWebSearchHistoryBlocks(body, false)

	require.Equal(t, string(body), string(out))
}

// TestFilterWebSearchHistoryBlocks_InvalidMessagesUnchanged supplies a string
// messages value containing a search label. Filtering must retain the body
// because no conversation array is available for structured removal.
func TestFilterWebSearchHistoryBlocks_InvalidMessagesUnchanged(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","messages":"server_tool_use"}`)
	out := FilterWebSearchHistoryBlocks(body, false)

	require.Equal(t, string(body), string(out))
}

// TestFilterWebSearchHistoryBlocks_PreservesOtherToolBlocks mixes an ordinary
// tool exchange with server-search history. stripAll must remove supported
// server-search blocks while retaining ordinary tool_use, tool_result and text
// order.
func TestFilterWebSearchHistoryBlocks_PreservesOtherToolBlocks(t *testing.T) {
	body := []byte(`{"model":"glm-4.7","messages":[` +
		`{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"toolu_01A","name":"get_weather","input":{}},` +
		`{"type":"server_tool_use","id":"srvtoolu_ws_abc","name":"web_search","input":{"query":"q"}},` +
		`{"type":"text","text":"result"}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01A","content":"sunny"}]}]}`)

	out := FilterWebSearchHistoryBlocks(body, true)

	require.Equal(t, []string{"tool_use", "text", "tool_result"}, collectContentTypes(t, out))
}
