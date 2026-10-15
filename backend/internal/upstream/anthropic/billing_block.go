package anthropic

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/tidwall/gjson"
)

// billing_block.go builds Claude billing attribution text from CLI version and
// user-message bytes.
// Claude billing text carries a CLI version together with a suffix derived from
// selected bytes of the first user message. String content and text-block
// arrays supply that text, while absent bytes contribute ASCII zero to the hash
// input.
//
// The version participates in the same hash as the message bytes and salt, so a
// changed version requires recomputing the suffix even when the conversation is
// unchanged. Billing text is built separately from the system prompt cache
// breakpoint, allowing request preparation to place the marker ahead of
// cacheable instructions. The implementation's byte positions matter for
// multibyte text: these offsets index UTF-8 bytes, not characters.

// FingerprintSalt participates in the billing suffix hash together with
// selected first-user bytes and the CLI version.
const FingerprintSalt = "59cf53e54c78"

// ComputeClaudeCodeFingerprint returns three lowercase hexadecimal digits from
// the CLI version and first user text. It samples byte offsets four, seven, and
// twenty, with ASCII zero substituted when a position is absent.
//
// The salt, selected bytes, and supplied version are concatenated before
// SHA-256. UTF-8 text is indexed as bytes, so selecting characters instead
// would alter the marker. The version is an input even for empty text, which is
// why billing version synchronization recomputes the suffix.
func ComputeClaudeCodeFingerprint(body []byte, version string) string {
	firstText := ExtractFirstUserText(body)
	// These fixed positions select UTF-8 bytes from the first user text. Missing
	// positions use ASCII zero so short or absent text still forms the hash input.
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
	// Billing synchronization recognizes exactly three hexadecimal suffix digits,
	// so keep this digest fragment aligned with its marker pattern.
	return hex.EncodeToString(sum[:])[:3]
}

// ExtractFirstUserText reads string content or the first text block in the
// first user message. It stops at that message even when no text is available
// and then returns an empty string.
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

// BuildBillingAttributionText builds a CLI system billing marker using the
// supplied version and message-derived suffix. An empty version returns an
// error; callers place cache control on a subsequent prompt block.
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
