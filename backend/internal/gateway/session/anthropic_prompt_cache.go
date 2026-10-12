package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Anthropic Messages continuations can reuse a prompt cache key when the new
// request extends a previously bound digest chain. Bindings include both provider
// and API Key IDs, so identical content sent through another provider or key has
// its own cache identity. Lookup walks complete chain components from the longest
// candidate to shorter prefixes and removes unusable entries that it encounters.
//
// A replacement publishes the new chain before deleting a different old chain,
// keeping the continuation available during that sequence. Each map operation is
// concurrent-safe, but lookup cleanup and replacement are separate operations:
// cleanup can delete a concurrently replaced entry. Expiration occurs at the
// deadline, and entries receive no background cleanup.

// anthropicPromptBinding holds a prompt key and its lookup expiration deadline.
type anthropicPromptBinding struct {
	PromptCacheKey string // trimmed prompt cache identity
	// ExpiresAt ends validity at its deadline; a zero value keeps lookup validity unbounded.
	ExpiresAt time.Time
}

// AnthropicPromptCache keeps process-local provider/key digest bindings for Messages conversion.
// The clock is used by concurrent reads and writes, so an injected clock needs
// its own synchronization when its state can change during those calls.
type AnthropicPromptCache struct {
	entries sync.Map // namespaced digest binding entries
	// clock supplies concurrent lookup and binding time; injected mutable state
	// requires caller synchronization.
	clock func() time.Time
}

// BuildAnthropicDigestChain hashes supplied system and message JSON bytes into an
// ordered, role-labelled chain. A nil request or absent usable content returns an
// empty chain. System null is omitted; messages with empty bytes are omitted,
// while other message bytes, including null, participate.
//
// Only the exact trimmed assistant role receives an assistant prefix. Every
// other role receives a user prefix, and the raw content representation affects
// each hash. Callers use this chain for prefix continuity, so reformatting JSON
// or changing role classification can change which binding matches.
func BuildAnthropicDigestChain(req *protocolanthropic.AnthropicRequest) string {
	if req == nil {
		return ""
	}

	parts := make([]string, 0, len(req.Messages)+1)
	if len(req.System) > 0 && strings.TrimSpace(string(req.System)) != "" && strings.TrimSpace(string(req.System)) != "null" {
		parts = append(parts, "s:"+upstream.ShortHash(req.System))
	}
	for _, msg := range req.Messages {
		content := msg.Content
		if len(content) == 0 || strings.TrimSpace(string(content)) == "" {
			continue
		}
		prefix := "u"
		if strings.TrimSpace(msg.Role) == "assistant" {
			prefix = "a"
		}
		parts = append(parts, prefix+":"+upstream.ShortHash(content))
	}
	return strings.Join(parts, "-")
}

// anthropicDigestNamespace separates provider/key pairs with decimal IDs and delimiters.
// A nonpositive provider ID disables binding; the API Key ID is included as
// supplied, including zero or a negative value.
func anthropicDigestNamespace(providerID int64, cAPIKeyID int64) string {
	if providerID <= 0 {
		return ""
	}
	return fmt.Sprintf("%d|%d|", providerID, cAPIKeyID)
}

// NewAnthropicPromptCache constructs an empty process-local digest binding cache.
// A nil clock selects time.Now; an injected clock is called by concurrent cache
// operations and must support that use.
func NewAnthropicPromptCache(clock func() time.Time) *AnthropicPromptCache {
	if clock == nil {
		clock = time.Now
	}
	return &AnthropicPromptCache{clock: clock}
}

// Find returns the prompt key and longest live component prefix in a provider/key
// namespace. A nil cache, empty chain, or nonpositive provider returns two empty
// strings. Keys are trimmed before acceptance, and expiration rejects at the exact
// deadline unless the stored deadline is zero.
//
// Each rejected map value is deleted before trying a shorter prefix. The chain
// is shortened at its final hyphen, so callers supply BuildAnthropicDigestChain's
// component format. Lookup and deletion are separate map operations; a concurrent
// replacement at that key can be deleted by cleanup of an older loaded value.
func (s *AnthropicPromptCache) Find(providerID int64, cAPIKeyID int64, digestChain string) (promptCacheKey string, matchedChain string) {
	if s == nil || digestChain == "" {
		return "", ""
	}
	ns := anthropicDigestNamespace(providerID, cAPIKeyID)
	if ns == "" {
		return "", ""
	}
	chain := digestChain
	for {
		if raw, ok := s.entries.Load(ns + chain); ok {
			if binding, ok := raw.(anthropicPromptBinding); ok {
				if binding.ExpiresAt.IsZero() || s.now().Before(binding.ExpiresAt) {
					if key := strings.TrimSpace(binding.PromptCacheKey); key != "" {
						return key, chain
					}
				}
			}
			// Remove unusable prefixes so later lookups can reach a live ancestor;
			// this separate delete can race with replacement of the loaded entry.
			s.entries.Delete(ns + chain)
		}
		// Remove a complete final digest component to reuse an earlier stored
		// conversation prefix; slicing inside a content hash would create unrelated candidates.
		i := strings.LastIndex(chain, "-")
		if i < 0 {
			return "", ""
		}
		chain = chain[:i]
	}
}

// Bind publishes a trimmed prompt key for the supplied provider/key digest chain.
// Nil caches, empty chains or keys, and nonpositive providers are ignored. TTL is
// added as supplied, so nonpositive durations normally expire by the next lookup.
//
// The new chain is stored before a different old chain is removed. This order
// makes a growing continuation discoverable before its old prefix disappears,
// while readers can still observe either map operation independently. Callers
// supply the old chain from the same namespace; concurrent same-key replacement
// or lookup cleanup is not one atomic transaction.
func (s *AnthropicPromptCache) Bind(providerID int64, cAPIKeyID int64, digestChain, promptCacheKey, oldDigestChain string, ttl time.Duration) {
	if s == nil || digestChain == "" || strings.TrimSpace(promptCacheKey) == "" {
		return
	}
	ns := anthropicDigestNamespace(providerID, cAPIKeyID)
	if ns == "" {
		return
	}
	binding := anthropicPromptBinding{
		PromptCacheKey: strings.TrimSpace(promptCacheKey),
		ExpiresAt:      s.now().Add(ttl),
	}
	// Publish the continuation before retiring its prior prefix so extension
	// lookup can find the new chain while these separate map mutations proceed.
	s.entries.Store(ns+digestChain, binding)
	if oldDigestChain != "" && oldDigestChain != digestChain {
		s.entries.Delete(ns + oldDigestChain)
	}
}

// AnthropicDigestPromptCacheKey derives a labelled hash from a trimmed digest
// chain. Whitespace-only input returns empty; HashSensitiveValueForLog trims
// nonblank input too, so adding surrounding whitespace retains the same key.
func AnthropicDigestPromptCacheKey(digestChain string) string {
	if strings.TrimSpace(digestChain) == "" {
		return ""
	}
	return "anthropic-digest-" + upstream.HashSensitiveValueForLog(digestChain)
}

// AnthropicMetadataPromptCacheKey derives a prompt key from parsed client identity.
// A nil request, malformed metadata, unrecognized user_id, or blank session ID
// returns empty. Device, account, and session components are trimmed before joining.
//
// The seed includes an anthropic-metadata label and separates the three identity
// components with vertical bars before hashing. Component values are unescaped,
// so embedded bars can give distinct identity tuples the same seed. Compatible
// Messages requests can use this declared identity independently of content changes.
// The parser defines accepted user_id formats; callers still apply their provider
// and API Key isolation when using the resulting key.
func AnthropicMetadataPromptCacheKey(req *protocolanthropic.AnthropicRequest) string {
	if req == nil || len(req.Metadata) == 0 {
		return ""
	}
	var metadata struct {
		UserID string `json:"user_id"` // encoded client identity string
	}
	// Malformed metadata cannot identify a reusable session; let the caller
	// choose its other prompt-key evidence by returning an empty candidate.
	if err := json.Unmarshal(req.Metadata, &metadata); err != nil {
		return ""
	}
	parsed := protocolanthropic.ParseMetadataUserID(metadata.UserID)
	if parsed == nil || strings.TrimSpace(parsed.SessionID) == "" {
		return ""
	}
	seed := strings.Join([]string{
		"anthropic-metadata",
		strings.TrimSpace(parsed.DeviceID),
		strings.TrimSpace(parsed.AccountUUID),
		strings.TrimSpace(parsed.SessionID),
	}, "|")
	return "anthropic-metadata-" + upstream.HashSensitiveValueForLog(seed)
}

// CloneAnthropicDigestRequest returns a shallow request copy with separate nonempty
// system bytes and message-slice storage. Nil returns nil; message content,
// metadata, and other nested values remain shared and need caller-controlled mutation.
func CloneAnthropicDigestRequest(req *protocolanthropic.AnthropicRequest) *protocolanthropic.AnthropicRequest {
	if req == nil {
		return nil
	}
	cp := *req
	if len(req.System) > 0 {
		cp.System = append(json.RawMessage(nil), req.System...)
	}
	if len(req.Messages) > 0 {
		cp.Messages = append([]protocolanthropic.AnthropicMessage(nil), req.Messages...)
	}
	return &cp
}

// now returns the configured clock reading for cache expiration calculations.
// A missing clock uses time.Now, including a manually constructed cache whose
// clock field was left nil.
func (s *AnthropicPromptCache) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}
