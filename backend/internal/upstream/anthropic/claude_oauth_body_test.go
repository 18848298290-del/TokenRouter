package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestNormalizeClaudeOAuthRequestBody_PreservesTopLevelFieldOrder normalizes
// identity and absent-field defaults in a deliberately ordered request.
// Existing top-level fields keep their order, explicit temperature survives,
// and unusable tool choice is removed.
func TestNormalizeClaudeOAuthRequestBody_PreservesTopLevelFieldOrder(t *testing.T) {
	body := []byte(`{"alpha":1,"model":"claude-3-5-sonnet-latest","temperature":0.2,"system":"You are OpenCode, the best coding agent on the planet.","messages":[],"tool_choice":{"type":"auto"},"omega":2}`)

	result := NormalizeClaudeOAuthRequestBody(body, ClaudeOAuthNormalizeOptions{
		InjectMetadata: true,
		MetadataUserID: "user-1",
	})
	resultStr := string(result)

	require.Equal(t, "claude-3-5-sonnet-latest", gjson.GetBytes(result, "model").String())
	assertJSONTokenOrder(t, resultStr, `"alpha"`, `"model"`, `"temperature"`, `"system"`, `"messages"`, `"omega"`, `"tools"`, `"metadata"`, `"max_tokens"`)
	require.Contains(t, resultStr, `"temperature":0.2`)
	require.NotContains(t, resultStr, `"tool_choice"`)
	require.Contains(t, resultStr, `"system":"`+ClaudeCodeSystemPrompt+`"`)
	require.Contains(t, resultStr, `"tools":[]`)
	require.Contains(t, resultStr, `"metadata":{"user_id":"user-1"}`)
	require.Contains(t, resultStr, `"max_tokens":128000`)
}

// TestInjectClaudeCodePrompt_PreservesFieldOrder injects identity into a
// request whose system block has an extra id field. Top-level field order and
// that block's id/type ordering remain visible after text prefixing.
func TestInjectClaudeCodePrompt_PreservesFieldOrder(t *testing.T) {
	body := []byte(`{"alpha":1,"system":[{"id":"block-1","type":"text","text":"Custom"}],"messages":[],"omega":2}`)

	result := InjectClaudeCodePrompt(body, []any{
		map[string]any{"id": "block-1", "type": "text", "text": "Custom"},
	})
	resultStr := string(result)

	assertJSONTokenOrder(t, resultStr, `"alpha"`, `"system"`, `"messages"`, `"omega"`)
	require.Contains(t, resultStr, `{"id":"block-1","type":"text","text":"`+ClaudeCodeSystemPrompt+`\n\nCustom"}`)
}

// TestEnforceCacheControlLimit_PreservesTopLevelFieldOrder supplies five system
// and message cache markers in an ordered request. Reduction leaves four
// markers while keeping the top-level field sequence intact.
func TestEnforceCacheControlLimit_PreservesTopLevelFieldOrder(t *testing.T) {
	body := []byte(`{"alpha":1,"system":[{"type":"text","text":"s1","cache_control":{"type":"ephemeral"}},{"type":"text","text":"s2","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"m1","cache_control":{"type":"ephemeral"}},{"type":"text","text":"m2","cache_control":{"type":"ephemeral"}},{"type":"text","text":"m3","cache_control":{"type":"ephemeral"}}]}],"omega":2}`)

	result := EnforceCacheControlLimit(body)
	resultStr := string(result)

	assertJSONTokenOrder(t, resultStr, `"alpha"`, `"system"`, `"messages"`, `"omega"`)
	require.Equal(t, 4, strings.Count(resultStr, `"cache_control"`))
}

// TestEnforceCacheControlLimit_CountsToolsAndPreservesMessageAnchorsFirst adds
// a tool marker to three message markers and one system marker. Reduction
// removes the tool marker first and retains all four system/message anchors.
func TestEnforceCacheControlLimit_CountsToolsAndPreservesMessageAnchorsFirst(t *testing.T) {
	body := []byte(`{"alpha":1,"system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"m1","cache_control":{"type":"ephemeral"}},{"type":"text","text":"m2","cache_control":{"type":"ephemeral"}},{"type":"text","text":"m3","cache_control":{"type":"ephemeral"}}]}],"tools":[{"name":"a","input_schema":{},"cache_control":{"type":"ephemeral"}}],"omega":2}`)

	result := EnforceCacheControlLimit(body)
	resultStr := string(result)

	assertJSONTokenOrder(t, resultStr, `"alpha"`, `"system"`, `"messages"`, `"tools"`, `"omega"`)
	require.Equal(t, 4, strings.Count(resultStr, `"cache_control"`))
	require.True(t, gjson.GetBytes(result, "system.0.cache_control").Exists())
	require.True(t, gjson.GetBytes(result, "messages.0.content.0.cache_control").Exists())
	require.True(t, gjson.GetBytes(result, "messages.0.content.1.cache_control").Exists())
	require.True(t, gjson.GetBytes(result, "messages.0.content.2.cache_control").Exists())
	require.False(t, gjson.GetBytes(result, "tools.0.cache_control").Exists())
}

// TestInjectAnthropicCacheControlTTL1h_OnlyUpdatesExistingEphemeralCacheControl
// mixes top-level and block ephemeral markers with plain and persistent
// content. Ephemeral TTLs become one hour, while missing markers and the
// persistent TTL remain unchanged.
func TestInjectAnthropicCacheControlTTL1h_OnlyUpdatesExistingEphemeralCacheControl(t *testing.T) {
	body := []byte(`{"alpha":1,"cache_control":{"type":"ephemeral"},"system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral","ttl":"5m"}},{"type":"text","text":"plain"}],"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}},{"type":"text","text":"non","cache_control":{"type":"persistent","ttl":"5m"}}]}],"tools":[{"name":"a","input_schema":{},"cache_control":{"type":"ephemeral"}}],"omega":2}`)

	result := InjectAnthropicCacheControlTTL1h(body)
	resultStr := string(result)

	assertJSONTokenOrder(t, resultStr, `"alpha"`, `"cache_control"`, `"system"`, `"messages"`, `"tools"`, `"omega"`)
	require.Equal(t, "1h", gjson.GetBytes(result, "cache_control.ttl").String())
	require.Equal(t, "1h", gjson.GetBytes(result, "system.0.cache_control.ttl").String())
	require.False(t, gjson.GetBytes(result, "system.1.cache_control").Exists())
	require.Equal(t, "1h", gjson.GetBytes(result, "messages.0.content.0.cache_control.ttl").String())
	require.Equal(t, "5m", gjson.GetBytes(result, "messages.0.content.1.cache_control.ttl").String())
	require.Equal(t, "1h", gjson.GetBytes(result, "tools.0.cache_control.ttl").String())
}

// TestNormalizeClaudeOAuthRequestBody_InjectsContextManagement_ThinkingEnabled
// normalizes a body with explicitly enabled thinking and no context field. The
// result gains context management with the expected clear-thinking edit type.
func TestNormalizeClaudeOAuthRequestBody_InjectsContextManagement_ThinkingEnabled(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","thinking":{"type":"enabled","budget_tokens":1000},"messages":[]}`)
	out := NormalizeClaudeOAuthRequestBody(body, ClaudeOAuthNormalizeOptions{})
	require.True(t, gjson.GetBytes(out, "context_management").Exists())
	require.Equal(t, "clear_thinking_20251015",
		gjson.GetBytes(out, "context_management.edits.0.type").String())
}

// TestNormalizeClaudeOAuthRequestBody_InjectsContextManagement_ThinkingAdaptive
// normalizes an adaptive-thinking request with no context field. The result
// contains context management, verifying that this thinking mode also triggers
// the absent-field default.
func TestNormalizeClaudeOAuthRequestBody_InjectsContextManagement_ThinkingAdaptive(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-7","thinking":{"type":"adaptive"},"messages":[]}`)
	out := NormalizeClaudeOAuthRequestBody(body, ClaudeOAuthNormalizeOptions{})
	require.True(t, gjson.GetBytes(out, "context_management").Exists())
}

// TestNormalizeClaudeOAuthRequestBody_HaikuStillInjects_StripDeferredToSanitize
// normalizes enabled thinking on a Haiku body before final beta sanitation.
// Context management is present at this stage, so the model name does not
// short-circuit injection.
func TestNormalizeClaudeOAuthRequestBody_HaikuStillInjects_StripDeferredToSanitize(t *testing.T) {
	// Normalization supplies the field for Haiku too; final beta gating may remove it.
	body := []byte(`{"model":"claude-haiku-4-5","thinking":{"type":"enabled","budget_tokens":1000},"messages":[]}`)
	out := NormalizeClaudeOAuthRequestBody(body, ClaudeOAuthNormalizeOptions{})
	require.True(t, gjson.GetBytes(out, "context_management").Exists(),
		"normalize 不再按 model 名短路；strip 责任移交 sanitize 层")
}

// TestNormalizeClaudeOAuthRequestBody_PreservesClientContextManagement supplies
// a client context edit alongside enabled thinking. Normalization retains the
// custom strategy, demonstrating that an existing context field takes
// precedence over the default.
func TestNormalizeClaudeOAuthRequestBody_PreservesClientContextManagement(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-7","context_management":{"edits":[{"type":"custom_strategy"}]},"thinking":{"type":"enabled","budget_tokens":1000},"messages":[]}`)
	out := NormalizeClaudeOAuthRequestBody(body, ClaudeOAuthNormalizeOptions{})
	require.Equal(t, "custom_strategy",
		gjson.GetBytes(out, "context_management.edits.0.type").String(),
		"客户端透传的 context_management 内容必须原样保留")
}

// TestNormalizeClaudeOAuthRequestBody_NoThinking_NoInject normalizes a body
// without thinking or context management. The result still lacks context
// management, keeping the thinking-dependent default from appearing on an
// ordinary request.
func TestNormalizeClaudeOAuthRequestBody_NoThinking_NoInject(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[]}`)
	out := NormalizeClaudeOAuthRequestBody(body, ClaudeOAuthNormalizeOptions{})
	require.False(t, gjson.GetBytes(out, "context_management").Exists())
}

// TestNormalizeClaudeOAuthRequestBody_PreservesHaikuModel normalizes an
// ordinary Haiku request with missing defaults. The model field retains the
// client's exact value after OAuth normalization has supplied other request
// fields.
func TestNormalizeClaudeOAuthRequestBody_PreservesHaikuModel(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","messages":[]}`)
	out := NormalizeClaudeOAuthRequestBody(body, ClaudeOAuthNormalizeOptions{})
	require.Equal(t, "claude-haiku-4-5", gjson.GetBytes(out, "model").String())
}

// TestSanitizeOpenCodeText_RewritesCanonicalSentence supplies exactly the
// recognizable OpenCode identity sentence. The returned text equals the trimmed
// Claude Code banner, pinning this canonical replacement rather than arbitrary
// client identity text.
func TestSanitizeOpenCodeText_RewritesCanonicalSentence(t *testing.T) {
	in := "You are OpenCode, the best coding agent on the planet."
	got := SanitizeSystemText(in)
	require.Equal(t, strings.TrimSpace(ClaudeCodeSystemPrompt), got)
}

// TestSystemHasClaudeCodeBillingAttribution covers billing marker placement,
// missing entrypoint, lookalike prefix, scalar system, and malformed input.
// Recognition requires the marker in a system-array text value with both
// required text features.
func TestSystemHasClaudeCodeBillingAttribution(t *testing.T) {
	tests := []struct {
		name string // case display name
		body string // request JSON input
		want bool   // expected comparison value
	}{
		{
			name: "完整计费块",
			body: `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.220.abc; cc_entrypoint=cli;"}]}`,
			want: true,
		},
		{
			name: "计费块可位于后续 system 元素",
			body: `{"system":[{"type":"text","text":"project"},{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.220.abc; cc_entrypoint=claude-vscode;"}]}`,
			want: true,
		},
		{
			name: "缺少入口字段",
			body: `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.220.abc;"}]}`,
		},
		{
			name: "前缀不精确",
			body: `{"system":[{"type":"text","text":"prefix x-anthropic-billing-header: cc_version=2.1.220.abc; cc_entrypoint=cli;"}]}`,
		},
		{
			name: "system 字符串不接受",
			body: `{"system":"x-anthropic-billing-header: cc_version=2.1.220.abc; cc_entrypoint=cli;"}`,
		},
		{
			name: "无效 JSON",
			body: `{"system":`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, SystemHasClaudeCodeBillingAttribution([]byte(tt.body)))
		})
	}
}

// TestIsProxiedClaudeCodeRequest combines parseable metadata with a complete
// system billing marker. Both features yield true, while arbitrary identity
// text or a missing billing marker yields false.
func TestIsProxiedClaudeCodeRequest(t *testing.T) {
	validMetadata := FormatMetadataUserID(
		"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
		"550e8400-e29b-41d4-a716-446655440000",
		"123e4567-e89b-42d3-a456-426614174000",
		CLICurrentVersion,
	)
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.220.abc; cc_entrypoint=cli;"}]}`)

	require.True(t, IsProxiedClaudeCodeRequest(body, validMetadata))
	require.False(t, IsProxiedClaudeCodeRequest(body, "arbitrary-user-id"))
	require.False(t, IsProxiedClaudeCodeRequest([]byte(`{"system":[{"type":"text","text":"project"}]}`), validMetadata))
}

// TestRewriteSystemForNonClaudeCodeWithPrompt_UsesCustomExpansionPrompt
// rewrites a custom client system prompt using a configured expansion string.
// The default three-block layout remains, with that expansion text and its
// ephemeral marker in the final block.
func TestRewriteSystemForNonClaudeCodeWithPrompt_UsesCustomExpansionPrompt(t *testing.T) {
	body := []byte(`{"model":"claude-3","system":"Project instructions","messages":[{"role":"user","content":"hello"}]}`)
	customPrompt := "Custom Claude OAuth expansion prompt"

	result := RewriteSystemForNonClaudeCodeWithPromptBlocks(body, "Project instructions", customPrompt, "")

	system := gjson.GetBytes(result, "system")
	require.True(t, system.IsArray())
	require.Len(t, system.Array(), 3)
	require.Equal(t, customPrompt, system.Array()[2].Get("text").String())
	require.Equal(t, "ephemeral", system.Array()[2].Get("cache_control.type").String())
}

// TestRewriteSystemForNonClaudeCode_PreservesSystemCacheControlOnMigratedMessage
// moves cached client system instructions into message history. The instruction
// text and its ephemeral one-hour marker appear together on the injected user
// message block.
func TestRewriteSystemForNonClaudeCode_PreservesSystemCacheControlOnMigratedMessage(t *testing.T) {
	body := []byte(`{"model":"claude-3","system":[{"type":"text","text":"Stable project instructions","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":"hello"}]}`)
	system := []any{
		map[string]any{
			"type":          "text",
			"text":          "Stable project instructions",
			"cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"},
		},
	}

	result := RewriteSystemForNonClaudeCodeWithPromptBlocks(body, system, "", "")

	require.Equal(t, "[System Instructions]\nStable project instructions", gjson.GetBytes(result, "messages.0.content.0.text").String())
	require.Equal(t, "ephemeral", gjson.GetBytes(result, "messages.0.content.0.cache_control.type").String())
	require.Equal(t, "1h", gjson.GetBytes(result, "messages.0.content.0.cache_control.ttl").String())
}

// TestRewriteSystemForNonClaudeCode_LeavesMigratedMessageUncachedWithoutSystemBreakpoint
// moves client instructions from an unmarked system block into message history.
// The injected instruction block has no cache_control, keeping absent client
// caching absent on that migrated text.
func TestRewriteSystemForNonClaudeCode_LeavesMigratedMessageUncachedWithoutSystemBreakpoint(t *testing.T) {
	body := []byte(`{"model":"claude-3","system":[{"type":"text","text":"Project instructions"}],"messages":[{"role":"user","content":"hello"}]}`)
	system := []any{
		map[string]any{"type": "text", "text": "Project instructions"},
	}

	result := RewriteSystemForNonClaudeCodeWithPromptBlocks(body, system, "", "")

	require.False(t, gjson.GetBytes(result, "messages.0.content.0.cache_control").Exists())
}

// TestRewriteSystemForNonClaudeCodeWithPromptBlocks_UsesConfiguredBlocks
// renders enabled, disabled, templated, and differently cached block settings.
// Disabled text disappears, placeholders expand, and the retained blocks carry
// their individually configured cache values.
func TestRewriteSystemForNonClaudeCodeWithPromptBlocks_UsesConfiguredBlocks(t *testing.T) {
	body := []byte(`{"model":"claude-3","system":"Project instructions","messages":[{"role":"user","content":"hello"}]}`)
	blocks := `{
		"blocks": [
			{"type":"text","text":"prefix {cc_version}.{fp}","cache_control":true},
			{"enabled":false,"type":"text","text":"disabled"},
			{"type":"text","text":"{claude_code_system_prompt}"},
			{"type":"text","text":"tail","cache_control":{"type":"ephemeral","ttl":"1h"}}
		]
	}`

	result := RewriteSystemForNonClaudeCodeWithPromptBlocks(body, "Project instructions", "", blocks)

	system := gjson.GetBytes(result, "system")
	require.True(t, system.IsArray())
	arr := system.Array()
	require.Len(t, arr, 3)
	require.Contains(t, arr[0].Get("text").String(), "prefix "+CLICurrentVersion+".")
	require.Equal(t, "ephemeral", arr[0].Get("cache_control.type").String())
	require.Equal(t, DefaultCacheControlTTL, arr[0].Get("cache_control.ttl").String())
	require.Equal(t, ClaudeCodeSystemPrompt, arr[1].Get("text").String())
	require.False(t, arr[1].Get("cache_control").Exists())
	require.Equal(t, "tail", arr[2].Get("text").String())
	require.Equal(t, "1h", arr[2].Get("cache_control.ttl").String())
}

// TestSystemIncludesClaudeCodePrompt covers string, array, and RawMessage
// system values with recognized and unrelated text. Recognition works through
// normalized representations and can find the banner in a later array element.
func TestSystemIncludesClaudeCodePrompt(t *testing.T) {
	tests := []struct {
		name   string // case display name
		system any    // input system representation
		want   bool   // expected comparison value
	}{
		{
			name:   "nil system",
			system: nil,
			want:   false,
		},
		{
			name:   "empty string",
			system: "",
			want:   false,
		},
		{
			name:   "string with Claude Code prompt",
			system: ClaudeCodeSystemPrompt,
			want:   true,
		},
		{
			name:   "string with different content",
			system: "You are a helpful assistant.",
			want:   false,
		},
		{
			name:   "empty array",
			system: []any{},
			want:   false,
		},
		{
			name: "array with Claude Code prompt",
			system: []any{
				map[string]any{
					"type": "text",
					"text": ClaudeCodeSystemPrompt,
				},
			},
			want: true,
		},
		{
			name: "array with Claude Code prompt in second position",
			system: []any{
				map[string]any{"type": "text", "text": "First prompt"},
				map[string]any{"type": "text", "text": ClaudeCodeSystemPrompt},
			},
			want: true,
		},
		{
			name: "array without Claude Code prompt",
			system: []any{
				map[string]any{"type": "text", "text": "Custom prompt"},
			},
			want: false,
		},
		{
			name: "array with partial match (should not match)",
			system: []any{
				map[string]any{"type": "text", "text": "You are Claude"},
			},
			want: false,
		},
		// Protocol conversion supplies system as json.RawMessage.
		{
			name:   "json.RawMessage string with Claude Code prompt",
			system: json.RawMessage(`"` + ClaudeCodeSystemPrompt + `"`),
			want:   true,
		},
		{
			name:   "json.RawMessage string without Claude Code prompt",
			system: json.RawMessage(`"You are a helpful assistant"`),
			want:   false,
		},
		{
			name:   "json.RawMessage nil (empty)",
			system: json.RawMessage(nil),
			want:   false,
		},
		{
			name:   "json.RawMessage empty string",
			system: json.RawMessage(`""`),
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SystemIncludesClaudeCodePrompt(tt.system)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestInjectClaudeCodePrompt covers missing, string, array, duplicate identity,
// and RawMessage system input. Results start with a cached identity block and
// retain eligible custom text with the expected prefix.
func TestInjectClaudeCodePrompt(t *testing.T) {
	claudePrefix := strings.TrimSpace(ClaudeCodeSystemPrompt)

	tests := []struct {
		name           string // case display name
		body           string // request JSON input
		system         any    // input system representation
		wantSystemLen  int    // expected system-block count
		wantFirstText  string // expected identity text
		wantSecondText string // expected prefixed instructions
	}{
		{
			name:          "nil system",
			body:          `{"model":"claude-3"}`,
			system:        nil,
			wantSystemLen: 1,
			wantFirstText: ClaudeCodeSystemPrompt,
		},
		{
			name:          "empty string system",
			body:          `{"model":"claude-3"}`,
			system:        "",
			wantSystemLen: 1,
			wantFirstText: ClaudeCodeSystemPrompt,
		},
		{
			name:           "string system",
			body:           `{"model":"claude-3"}`,
			system:         "Custom prompt",
			wantSystemLen:  2,
			wantFirstText:  ClaudeCodeSystemPrompt,
			wantSecondText: claudePrefix + "\n\nCustom prompt",
		},
		{
			name:          "string system equals Claude Code prompt",
			body:          `{"model":"claude-3"}`,
			system:        ClaudeCodeSystemPrompt,
			wantSystemLen: 1,
			wantFirstText: ClaudeCodeSystemPrompt,
		},
		{
			name:   "array system",
			body:   `{"model":"claude-3"}`,
			system: []any{map[string]any{"type": "text", "text": "Custom"}},
			// Claude Code + Custom = 2
			wantSystemLen:  2,
			wantFirstText:  ClaudeCodeSystemPrompt,
			wantSecondText: claudePrefix + "\n\nCustom",
		},
		{
			name: "array system with existing Claude Code prompt (should dedupe)",
			body: `{"model":"claude-3"}`,
			system: []any{
				map[string]any{"type": "text", "text": ClaudeCodeSystemPrompt},
				map[string]any{"type": "text", "text": "Other"},
			},
			// Claude Code at start + Other = 2 (deduped)
			wantSystemLen:  2,
			wantFirstText:  ClaudeCodeSystemPrompt,
			wantSecondText: claudePrefix + "\n\nOther",
		},
		{
			name:          "empty array",
			body:          `{"model":"claude-3"}`,
			system:        []any{},
			wantSystemLen: 1,
			wantFirstText: ClaudeCodeSystemPrompt,
		},
		// Protocol conversion supplies system as json.RawMessage.
		{
			name:           "json.RawMessage string system",
			body:           `{"model":"claude-3","system":"Custom prompt"}`,
			system:         json.RawMessage(`"Custom prompt"`),
			wantSystemLen:  2,
			wantFirstText:  ClaudeCodeSystemPrompt,
			wantSecondText: claudePrefix + "\n\nCustom prompt",
		},
		{
			name:          "json.RawMessage nil system",
			body:          `{"model":"claude-3"}`,
			system:        json.RawMessage(nil),
			wantSystemLen: 1,
			wantFirstText: ClaudeCodeSystemPrompt,
		},
		{
			name:          "json.RawMessage Claude Code prompt (should not duplicate)",
			body:          `{"model":"claude-3","system":"` + ClaudeCodeSystemPrompt + `"}`,
			system:        json.RawMessage(`"` + ClaudeCodeSystemPrompt + `"`),
			wantSystemLen: 1,
			wantFirstText: ClaudeCodeSystemPrompt,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := InjectClaudeCodePrompt([]byte(tt.body), tt.system)

			var parsed map[string]any
			err := json.Unmarshal(result, &parsed)
			require.NoError(t, err)

			system, ok := parsed["system"].([]any)
			require.True(t, ok, "system should be an array")
			require.Len(t, system, tt.wantSystemLen)

			first, ok := system[0].(map[string]any)
			require.True(t, ok)
			require.Equal(t, tt.wantFirstText, first["text"])
			require.Equal(t, "text", first["type"])

			// Check cache_control
			cc, ok := first["cache_control"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "ephemeral", cc["type"])

			if tt.wantSecondText != "" && len(system) > 1 {
				second, ok := system[1].(map[string]any)
				require.True(t, ok)
				require.Equal(t, tt.wantSecondText, second["text"])
			}
		})
	}
}

// TestRewriteSystemForNonClaudeCode rewrites several system representations
// alongside existing message histories. It checks the default system blocks,
// the injected user/assistant pair in the first two positions, and total history
// lengths that include the supplied turns.
func TestRewriteSystemForNonClaudeCode(t *testing.T) {
	tests := []struct {
		name             string // case display name
		body             string // request JSON input
		system           any    // input system representation
		wantSystemText   string // expected identity prompt
		wantMessagesLen  int    // expected history length
		wantFirstMsgRole string // migrated instruction role
		wantFirstMsgText string // migrated instruction text
		wantAckMsgText   string // injected acknowledgment text
	}{
		{
			name:            "nil system - no messages injected",
			body:            `{"model":"claude-3","messages":[{"role":"user","content":"hello"}]}`,
			system:          nil,
			wantSystemText:  ClaudeCodeSystemPrompt,
			wantMessagesLen: 1, // Original message; no injection.
		},
		{
			name:            "empty string system - no messages injected",
			body:            `{"model":"claude-3","messages":[{"role":"user","content":"hello"}]}`,
			system:          "",
			wantSystemText:  ClaudeCodeSystemPrompt,
			wantMessagesLen: 1,
		},
		{
			name:             "custom string system - migrated to messages",
			body:             `{"model":"claude-3","messages":[{"role":"user","content":"hello"}]}`,
			system:           "You are a personal assistant running inside OpenClaw.",
			wantSystemText:   ClaudeCodeSystemPrompt,
			wantMessagesLen:  3, // instruction + ack + original
			wantFirstMsgRole: "user",
			wantFirstMsgText: "[System Instructions]\nYou are a personal assistant running inside OpenClaw.",
			wantAckMsgText:   "Understood. I will follow these instructions.",
		},
		{
			name:            "system equals Claude Code prompt - no messages injected",
			body:            `{"model":"claude-3","messages":[{"role":"user","content":"hello"}]}`,
			system:          ClaudeCodeSystemPrompt,
			wantSystemText:  ClaudeCodeSystemPrompt,
			wantMessagesLen: 1,
		},
		{
			name: "array system with custom blocks - text joined and migrated",
			body: `{"model":"claude-3","messages":[{"role":"user","content":"hello"}]}`,
			system: []any{
				map[string]any{"type": "text", "text": "First instruction"},
				map[string]any{"type": "text", "text": "Second instruction"},
			},
			wantSystemText:   ClaudeCodeSystemPrompt,
			wantMessagesLen:  3,
			wantFirstMsgRole: "user",
			wantFirstMsgText: "[System Instructions]\nFirst instruction\n\nSecond instruction",
			wantAckMsgText:   "Understood. I will follow these instructions.",
		},
		{
			name:            "empty array system - no messages injected",
			body:            `{"model":"claude-3","messages":[{"role":"user","content":"hello"}]}`,
			system:          []any{},
			wantSystemText:  ClaudeCodeSystemPrompt,
			wantMessagesLen: 1,
		},
		{
			name:             "json.RawMessage string system",
			body:             `{"model":"claude-3","system":"Custom prompt","messages":[{"role":"user","content":"hello"}]}`,
			system:           json.RawMessage(`"Custom prompt"`),
			wantSystemText:   ClaudeCodeSystemPrompt,
			wantMessagesLen:  3,
			wantFirstMsgRole: "user",
			wantFirstMsgText: "[System Instructions]\nCustom prompt",
			wantAckMsgText:   "Understood. I will follow these instructions.",
		},
		{
			name:            "json.RawMessage nil system",
			body:            `{"model":"claude-3","messages":[{"role":"user","content":"hello"}]}`,
			system:          json.RawMessage(nil),
			wantSystemText:  ClaudeCodeSystemPrompt,
			wantMessagesLen: 1,
		},
		{
			name:             "multiple original messages preserved",
			body:             `{"model":"claude-3","messages":[{"role":"user","content":"msg1"},{"role":"assistant","content":"resp1"},{"role":"user","content":"msg2"}]}`,
			system:           "Be helpful",
			wantSystemText:   ClaudeCodeSystemPrompt,
			wantMessagesLen:  5, // 2 injected + 3 original
			wantFirstMsgRole: "user",
			wantFirstMsgText: "[System Instructions]\nBe helpful",
			wantAckMsgText:   "Understood. I will follow these instructions.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RewriteSystemForNonClaudeCodeWithPromptBlocks([]byte(tt.body), tt.system, "", "")

			var parsed map[string]any
			err := json.Unmarshal(result, &parsed)
			require.NoError(t, err)

			// The default system contains billing, identity, and expansion blocks.
			// The expansion owns the cache breakpoint.
			systemArr, ok := parsed["system"].([]any)
			require.True(t, ok, "system should be an array, got %T", parsed["system"])
			require.Len(t, systemArr, 3, "system array should have exactly 3 blocks (billing + cc prompt + expansion)")

			billingBlock, ok := systemArr[0].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "text", billingBlock["type"])
			require.Contains(t, billingBlock["text"], "x-anthropic-billing-header:")
			require.Contains(t, billingBlock["text"], "cc_version=")
			require.Contains(t, billingBlock["text"], "cc_entrypoint=cli")
			// The billing marker omits cch.
			require.NotContains(t, billingBlock["text"], "cch=")

			systemBlock, ok := systemArr[1].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "text", systemBlock["type"])
			require.Equal(t, tt.wantSystemText, systemBlock["text"])
			_, hasCC := systemBlock["cache_control"]
			require.False(t, hasCC, "身份前缀 block 不应带 cache_control（断点落在扩充块）")

			expansionBlock, ok := systemArr[2].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "text", expansionBlock["type"])
			require.Equal(t, ClaudeCodeSystemPromptExpansion, expansionBlock["text"])
			cc, ok := expansionBlock["cache_control"].(map[string]any)
			require.True(t, ok, "expansion block should have cache_control")
			require.Equal(t, "ephemeral", cc["type"])

			messages, ok := parsed["messages"].([]any)
			require.True(t, ok, "messages should be an array")
			require.Len(t, messages, tt.wantMessagesLen)

			if tt.wantFirstMsgRole != "" && len(messages) >= 2 {
				// Check the injected instruction and acknowledgment pair.
				firstMsg, ok := messages[0].(map[string]any)
				require.True(t, ok)
				require.Equal(t, tt.wantFirstMsgRole, firstMsg["role"])

				firstContent, ok := firstMsg["content"].([]any)
				require.True(t, ok)
				require.Len(t, firstContent, 1)
				firstBlock, ok := firstContent[0].(map[string]any)
				require.True(t, ok)
				require.Equal(t, tt.wantFirstMsgText, firstBlock["text"])

				// Check the injected acknowledgment.
				ackMsg, ok := messages[1].(map[string]any)
				require.True(t, ok)
				require.Equal(t, "assistant", ackMsg["role"])

				ackContent, ok := ackMsg["content"].([]any)
				require.True(t, ok)
				require.Len(t, ackContent, 1)
				ackBlock, ok := ackContent[0].(map[string]any)
				require.True(t, ok)
				require.Equal(t, tt.wantAckMsgText, ackBlock["text"])
			}
		})
	}
}
