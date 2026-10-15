package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"
)

// beta_header.go defines Anthropic beta tokens and parses configured token
// lists.
// Anthropic request preparation uses beta tokens to select optional Messages
// features and filter fields tied to those features. Configuration can express
// the token list as a comma-separated header or a JSON array, so both forms
// enter the same ordered token parser before platform-specific policy runs.

// These beta tokens identify features used by request preparation, filtering,
// and client fingerprint construction in the Anthropic and Bedrock adapters.
const (
	BetaOAuth                    = "oauth-2025-04-20"                       // OAuth inference feature
	BetaClaudeCode               = "claude-code-20250219"                   // Claude Code attribution
	BetaInterleavedThinking      = "interleaved-thinking-2025-05-14"        // interleaved reasoning blocks
	BetaFineGrainedToolStreaming = "fine-grained-tool-streaming-2025-05-14" // incremental tool arguments
	BetaTokenCounting            = "token-counting-2024-11-01"              // token counting endpoint
	BetaContext1M                = "context-1m-2025-08-07"                  // million-token context feature
	BetaFastMode                 = "fast-mode-2026-02-01"                   // fast processing mode

	// These tokens select cache, effort, reasoning, and context features used
	// by the Claude Code request defaults alongside the attribution tokens above.
	BetaPromptCachingScope = "prompt-caching-scope-2026-01-05" // prompt cache scope
	BetaEffort             = "effort-2025-11-24"               // reasoning effort control
	BetaRedactThinking     = "redact-thinking-2026-02-12"      // reasoning redaction feature
	BetaContextManagement  = "context-management-2025-06-27"   // conversation context editing
	BetaExtendedCacheTTL   = "extended-cache-ttl-2025-04-11"   // extended cache lifetime

	// These tokens control request-body cleanup after the final beta list is
	// selected. Anthropic and Bedrock preparation retain fallbacks for the
	// server-side token; fallback_credit_token also accepts either credit token.
	// Default and Bedrock beta lists omit these optional capabilities. Request
	// sanitizers use the tokens when selecting which fallback fields to remove.
	BetaServerSideFallback   = "server-side-fallback-2026-07-01" // server-selected replacement model
	BetaFallbackCredit       = "fallback-credit-2026-07-01"      // current fallback credit
	BetaFallbackCreditLegacy = "fallback-credit-2026-06-01"      // legacy fallback credit
)

// ParseAnthropicBetaHeader returns trimmed tokens in their supplied order.
// Empty input returns nil. JSON array elements are stringified; invalid array
// input is split on commas, and empty entries are removed without deduplication.
func ParseAnthropicBetaHeader(header string) []string {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	if strings.HasPrefix(header, "[") && strings.HasSuffix(header, "]") {
		var parsed []any
		if err := json.Unmarshal([]byte(header), &parsed); err == nil {
			tokens := make([]string, 0, len(parsed))
			for _, item := range parsed {
				token := strings.TrimSpace(fmt.Sprint(item))
				if token != "" {
					tokens = append(tokens, token)
				}
			}
			return tokens
		}
	}
	// A malformed array still follows the header parser, allowing configuration
	// readers to pass its literal tokens to the platform's later allowlist check.
	var tokens []string
	for part := range strings.SplitSeq(header, ",") {
		t := strings.TrimSpace(part)
		if t != "" {
			tokens = append(tokens, t)
		}
	}
	return tokens
}
