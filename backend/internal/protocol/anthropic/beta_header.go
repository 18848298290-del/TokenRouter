package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Beta header tokens used by the Anthropic request policies.
//
// The Claude Code mimicry set follows CLI traffic observed in April 2026
// and Parrot's BETAS in src/transform/cc_mimicry.py.
// The full anthropic-beta set participates in upstream client attribution;
// an incomplete set can produce the extra-usage rejection:
// Third-party apps now draw from your extra usage, not your plan limits.
const (
	BetaOAuth                    = "oauth-2025-04-20"
	BetaClaudeCode               = "claude-code-20250219"
	BetaInterleavedThinking      = "interleaved-thinking-2025-05-14"
	BetaFineGrainedToolStreaming = "fine-grained-tool-streaming-2025-05-14"
	BetaTokenCounting            = "token-counting-2024-11-01"
	BetaContext1M                = "context-1m-2025-08-07"
	BetaFastMode                 = "fast-mode-2026-02-01"

	// These tokens appear in CLI 2.1.9x traffic and later.
	BetaPromptCachingScope = "prompt-caching-scope-2026-01-05"
	BetaEffort             = "effort-2025-11-24"
	BetaRedactThinking     = "redact-thinking-2026-02-12"
	BetaContextManagement  = "context-management-2025-06-27"
	BetaExtendedCacheTTL   = "extended-cache-ttl-2025-04-11"

	// These tokens gate the beta Messages refusal-fallback fields.
	// Clients may send body.fallbacks and body.fallback_credit_token;
	// the upstream requires the corresponding anthropic-beta token.
	// A missing token can yield "fallbacks: Extra inputs are not permitted".
	// Use these tokens to decide whether to strip or retain those fields.
	// Exclude them from default, mimicry, API Key, and Bedrock allowlists:
	// server-side fallback can change the model and billing.
	BetaServerSideFallback   = "server-side-fallback-2026-07-01"
	BetaFallbackCredit       = "fallback-credit-2026-07-01"
	BetaFallbackCreditLegacy = "fallback-credit-2026-06-01"
)

// ParseAnthropicBetaHeader parses comma-separated tokens or a JSON array, trimming empty entries.
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
	var tokens []string
	for part := range strings.SplitSeq(header, ",") {
		t := strings.TrimSpace(part)
		if t != "" {
			tokens = append(tokens, t)
		}
	}
	return tokens
}
