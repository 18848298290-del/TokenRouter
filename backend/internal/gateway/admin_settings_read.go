package gateway

import (
	"log/slog"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/gateway/promptpolicy"
)

// AdminReadSettings contains gateway settings displayed by the administration page.
type AdminReadSettings struct {
	AntigravityUserAgentVersion            string
	BackendModeEnabled                     bool
	ClaudeOAuthSystemPrompt                string
	ClaudeOAuthSystemPromptBlocks          string
	EnableAnthropicCacheTTL1hInjection     bool
	EnableCCHSigning                       bool
	EnableClaudeOAuthSystemPromptInjection bool
	EnableClientDatelineNormalization      bool
	EnableFingerprintUnification           bool
	EnableIdentityPatch                    bool
	EnableMetadataPassthrough              bool
	GrokDefaultBaseURLMode                 string
	GrokDefaultTextModel                   string
	IdentityPatchPrompt                    string
	MaxClaudeCodeVersion                   string
	MinClaudeCodeVersion                   string
	OpenAIAllowClaudeCodeCodexPlugin       bool
	OpenAICodexUserAgent                   string
	OpenAITTFTMode                         string
	RewriteMessageCacheControl             bool
	UserPromptReplacementConfig            *promptpolicy.UserPromptReplacementConfig
}

// ReadAdminSettings parses persisted values and applies defaults for missing settings.
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

// parseAdminPromptConfig parses prompt policy settings and logs diagnostics.
func parseAdminPromptConfig(raw string) *promptpolicy.UserPromptReplacementConfig {
	return promptpolicy.ParseConfig(raw, slog.Warn)
}
