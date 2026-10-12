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

const compatPromptCacheKeyPrefix = "compat_cc_"

// ShouldAutoInjectPromptCacheKeyForCompat reports whether this model receives an automatic compatibility cache key.
func ShouldAutoInjectPromptCacheKeyForCompat(model string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(model))
	return trimmed == "gpt-6-astra" || strings.HasPrefix(trimmed, "gpt-5") || strings.Contains(trimmed, "codex")
}

// DeriveCompatPromptCacheKey hashes stable opening content and request configuration into a cache key.
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
	if len(req.Tools) > 0 {
		if raw, err := json.Marshal(req.Tools); err == nil {
			seedParts = append(seedParts, "tools="+protocolopenai.NormalizeCompatSeedJSON(raw))
		}
	}
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

// DeriveAnthropicCompatPromptCacheKey prefers cache_control anchors and otherwise hashes stable opening content.
func DeriveAnthropicCompatPromptCacheKey(req *protocolanthropic.AnthropicRequest, mappedModel string) string {
	if req == nil {
		return ""
	}
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

// DeriveAnthropicCacheControlPromptCacheKey hashes ephemeral text anchors and returns empty when none exist.
func DeriveAnthropicCacheControlPromptCacheKey(req *protocolanthropic.AnthropicRequest) string {
	if req == nil {
		return ""
	}

	var parts []string
	var systemBlocks []protocolanthropic.AnthropicContentBlock
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
	return fmt.Sprintf("anthropic-cache-%x", sum[:16])
}
