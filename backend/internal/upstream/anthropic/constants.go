package anthropic

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

const (

	// Wire beta values come from the protocol package; this package selects header combinations.
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

	// DefaultBetaHeader is the default Claude Code beta header.
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

	// DefaultCacheControlTTL is the TTL for gateway-generated cache breakpoints.
	// Client-supplied TTLs take precedence; missing values use five minutes.
	DefaultCacheControlTTL = "5m"

	// CLICurrentVersion is the built-in three-part CLI version.
	// Use CLIVersion for outbound headers and billing markers so environment
	// overrides are applied consistently.
	CLICurrentVersion = "2.1.220"

	// DefaultTestModel is used for provider probes.
	DefaultTestModel = "claude-sonnet-4-5-20250929"

	ClaudeAPIURL                    = "https://api.anthropic.com/v1/messages?beta=true"
	ClaudeAPICountTokensURL         = "https://api.anthropic.com/v1/messages/count_tokens?beta=true"
	ClaudeCodeSystemPrompt          = "You are Claude Code, Anthropic's official CLI for Claude."
	ClaudeCodeSystemPromptExpansion = `You are an interactive agent that helps users with software engineering tasks. Use the instructions below and the tools available to you to assist the user.

IMPORTANT: Assist with authorized security testing, defensive security, CTF challenges, and educational contexts. Refuse requests for destructive techniques, DoS attacks, mass targeting, supply chain compromise, or detection evasion for malicious purposes. Dual-use security tools (C2 frameworks, credential testing, exploit development) require clear authorization context: pentesting engagements, CTF competitions, security research, or defensive use cases.
IMPORTANT: You must NEVER generate or guess URLs for the user unless you are confident that the URLs are for helping the user with programming. You may use URLs provided by the user in their messages or local files.

# Tone and style
 - Only use emojis if the user explicitly requests it. Avoid using emojis in all communication unless asked.
 - Your responses should be short and concise.
 - When referencing specific functions or pieces of code include the pattern file_path:line_number to allow the user to easily navigate to the source code location.
 - When referencing GitHub issues or pull requests, use the owner/repo#123 format (e.g. anthropics/claude-code#100) so they render as clickable links.
 - Do not use a colon before tool calls. Your tool calls may not be shown directly in the output, so text like "Let me read the file:" followed by a read tool call should just be "Let me read the file." with a period.`

	MaxCacheControlBlocks         = 4
	CacheTTLTarget1h              = "1h"
	ClaudeCodeBillingHeaderPrefix = anthropic.ClaudeCodeBillingHeaderPrefix

	ClaudeCodeEntrypointMarker = anthropic.ClaudeCodeEntrypointMarker
)

var (
	// DroppedBetas lists tokens removed from outbound beta headers.
	DroppedBetas = []string{}

	// DefaultHeaders contains the Claude Code request fingerprint.
	DefaultHeaders = map[string]string{
		// Keep these in sync with recent Claude CLI traffic to reduce the chance
		// that Claude Code-scoped OAuth credentials are rejected as "non-CLI" usage.
		// Version reference: Parrot src/transform/cc_mimicry.py, CLI_USER_AGENT.
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

	ClaudeCodePromptPrefixes = []string{
		"You are Claude Code, Anthropic's official CLI for Claude",             // Standard CLI and Agent SDK.
		"You are a Claude agent, built on Anthropic's Claude Agent SDK",        // Agent SDK variant.
		"You are a file search specialist for Claude Code",                     // Explore agent.
		"You are a helpful AI assistant tasked with summarizing conversations", // Compaction.
	}
)

// FullClaudeCodeMimicryBetas returns the ordered beta set for OAuth mimic requests,
// including Haiku. Passthrough uses client betas or model defaults; API Key
// requests use APIKeyBetaHeader. Redact-thinking is omitted to retain thinking
// content unless an applicable merge includes a client-supplied token.
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

// IsAnthropicFableModel reports whether model contains "fable", ignoring case.
func IsAnthropicFableModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "fable")
}
