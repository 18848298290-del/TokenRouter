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

type anthropicPromptBinding struct {
	PromptCacheKey string
	ExpiresAt      time.Time
}

// AnthropicPromptCache stores digest bindings scoped by provider and API key. Reads remove expired entries.
type AnthropicPromptCache struct {
	entries sync.Map
	clock   func() time.Time
}

// BuildAnthropicDigestChain joins role-prefixed hashes of system content and each nonempty message.
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

func anthropicDigestNamespace(providerID int64, cAPIKeyID int64) string {
	if providerID <= 0 {
		return ""
	}
	return fmt.Sprintf("%d|%d|", providerID, cAPIKeyID)
}

// NewAnthropicPromptCache uses the supplied clock, or time.Now when nil.
func NewAnthropicPromptCache(clock func() time.Time) *AnthropicPromptCache {
	if clock == nil {
		clock = time.Now
	}
	return &AnthropicPromptCache{clock: clock}
}

// Find returns the longest valid digest prefix and removes expired bindings encountered during lookup.
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
			s.entries.Delete(ns + chain)
		}
		i := strings.LastIndex(chain, "-")
		if i < 0 {
			return "", ""
		}
		chain = chain[:i]
	}
}

// Bind stores a binding with its own TTL, then removes a different old continuation chain.
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
	s.entries.Store(ns+digestChain, binding)
	if oldDigestChain != "" && oldDigestChain != digestChain {
		s.entries.Delete(ns + oldDigestChain)
	}
}

// AnthropicDigestPromptCacheKey hashes the chain in the anthropic-digest namespace.
func AnthropicDigestPromptCacheKey(digestChain string) string {
	if strings.TrimSpace(digestChain) == "" {
		return ""
	}
	return "anthropic-digest-" + upstream.HashSensitiveValueForLog(digestChain)
}

// AnthropicMetadataPromptCacheKey hashes the device, account, and session IDs in client metadata.
func AnthropicMetadataPromptCacheKey(req *protocolanthropic.AnthropicRequest) string {
	if req == nil || len(req.Metadata) == 0 {
		return ""
	}
	var metadata struct {
		UserID string `json:"user_id"`
	}
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

// CloneAnthropicDigestRequest copies the request, system bytes, and message slice.
// Nested message content remains shared.
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

func (s *AnthropicPromptCache) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}
