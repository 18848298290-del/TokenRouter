package anthropic

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// BudgetRectifyBudgetTokens is the budget_tokens value to set when rectifying.
	BudgetRectifyBudgetTokens = 32000
	// BudgetRectifyMaxTokens is the max_tokens value to set when rectifying.
	BudgetRectifyMaxTokens = 64000
	// BudgetRectifyMinMaxTokens is the smallest output limit above the rectified thinking budget.
	BudgetRectifyMinMaxTokens = 32001
)

// IsThinkingBudgetConstraintError detects whether an upstream error message indicates
// a budget_tokens constraint violation (e.g. "budget_tokens >= 1024").
// Matches three conditions (all must be true):
//  1. Contains "budget_tokens" or "budget tokens"
//  2. Contains "thinking"
//  3. Contains ">= 1024" or "greater than or equal to 1024" or ("1024" + "input should be")
func IsThinkingBudgetConstraintError(errMsg string) bool {
	m := strings.ToLower(errMsg)

	hasBudget := strings.Contains(m, "budget_tokens") || strings.Contains(m, "budget tokens")
	if !hasBudget {
		return false
	}

	if !strings.Contains(m, "thinking") {
		return false
	}

	if strings.Contains(m, ">= 1024") || strings.Contains(m, "greater than or equal to 1024") {
		return true
	}
	if strings.Contains(m, "1024") && strings.Contains(m, "input should be") {
		return true
	}

	return false
}

// RectifyThinkingBudget skips adaptive thinking. Otherwise it sets enabled
// thinking with a 32000-token budget and raises max_tokens to 64000 when below
// 32001. JSON update failures leave that field unchanged; the boolean reports
// whether any update succeeded.
func RectifyThinkingBudget(body []byte) ([]byte, bool) {
	thinkingType := gjson.GetBytes(body, "thinking.type").String()
	if thinkingType == "adaptive" {
		return body, false
	}

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
	if maxTokens < int64(BudgetRectifyMinMaxTokens) {
		if result, err := sjson.SetBytes(modified, "max_tokens", BudgetRectifyMaxTokens); err == nil {
			modified = result
			changed = true
		}
	}

	return modified, changed
}
