package openai

import (
	"strings"
)

// recorded_effort.go selects request reasoning fields and normalizes their
// values for usage-record reporting.
// Request reasoning values and recorded effort use different vocabularies.
// A request can supply none or minimal even though recorded effort keeps only
// low, medium, high, xhigh, and max, so an empty reporting value alone cannot
// distinguish omission from a supplied request choice.
//
// Decoded map extraction returns string presence alongside the normalized
// label. It selects between nested and top-level field spellings before
// normalization removes separators and collapses accepted aliases. The
// requeststate consumer then chooses a retained label for usage records, while
// request qualification and upstream parameter validation remain with the
// callers that prepare the outgoing request.

// NormalizeRecordedReasoningEffort returns the reporting label for a supplied
// effort after trimming, lowercasing, and removing separators. None, minimal,
// blank, and unsupported labels return empty; extrahigh and separated x-high
// spellings normalize to xhigh.
func NormalizeRecordedReasoningEffort(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return ""
	}

	// Reporting accepts equivalent client spellings such as x-high, x_high,
	// and x high, so separator removal precedes the recognized-label lookup.
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
		// An unknown client label cannot supply a recognized reporting effort.
		// Return an empty value while the request reader retains whether the
		// string field existed.
		return ""
	}
}

// GetOpenAIReasoningEffortFromReqBody returns the normalized recorded effort
// and whether a string field was supplied. A nested reasoning.effort string
// wins, including an empty or unsupported value; a missing or nonstring nested
// field permits a top-level reasoning_effort string. Nil maps and requests
// without either string field return empty and false.
//
// Presence survives normalization independently of the reporting label. For
// example, nested none beside top-level high returns empty and true, because
// the supplied nested choice owns the decision even though none has no
// reporting label. Callers retain the flag when they need to distinguish that
// supplied choice from an absent effort field.
func GetOpenAIReasoningEffortFromReqBody(reqBody map[string]any) (value string, present bool) {
	if reqBody == nil {
		return "", false
	}

	// A supplied nested string owns the reporting decision, even when its
	// normalized value is empty; falling through would substitute a
	// conflicting top-level effort.
	if reasoning, ok := reqBody["reasoning"].(map[string]any); ok {
		if effort, ok := reasoning["effort"].(string); ok {
			return NormalizeRecordedReasoningEffort(effort), true
		}
	}

	// An absent or nonstring nested effort permits the compatible top-level
	// spelling; its own string presence remains visible even when
	// normalization drops its label.
	if effort, ok := reqBody["reasoning_effort"].(string); ok {
		return NormalizeRecordedReasoningEffort(effort), true
	}

	return "", false
}
