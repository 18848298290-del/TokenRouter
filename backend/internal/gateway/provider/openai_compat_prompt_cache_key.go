package provider

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Compatibility cache identity follows stable request content rather than the
// entire growing conversation. Chat seeds include system text, the first user
// content, and selected model/tool configuration; ordinary Anthropic seeds use
// the same opening-content approach, while explicit ephemeral anchors select a
// separate identity path.
//
// Anchor identity includes all qualifying system and assistant text blocks
// plus one first-user text anchor, and it excludes model/configuration fields.
// That distinction lets callers recognize bridge keys by prefix but does not
// make every pair of different requests distinct. JSON normalization and
// hashing happen after seed selection; unencodable tool declarations or
// malformed anchor content are omitted so remaining evidence can still produce
// a key.

// compatPromptCacheKeyPrefix identifies hashes derived from compatibility opening
// content and configuration, distinguishing them from explicit Anthropic anchor keys.
const compatPromptCacheKeyPrefix = "compat_cc_"

// ShouldAutoInjectPromptCacheKeyForCompat recognizes automatic-key model
// identities after trimming and case folding. Exact gpt-6-astra, gpt-5
// prefixes, and names containing codex qualify; other GPT-6 spellings and
// vendor-prefixed Astra names do not.
func ShouldAutoInjectPromptCacheKeyForCompat(model string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(model))
	return trimmed == "gpt-6-astra" || strings.HasPrefix(trimmed, "gpt-5") || strings.Contains(trimmed, "codex")
}

// DeriveCompatPromptCacheKey hashes opening Chat content and selected request
// configuration for compatibility caching. Nil requests return empty, and a
// blank mappedModel uses the trimmed request model.
//
// The seed includes effort, tool choice, serializable tool/function
// declarations, every system message, and the first user message. Later user
// and assistant turns stay outside this seed so appended history can reuse a
// stable opening identity. Failed declaration encoding omits that seed
// component, while NormalizeCompatSeedJSON defines how each accepted JSON
// component is normalized before hashing.
func DeriveCompatPromptCacheKey(req *protocolopenai.ChatCompletionsRequest, mappedModel string) string {
	if req == nil {
		return ""
	}

	normalizedModel := strings.TrimSpace(mappedModel)
	if normalizedModel == "" {
		normalizedModel = strings.TrimSpace(req.Model)
	}

	seedParts := []string{"model=" + normalizedModel}
	if req.ReasoningEffort != "" {
		seedParts = append(seedParts, "reasoning_effort="+strings.TrimSpace(req.ReasoningEffort))
	}
	if len(req.ToolChoice) > 0 {
		seedParts = append(seedParts, "tool_choice="+protocolopenai.NormalizeCompatSeedJSON(req.ToolChoice))
	}
	// Tool declarations participate when serializable; a failed JSON encoding leaves
	// the remaining opening-content seed usable, with no declaration component added.
	if len(req.Tools) > 0 {
		if raw, err := json.Marshal(req.Tools); err == nil {
			seedParts = append(seedParts, "tools="+protocolopenai.NormalizeCompatSeedJSON(raw))
		}
	}
	// Legacy function declarations use the same serializable-seed rule as tools;
	// encoding failure retains the other seed parts instead of rejecting cache identity.
	if len(req.Functions) > 0 {
		if raw, err := json.Marshal(req.Functions); err == nil {
			seedParts = append(seedParts, "functions="+protocolopenai.NormalizeCompatSeedJSON(raw))
		}
	}

	firstUserCaptured := false
	for _, msg := range req.Messages {
		switch strings.TrimSpace(msg.Role) {
		case "system":
			seedParts = append(seedParts, "system="+protocolopenai.NormalizeCompatSeedJSON(msg.Content))
		case "user":
			if !firstUserCaptured {
				seedParts = append(seedParts, "first_user="+protocolopenai.NormalizeCompatSeedJSON(msg.Content))
				firstUserCaptured = true
			}
		}
	}

	return compatPromptCacheKeyPrefix + upstream.HashSensitiveValueForLog(strings.Join(seedParts, "|"))
}

// DeriveAnthropicCompatPromptCacheKey prefers an ephemeral-text anchor key
// before deriving a configuration/opening-content key. Nil requests return
// empty, and nonempty anchor output is returned without adding the model or
// other configuration seed fields.
//
// The ordinary seed uses mapped model or request model, explicit effort, tool
// choice, serializable tools, system content, and the first user message.
// Later history is excluded from that path. Anchor selection uses its own
// role/text rules, so callers distinguish the resulting anthropic-cache prefix
// from the compat_cc prefix when identifying bridge requests.
func DeriveAnthropicCompatPromptCacheKey(req *protocolanthropic.AnthropicRequest, mappedModel string) string {
	if req == nil {
		return ""
	}
	// Explicit text anchors select their own content-only namespace before model
	// and configuration seeds are assembled for the ordinary compatibility key.
	if anchorKey := DeriveAnthropicCacheControlPromptCacheKey(req); anchorKey != "" {
		return anchorKey
	}

	normalizedModel := strings.TrimSpace(mappedModel)
	if normalizedModel == "" {
		normalizedModel = strings.TrimSpace(req.Model)
	}

	seedParts := []string{"model=" + normalizedModel}
	if req.OutputConfig != nil && strings.TrimSpace(req.OutputConfig.Effort) != "" {
		seedParts = append(seedParts, "effort="+strings.TrimSpace(req.OutputConfig.Effort))
	}
	if len(req.ToolChoice) > 0 {
		seedParts = append(seedParts, "tool_choice="+protocolopenai.NormalizeCompatSeedJSON(req.ToolChoice))
	}
	// Tool declarations participate when serializable; a failed JSON encoding leaves
	// the remaining opening-content seed usable, with no declaration component added.
	if len(req.Tools) > 0 {
		if raw, err := json.Marshal(req.Tools); err == nil {
			seedParts = append(seedParts, "tools="+protocolopenai.NormalizeCompatSeedJSON(raw))
		}
	}
	if len(req.System) > 0 {
		seedParts = append(seedParts, "system="+protocolopenai.NormalizeCompatSeedJSON(req.System))
	}

	firstUserCaptured := false
	for _, msg := range req.Messages {
		if strings.TrimSpace(msg.Role) != "user" || firstUserCaptured {
			continue
		}
		seedParts = append(seedParts, "first_user="+protocolopenai.NormalizeCompatSeedJSON(msg.Content))
		firstUserCaptured = true
	}

	return compatPromptCacheKeyPrefix + upstream.HashSensitiveValueForLog(strings.Join(seedParts, "|"))
}

// DeriveAnthropicCacheControlPromptCacheKey hashes selected nonblank ephemeral
// text anchors and returns empty when none are found. Malformed system/content
// JSON contributes no blocks, leaving other decodable messages eligible.
//
// All qualifying system and assistant text anchors are retained in encounter
// order, while the first user anchor is appended after them. Role and text
// values are trimmed; block type and cache-control spelling require their
// supported exact values. The key uses the first sixteen SHA-256 bytes over
// this text seed and intentionally omits model identity and nontext anchor
// data.
func DeriveAnthropicCacheControlPromptCacheKey(req *protocolanthropic.AnthropicRequest) string {
	if req == nil {
		return ""
	}

	var parts []string
	var systemBlocks []protocolanthropic.AnthropicContentBlock
	// Malformed system blocks cannot supply typed text anchors. Ignore that
	// component while decodable message anchors still contribute to the cache key.
	if len(req.System) > 0 && json.Unmarshal(req.System, &systemBlocks) == nil {
		for _, block := range systemBlocks {
			if block.Type == "text" &&
				block.CacheControl != nil &&
				strings.TrimSpace(block.CacheControl.Type) == "ephemeral" &&
				strings.TrimSpace(block.Text) != "" {
				parts = append(parts, "system:"+strings.TrimSpace(block.Text))
			}
		}
	}

	firstUserAnchor := ""
	for _, msg := range req.Messages {
		var blocks []protocolanthropic.AnthropicContentBlock
		// Unreadable message content supplies no reliable anchor blocks; continue with
		// other decodable history so one malformed message does not abort key derivation.
		if len(msg.Content) == 0 || json.Unmarshal(msg.Content, &blocks) != nil {
			continue
		}
		role := strings.TrimSpace(msg.Role)
		for _, block := range blocks {
			if block.Type != "text" ||
				block.CacheControl == nil ||
				strings.TrimSpace(block.CacheControl.Type) != "ephemeral" ||
				strings.TrimSpace(block.Text) == "" {
				continue
			}
			switch role {
			case "user":
				if firstUserAnchor == "" {
					firstUserAnchor = strings.TrimSpace(block.Text)
				}
			case "assistant":
				parts = append(parts, "assistant:"+strings.TrimSpace(block.Text))
			}
		}
	}
	if firstUserAnchor != "" {
		parts = append(parts, "user_anchor:"+firstUserAnchor)
	}
	if len(parts) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte("anthropic-cache:" + strings.Join(parts, "\n")))
	// Hex encoding this prefix keeps the digest suffix at 32 characters.
	return fmt.Sprintf("anthropic-cache-%x", sum[:16])
}
