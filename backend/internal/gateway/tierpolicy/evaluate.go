package tierpolicy

import (
	"slices"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/routing/modelmap"
)

// evaluate.go selects Fast policy actions from user-scoped and global rules.
// Evaluation scans matching user rules first, then global rules, with provider
// scope and service tier qualifying each rule before model actions are
// resolved.
//
// A selected rule consumes the model decision even when its whitelist chooses a
// fallback action or pass. Later rules are therefore not searched for a more
// restrictive result. The caller supplies the user identity and normalized
// tier. ExecutionFastPolicy supplies the payer identity and folds case and
// whitespace; Resolve canonicalizes tier input before invoking its callback.

// Evaluate returns the first qualified user action, then the first qualified
// global action, or pass when no rule qualifies. The caller supplies a
// normalized tier and its trusted user identity; nil settings pass
// immediately.
//
// Provider scope and tier qualify a rule before its model whitelist is
// evaluated. A selected rule returns its model fallback, including pass,
// directly, so a later global rule cannot override the user exception. Order
// remains significant within each of the two rule sets.
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

// userMatches reports whether the supplied user satisfies a rule's user scope.
// An empty list makes the rule global; nonempty lists use exact numeric
// membership with the caller-provided identity.
func userMatches(ruleUserIDs []int64, userID int64) bool {
	if len(ruleUserIDs) == 0 {
		return true
	}
	return slices.Contains(ruleUserIDs, userID)
}
