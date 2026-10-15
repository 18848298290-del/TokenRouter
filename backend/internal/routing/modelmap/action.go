package modelmap

// action.go selects a model-scoped action and its paired rejection message.
// A model-scoped rule pairs its primary action with one rejection message and
// its unmatched action with another. The absence of whitelist entries applies
// the primary action to every model, while a populated list restricts that
// branch through exact or trailing-star matching. The action rule therefore
// treats an empty list differently from MatchesAny, which reports no match
// for that list.
//
// An unmatched configured list selects its supplied fallback action and paired
// message. When that action is empty, the result is pass with an empty message,
// even if fallback text was supplied. The tier-policy caller uses this result
// immediately, so the model decision stays with the rule selected by its user,
// provider, and tier scope.

// ActionRule selects primary and fallback actions with their corresponding
// rejection text.
type ActionRule struct {
	// Action and ErrorMessage describe the primary branch; FallbackAction and
	// FallbackErrorMessage describe unmatched models. ResolveAction returns each
	// action with its paired message, keeping the branches separate.
	Action       string // primary matched action selection
	ErrorMessage string // primary branch rejection text
	// ModelWhitelist scopes the primary action through MatchesAny. An empty list
	// applies the primary action to every model; a populated list can select the
	// fallback branch.
	ModelWhitelist       []string
	FallbackAction       string // unmatched model action selection
	FallbackErrorMessage string // unmatched branch rejection text
}

// MatchesAny reports whether any configured exact or trailing-wildcard pattern
// matches model. Empty lists match nothing; the literal star pattern matches
// every string through the shared pattern matcher.
func MatchesAny(model string, whitelist []string) bool {
	for _, pattern := range whitelist {
		if Matches(pattern, model) {
			return true
		}
	}
	return false
}

// ResolveAction returns the action and paired message chosen by a model rule.
// An empty whitelist applies the primary branch universally; a populated list
// applies that branch only when MatchesAny accepts the model.
//
// The empty-list decision uses configuration presence before matching because
// MatchesAny itself returns false for an empty list. An unmatched configured
// list chooses its fallback pair, or pass with an empty message when
// FallbackAction is empty. Supplied fallback text alone does not activate a
// rejection, and action strings pass through as configured.
func ResolveAction(rule ActionRule, model string) (action, errorMessage string) {
	if len(rule.ModelWhitelist) == 0 {
		return rule.Action, rule.ErrorMessage
	}
	if MatchesAny(model, rule.ModelWhitelist) {
		return rule.Action, rule.ErrorMessage
	}
	// The configured whitelist rejected this model. Use its paired fallback
	// action and message so the selected rule controls unmatched-model handling.
	if rule.FallbackAction != "" {
		return rule.FallbackAction, rule.FallbackErrorMessage
	}
	return "pass", ""
}
