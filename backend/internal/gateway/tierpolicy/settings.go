package tierpolicy

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// OpenAI service tiers determine which Fast policy rules match.
// priority represents Fast (including the fast alias); ultrafast is separate, and flex has lower priority.
// An omitted tier uses the upstream default. Rules select an action and provider scope.
const (
	OpenAIFastTierAny       = "all"                               // Matches any recognized service_tier.
	OpenAIFastTierPriority  = protocolopenai.ServiceTierPriority  // Matches priority, including the fast alias.
	OpenAIFastTierUltrafast = protocolopenai.ServiceTierUltrafast // Matches ultrafast.
	OpenAIFastTierFlex      = protocolopenai.ServiceTierFlex      // Matches flex.

	// OpenAIFastPolicyActionForcePriority rewrites recognized tiers, including flex, auto, default, and scale, to priority.
	OpenAIFastPolicyActionForcePriority = "force_priority"
	// OpenAIFastPolicyActionForceUltrafast uses the same scope and model fallback rules as force_priority.
	OpenAIFastPolicyActionForceUltrafast = "force_ultrafast"
)

// OpenAIFastPolicyRule matches a service tier and applies an action.
type OpenAIFastPolicyRule struct {
	ServiceTier          string   `json:"service_tier"`                     // "priority" | "ultrafast" | "flex" | "all"
	Action               string   `json:"action"`                           // "pass" | "filter" | "block" | "force_priority" | "force_ultrafast"
	Scope                string   `json:"scope"`                            // "all" | "oauth" | "apikey" | "bedrock"
	UserIDs              []int64  `json:"user_ids,omitempty"`               // Empty matches all users; otherwise matches the API Key owner.
	ErrorMessage         string   `json:"error_message,omitempty"`          // Custom message when action is block.
	ModelWhitelist       []string `json:"model_whitelist,omitempty"`        // Model patterns; empty matches all models.
	FallbackAction       string   `json:"fallback_action,omitempty"`        // Action for models outside ModelWhitelist.
	FallbackErrorMessage string   `json:"fallback_error_message,omitempty"` // Custom message when fallback_action is block.
}

// OpenAIFastPolicySettings contains the service-tier policy rules.
type OpenAIFastPolicySettings struct {
	Rules []OpenAIFastPolicyRule `json:"rules"`
}

// Default returns no rules, leaving requested tiers unchanged.
func Default() *OpenAIFastPolicySettings {
	return &OpenAIFastPolicySettings{Rules: []OpenAIFastPolicyRule{}}
}

// Prepare validates and encodes a normalized copy; the caller persists the result.
func Prepare(settings *OpenAIFastPolicySettings) (string, error) {
	if settings == nil {
		return "", fmt.Errorf("settings cannot be nil")
	}

	copySettings := *settings
	copySettings.Rules = slices.Clone(settings.Rules)
	for i := range copySettings.Rules {
		copySettings.Rules[i].UserIDs = slices.Clone(settings.Rules[i].UserIDs)
		copySettings.Rules[i].ModelWhitelist = slices.Clone(settings.Rules[i].ModelWhitelist)
	}
	settings = &copySettings

	validActions := map[string]bool{
		"pass": true, "filter": true, "block": true,
		OpenAIFastPolicyActionForcePriority: true, OpenAIFastPolicyActionForceUltrafast: true,
	}
	validScopes := map[string]bool{
		"all": true, "oauth": true, "apikey": true, "bedrock": true,
	}
	validTiers := map[string]bool{
		OpenAIFastTierAny: true, OpenAIFastTierPriority: true, OpenAIFastTierUltrafast: true, OpenAIFastTierFlex: true,
	}

	for i, rule := range settings.Rules {
		tier := strings.ToLower(strings.TrimSpace(rule.ServiceTier))
		if tier == "" {
			tier = OpenAIFastTierAny
		}
		if !validTiers[tier] {
			return "", fmt.Errorf("rule[%d]: invalid service_tier %q", i, rule.ServiceTier)
		}
		settings.Rules[i].ServiceTier = tier
		if !validActions[rule.Action] {
			return "", fmt.Errorf("rule[%d]: invalid action %q", i, rule.Action)
		}
		if !validScopes[rule.Scope] {
			return "", fmt.Errorf("rule[%d]: invalid scope %q", i, rule.Scope)
		}
		seenUserIDs := make(map[int64]struct{}, len(rule.UserIDs))
		for j, userID := range rule.UserIDs {
			if userID <= 0 {
				return "", fmt.Errorf("rule[%d]: user_ids[%d] must be positive", i, j)
			}
			if _, exists := seenUserIDs[userID]; exists {
				return "", fmt.Errorf("rule[%d]: user_ids[%d] duplicates user_id %d", i, j, userID)
			}
			seenUserIDs[userID] = struct{}{}
		}
		for j, pattern := range rule.ModelWhitelist {
			trimmed := strings.TrimSpace(pattern)
			if trimmed == "" {
				return "", fmt.Errorf("rule[%d]: model_whitelist[%d] cannot be empty", i, j)
			}
			settings.Rules[i].ModelWhitelist[j] = trimmed
		}
		if rule.FallbackAction != "" && !validActions[rule.FallbackAction] {
			return "", fmt.Errorf("rule[%d]: invalid fallback_action %q", i, rule.FallbackAction)
		}
	}

	data, err := json.Marshal(settings)
	if err != nil {
		return "", fmt.Errorf("marshal openai fast policy settings: %w", err)
	}

	return string(data), nil
}
