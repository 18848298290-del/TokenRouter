package anthropic

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/tidwall/gjson"
)

// FingerprintSalt is the salt for the cc_version suffix. It matches
// FINGERPRINT_SALT in Parrot src/transform/cc_mimicry.py.
const FingerprintSalt = "59cf53e54c78"

// ComputeClaudeCodeFingerprint derives the three-digit cc_version suffix.
// It takes bytes 4, 7, and 20 from the first user text, substitutes ASCII zero
// for missing bytes, and hashes the salt, those bytes, and the CLI version.
// The algorithm follows Parrot src/transform/cc_mimicry.py:compute_fingerprint.
func ComputeClaudeCodeFingerprint(body []byte, version string) string {
	firstText := ExtractFirstUserText(body)
	indices := []int{4, 7, 20}
	chars := make([]byte, 0, 3)
	for _, i := range indices {
		if i < len(firstText) {
			chars = append(chars, firstText[i])
		} else {
			chars = append(chars, '0')
		}
	}
	sum := sha256.Sum256([]byte(FingerprintSalt + string(chars) + version))
	return hex.EncodeToString(sum[:])[:3]
}

// ExtractFirstUserText returns the first text in the first user message.
// It accepts string content and block arrays, returning empty when text is absent.
func ExtractFirstUserText(body []byte) string {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return ""
	}
	first := ""
	messages.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "user" {
			return true
		}
		content := msg.Get("content")
		if content.Type == gjson.String {
			first = content.String()
			return false
		}
		if content.IsArray() {
			content.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "text" {
					first = block.Get("text").String()
					return false
				}
				return true
			})
			return false
		}
		return false
	})
	return first
}

// BuildBillingAttributionText builds the system billing marker with cc_version
// and cc_entrypoint=cli. The version includes the message-derived fingerprint.
// Cache breakpoints belong on a subsequent system prompt block.
func BuildBillingAttributionText(body []byte, cliVersion string) (string, error) {
	if cliVersion == "" {
		return "", fmt.Errorf("cliVersion required")
	}
	fp := ComputeClaudeCodeFingerprint(body, cliVersion)
	return fmt.Sprintf(
		"x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=cli;",
		cliVersion, fp,
	), nil
}
