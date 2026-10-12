package clientmeta

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
)

const claudeCodeMetadataUserIDJSON = `{"device_id":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","account_uuid":"","session_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}`

func TestSystemPromptSimilarity(t *testing.T) {
	v := NewClaudeCodeValidator()

	tests := []struct {
		name   string
		prompt string
		want   bool
	}{
		{"精确匹配", "You are Claude Code, Anthropic's official CLI for Claude.", true},
		{"带多余空格", "You  are  Claude  Code,  Anthropic's  official  CLI  for  Claude.", true},
		{"Agent SDK 模板", "You are a Claude agent, built on Anthropic's Claude Agent SDK.", true},
		{"文件搜索专家模板", "You are a file search specialist for Claude Code, Anthropic's official CLI for Claude.", true},
		{"对话摘要模板", "You are a helpful AI assistant tasked with summarizing conversations.", true},
		{"交互式 CLI 模板", "You are an interactive CLI tool that helps users", true},
		{"无关文本", "Write me a poem about cats", false},
		{"空文本", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := map[string]any{
				"model": "claude-sonnet-4",
				"system": []any{
					map[string]any{"type": "text", "text": tt.prompt},
				},
			}
			result := v.hasClaudeCodeSystemPrompt(body)
			require.Equal(t, tt.want, result, "提示词: %q", tt.prompt)
		})
	}
}

func TestDiceCoefficient(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want float64
		tol  float64
	}{
		{"相同字符串", "hello", "hello", 1.0, 0.001},
		{"完全不同", "abc", "xyz", 0.0, 0.001},
		{"空字符串", "", "hello", 0.0, 0.001},
		{"单字符", "a", "b", 0.0, 0.001},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DiceCoefficient(tt.a, tt.b)
			require.InDelta(t, tt.want, result, tt.tol)
		})
	}
}

// TestClaudeCodeValidationInputPreservesGateOrder checks User-Agent validation before probe bypass and payload validation.
func TestClaudeCodeValidationInputPreservesGateOrder(t *testing.T) {
	validator := NewClaudeCodeValidator()
	cases := []struct {
		name  string
		input ClaudeCodeValidationInput
		want  bool
	}{
		{"invalid UA with probe", ClaudeCodeValidationInput{Path: "/v1/messages", UserAgent: "curl/1.0.0", MaxTokensOneHaiku: true}, false},
		{"CLI probe", ClaudeCodeValidationInput{Path: "/v1/messages", UserAgent: "claude-cli/2.1.156", MaxTokensOneHaiku: true}, true},
		{"messages requires body", ClaudeCodeValidationInput{Path: "/v1/messages", UserAgent: "claude-cli/2.1.156"}, false},
		{"count tokens bypass", ClaudeCodeValidationInput{Path: "/v1/messages/count_tokens", UserAgent: "claude-cli/2.1.156"}, true},
		{"models bypass", ClaudeCodeValidationInput{Path: "/v1/models", UserAgent: "claude-cli/2.1.156"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, validator.Validate(tc.input, nil)) })
	}
}

func newTestValidator() *ClaudeCodeValidator {
	return NewClaudeCodeValidator()
}

// validClaudeCodeBody builds a request body accepted by the validator.
func validClaudeCodeBody() map[string]any {
	return map[string]any{
		"model": "claude-sonnet-4-20250514",
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "You are Claude Code, Anthropic's official CLI for Claude.",
			},
		},
		"metadata": map[string]any{
			"user_id": "user_" + "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2" + "_account__session_" + "12345678-1234-1234-1234-123456789abc",
		},
	}
}

func TestValidate_ClaudeCLIUserAgent(t *testing.T) {
	v := newTestValidator()

	tests := []struct {
		name string
		ua   string
		want bool
	}{
		{"标准版本号", "claude-cli/1.0.0", true},
		{"多位版本号", "claude-cli/12.34.56", true},
		{"大写开头", "Claude-CLI/1.0.0", true},
		{"非 claude-cli", "curl/7.64.1", false},
		{"空 User-Agent", "", false},
		{"部分匹配", "not-claude-cli/1.0.0", false},
		{"缺少版本号", "claude-cli/", false},
		{"版本格式不对", "claude-cli/1.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, v.ValidateUserAgent(tt.ua), "UA: %q", tt.ua)
		})
	}
}

func TestValidate_NonMessagesPath_UAOnly(t *testing.T) {
	v := newTestValidator()

	// Non-Messages paths check the User-Agent alone.
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("User-Agent", "claude-cli/1.0.0")

	result := v.Validate(claudeCodeInputFixture(req), nil)
	require.True(t, result, "非 messages 路径只需 UA 匹配")
}

func TestValidate_NonMessagesPath_InvalidUA(t *testing.T) {
	v := newTestValidator()

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("User-Agent", "curl/7.64.1")

	result := v.Validate(claudeCodeInputFixture(req), nil)
	require.False(t, result, "UA 不匹配时应返回 false")
}

func TestValidate_MessagesPath_FullValid(t *testing.T) {
	v := newTestValidator()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/1.0.0")
	req.Header.Set("X-App", "claude-code")
	req.Header.Set("anthropic-beta", "max-tokens-3-5-sonnet-2024-07-15")
	req.Header.Set("anthropic-version", "2023-06-01")

	result := v.Validate(claudeCodeInputFixture(req), validClaudeCodeBody())
	require.True(t, result, "完整有效请求应通过")
}

func TestValidate_MessagesPath_MissingHeaders(t *testing.T) {
	v := newTestValidator()
	body := validClaudeCodeBody()

	tests := []struct {
		name          string
		missingHeader string
	}{
		{"缺少 X-App", "X-App"},
		{"缺少 anthropic-beta", "anthropic-beta"},
		{"缺少 anthropic-version", "anthropic-version"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			req.Header.Set("User-Agent", "claude-cli/1.0.0")
			req.Header.Set("X-App", "claude-code")
			req.Header.Set("anthropic-beta", "beta")
			req.Header.Set("anthropic-version", "2023-06-01")
			req.Header.Del(tt.missingHeader)

			result := v.Validate(claudeCodeInputFixture(req), body)
			require.False(t, result, "缺少 %s 应返回 false", tt.missingHeader)
		})
	}
}

func TestValidate_MessagesPath_InvalidMetadataUserID(t *testing.T) {
	v := newTestValidator()

	tests := []struct {
		name     string
		metadata map[string]any
	}{
		{"缺少 metadata", nil},
		{"缺少 user_id", map[string]any{"other": "value"}},
		{"空 user_id", map[string]any{"user_id": ""}},
		{"格式错误", map[string]any{"user_id": "invalid-format"}},
		{"hex 长度不足", map[string]any{"user_id": "user_abc_account__session_uuid"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			req.Header.Set("User-Agent", "claude-cli/1.0.0")
			req.Header.Set("X-App", "claude-code")
			req.Header.Set("anthropic-beta", "beta")
			req.Header.Set("anthropic-version", "2023-06-01")

			body := map[string]any{
				"model": "claude-sonnet-4",
				"system": []any{
					map[string]any{
						"type": "text",
						"text": "You are Claude Code, Anthropic's official CLI for Claude.",
					},
				},
			}
			if tt.metadata != nil {
				body["metadata"] = tt.metadata
			}

			result := v.Validate(claudeCodeInputFixture(req), body)
			require.False(t, result, "metadata.user_id: %v", tt.metadata)
		})
	}
}

func TestValidate_MessagesPath_InvalidSystemPrompt(t *testing.T) {
	v := newTestValidator()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/1.0.0")
	req.Header.Set("X-App", "claude-code")
	req.Header.Set("anthropic-beta", "beta")
	req.Header.Set("anthropic-version", "2023-06-01")

	body := map[string]any{
		"model": "claude-sonnet-4",
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "Generate JSON data for testing database migrations.",
			},
		},
		"metadata": map[string]any{
			"user_id": "user_" + "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2" + "_account__session_12345678-1234-1234-1234-123456789abc",
		},
	}

	result := v.Validate(claudeCodeInputFixture(req), body)
	require.False(t, result, "无关系统提示词应返回 false")
}

func TestValidate_MaxTokensOneHaikuBypass(t *testing.T) {
	v := newTestValidator()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/1.0.0")
	// Mark a Haiku probe in context while omitting X-App and the other required headers.
	ctx := requeststate.WithIsMaxTokensOneHaikuRequest(req.Context(), true)
	req = req.WithContext(ctx)

	// The probe bypass accepts a body without a system prompt.
	result := v.Validate(claudeCodeInputFixture(req), map[string]any{"model": "claude-3-haiku", "max_tokens": 1})
	require.True(t, result, "max_tokens=1+haiku 探测请求应绕过严格验证")
}

func TestValidate_NilBody_MessagesPath(t *testing.T) {
	v := newTestValidator()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/1.0.0")
	req.Header.Set("X-App", "claude-code")
	req.Header.Set("anthropic-beta", "beta")
	req.Header.Set("anthropic-version", "2023-06-01")

	result := v.Validate(claudeCodeInputFixture(req), nil)
	require.False(t, result, "nil body 的 messages 请求应返回 false")
}

func TestIsClaudeCodeClient(t *testing.T) {
	// Legacy metadata.user_id: 64 hex characters, provider UUID, and session UUID.
	legacyUserID := "user_a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2_account_550e8400-e29b-41d4-a716-446655440000_session_123e4567-e89b-12d3-a456-426614174000"
	// JSON metadata.user_id format used by version 2.1.78 and later.
	jsonUserID := `{"device_id":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2","account_uuid":"550e8400-e29b-41d4-a716-446655440000","session_id":"123e4567-e89b-12d3-a456-426614174000"}`

	tests := []struct {
		name           string
		userAgent      string
		metadataUserID string
		want           bool
	}{
		{
			name:           "Claude Code client with legacy user_id",
			userAgent:      "claude-cli/1.0.62 (darwin; arm64)",
			metadataUserID: legacyUserID,
			want:           true,
		},
		{
			name:           "Claude Code client with JSON user_id",
			userAgent:      "claude-cli/2.1.92 (external, cli)",
			metadataUserID: jsonUserID,
			want:           true,
		},
		{
			name:           "Claude Code case insensitive UA",
			userAgent:      "Claude-CLI/2.0.0",
			metadataUserID: legacyUserID,
			want:           true,
		},
		{
			name:           "Missing metadata user_id",
			userAgent:      "claude-cli/1.0.0",
			metadataUserID: "",
			want:           false,
		},
		{
			name:           "Claude CLI UA with invalid user_id format",
			userAgent:      "claude-cli/2.0.0",
			metadataUserID: "fake-user-id-12345",
			want:           false,
		},
		{
			name:           "Different user agent with valid user_id",
			userAgent:      "curl/7.68.0",
			metadataUserID: legacyUserID,
			want:           false,
		},
		{
			name:           "Empty user agent",
			userAgent:      "",
			metadataUserID: legacyUserID,
			want:           false,
		},
		{
			name:           "Similar but not Claude CLI",
			userAgent:      "claude-api/1.0.0",
			metadataUserID: legacyUserID,
			want:           false,
		},
		{
			name:           "Opencode spoofing UA with arbitrary user_id",
			userAgent:      "claude-cli/2.1.92",
			metadataUserID: "session_abc",
			want:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsClaudeCodeClient(tt.userAgent, tt.metadataUserID)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestClaudeCodeValidator_ProbeBypass(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/1.2.3 (darwin; arm64)")
	req = req.WithContext(requeststate.WithIsMaxTokensOneHaikuRequest(req.Context(), true))

	ok := validator.Validate(claudeCodeInputFixture(req), map[string]any{
		"model":      "claude-haiku-4-5",
		"max_tokens": 1,
	})
	require.True(t, ok)
}

func TestClaudeCodeValidator_ProbeBypassRequiresUA(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "curl/8.0.0")
	req = req.WithContext(requeststate.WithIsMaxTokensOneHaikuRequest(req.Context(), true))

	ok := validator.Validate(claudeCodeInputFixture(req), map[string]any{
		"model":      "claude-haiku-4-5",
		"max_tokens": 1,
	})
	require.False(t, ok)
}

func TestClaudeCodeValidator_MessagesWithoutProbeStillNeedStrictValidation(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/1.2.3 (darwin; arm64)")

	ok := validator.Validate(claudeCodeInputFixture(req), map[string]any{
		"model":      "claude-haiku-4-5",
		"max_tokens": 1,
	})
	require.False(t, ok)
}

func TestClaudeCodeValidator_CountTokensPathUAOnly(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages/count_tokens", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.156 (Claude Code)")

	ok := validator.Validate(claudeCodeInputFixture(req), map[string]any{
		"model": "claude-opus-4-8",
	})
	require.True(t, ok)
}

func TestClaudeCodeValidator_CountTokensPathRequiresUA(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages/count_tokens", nil)
	req.Header.Set("User-Agent", "curl/8.0.0")

	ok := validator.Validate(claudeCodeInputFixture(req), map[string]any{
		"model": "claude-opus-4-8",
	})
	require.False(t, ok)
}

func TestClaudeCodeValidator_MessagesPathFullValid(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.156 (Claude Code)")
	req.Header.Set("X-App", "claude-code")
	req.Header.Set("anthropic-beta", "claude-code-20250219")
	req.Header.Set("anthropic-version", "2023-06-01")

	ok := validator.Validate(claudeCodeInputFixture(req), map[string]any{
		"model":  "claude-opus-4-8",
		"stream": true,
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "You are Claude Code, Anthropic's official CLI for Claude.",
			},
		},
		"metadata": map[string]any{
			"user_id": "user_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa_account__session_aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		},
	})
	require.True(t, ok)
}

func TestClaudeCodeValidator_BillingBlockRecognizedWithoutIdentityPrompt(t *testing.T) {
	// The captured security-monitor prompt lacks the usual identity text.
	monitorPrompt, err := os.ReadFile("testdata/security_monitor_system_prompt.txt")
	require.NoError(t, err)

	validator := NewClaudeCodeValidator()

	// The prompt falls below the Dice threshold; the billing block provides recognition.
	require.Less(t, validator.BestSimilarityScore(string(monitorPrompt)), ClaudeCodeSystemPromptThreshold)

	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.162 (external, cli)")
	req.Header.Set("X-App", "cli")
	req.Header.Set("anthropic-beta", "claude-code-20250219")
	req.Header.Set("anthropic-version", "2023-06-01")

	// The billing block and cc_entrypoint=cli identify the security-monitor subrequest.
	ok := validator.Validate(claudeCodeInputFixture(req), map[string]any{
		"model": "claude-3-5-haiku-20241022",
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "x-anthropic-billing-header: cc_version=2.1.162.884; cc_entrypoint=cli; cch=d8726;",
			},
			map[string]any{
				"type": "text",
				"text": string(monitorPrompt),
			},
		},
		"metadata": map[string]any{
			"user_id": claudeCodeMetadataUserIDJSON,
		},
	})
	require.True(t, ok)
}

func TestClaudeCodeValidator_SecurityMonitorWithoutBillingBlock(t *testing.T) {
	monitorPrompt, err := os.ReadFile("testdata/security_monitor_system_prompt.txt")
	require.NoError(t, err)

	validHeaders := map[string]string{
		"User-Agent":        "claude-cli/2.1.220 (external, cli)",
		"X-App":             "cli",
		"anthropic-beta":    "claude-code-20250219",
		"anthropic-version": "2023-06-01",
	}
	validBody := func(prompt string) map[string]any {
		return map[string]any{
			"model": "claude-haiku-4-5-20251001",
			"system": []any{
				map[string]any{"type": "text", "text": prompt},
			},
			"metadata": map[string]any{"user_id": claudeCodeMetadataUserIDJSON},
		}
	}
	// The CLI may append changing conversation context outside the monitor prompt.
	sessionContext := "\n\n## Session Context\n\n- **User identity**: testuser\n" +
		"- **Working directory**: /home/testuser/project\n- **Platform**: linux"

	tests := []struct {
		name       string
		headers    map[string]string
		body       map[string]any
		wantAccept bool
	}{
		{
			name:       "official classifier request",
			headers:    validHeaders,
			body:       validBody(string(monitorPrompt)),
			wantAccept: true,
		},
		{
			name: "non-Claude user agent",
			headers: map[string]string{
				"User-Agent":        "curl/8.0.0",
				"X-App":             "cli",
				"anthropic-beta":    "claude-code-20250219",
				"anthropic-version": "2023-06-01",
			},
			body: validBody(string(monitorPrompt)),
		},
		{
			name: "missing X-App",
			headers: map[string]string{
				"User-Agent":        validHeaders["User-Agent"],
				"anthropic-beta":    validHeaders["anthropic-beta"],
				"anthropic-version": validHeaders["anthropic-version"],
			},
			body: validBody(string(monitorPrompt)),
		},
		{
			name: "missing anthropic-beta",
			headers: map[string]string{
				"User-Agent":        validHeaders["User-Agent"],
				"X-App":             validHeaders["X-App"],
				"anthropic-version": validHeaders["anthropic-version"],
			},
			body: validBody(string(monitorPrompt)),
		},
		{
			name: "missing anthropic-version",
			headers: map[string]string{
				"User-Agent":     validHeaders["User-Agent"],
				"X-App":          validHeaders["X-App"],
				"anthropic-beta": validHeaders["anthropic-beta"],
			},
			body: validBody(string(monitorPrompt)),
		},
		{
			name:    "missing metadata",
			headers: validHeaders,
			body: map[string]any{
				"model":  "claude-haiku-4-5-20251001",
				"system": []any{map[string]any{"type": "text", "text": string(monitorPrompt)}},
			},
		},
		{
			name:    "invalid metadata user ID",
			headers: validHeaders,
			body: func() map[string]any {
				body := validBody(string(monitorPrompt))
				body["metadata"] = map[string]any{"user_id": "invalid"}
				return body
			}(),
		},
		{
			name:       "unrelated prompt",
			headers:    validHeaders,
			body:       validBody("You are a different security classifier for coding agents."),
			wantAccept: false,
		},
		{
			name:       "opening sentence alone",
			headers:    validHeaders,
			body:       validBody(ClaudeCodeSecurityMonitorPrefix),
			wantAccept: false,
		},
		{
			name:    "opening sentence plus arbitrary altered suffix",
			headers: validHeaders,
			body: validBody(ClaudeCodeSecurityMonitorPrefix + "\n\n" +
				strings.Repeat("This is arbitrary altered classifier content. ", 300)),
			wantAccept: false,
		},
		{
			name:    "classifier with trailing session context entry",
			headers: validHeaders,
			body: func() map[string]any {
				body := validBody(string(monitorPrompt))
				// Check fixture types first so malformed fixtures produce assertion failures.
				system, ok := body["system"].([]any)
				require.True(t, ok)
				body["system"] = append(system, map[string]any{
					"type": "text",
					"text": sessionContext,
				})
				return body
			}(),
			wantAccept: true,
		},
		{
			name:    "classifier with leading session context entry",
			headers: validHeaders,
			body: func() map[string]any {
				body := validBody(string(monitorPrompt))
				system, ok := body["system"].([]any)
				require.True(t, ok)
				body["system"] = append([]any{map[string]any{
					"type": "text",
					"text": sessionContext,
				}}, system...)
				return body
			}(),
			wantAccept: true,
		},
		{
			name:       "session context entry alone",
			headers:    validHeaders,
			body:       validBody(sessionContext),
			wantAccept: false,
		},
		{
			name:    "tampered classifier with session context entry",
			headers: validHeaders,
			body: func() map[string]any {
				body := validBody(strings.ReplaceAll(
					string(monitorPrompt), "## HARD BLOCK", "## ALTERED BLOCK"))
				system, ok := body["system"].([]any)
				require.True(t, ok)
				body["system"] = append(system, map[string]any{
					"type": "text",
					"text": sessionContext,
				})
				return body
			}(),
			wantAccept: false,
		},
	}

	validator := NewClaudeCodeValidator()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
			for name, value := range tt.headers {
				req.Header.Set(name, value)
			}

			require.Equal(t, tt.wantAccept, validator.Validate(claudeCodeInputFixture(req), tt.body))
		})
	}
}

func TestClaudeCodeValidator_BillingBlockVSCodeEntrypointRecognized(t *testing.T) {
	// The VSCode extension uses cc_entrypoint=claude-vscode and may omit identity text.
	// Accepting multiple entrypoints prevents rejecting these monitor requests.
	monitorPrompt, err := os.ReadFile("testdata/security_monitor_system_prompt.txt")
	require.NoError(t, err)

	validator := NewClaudeCodeValidator()

	// The prompt falls below the Dice threshold; the billing block provides recognition.
	require.Less(t, validator.BestSimilarityScore(string(monitorPrompt)), ClaudeCodeSystemPromptThreshold)

	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.181 (external, claude-vscode, agent-sdk/0.3.181)")
	req.Header.Set("X-App", "cli")
	req.Header.Set("anthropic-beta", "claude-code-20250219")
	req.Header.Set("anthropic-version", "2023-06-01")

	ok := validator.Validate(claudeCodeInputFixture(req), map[string]any{
		"model": "claude-opus-4-8",
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "x-anthropic-billing-header: cc_version=2.1.181.f17; cc_entrypoint=claude-vscode;",
			},
			map[string]any{
				"type": "text",
				"text": string(monitorPrompt),
			},
		},
		"metadata": map[string]any{
			"user_id": claudeCodeMetadataUserIDJSON,
		},
	})
	require.True(t, ok)
}

func TestClaudeCodeValidator_BillingBlockWithoutEntrypointFallsThrough(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.162 (external, cli)")
	req.Header.Set("X-App", "cli")
	req.Header.Set("anthropic-beta", "claude-code-20250219")
	req.Header.Set("anthropic-version", "2023-06-01")

	// Without cc_entrypoint or identity text, template matching rejects this request.
	ok := validator.Validate(claudeCodeInputFixture(req), map[string]any{
		"model": "claude-3-5-haiku-20241022",
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "x-anthropic-billing-header: cc_version=2.1.162.884; cch=d8726;",
			},
			map[string]any{
				"type": "text",
				"text": "Some unrelated system prompt that does not resemble Claude Code.",
			},
		},
		"metadata": map[string]any{
			"user_id": claudeCodeMetadataUserIDJSON,
		},
	})
	require.False(t, ok)
}

func TestClaudeCodeValidator_BillingBlockStillRequiresClaudeCodeUA(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "curl/8.0.0")
	req.Header.Set("X-App", "cli")
	req.Header.Set("anthropic-beta", "claude-code-20250219")
	req.Header.Set("anthropic-version", "2023-06-01")

	// A billing block cannot bypass the initial claude-cli User-Agent check.
	ok := validator.Validate(claudeCodeInputFixture(req), map[string]any{
		"model": "claude-3-5-haiku-20241022",
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "x-anthropic-billing-header: cc_version=2.1.162.884; cc_entrypoint=cli; cch=d8726;",
			},
		},
	})
	require.False(t, ok)
}

// TestClaudeCodeValidator_BillingBlockRecognizedWithoutCCH covers billing attribution without a cch signature:
// `x-anthropic-billing-header: cc_version=...; cc_entrypoint=cli;`.
// The prefix and entrypoint identify subrequests even when cch and identity text are absent.
// This also covers the block format injected by buildBillingAttributionText.
func TestClaudeCodeValidator_BillingBlockRecognizedWithoutCCH(t *testing.T) {
	monitorPrompt, err := os.ReadFile("testdata/security_monitor_system_prompt.txt")
	require.NoError(t, err)

	validator := NewClaudeCodeValidator()
	require.Less(t, validator.BestSimilarityScore(string(monitorPrompt)), ClaudeCodeSystemPromptThreshold)

	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.162 (external, cli)")
	req.Header.Set("X-App", "cli")
	req.Header.Set("anthropic-beta", "claude-code-20250219")
	req.Header.Set("anthropic-version", "2023-06-01")

	ok := validator.Validate(claudeCodeInputFixture(req), map[string]any{
		"model": "claude-3-5-haiku-20241022",
		"system": []any{
			map[string]any{
				"type": "text",
				// Omit cch from the attribution block.
				"text": "x-anthropic-billing-header: cc_version=2.1.162.884; cc_entrypoint=cli;",
			},
			map[string]any{
				"type": "text",
				"text": string(monitorPrompt),
			},
		},
		"metadata": map[string]any{
			"user_id": claudeCodeMetadataUserIDJSON,
		},
	})
	require.True(t, ok, "无 cch 的新版 billing block 仍应被识别为 Claude Code")
}

// TestClaudeCodeValidator_NoCCHBlockStillRequiresClaudeCodeUA checks the User-Agent gate when cch is absent.
// Other clients are rejected even when they supply a billing block.
func TestClaudeCodeValidator_NoCCHBlockStillRequiresClaudeCodeUA(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "curl/8.0.0")
	req.Header.Set("X-App", "cli")
	req.Header.Set("anthropic-beta", "claude-code-20250219")
	req.Header.Set("anthropic-version", "2023-06-01")

	ok := validator.Validate(claudeCodeInputFixture(req), map[string]any{
		"model": "claude-3-5-haiku-20241022",
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "x-anthropic-billing-header: cc_version=2.1.162.884; cc_entrypoint=cli;",
			},
		},
	})
	require.False(t, ok)
}

func TestClaudeCodeValidator_MessagesPathRejectsNonClaudeCodeUA(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "curl/8.0.0")
	req.Header.Set("X-App", "claude-code")
	req.Header.Set("anthropic-beta", "claude-code-20250219")
	req.Header.Set("anthropic-version", "2023-06-01")

	ok := validator.Validate(claudeCodeInputFixture(req), map[string]any{
		"model":  "claude-opus-4-8",
		"stream": true,
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "You are Claude Code, Anthropic's official CLI for Claude.",
			},
		},
		"metadata": map[string]any{
			"user_id": "user_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa_account__session_aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		},
	})
	require.False(t, ok)
}

func TestClaudeCodeValidator_MessagesPathWithoutSystemPromptStillRejected(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.156 (Claude Code)")
	req.Header.Set("X-App", "claude-code")
	req.Header.Set("anthropic-beta", "claude-code-20250219")
	req.Header.Set("anthropic-version", "2023-06-01")

	ok := validator.Validate(claudeCodeInputFixture(req), map[string]any{
		"model":  "claude-opus-4-8",
		"stream": true,
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
		"metadata": map[string]any{
			"user_id": "user_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa_account__session_aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		},
	})
	require.False(t, ok)
}

func TestClaudeCodeValidator_NonMessagesPathUAOnly(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/models", nil)
	req.Header.Set("User-Agent", "claude-cli/1.2.3 (darwin; arm64)")

	ok := validator.Validate(claudeCodeInputFixture(req), nil)
	require.True(t, ok)
}

// claudeCodeInputFixture extracts client-validation input from an HTTP test request.
func claudeCodeInputFixture(r *http.Request) ClaudeCodeValidationInput {
	bypass, _ := requeststate.IsMaxTokensOneHaikuRequestFromContext(r.Context())
	return ClaudeCodeValidationInput{Path: r.URL.Path, UserAgent: r.Header.Get("User-Agent"), XApp: r.Header.Get("X-App"), AnthropicBeta: r.Header.Get("anthropic-beta"), AnthropicVersion: r.Header.Get("anthropic-version"), MaxTokensOneHaiku: bypass}
}
