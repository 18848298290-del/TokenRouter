package modelmap

// ActionRule selects an action and error message by model scope.
type ActionRule struct {
	Action               string
	ErrorMessage         string
	ModelWhitelist       []string
	FallbackAction       string
	FallbackErrorMessage string
}

// MatchesAny checks exact names and trailing-wildcard patterns in a whitelist.
func MatchesAny(model string, whitelist []string) bool {
	for _, pattern := range whitelist {
		if Matches(pattern, model) {
			return true
		}
	}
	return false
}

// ResolveAction uses the primary action for an empty or matching whitelist; an unmatched model uses the fallback
// or pass.
func ResolveAction(rule ActionRule, model string) (action, errorMessage string) {
	if len(rule.ModelWhitelist) == 0 {
		return rule.Action, rule.ErrorMessage
	}
	if MatchesAny(model, rule.ModelWhitelist) {
		return rule.Action, rule.ErrorMessage
	}
	if rule.FallbackAction != "" {
		return rule.FallbackAction, rule.FallbackErrorMessage
	}
	return "pass", ""
}
