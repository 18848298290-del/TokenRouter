package antigravity

import (
	"encoding/json"
	"regexp"
	"strings"
)

// urlPattern extracts an HTTPS link when structured error metadata is unavailable.
var urlPattern = regexp.MustCompile(`https://[^\s"'\\]+`)

// ClassifyForbiddenType classifies a 403 body as validation, violation, or forbidden.
func ClassifyForbiddenType(body string) string {
	lower := strings.ToLower(body)
	switch {
	case strings.Contains(lower, "validation_required") ||
		strings.Contains(lower, "verify your account") ||
		strings.Contains(lower, "validation_url"):
		return "validation"
	case strings.Contains(lower, "terms of service") ||
		strings.Contains(lower, "violation"):
		return "violation"
	default:
		return "forbidden"
	}
}

// ExtractValidationURL reads a validation or appeal link from a 403 response.
func ExtractValidationURL(body string) string {
	// Prefer links in error.details metadata.
	var parsed struct {
		Error struct {
			Details []struct {
				Metadata map[string]string `json:"metadata"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &parsed) == nil {
		for _, detail := range parsed.Error.Details {
			if u := detail.Metadata["validation_url"]; u != "" {
				return u
			}
			if u := detail.Metadata["appeal_url"]; u != "" {
				return u
			}
		}
	}

	// Raw URL extraction requires validation, verification, or appeal wording.
	lower := strings.ToLower(body)
	if !strings.Contains(lower, "validation") &&
		!strings.Contains(lower, "verify") &&
		!strings.Contains(lower, "appeal") {
		return ""
	}
	// Decode escaped ampersands before extracting query parameters.
	normalized := strings.ReplaceAll(body, `\u0026`, "&")
	if m := urlPattern.FindString(normalized); m != "" {
		return m
	}
	return ""
}
