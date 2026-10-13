package anthropic

import (
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/routing/modelmap"
)

// Configured beta rules select their action from provider scope and the final
// model's whitelist match. Blocking examines supplied tokens, while filtering
// builds a set that request builders can apply to both required and
// client-supplied headers.
//
// Rules run in configuration order. The first blocking message remains selected
// even as later filter rules contribute tokens, and a model outside a whitelist
// can have a different action and message. Evaluating synthesized tokens
// separately lets request preparation reject a capability that was added after
// the client header check, while the caller decides how a reported block
// becomes an HTTP error.

// BetaPolicyResult holds the first block and accumulated filter tokens.
type BetaPolicyResult struct {
	// BlockErr retains the first matching block in rule order; nil means no block
	// action matched a supplied token.
	BlockErr *BetaBlockedError // selected blocking error
	// FilterSet contains exact filter tokens even when absent from the client
	// Header; nil means no filter rule contributed.
	FilterSet map[string]struct{} // outbound drop tokens
}

// BetaBlockedError carries the message selected for a blocked beta request.
type BetaBlockedError struct {
	Message string // selected rejection text
}

// Error returns the configured beta blocking message for the caller.
func (e *BetaBlockedError) Error() string { return e.Message }

// EvaluateBetaPolicy resolves rules by provider scope and model to report a
// block and filter tokens. Nil settings return an empty result, and matching
// rules run in configuration order.
//
// Block actions require the rule's token in betaHeader; the first block message
// wins, with a generated message when its configured text is empty. Filter
// actions contribute tokens even when absent from that header, allowing later
// header synthesis to apply the same exclusions. Callers decide whether and how
// to reject the request from BlockErr.
func EvaluateBetaPolicy(settings *BetaPolicySettings, betaHeader string, isOAuth, isBedrock bool, model string) BetaPolicyResult {
	if settings == nil {
		return BetaPolicyResult{}
	}
	var result BetaPolicyResult
	for _, rule := range settings.Rules {
		if !capability.PolicyScopeMatches(rule.Scope, isOAuth, isBedrock) {
			continue
		}
		effectiveAction, effectiveErrMsg := modelmap.ResolveAction(modelmap.ActionRule{Action: rule.Action, ErrorMessage: rule.ErrorMessage, ModelWhitelist: rule.ModelWhitelist, FallbackAction: rule.FallbackAction, FallbackErrorMessage: rule.FallbackErrorMessage}, model)
		switch effectiveAction {
		case BetaPolicyActionBlock:
			if result.BlockErr == nil && betaHeader != "" && ContainsBetaToken(betaHeader, rule.BetaToken) {
				msg := effectiveErrMsg
				if msg == "" {
					msg = "beta feature " + rule.BetaToken + " is not allowed"
				}
				result.BlockErr = &BetaBlockedError{Message: msg}
			}
		case BetaPolicyActionFilter:
			if result.FilterSet == nil {
				result.FilterSet = make(map[string]struct{})
			}
			result.FilterSet[rule.BetaToken] = struct{}{}
		}
	}
	return result
}

// CheckBetaPolicyBlockForTokens finds the first blocking rule for a supplied
// token list, provider scope, and model. Nil settings or no tokens return nil;
// the list becomes an exact set so repeated tokens do not change the selected
// rule.
//
// Rules resolve primary or fallback actions before their block and scope
// checks. A matching token returns the configured message or a generated
// token-specific message. This lets callers check synthesized tokens after
// their earlier client-header evaluation, while request rejection remains with
// the caller.
func CheckBetaPolicyBlockForTokens(settings *BetaPolicySettings, tokens []string, isOAuth, isBedrock bool, model string) *BetaBlockedError {
	if settings == nil || len(tokens) == 0 {
		return nil
	}
	tokenSet := BuildBetaTokenSet(tokens)
	for _, rule := range settings.Rules {
		effectiveAction, effectiveErrMsg := modelmap.ResolveAction(modelmap.ActionRule{Action: rule.Action, ErrorMessage: rule.ErrorMessage, ModelWhitelist: rule.ModelWhitelist, FallbackAction: rule.FallbackAction, FallbackErrorMessage: rule.FallbackErrorMessage}, model)
		if effectiveAction != BetaPolicyActionBlock {
			continue
		}
		if !capability.PolicyScopeMatches(rule.Scope, isOAuth, isBedrock) {
			continue
		}
		if _, present := tokenSet[rule.BetaToken]; present {
			msg := effectiveErrMsg
			if msg == "" {
				msg = "beta feature " + rule.BetaToken + " is not allowed"
			}
			return &BetaBlockedError{Message: msg}
		}
	}
	return nil
}
