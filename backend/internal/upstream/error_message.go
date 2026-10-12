package upstream

import (
	"strings"

	"github.com/tidwall/gjson"
)

// Error bodies can carry their message in a nested object, a top-level field,
// or another JSON object encoded inside the message string. Consumers need the
// same error text and code whether a compatible gateway adds that extra
// envelope. Path extraction here is best effort and can read fields from text
// that is not a complete JSON document.
//
// Message selection prefers the nested error, then detail, then the top-level
// message. Code selection prefers the outer code before looking inside the
// encoded message and trying a prefix ending at its last brace. Separate
// balance, Google-project, and HTML classifiers inspect wording or prefixes
// for caller-owned failure handling; their matches describe response evidence
// rather than prove provider state.

// CNResponseIndicatesInsufficientBalance reports whether the body contains a
// supported balance-error phrase. It lowercases raw text and accepts Chinese
// or English wording anywhere in the body; empty input returns false.
func CNResponseIndicatesInsufficientBalance(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	s := strings.ToLower(string(body))
	return strings.Contains(s, "余额不足") ||
		strings.Contains(s, "insufficient balance") ||
		strings.Contains(s, "insufficient_credit") ||
		strings.Contains(s, "balance is not enough") ||
		strings.Contains(s, "no enough balance")
}

// ExtractErrorMessage selects the message used by upstream diagnostics and
// failure classification. It first reads error.message; when its trimmed
// contents begin with an object, a nonblank inner error.message takes
// precedence over that outer string. Otherwise the original selected text,
// including surrounding whitespace, is returned.
//
// Absent nested messages fall through to detail and then message. Path
// extraction does not require complete JSON validation, so callers apply any
// logging redaction and trust rules to the returned text.
func ExtractErrorMessage(body []byte) string {
	// Compatible error envelopes can wrap the useful message in another JSON
	// string. Read its nested message before returning the wrapper text to diagnostics.
	if m := gjson.GetBytes(body, "error.message").String(); strings.TrimSpace(m) != "" {
		inner := strings.TrimSpace(m)
		// A leading object is the signal for the encoded-error compatibility path;
		// retain the outer message when no nonblank inner message can be extracted.
		if strings.HasPrefix(inner, "{") {
			if innerMsg := gjson.Get(inner, "error.message").String(); strings.TrimSpace(innerMsg) != "" {
				return innerMsg
			}
		}
		return m
	}

	// The internal ChatGPT response shape uses detail. Keep that text available
	// when a compatible response supplies no usable nested error message.
	if d := gjson.GetBytes(body, "detail").String(); strings.TrimSpace(d) != "" {
		return d
	}

	// Top-level message is the last response field inspected so generic gateway
	// text remains available after both provider-specific message paths are absent.
	return gjson.GetBytes(body, "message").String()
}

// ExtractErrorCode selects a trimmed error.code for retry and diagnostic
// callers. A nonblank outer code wins before the helper examines a JSON-
// looking error.message string. If direct inner extraction finds no code, it
// also tries that string through its last closing brace, allowing an encoded
// object followed by other text to be inspected.
//
// Missing codes return empty. Extraction is best effort and uses gjson paths
// without validating the entire response or the embedded message as a JSON
// document.
func ExtractErrorCode(body []byte) string {
	if code := strings.TrimSpace(gjson.GetBytes(body, "error.code").String()); code != "" {
		return code
	}

	inner := strings.TrimSpace(gjson.GetBytes(body, "error.message").String())
	if !strings.HasPrefix(inner, "{") {
		return ""
	}

	if code := strings.TrimSpace(gjson.Get(inner, "error.code").String()); code != "" {
		return code
	}

	// An encoded object may be followed by diagnostic text. Retry the prefix
	// ending at its last brace before treating the upstream code as absent.
	if lastBrace := strings.LastIndex(inner, "}"); lastBrace >= 0 {
		if code := strings.TrimSpace(gjson.Get(inner[:lastBrace+1], "error.code").String()); code != "" {
			return code
		}
	}

	return ""
}

// IsGoogleProjectConfigError reports whether a lowercase message contains the
// recognized project-resource phrase. Callers lowercase the message first and
// combine this wording with status and request context before choosing
// recovery.
func IsGoogleProjectConfigError(lowerMsg string) bool {
	// The response wording alone does not establish whether the project is
	// actually invalid.
	return strings.Contains(lowerMsg, "invalid project resource name")
}

// IsHTMLResponse reports whether trimmed response text starts with a doctype
// or html tag. Matching ignores case; empty, JSON, and XML-prefixed inputs
// return false unless their leading text matches those prefixes.
func IsHTMLResponse(body []byte) bool {
	trimmed := strings.TrimSpace(strings.ToLower(string(body)))
	return strings.HasPrefix(trimmed, "<!doctype html") ||
		strings.HasPrefix(trimmed, "<html")
}
