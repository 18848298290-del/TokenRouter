package anthropic

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// A rejected nonadaptive thinking budget can be rewritten into an enabled
// 32000-token budget. The output limit is raised only when it is too small for
// that budget, and adaptive requests keep their supplied thinking
// configuration.
//
// Repair updates type, budget, and output limit separately because the exchange
// caller needs to know whether any JSON edit succeeded before replaying the
// body. A later failure does not undo earlier edits, and an unchanged
// sufficiently large max_tokens value remains available for generation. Error
// matching requires a budget term, a thinking term, and a recognized 1024-token
// minimum phrase, keeping repair tied to the identified constraint.

// Thinking repair values set the retry budget and output limit, with a separate
// minimum deciding when an existing output limit needs replacement.
const (
	// BudgetRectifyBudgetTokens is the budget_tokens value to set when rectifying.
	BudgetRectifyBudgetTokens = 32000
	// BudgetRectifyMaxTokens is the max_tokens value to set when rectifying.
	BudgetRectifyMaxTokens = 64000
	// BudgetRectifyMinMaxTokens is the smallest output limit above the rectified
	// thinking budget.
	BudgetRectifyMinMaxTokens = 32001
)

// IsThinkingBudgetConstraintError matches a thinking-budget error with a
// recognized 1024 minimum phrase. Lowercased text needs both a budget term and
// thinking, then either an equality-bound phrase or the input-validation
// wording.
func IsThinkingBudgetConstraintError(errMsg string) bool {
	m := strings.ToLower(errMsg)

	hasBudget := strings.Contains(m, "budget_tokens") || strings.Contains(m, "budget tokens")
	if !hasBudget {
		return false
	}

	if !strings.Contains(m, "thinking") {
		return false
	}

	// The supported minimum appears in comparison or input-validation wording.
	// Recognizing those alternatives keeps the same budget repair available for
	// each listed error-text form.
	if strings.Contains(m, ">= 1024") || strings.Contains(m, "greater than or equal to 1024") {
		return true
	}
	if strings.Contains(m, "1024") && strings.Contains(m, "input should be") {
		return true
	}

	return false
}

// RectifyThinkingBudget rewrites nonadaptive requests toward enabled thinking
// with BudgetRectifyBudgetTokens and a sufficient output limit. The literal
// adaptive type returns unchanged body and false; an existing max_tokens at least
// BudgetRectifyMinMaxTokens retains its value.
//
// Type, budget, and output limit edits run separately, and only successful
// edits replace the working body or set changed. An edit failure can therefore
// leave a partial repair for the exchange caller to inspect. Raising the output
// limit to BudgetRectifyMaxTokens keeps it above the configured thinking budget
// for retry construction.
func RectifyThinkingBudget(body []byte) ([]byte, bool) {
	thinkingType := gjson.GetBytes(body, "thinking.type").String()
	// This recovery hook can receive several thinking representations. Returning
	// adaptive bytes keeps the nonadaptive repair from constructing an
	// enabled-thinking payload for that request.
	if thinkingType == "adaptive" {
		return body, false
	}

	// Apply type, thinking budget, and output-limit edits independently so a later
	// JSON failure keeps earlier successful repair bytes available to the exchange
	// caller.
	modified := body
	changed := false

	if thinkingType != "enabled" {
		if result, err := sjson.SetBytes(modified, "thinking.type", "enabled"); err == nil {
			modified = result
			changed = true
		}
	}

	currentBudget := gjson.GetBytes(modified, "thinking.budget_tokens").Int()
	if currentBudget != BudgetRectifyBudgetTokens {
		if result, err := sjson.SetBytes(modified, "thinking.budget_tokens", BudgetRectifyBudgetTokens); err == nil {
			modified = result
			changed = true
		}
	}

	maxTokens := gjson.GetBytes(modified, "max_tokens").Int()
	// Raise undersized output limits above the repaired thinking budget while
	// leaving larger client limits intact, maintaining the threshold used to
	// construct the retry body.
	if maxTokens < int64(BudgetRectifyMinMaxTokens) {
		if result, err := sjson.SetBytes(modified, "max_tokens", BudgetRectifyMaxTokens); err == nil {
			modified = result
			changed = true
		}
	}

	return modified, changed
}
