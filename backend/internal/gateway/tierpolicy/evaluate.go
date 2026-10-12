package tierpolicy

import (
	"slices"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/routing/modelmap"
)

// Evaluate applies user-specific rules before global rules, taking the first match within each set.
func Evaluate(settings *OpenAIFastPolicySettings, userID int64, isOAuth, isBedrock bool, model, tier string) (action, errMsg string) {
	if settings == nil {
		return "pass", ""
	}

	// User-specific rules take precedence even when global rules appear earlier,
	// allowing per-user exceptions. Each set retains configuration order.
	for _, userScoped := range []bool{true, false} {
		for _, rule := range settings.Rules {
			if (len(rule.UserIDs) > 0) != userScoped || !userMatches(rule.UserIDs, userID) {
				continue
			}
			if !capability.PolicyScopeMatches(rule.Scope, isOAuth, isBedrock) {
				continue
			}
			ruleTier := strings.ToLower(strings.TrimSpace(rule.ServiceTier))
			if ruleTier != "" && ruleTier != OpenAIFastTierAny && ruleTier != tier {
				continue
			}
			eff := modelmap.ActionRule{
				Action:               rule.Action,
				ErrorMessage:         rule.ErrorMessage,
				ModelWhitelist:       rule.ModelWhitelist,
				FallbackAction:       rule.FallbackAction,
				FallbackErrorMessage: rule.FallbackErrorMessage,
			}
			return modelmap.ResolveAction(eff, model)
		}
	}
	return "pass", ""
}

// userMatches accepts global rules or rules listing the current user.
func userMatches(ruleUserIDs []int64, userID int64) bool {
	if len(ruleUserIDs) == 0 {
		return true
	}
	return slices.Contains(ruleUserIDs, userID)
}
