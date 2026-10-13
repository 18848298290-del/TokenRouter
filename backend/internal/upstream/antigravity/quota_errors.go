package antigravity

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Antigravity forbidden messages can indicate verification, terms violations,
// or ordinary permission rejection. Classification prioritizes verification
// wording, while link extraction prefers structured account guidance over a
// raw HTTPS scan.
//
// Metadata links are returned verbatim without URL validation. Raw extraction
// requires verification-related wording, replaces the literal ampersand
// escape, and selects the first HTTPS-shaped match. These helpers supply
// diagnostics; provider health and authorization callers decide the
// classification effect and whether a selected link suits its destination.
// An appeal link in an earlier detail can win over a validation link in a
// later detail.

// urlPattern recognizes an HTTPS-shaped text link after verification wording
// permits raw extraction, retaining punctuation accepted by its character
// class.
var urlPattern = regexp.MustCompile(`https://[^\s"'\\]+`)

// ClassifyForbiddenType selects validation before violation when case-folded
// message patterns overlap. Unmatched text returns forbidden, allowing callers
// to retain generic permission diagnosis rather than infer an unsupported
// account action.
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

// ExtractValidationURL selects account guidance from structured details before
// scanning raw HTTPS text. Each detail tries validation_url before appeal_url,
// and the first nonempty value returns verbatim without URL validation.
//
// If structured decoding or lookup supplies no link, verification, validation,
// or appeal wording permits the raw scan. Literal escaped ampersands become
// query separators before the first HTTPS-shaped match is returned. The
// pattern can retain trailing punctuation, so callers assess suitability for
// the destination; absent gating words or a matching link produces an empty
// string.
func ExtractValidationURL(body string) string {
	// Prefer validation_url before appeal_url within each detail so structured
	// guidance selects its link before broad scanning can match unrelated body
	// text.
	var parsed struct {
		Error struct { // structured forbidden error details
			Details []struct { // ordered account guidance details
				Metadata map[string]string `json:"metadata"` // validation and appeal link values
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

	// Gate raw scanning with verification or appeal wording because a generic
	// forbidden body can contain unrelated HTTPS text that should not become
	// account guidance.
	lower := strings.ToLower(body)
	if !strings.Contains(lower, "validation") &&
		!strings.Contains(lower, "verify") &&
		!strings.Contains(lower, "appeal") {
		return ""
	}
	// Replace the literal ampersand escape before matching so query separators
	// from captured error text remain usable in the selected HTTPS-shaped link.
	normalized := strings.ReplaceAll(body, `\u0026`, "&")
	if m := urlPattern.FindString(normalized); m != "" {
		return m
	}
	return ""
}
