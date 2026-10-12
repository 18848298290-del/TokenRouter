package composite

import (
	"github.com/TokenFlux/TokenRouter/internal/gateway"
)

// Gateway administrator values mix model defaults, request header choices,
// and prompt rules in the combined Snapshot. Extraction supplies the gateway
// preparer with those fields, and readback transfers the gateway reader's result;
// the prompt-replacement pointer remains shared during either transfer.

// GatewayAdminSettings returns forwarding, model, and prompt controls for gateway
// preparation. UserPromptReplacementConfig retains the Snapshot's pointer and nested
// rules; callers keep that object stable while it is encoded or compared for audit.
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

// ApplyGatewayAdminReadSettings transfers gateway model defaults and request
// processing fields from a nonnil read result. Prompt replacement retains the
// returned configuration pointer, while scalar header and prompt text values are
// assigned into the combined Snapshot.
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
