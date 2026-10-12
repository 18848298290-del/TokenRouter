package openai

import (
	"strings"
)

func NormalizeRecordedReasoningEffort(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return ""
	}

	// Accept client spellings such as x-high, x_high, and x high.
	value = strings.NewReplacer("-", "", "_", "", " ", "").Replace(value)

	switch value {
	case "none", "minimal":
		return ""
	case "low", "medium", "high":
		return value
	case "xhigh", "extrahigh":
		return "xhigh"
	case "max":
		return value
	default:
		// Unknown effort values are omitted from usage records.
		return ""
	}
}

// GetOpenAIReasoningEffortFromReqBody returns normalized effort and whether a string field was present.
func GetOpenAIReasoningEffortFromReqBody(reqBody map[string]any) (value string, present bool) {
	if reqBody == nil {
		return "", false
	}

	// The nested reasoning.effort field takes precedence.
	if reasoning, ok := reqBody["reasoning"].(map[string]any); ok {
		if effort, ok := reasoning["effort"].(string); ok {
			return NormalizeRecordedReasoningEffort(effort), true
		}
	}

	// Some clients send the effort in a top-level field.
	if effort, ok := reqBody["reasoning_effort"].(string); ok {
		return NormalizeRecordedReasoningEffort(effort), true
	}

	return "", false
}
