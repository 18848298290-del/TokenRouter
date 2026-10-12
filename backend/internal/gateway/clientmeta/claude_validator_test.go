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

// claudeCodeMetadataUserIDJSON supplies a valid JSON identity for monitor and
// billing-attribution fixtures that omit the ordinary identity prompt.
const claudeCodeMetadataUserIDJSON = `{"device_id":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","account_uuid":"","session_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}`

// TestSystemPromptSimilarity supplies identity and auxiliary templates
// alongside unrelated or empty text. Whitespace variants and supported
// templates are recognized, while unrelated prompts remain below the acceptance
// threshold.
func TestSystemPromptSimilarity(t *testing.T) {
	v := NewClaudeCodeValidator()

	tests := []struct {
		name   string // test scenario label
		prompt string // input system prompt
		want   bool   // expected recognition result
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

// TestDiceCoefficient compares equal, disjoint, empty, and single-character
// strings. The asserted scores protect the basic equality and no-shared-bigram
// cases within the table's numeric tolerance.
func TestDiceCoefficient(t *testing.T) {
	tests := []struct {
		name string  // test scenario label
		a    string  // first comparison input
		b    string  // second comparison input
		want float64 // expected similarity score
		tol  float64 // accepted score tolerance
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

// TestClaudeCodeValidationInputPreservesGateOrder combines probe flags, CLI
// headers, and auxiliary paths with an absent body. User-Agent rejection takes
// precedence over bypasses, while ordinary Messages requests still require
// payload validation.
func TestClaudeCodeValidationInputPreservesGateOrder(t *testing.T) {
	validator := NewClaudeCodeValidator()
	cases := []struct {
		name  string                    // test scenario label
		input ClaudeCodeValidationInput // request validation evidence
		want  bool                      // expected recognition result
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

// newTestValidator constructs a stateless client validator for
// request-recognition tests.
func newTestValidator() *ClaudeCodeValidator {
	return NewClaudeCodeValidator()
}

// validClaudeCodeBody returns a newly allocated model, identity prompt, and
// legacy metadata fixture accepted by ordinary Messages validation. Tests can
// modify its nested maps and slices without sharing them with another call.
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

// TestValidate_ClaudeCLIUserAgent supplies complete CLI release prefixes and
// malformed or unrelated headers. Case variants and numeric component lengths
// pass, while partial matches and missing release components fail.
func TestValidate_ClaudeCLIUserAgent(t *testing.T) {
	v := newTestValidator()

	tests := []struct {
		name string // test scenario label
		ua   string // input User-Agent value
		want bool   // expected recognition result
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

// TestValidate_NonMessagesPath_UAOnly supplies a models request with a matching
// CLI header and nil body. The auxiliary path passes client-format validation
// without Messages-specific payload or header evidence.
func TestValidate_NonMessagesPath_UAOnly(t *testing.T) {
	v := newTestValidator()

	// Non-Messages paths check the User-Agent alone.
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("User-Agent", "claude-cli/1.0.0")

	result := v.Validate(claudeCodeInputFixture(req), nil)
	require.True(t, result, "非 messages 路径只需 UA 匹配")
}

// TestValidate_NonMessagesPath_InvalidUA supplies a models request from curl
// with an absent payload. The non-Messages bypass cannot override the initial
// CLI User-Agent requirement and validation returns false.
func TestValidate_NonMessagesPath_InvalidUA(t *testing.T) {
	v := newTestValidator()

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("User-Agent", "curl/7.64.1")

	result := v.Validate(claudeCodeInputFixture(req), nil)
	require.False(t, result, "UA 不匹配时应返回 false")
}

// TestValidate_MessagesPath_FullValid combines a recognized identity prompt,
// legacy metadata, and every required header. Ordinary Messages validation
// accepts the complete fixture, covering their combined client-format evidence.
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

// TestValidate_MessagesPath_MissingHeaders removes each required header from an
// otherwise valid request. X-App, anthropic-beta, and anthropic-version each
// remain necessary for ordinary Messages client-format validation.
func TestValidate_MessagesPath_MissingHeaders(t *testing.T) {
	v := newTestValidator()
	body := validClaudeCodeBody()

	tests := []struct {
		name          string // test scenario label
		missingHeader string // header omitted from request
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

// TestValidate_MessagesPath_InvalidMetadataUserID supplies absent, empty, and
// malformed metadata with a valid prompt and headers. Every invalid metadata
// fixture is rejected despite satisfying the other client-format gates.
func TestValidate_MessagesPath_InvalidMetadataUserID(t *testing.T) {
	v := newTestValidator()

	tests := []struct {
		name     string         // test scenario label
		metadata map[string]any // supplied identity metadata
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

// TestValidate_MessagesPath_InvalidSystemPrompt supplies an unrelated prompt
// with valid CLI headers and legacy metadata. Ordinary Messages validation
// rejects the request when its remaining client evidence lacks a recognized
// system prompt.
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

// TestValidate_MaxTokensOneHaikuBypass marks a probe in request context while
// omitting Messages headers and identity text. A valid CLI header permits the
// marked auxiliary request through the reduced validation path.
func TestValidate_MaxTokensOneHaikuBypass(t *testing.T) {
	v := newTestValidator()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/1.0.0")
	// Mark the reduced probe form before fixture extraction. Omitting ordinary
	// Messages headers makes acceptance depend on the contextual bypass and valid CLI header.
	ctx := requeststate.WithIsMaxTokensOneHaikuRequest(req.Context(), true)
	req = req.WithContext(ctx)

	// The probe bypass accepts a body without a system prompt.
	result := v.Validate(claudeCodeInputFixture(req), map[string]any{"model": "claude-3-haiku", "max_tokens": 1})
	require.True(t, result, "max_tokens=1+haiku 探测请求应绕过严格验证")
}

// TestValidate_NilBody_MessagesPath supplies every required header but no
// Messages payload. Header evidence alone cannot establish ordinary client
// format, and validation rejects the absent system prompt and metadata.
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

// TestIsClaudeCodeClient combines CLI headers with legacy and JSON metadata
// identities. Both supported identity formats pass with a matching header,
// while absent, malformed, or unrelated client evidence fails.
func TestIsClaudeCodeClient(t *testing.T) {
	// Legacy metadata.user_id: 64 device hex characters, account UUID, and session UUID.
	legacyUserID := "user_a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2_account_550e8400-e29b-41d4-a716-446655440000_session_123e4567-e89b-12d3-a456-426614174000"
	// JSON metadata.user_id supplies the device, account, and session fields
	// consumed by the same parser as the legacy identity above.
	jsonUserID := `{"device_id":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2","account_uuid":"550e8400-e29b-41d4-a716-446655440000","session_id":"123e4567-e89b-12d3-a456-426614174000"}`

	tests := []struct {
		name           string // test scenario label
		userAgent      string // input User-Agent value
		metadataUserID string // encoded metadata identity
		want           bool   // expected recognition result
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

// TestClaudeCodeValidator_ProbeBypass supplies a marked Haiku probe with a CLI
// header and minimal body. Validation accepts the auxiliary request despite the
// omitted ordinary Messages metadata and headers.
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

// TestClaudeCodeValidator_ProbeBypassRequiresUA supplies a marked probe from
// curl. The bypass flag cannot establish CLI format, so the initial User-Agent
// gate rejects the request before probe acceptance.
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

// TestClaudeCodeValidator_MessagesWithoutProbeStillNeedStrictValidation
// supplies a single-token Haiku body without marking the probe. Its CLI header
// does not waive ordinary Messages system-prompt, metadata, and header
// requirements.
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

// TestClaudeCodeValidator_CountTokensPathUAOnly sends a minimal count-token
// payload with a CLI header. This auxiliary endpoint passes client validation
// despite omitting the system prompt, identity metadata, and other Messages
// headers.
func TestClaudeCodeValidator_CountTokensPathUAOnly(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages/count_tokens", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.156 (Claude Code)")

	ok := validator.Validate(claudeCodeInputFixture(req), map[string]any{
		"model": "claude-opus-4-8",
	})
	require.True(t, ok)
}

// TestClaudeCodeValidator_CountTokensPathRequiresUA sends a minimal count-token
// request from curl. Its auxiliary path cannot bypass CLI header recognition,
// and client-format validation returns false.
func TestClaudeCodeValidator_CountTokensPathRequiresUA(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages/count_tokens", nil)
	req.Header.Set("User-Agent", "curl/8.0.0")

	ok := validator.Validate(claudeCodeInputFixture(req), map[string]any{
		"model": "claude-opus-4-8",
	})
	require.False(t, ok)
}

// TestClaudeCodeValidator_MessagesPathFullValid supplies a streaming Messages
// body with recognized identity text and legacy metadata. All required headers
// accompany it, and ordinary client-format validation accepts the request.
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

// TestClaudeCodeValidator_BillingBlockRecognizedWithoutIdentityPrompt supplies
// billing attribution alongside the captured security-monitor prompt. The
// complete request passes with a low identity-template score; monitor
// recognition can satisfy the prompt check independently.
func TestClaudeCodeValidator_BillingBlockRecognizedWithoutIdentityPrompt(t *testing.T) {
	// The captured security-monitor prompt lacks the usual identity text.
	monitorPrompt, err := os.ReadFile("testdata/security_monitor_system_prompt.txt")
	require.NoError(t, err)

	validator := NewClaudeCodeValidator()

	// The monitor's score stays below the template threshold, so successful
	// validation of this fixture requires a structural prompt recognizer.
	require.Less(t, validator.BestSimilarityScore(string(monitorPrompt)), ClaudeCodeSystemPromptThreshold)

	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.162 (external, cli)")
	req.Header.Set("X-App", "cli")
	req.Header.Set("anthropic-beta", "claude-code-20250219")
	req.Header.Set("anthropic-version", "2023-06-01")

	// Billing attribution accompanies a monitor block that also satisfies its
	// own recognizer. The request fixture exercises their combined accepted form.
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

// TestClaudeCodeValidator_SecurityMonitorWithoutBillingBlock varies structural
// markers, headers, metadata, and surrounding context blocks. A complete
// monitor block passes, while missing gates, altered classifier structure, or
// context alone remain rejected.
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
		name       string            // test scenario label
		headers    map[string]string // supplied request headers
		body       map[string]any    // supplied request payload
		wantAccept bool              // expected validation acceptance
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

// TestClaudeCodeValidator_BillingBlockVSCodeEntrypointRecognized includes
// VSCode billing attribution and a captured monitor prompt with valid headers
// and metadata. Acceptance covers the combined request; its monitor prompt also
// supplies independent recognition.
func TestClaudeCodeValidator_BillingBlockVSCodeEntrypointRecognized(t *testing.T) {
	// This fixture supplies a VSCode entrypoint with the captured monitor text.
	// Its system array can pass monitor recognition independently of the billing block.
	monitorPrompt, err := os.ReadFile("testdata/security_monitor_system_prompt.txt")
	require.NoError(t, err)

	validator := NewClaudeCodeValidator()

	// The monitor's score stays below the template threshold, so successful
	// validation of this fixture requires a structural prompt recognizer.
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

// TestClaudeCodeValidator_BillingBlockWithoutEntrypointFallsThrough supplies
// billing attribution without an entrypoint and an unrelated second prompt.
// Valid headers and metadata cannot rescue the missing prompt evidence, so
// validation rejects the request.
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

// TestClaudeCodeValidator_BillingBlockStillRequiresClaudeCodeUA supplies
// billing attribution under a curl header. The initial CLI gate rejects the
// request even though the body carries a billing prefix and entrypoint marker.
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

// TestClaudeCodeValidator_BillingBlockRecognizedWithoutCCH supplies a
// signature-free billing block and the captured monitor prompt. The complete
// request is accepted with valid headers and metadata; the fixture does not
// isolate billing-only recognition.
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

// TestClaudeCodeValidator_NoCCHBlockStillRequiresClaudeCodeUA supplies a
// signature-free billing block under a curl header. Validation rejects the
// non-CLI request before consulting that block or any remaining payload gates.
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

// TestClaudeCodeValidator_MessagesPathRejectsNonClaudeCodeUA supplies a
// recognized prompt, valid metadata, and required Anthropic headers under curl.
// The initial CLI User-Agent gate still rejects this otherwise complete
// Messages fixture.
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

// TestClaudeCodeValidator_MessagesPathWithoutSystemPromptStillRejected supplies
// messages, metadata, and required CLI headers without system text. Ordinary
// Messages validation rejects the missing prompt evidence instead of inferring
// it from the remaining fields.
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

// TestClaudeCodeValidator_NonMessagesPathUAOnly supplies a models request with
// a CLI header and no body. The auxiliary path is recognized from that header
// while ordinary Messages payload requirements remain unused.
func TestClaudeCodeValidator_NonMessagesPathUAOnly(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/models", nil)
	req.Header.Set("User-Agent", "claude-cli/1.2.3 (darwin; arm64)")

	ok := validator.Validate(claudeCodeInputFixture(req), nil)
	require.True(t, ok)
}

// claudeCodeInputFixture extracts validation headers, path, and the contextual
// probe flag from a nonnil HTTP request. An absent flag becomes false, matching
// ordinary Messages validation before a caller identifies a probe.
func claudeCodeInputFixture(r *http.Request) ClaudeCodeValidationInput {
	bypass, _ := requeststate.IsMaxTokensOneHaikuRequestFromContext(r.Context())
	return ClaudeCodeValidationInput{Path: r.URL.Path, UserAgent: r.Header.Get("User-Agent"), XApp: r.Header.Get("X-App"), AnthropicBeta: r.Header.Get("anthropic-beta"), AnthropicVersion: r.Header.Get("anthropic-version"), MaxTokensOneHaiku: bypass}
}
