package grok

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// A prompt-cache identity combines the downstream API Key, final model, and
// selected conversation or content seed. Requests lacking a positive key,
// requests marked compact, or requests with no model return an empty
// identity, allowing callers to remove upstream cache routing for those
// cases.
//
// Explicit session signals precede content seeds so later turns can reuse a
// declared conversation. Content callbacks provide a stable prefix, then an
// initial-input anchor when the prefix is absent. The selected seed is hashed
// inside a versioned namespace with the key and normalized model; identical
// session text under different keys or normalized model IDs therefore
// produces different seed input. Callback requirements depend on which earlier signals are
// absent.

// CacheIdentityInput supplies tenant, model, session, and content-seed inputs
// for Grok cache routing.
type CacheIdentityInput struct {
	// APIKeyID and Compact gate ResolveCacheIdentity before Model or seed
	// callbacks are read. Model participates in the digest after trimming and
	// lowercasing; callers pass the final upstream model to isolate its cache
	// routing.
	APIKeyID int64 // downstream API Key ID
	Compact  bool  // request is compact
	// Model is the final upstream model ID. ClaudeSession is the declared Claude
	// client session. HeaderSession is the explicit header session. ConversationID
	// is the Grok conversation header ID. ExplicitKey is the supplied cache key.
	// ExplicitCacheSeed defines their priority. HeaderSession is used as supplied,
	// so even whitespace text suppresses later choices after it is selected.
	Model, ClaudeSession, HeaderSession, ConversationID, ExplicitKey string // direct request identity signals
	// StablePrefixSeed supplies a reusable instruction/tool prefix seed.
	// AnchoredSeed supplies an initial user-input anchor seed. PreviousResponseSeed
	// supplies a recognized previous-response seed. Each callback is required when
	// its selection branch is reached and reads the supplied body.
	StablePrefixSeed, AnchoredSeed, PreviousResponseSeed func([]byte) string // request content seed functions
}

// ResolveCacheIdentity returns a UUID-shaped digest for a selected Grok cache
// seed. A nonpositive APIKeyID, compact request, blank model, or absent seed
// returns empty. Supply StablePrefixSeed and AnchoredSeed when explicit
// signals may be absent, and PreviousResponseSeed when its body-dependent
// branch can be reached; these callbacks are invoked without nil checks.
//
// ExplicitCacheSeed chooses session evidence first. Otherwise a stable prefix
// wins before the anchor callback, allowing instruction/tool prefixes to
// remain shared across turns under the same key and model. The versioned
// digest seed includes APIKeyID and the trimmed, lowercased model, so equal
// session signals from different keys or normalized model IDs have distinct
// inputs to the digest.
func ResolveCacheIdentity(input CacheIdentityInput, body []byte) string {
	apiKeyID := input.APIKeyID
	if apiKeyID <= 0 {
		return ""
	}
	// Compact bypasses cache identity construction before callbacks are reached,
	// letting the caller remove routing fields for this summary operation.
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
			// A missing reusable prefix delegates to the caller's initial-input anchor,
			// so the seed still identifies content instead of using the model alone.
			seed = input.AnchoredSeed(body)
		}
	}
	if seed == "" {
		return ""
	}

	// Hash the scoped seed after selection so a raw session signal never becomes
	// the upstream identifier by itself, and other session namespaces use different input.
	isolatedSeed := fmt.Sprintf("grok-prompt-cache:v1:%d:%s:%s", apiKeyID, model, seed)
	return upstream.GenerateSessionUUID(isolatedSeed)
}

// ExplicitCacheSeed selects ClaudeSession, payload Claude metadata,
// HeaderSession, body prompt_cache_key, ConversationID, ExplicitKey, then a
// previous-response callback. HeaderSession is retained as supplied; the
// other direct session strings are trimmed. Supply PreviousResponseSeed when
// a nonempty body may reach the last branch.
//
// Choosing a body prompt_cache_key before ConversationID allows a parent
// session signal to win over a per-request conversation header. This function
// returns the selected seed directly; ResolveCacheIdentity adds tenant and
// model scope before hashing it. Rejection of invalid previous-response
// formats belongs to the supplied callback.
func ExplicitCacheSeed(input CacheIdentityInput, body []byte) string {
	// Claude session signals win before other explicit identities, keeping a
	// declared Messages conversation selected when additional routing headers are present.
	seed := strings.TrimSpace(input.ClaudeSession)
	if seed == "" {
		seed = ExtractClaudeCodeSessionIDFromPayload(body)
	}
	if seed == "" {
		seed = input.HeaderSession
	}
	// Read the body prompt_cache_key before the conversation header so a parent
	// session key remains selected when an individual request has its own header ID.
	if seed == "" && len(body) > 0 {
		seed = strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
	}
	if seed == "" {
		seed = strings.TrimSpace(input.ConversationID)
	}
	if seed == "" {
		seed = strings.TrimSpace(input.ExplicitKey)
	}
	// With no direct session signal, delegate continuation interpretation to the
	// caller. Its callback decides which previous-response values can identify a session.
	if seed == "" && len(body) > 0 {
		seed = input.PreviousResponseSeed(body)
	}
	return seed
}
