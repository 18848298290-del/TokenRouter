package upstream

import (
	"strings"

	"github.com/tidwall/gjson"
)

// CNResponseIndicatesInsufficientBalance recognizes balance-error wording in a response.
// This also supports providers whose balance is reported only through request errors.
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

func ExtractErrorMessage(body []byte) string {
	// Prefer the nested error.message used by Claude and compatible APIs.
	if m := gjson.GetBytes(body, "error.message").String(); strings.TrimSpace(m) != "" {
		inner := strings.TrimSpace(m)
		// Some gateways encode another error object inside the message string.
		if strings.HasPrefix(inner, "{") {
			if innerMsg := gjson.Get(inner, "error.message").String(); strings.TrimSpace(innerMsg) != "" {
				return innerMsg
			}
		}
		return m
	}

	// ChatGPT internal API errors can use detail.
	if d := gjson.GetBytes(body, "detail").String(); strings.TrimSpace(d) != "" {
		return d
	}

	// Use a top-level message when the more specific fields are absent.
	return gjson.GetBytes(body, "message").String()
}

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

	if lastBrace := strings.LastIndex(inner, "}"); lastBrace >= 0 {
		if code := strings.TrimSpace(gjson.Get(inner[:lastBrace+1], "error.code").String()); code != "" {
			return code
		}
	}

	return ""
}

// IsGoogleProjectConfigError recognizes project-configuration wording in a lowercase message.
// Callers use the classification when deciding whether to retry.
func IsGoogleProjectConfigError(lowerMsg string) bool {
	// The response wording alone does not establish whether the project is actually invalid.
	return strings.Contains(lowerMsg, "invalid project resource name")
}

// IsHTMLResponse recognizes a doctype or html tag at the start of a response.
func IsHTMLResponse(body []byte) bool {
	trimmed := strings.TrimSpace(strings.ToLower(string(body)))
	return strings.HasPrefix(trimmed, "<!doctype html") ||
		strings.HasPrefix(trimmed, "<html")
}
