package anthropic

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	// CcVersionInBillingRe matches the three-part version in cc_version.
	CcVersionInBillingRe = regexp.MustCompile(`cc_version=\d+\.\d+\.\d+`)

	CcVersionWithFingerprintInBillingRe = regexp.MustCompile(`cc_version=\d+\.\d+\.\d+\.[0-9a-fA-F]{3}\b`)
)

// EffectiveBillingUserAgent returns the outbound UA used to synchronize the billing marker.
// OAuth mimic overrides the provider fingerprint with the runtime CLI defaults.
// @project-doc docs/interfaces/anthropic_upstream.md#claude_billing_fingerprint
func EffectiveBillingUserAgent(tokenType string, mimicClaudeCode bool, fingerprint *Fingerprint) string {
	if tokenType == "oauth" && mimicClaudeCode {
		return DefaultHeaders["User-Agent"]
	}
	if fingerprint == nil {
		return ""
	}
	return fingerprint.UserAgent
}

// SyncBillingHeaderVersion updates system-array billing markers to match the outbound UA.
// The suffix depends on the version and user text, so a version change recomputes it.
func SyncBillingHeaderVersion(body []byte, userAgent string) []byte {
	version := ExtractCLIVersion(userAgent)
	if version == "" {
		return body
	}

	systemResult := gjson.GetBytes(body, "system")
	if !systemResult.Exists() || !systemResult.IsArray() {
		return body
	}

	replacement := "cc_version=" + version
	idx := 0
	systemResult.ForEach(func(_, item gjson.Result) bool {
		text := item.Get("text")
		if text.Exists() && text.Type == gjson.String &&
			strings.HasPrefix(text.String(), "x-anthropic-billing-header") {
			fingerprintedReplacement := replacement + "." + ComputeClaudeCodeFingerprint(body, version)
			newText := CcVersionWithFingerprintInBillingRe.ReplaceAllString(text.String(), fingerprintedReplacement)
			newText = CcVersionInBillingRe.ReplaceAllString(newText, replacement)
			if newText != text.String() {
				if updated, err := sjson.SetBytes(body, fmt.Sprintf("system.%d.text", idx), newText); err == nil {
					body = updated
				}
			}
		}
		idx++
		return true
	})

	return body
}
