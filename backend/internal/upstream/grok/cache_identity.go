package grok

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

type CacheIdentityInput struct {
	APIKeyID                                                         int64
	Compact                                                          bool
	Model, ClaudeSession, HeaderSession, ConversationID, ExplicitKey string
	StablePrefixSeed, AnchoredSeed, PreviousResponseSeed             func([]byte) string
}

// ResolveCacheIdentity derives a tenant-scoped routing identity for xAI prompt caching.
// The returned UUID hashes the client session identifier before sending it upstream.
//
// A positive downstream API key ID is required. Probes and incomplete request contexts
// disable caching to avoid sharing an identity between unrelated tenants.
func ResolveCacheIdentity(input CacheIdentityInput, body []byte) string {
	apiKeyID := input.APIKeyID
	if apiKeyID <= 0 {
		return ""
	}
	// Compact requests are summary operations and do not receive cache identities
	// or free-tier routing fields.
	if input.Compact {
		return ""
	}

	model := strings.ToLower(strings.TrimSpace(input.Model))
	if model == "" {
		return ""
	}

	seed := ExplicitCacheSeed(input, body)
	if seed == "" {
		seed = input.StablePrefixSeed(body)
		if seed == "" {
			// Anchor requests without a reusable prefix to the first user input so unrelated
			// prompts do not share a cache key merely because they use the same model.
			seed = input.AnchoredSeed(body)
		}
	}
	if seed == "" {
		return ""
	}

	// GenerateSessionUUID hashes the complete seed. The versioned namespace separates
	// prompt-cache identities from other upstream session identifiers.
	isolatedSeed := fmt.Sprintf("grok-prompt-cache:v1:%d:%s:%s", apiKeyID, model, seed)
	return upstream.GenerateSessionUUID(isolatedSeed)
}

func ExplicitCacheSeed(input CacheIdentityInput, body []byte) string {
	// Prefer the Claude Code session for multi-turn Messages bridges so their cache
	// routing follows the same session identity as CPA.
	seed := strings.TrimSpace(input.ClaudeSession)
	if seed == "" {
		seed = ExtractClaudeCodeSessionIDFromPayload(body)
	}
	if seed == "" {
		seed = input.HeaderSession
	}
	// Prefer prompt_cache_key to the per-call X-Grok-Conv-Id. Grok Build recap
	// requests can use the parent session as prompt_cache_key while assigning
	// a distinct conversation header to each summary or title request.
	// Keeping the parent identity lets these requests reuse its cache prefix.
	if seed == "" && len(body) > 0 {
		seed = strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
	}
	if seed == "" {
		seed = strings.TrimSpace(input.ConversationID)
	}
	if seed == "" {
		seed = strings.TrimSpace(input.ExplicitKey)
	}
	// Use the previous response as the last available session seed. The isolation
	// seed already includes the model; PreviousResponseSeed rejects message IDs.
	if seed == "" && len(body) > 0 {
		seed = input.PreviousResponseSeed(body)
	}
	return seed
}
