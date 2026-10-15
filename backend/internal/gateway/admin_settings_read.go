package gateway

import (
	"log/slog"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/gateway/promptpolicy"
)

// admin_settings_read.go builds the gateway administration view from persisted
// settings and their display defaults. Reading applies the forwarding switches'
// established defaults and platform normalizers, retaining configured prompt
// text and parsing prompt replacement JSON for the administration view.

// AdminReadSettings contains interpreted gateway values displayed by the administration page.
type AdminReadSettings struct {
	// Antigravity client version override
	AntigravityUserAgentVersion string
	BackendModeEnabled          bool   // backend-mode admission toggle
	ClaudeOAuthSystemPrompt     string // configured Anthropic system text
	// JSON system content blocks
	ClaudeOAuthSystemPromptBlocks string
	// one-hour Anthropic cache injection
	EnableAnthropicCacheTTL1hInjection bool
	// Claude billing attribution signing
	EnableCCHSigning bool
	// Anthropic system injection toggle
	EnableClaudeOAuthSystemPromptInjection bool
	// client dateline normalization toggle
	EnableClientDatelineNormalization bool
	// provider fingerprint unification toggle
	EnableFingerprintUnification bool
	EnableIdentityPatch          bool // identity prompt patching toggle
	// client metadata passthrough toggle
	EnableMetadataPassthrough bool
	GrokDefaultBaseURLMode    string // default Grok endpoint mode
	GrokDefaultTextModel      string // default Grok text model
	IdentityPatchPrompt       string // configured identity patch text
	MaxClaudeCodeVersion      string // upper CLI version bound
	MinClaudeCodeVersion      string // lower CLI version bound
	// Claude Codex plugin permission
	OpenAIAllowClaudeCodeCodexPlugin bool
	// outbound Codex User-Agent override
	OpenAICodexUserAgent string
	OpenAITTFTMode       string // semantic or visible TTFT
	// message cache-control rewrite toggle
	RewriteMessageCacheControl bool
	// configured user-prompt replacement rules
	UserPromptReplacementConfig *promptpolicy.UserPromptReplacementConfig
}

// ReadAdminSettings interprets persisted gateway values for the administration
// view. Supply the Antigravity version normalizer and Grok default model;
// missing values enable identity patching, fingerprint unification, prompt
// injection, and dateline normalization, while other switches default to false.
//
// Boolean values match the literal "true" rather than general boolean spellings.
// Prompt text and version bounds are retained, TTFT and Grok URL modes are
// normalized, and prompt replacement JSON uses its parser's default configuration
// when it is absent or invalid.
func ReadAdminSettings(settings map[string]string, rules AdminSettingsRules) *AdminReadSettings {
	result := &AdminReadSettings{}
	result.BackendModeEnabled = settings[SettingKeyBackendModeEnabled] == "true"
	if v, ok := settings[SettingKeyEnableIdentityPatch]; ok && v != "" {
		result.EnableIdentityPatch = v == "true"
	} else {
		result.EnableIdentityPatch = true
	}
	result.IdentityPatchPrompt = settings[SettingKeyIdentityPatchPrompt]
	result.GrokDefaultTextModel = strings.TrimSpace(settings[SettingKeyGrokDefaultTextModel])
	if result.GrokDefaultTextModel == "" {
		result.GrokDefaultTextModel = rules.GrokDefaultTextModel
	}
	result.GrokDefaultBaseURLMode = NormalizeGrokDefaultBaseURLMode(settings[SettingKeyGrokDefaultBaseURLMode])
	result.MinClaudeCodeVersion = settings[SettingKeyMinClaudeCodeVersion]
	result.MaxClaudeCodeVersion = settings[SettingKeyMaxClaudeCodeVersion]
	result.OpenAITTFTMode = NormalizeOpenAITTFTMode(settings[SettingKeyOpenAITTFTMode])
	if v, ok := settings[SettingKeyEnableFingerprintUnification]; ok && v != "" {
		result.EnableFingerprintUnification = v == "true"
	} else {
		result.EnableFingerprintUnification = true
	}
	result.EnableMetadataPassthrough = settings[SettingKeyEnableMetadataPassthrough] == "true"
	result.EnableCCHSigning = settings[SettingKeyEnableCCHSigning] == "true"
	if v, ok := settings[SettingKeyEnableClaudeOAuthSystemPromptInjection]; ok && v != "" {
		result.EnableClaudeOAuthSystemPromptInjection = v == "true"
	} else {
		result.EnableClaudeOAuthSystemPromptInjection = true
	}
	result.ClaudeOAuthSystemPrompt = settings[SettingKeyClaudeOAuthSystemPrompt]
	result.ClaudeOAuthSystemPromptBlocks = settings[SettingKeyClaudeOAuthSystemPromptBlocks]
	result.EnableAnthropicCacheTTL1hInjection = settings[SettingKeyEnableAnthropicCacheTTL1hInjection] == "true"
	if v, ok := settings[SettingKeyRewriteMessageCacheControl]; ok && v != "" {
		result.RewriteMessageCacheControl = v == "true"
	} else {
		result.RewriteMessageCacheControl = false
	}
	if v, ok := settings[SettingKeyEnableClientDatelineNormalization]; ok && v != "" {
		result.EnableClientDatelineNormalization = v == "true"
	} else {
		result.EnableClientDatelineNormalization = true
	}
	result.AntigravityUserAgentVersion = rules.NormalizeUserAgentVersion(settings[SettingKeyAntigravityUserAgentVersion])
	result.OpenAICodexUserAgent = strings.TrimSpace(settings[SettingKeyOpenAICodexUserAgent])
	result.OpenAIAllowClaudeCodeCodexPlugin = settings[SettingKeyOpenAIAllowClaudeCodeCodexPlugin] == "true"
	result.UserPromptReplacementConfig = parseAdminPromptConfig(settings[SettingKeyUserPromptReplacementConfig])
	return result
}

// parseAdminPromptConfig parses replacement-policy JSON for the administration
// view and logs invalid JSON through slog.Warn. Missing or invalid input returns
// the prompt-policy parser's default configuration for display.
func parseAdminPromptConfig(raw string) *promptpolicy.UserPromptReplacementConfig {
	return promptpolicy.ParseConfig(raw, slog.Warn)
}
