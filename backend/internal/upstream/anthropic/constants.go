package anthropic

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// constants.go defines Claude request endpoints, CLI identity defaults, and
// beta token combinations.
// Claude request defaults combine protocol beta constants with CLI prompt,
// Header, and cache settings. OAuth mimic, OAuth passthrough, CountTokens, and
// API Key requests select different beta combinations, so sharing the wire
// token names does not make their defaults interchangeable.

// Protocol beta names and platform defaults determine capability Headers, probe
// models, prompt templates, and cache-marker settings for request preparation.
const (

	// Wire beta values come from the protocol package; this package selects header
	// combinations.
	BetaOAuth                    = anthropic.BetaOAuth
	BetaClaudeCode               = anthropic.BetaClaudeCode
	BetaInterleavedThinking      = anthropic.BetaInterleavedThinking
	BetaFineGrainedToolStreaming = anthropic.BetaFineGrainedToolStreaming
	BetaTokenCounting            = anthropic.BetaTokenCounting
	BetaContext1M                = anthropic.BetaContext1M
	BetaFastMode                 = anthropic.BetaFastMode
	BetaPromptCachingScope       = anthropic.BetaPromptCachingScope
	BetaEffort                   = anthropic.BetaEffort
	BetaRedactThinking           = anthropic.BetaRedactThinking
	BetaContextManagement        = anthropic.BetaContextManagement
	BetaExtendedCacheTTL         = anthropic.BetaExtendedCacheTTL
	BetaServerSideFallback       = anthropic.BetaServerSideFallback
	BetaFallbackCredit           = anthropic.BetaFallbackCredit
	BetaFallbackCreditLegacy     = anthropic.BetaFallbackCreditLegacy

	// DefaultBetaHeader begins the platform's authentication-specific beta
	// combinations, which request builders select according to endpoint, model
	// family, and mimic mode.
	DefaultBetaHeader = BetaClaudeCode + "," + BetaOAuth + "," + BetaInterleavedThinking + "," + BetaFineGrainedToolStreaming

	// CountTokensBetaHeader is the default count_tokens beta header.
	CountTokensBetaHeader = BetaClaudeCode + "," + BetaOAuth + "," + BetaInterleavedThinking + "," + BetaTokenCounting

	// HaikuBetaHeader is the Haiku OAuth passthrough default.
	// OAuth mimic uses FullClaudeCodeMimicryBetas for all models.
	HaikuBetaHeader = BetaOAuth + "," + BetaInterleavedThinking

	// APIKeyBetaHeader is the default beta header for API Key requests.
	APIKeyBetaHeader = BetaClaudeCode + "," + BetaInterleavedThinking + "," + BetaFineGrainedToolStreaming

	// APIKeyHaikuBetaHeader enables interleaved thinking for API Key Haiku requests.
	APIKeyHaikuBetaHeader = BetaInterleavedThinking

	// DefaultCacheControlTTL supplies generated ephemeral markers. Message cache
	// injection fills absent or empty ttl values with this default;
	// ForceEphemeralCacheControlTTL can later override existing values.
	DefaultCacheControlTTL = "5m"

	// CLICurrentVersion is the built-in three-part CLI version.
	// Use CLIVersion for outbound headers and billing markers so environment
	// overrides are applied consistently.
	CLICurrentVersion = "2.1.220"

	// DefaultTestModel supplies the model used by provider probes when they have
	// no more specific test model configured.
	DefaultTestModel = "claude-sonnet-4-5-20250929"

	// ClaudeAPIURL and ClaudeAPICountTokensURL identify the default Messages
	// endpoints, including the beta query marker used during their request
	// construction.
	ClaudeAPIURL            = "https://api.anthropic.com/v1/messages?beta=true"
	ClaudeAPICountTokensURL = "https://api.anthropic.com/v1/messages/count_tokens?beta=true"
	// ClaudeCodeSystemPrompt provides the recognized CLI identity text used in
	// injected system blocks and client-banner comparison during OAuth request
	// preparation.
	ClaudeCodeSystemPrompt = "You are Claude Code, Anthropic's official CLI for Claude."
	// ClaudeCodeSystemPromptExpansion supplies the default instruction text used
	// when an OAuth system-block expansion has no configured nonblank value.
	ClaudeCodeSystemPromptExpansion = `You are an interactive agent that helps users with software engineering tasks. Use the instructions below and the tools available to you to assist the user.

IMPORTANT: Assist with authorized security testing, defensive security, CTF challenges, and educational contexts. Refuse requests for destructive techniques, DoS attacks, mass targeting, supply chain compromise, or detection evasion for malicious purposes. Dual-use security tools (C2 frameworks, credential testing, exploit development) require clear authorization context: pentesting engagements, CTF competitions, security research, or defensive use cases.
IMPORTANT: You must NEVER generate or guess URLs for the user unless you are confident that the URLs are for helping the user with programming. You may use URLs provided by the user in their messages or local files.

# Tone and style
 - Only use emojis if the user explicitly requests it. Avoid using emojis in all communication unless asked.
 - Your responses should be short and concise.
 - When referencing specific functions or pieces of code include the pattern file_path:line_number to allow the user to easily navigate to the source code location.
 - When referencing GitHub issues or pull requests, use the owner/repo#123 format (e.g. anthropics/claude-code#100) so they render as clickable links.
 - Do not use a colon before tool calls. Your tool calls may not be shown directly in the output, so text like "Let me read the file:" followed by a read tool call should just be "Let me read the file." with a period.`

	// MaxCacheControlBlocks and cache-marker values control block-count reduction,
	// forced lifetime selection, and recognition of CLI billing attribution in
	// system text.
	MaxCacheControlBlocks         = 4
	CacheTTLTarget1h              = "1h"
	ClaudeCodeBillingHeaderPrefix = anthropic.ClaudeCodeBillingHeaderPrefix

	ClaudeCodeEntrypointMarker = anthropic.ClaudeCodeEntrypointMarker
)

// Static beta drops, default fingerprint Headers, and recognized client banners
// supply process-wide inputs to request preparation and client identification.
var (
	// DroppedBetas supplies the static exclusions copied into beta drop sets;
	// configured beta policy contributes additional per-request filter tokens.
	DroppedBetas = []string{}

	// DefaultHeaders supplies the initialized CLI fingerprint values used to fill
	// OAuth Headers and replace client fingerprint values in mimic mode.
	DefaultHeaders = map[string]string{
		// User-Agent reads the same initialized version as billing templates.
		// The other fixed fields define the SDK and runtime identity sent by
		// the default Header and mimic helpers.
		"User-Agent":                                "claude-cli/" + CLIVersion() + " (external, cli)",
		"X-Stainless-Lang":                          "js",
		"X-Stainless-Package-Version":               "0.94.0",
		"X-Stainless-OS":                            "Linux",
		"X-Stainless-Arch":                          "arm64",
		"X-Stainless-Runtime":                       "node",
		"X-Stainless-Runtime-Version":               "v24.3.0",
		"X-Stainless-Retry-Count":                   "0",
		"X-Stainless-Timeout":                       "600",
		"X-App":                                     "cli",
		"Anthropic-Dangerous-Direct-Browser-Access": "true",
	}

	// ClaudeCodePromptPrefixes identifies recognized leading banners for ordinary
	// CLI, Agent SDK, explore, and compaction system text during request
	// preparation.
	ClaudeCodePromptPrefixes = []string{
		"You are Claude Code, Anthropic's official CLI for Claude",             // Standard CLI and Agent SDK.
		"You are a Claude agent, built on Anthropic's Claude Agent SDK",        // Agent SDK variant.
		"You are a file search specialist for Claude Code",                     // Explore agent.
		"You are a helpful AI assistant tasked with summarizing conversations", // Compaction.
	}
)

// FullClaudeCodeMimicryBetas returns a fresh ordered slice for OAuth mimic
// across models, including Haiku. It includes context-management and
// extended-cache capabilities; client redact-thinking can enter through a
// separate applicable merge.
func FullClaudeCodeMimicryBetas() []string {
	return []string{
		BetaClaudeCode,
		BetaOAuth,
		BetaInterleavedThinking,
		BetaPromptCachingScope,
		BetaEffort,
		BetaContextManagement,
		BetaExtendedCacheTTL,
	}
}

// IsAnthropicFableModel reports a case-insensitive fable substring in model.
func IsAnthropicFableModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "fable")
}
