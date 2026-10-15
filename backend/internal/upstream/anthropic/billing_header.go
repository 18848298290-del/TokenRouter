package anthropic

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// billing_header.go synchronizes Claude billing version markers with the
// selected request User-Agent.
// A Claude billing marker can outlive the User-Agent that originally supplied
// its version. OAuth mimic chooses the runtime CLI header, while ordinary
// fingerprint forwarding uses the provider's selected User-Agent, so request
// construction resolves that choice before synchronizing system text.
//
// Version replacement also recomputes an existing three-hexadecimal-digit
// suffix from the user message. Plain version markers remain plain, and
// unrelated system text stays at its existing JSON path. Each successful field
// update becomes the input for subsequent updates; an unsuccessful write leaves
// that marker as it was. Callers run synchronization before building the final
// request body and beta-dependent sanitization.

// Billing version patterns distinguish plain three-part markers from markers
// that also carry an existing hexadecimal fingerprint suffix.
var (
	// CcVersionInBillingRe matches the three-part version in cc_version.
	CcVersionInBillingRe = regexp.MustCompile(`cc_version=\d+\.\d+\.\d+`)

	CcVersionWithFingerprintInBillingRe = regexp.MustCompile(`cc_version=\d+\.\d+\.\d+\.[0-9a-fA-F]{3}\b`)
)

// EffectiveBillingUserAgent returns the UA used to synchronize system billing
// text. OAuth mimic selects runtime defaults even with no fingerprint; other
// requests use fingerprint.UserAgent or return empty when none is supplied.
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

// SyncBillingHeaderVersion rewrites recognized billing markers in a system
// array to match a leading Claude CLI User-Agent. An unrecognized UA or
// nonarray system leaves body unchanged, and ordinary system text stays at its
// existing path.
//
// An existing fingerprint suffix is recomputed with the selected version and
// user message before the plain version replacement runs. Plain markers retain
// their plain shape. Updates apply independently to each recognized item, with
// failed JSON writes leaving that item's text unchanged and successful writes
// carried into the returned body.
func SyncBillingHeaderVersion(body []byte, userAgent string) []byte {
	version := ExtractCLIVersion(userAgent)
	if version == "" {
		return body
	}

	systemResult := gjson.GetBytes(body, "system")
	if !systemResult.Exists() || !systemResult.IsArray() {
		return body
	}

	// Apply version and existing-suffix replacements at each billing text path.
	// An unsuccessful JSON write retains that item's text; other system items can
	// still synchronize against the same selected User-Agent.
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
