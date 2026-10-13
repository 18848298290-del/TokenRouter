package tierpolicy

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// Administrator Fast settings encode ordered service-tier rules with provider
// and user scopes. Preparation copies the rule collections before normalizing
// tier labels and model patterns, so validating saved JSON leaves the supplied
// settings available to their current readers.

// Fast tier labels select rule matches; force actions choose the tier written
// after scope and model qualification. The protocol normalizer owns accepted
// aliases.
const (
	OpenAIFastTierAny       = "all"                               // Matches every caller-supplied tier label.
	OpenAIFastTierPriority  = protocolopenai.ServiceTierPriority  // Matches the canonical priority tier.
	OpenAIFastTierUltrafast = protocolopenai.ServiceTierUltrafast // Matches ultrafast.
	OpenAIFastTierFlex      = protocolopenai.ServiceTierFlex      // Matches flex.

	// OpenAIFastPolicyActionForcePriority rewrites recognized tiers, including
	// flex, auto, default, and scale, to priority.
	OpenAIFastPolicyActionForcePriority = "force_priority"
	// OpenAIFastPolicyActionForceUltrafast uses the same scope and model fallback
	// rules as force_priority.
	OpenAIFastPolicyActionForceUltrafast = "force_ultrafast"
)

// OpenAIFastPolicyRule matches a service tier and applies an action.
type OpenAIFastPolicyRule struct {
	ServiceTier string `json:"service_tier"` // "priority" | "ultrafast" | "flex" | "all"
	Action      string `json:"action"`       // "pass" | "filter" | "block" | "force_priority" | "force_ultrafast"
	Scope       string `json:"scope"`        // "all" | "oauth" | "apikey" | "bedrock"
	// UserIDs selects caller-supplied trusted user identities; an empty list makes
	// the rule global. ExecutionFastPolicy supplies the authenticated payer user ID.
	UserIDs []int64 `json:"user_ids,omitempty"` // scoped trusted user identifiers
	// ErrorMessage accompanies the primary action. Resolve uses the message for
	// a block result and supplies its own diagnostic when that message is empty.
	ErrorMessage string `json:"error_message,omitempty"` // primary administrator refusal message
	// ModelWhitelist chooses the primary action for an empty or matching list.
	// An unmatched model uses FallbackAction, with an empty fallback selecting pass.
	ModelWhitelist []string `json:"model_whitelist,omitempty"` // accepted model name patterns
	// FallbackAction and FallbackErrorMessage replace the primary action and
	// message when ModelWhitelist excludes the model. An empty fallback action
	// selects pass with an empty message, regardless of the configured message.
	FallbackAction       string `json:"fallback_action,omitempty"`        // action for unmatched models
	FallbackErrorMessage string `json:"fallback_error_message,omitempty"` // message for unmatched models
}

// OpenAIFastPolicySettings contains ordered administrator rules for Fast tier
// evaluation.
type OpenAIFastPolicySettings struct {
	// Rules retains configuration order inside the user-scoped and global scans.
	// Evaluate checks qualifying user rules before it visits qualifying global rules.
	Rules []OpenAIFastPolicyRule `json:"rules"` // ordered administrator tier rules
}

// Default returns fresh empty settings that pass requested service tiers
// unchanged.
func Default() *OpenAIFastPolicySettings {
	return &OpenAIFastPolicySettings{Rules: []OpenAIFastPolicyRule{}}
}

// Prepare validates and JSON-encodes a normalized settings copy for
// persistence by its caller. Nil input is rejected; tier labels and model
// patterns are trimmed, while actions and provider scopes require exact
// configured values.
func Prepare(settings *OpenAIFastPolicySettings) (string, error) {
	if settings == nil {
		return "", fmt.Errorf("settings cannot be nil")
	}

	// Copy both rule storage and nested slices before normalizing model patterns,
	// because saved settings can also be retained by concurrent policy readers.
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
