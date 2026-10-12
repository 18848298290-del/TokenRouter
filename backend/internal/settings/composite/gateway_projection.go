package composite

import (
	"github.com/TokenFlux/TokenRouter/internal/gateway"
)

// GatewayAdminSettings extracts gateway settings from the snapshot.
func (s *Snapshot) GatewayAdminSettings() gateway.AdminSettings {
	return gateway.AdminSettings{
		AntigravityUserAgentVersion:            s.AntigravityUserAgentVersion,
		BackendModeEnabled:                     s.BackendModeEnabled,
		ClaudeOAuthSystemPrompt:                s.ClaudeOAuthSystemPrompt,
		ClaudeOAuthSystemPromptBlocks:          s.ClaudeOAuthSystemPromptBlocks,
		EnableAnthropicCacheTTL1hInjection:     s.EnableAnthropicCacheTTL1hInjection,
		EnableCCHSigning:                       s.EnableCCHSigning,
		EnableClaudeOAuthSystemPromptInjection: s.EnableClaudeOAuthSystemPromptInjection,
		EnableClientDatelineNormalization:      s.EnableClientDatelineNormalization,
		EnableFingerprintUnification:           s.EnableFingerprintUnification,
		EnableIdentityPatch:                    s.EnableIdentityPatch,
		EnableMetadataPassthrough:              s.EnableMetadataPassthrough,
		GrokDefaultBaseURLMode:                 s.GrokDefaultBaseURLMode,
		GrokDefaultTextModel:                   s.GrokDefaultTextModel,
		IdentityPatchPrompt:                    s.IdentityPatchPrompt,
		MaxClaudeCodeVersion:                   s.MaxClaudeCodeVersion,
		MinClaudeCodeVersion:                   s.MinClaudeCodeVersion,
		OpenAIAllowClaudeCodeCodexPlugin:       s.OpenAIAllowClaudeCodeCodexPlugin,
		OpenAICodexUserAgent:                   s.OpenAICodexUserAgent,
		OpenAITTFTMode:                         s.OpenAITTFTMode,
		RewriteMessageCacheControl:             s.RewriteMessageCacheControl,
		UserPromptReplacementConfig:            s.UserPromptReplacementConfig,
	}
}

// ApplyGatewayAdminReadSettings copies gateway request processing and model defaults into the snapshot.
func (s *Snapshot) ApplyGatewayAdminReadSettings(value *gateway.AdminReadSettings) {
	s.AntigravityUserAgentVersion = value.AntigravityUserAgentVersion
	s.BackendModeEnabled = value.BackendModeEnabled
	s.ClaudeOAuthSystemPrompt = value.ClaudeOAuthSystemPrompt
	s.ClaudeOAuthSystemPromptBlocks = value.ClaudeOAuthSystemPromptBlocks
	s.EnableAnthropicCacheTTL1hInjection = value.EnableAnthropicCacheTTL1hInjection
	s.EnableCCHSigning = value.EnableCCHSigning
	s.EnableClaudeOAuthSystemPromptInjection = value.EnableClaudeOAuthSystemPromptInjection
	s.EnableClientDatelineNormalization = value.EnableClientDatelineNormalization
	s.EnableFingerprintUnification = value.EnableFingerprintUnification
	s.EnableIdentityPatch = value.EnableIdentityPatch
	s.EnableMetadataPassthrough = value.EnableMetadataPassthrough
	s.GrokDefaultBaseURLMode = value.GrokDefaultBaseURLMode
	s.GrokDefaultTextModel = value.GrokDefaultTextModel
	s.IdentityPatchPrompt = value.IdentityPatchPrompt
	s.MaxClaudeCodeVersion = value.MaxClaudeCodeVersion
	s.MinClaudeCodeVersion = value.MinClaudeCodeVersion
	s.OpenAIAllowClaudeCodeCodexPlugin = value.OpenAIAllowClaudeCodeCodexPlugin
	s.OpenAICodexUserAgent = value.OpenAICodexUserAgent
	s.OpenAITTFTMode = value.OpenAITTFTMode
	s.RewriteMessageCacheControl = value.RewriteMessageCacheControl
	s.UserPromptReplacementConfig = value.UserPromptReplacementConfig
}
