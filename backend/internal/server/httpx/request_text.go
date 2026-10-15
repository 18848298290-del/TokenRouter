package httpx

import (
	"strings"
	"unicode/utf8"
)

// request_text.go normalizes persistent text and bounds its byte length while
// retaining valid UTF-8.
// Persistent User-Agent text is bounded in bytes even when it contains multibyte
// UTF-8 characters. A direct byte slice can cut a character in half: A你B
// occupies five bytes, and a three-byte cap leaves A after the incomplete rune
// is removed. A four-byte cap retains A你 as the complete prefix.
//
// Invalid UTF-8 is removed and surrounding whitespace is trimmed before the
// cap is applied. Backing off incomplete bytes keeps the returned prefix valid
// for downstream persistence and session-binding consumers. Truncation can
// expose new trailing whitespace, because trimming happened earlier; callers
// that need another final trim apply it to the bounded result.

// NormalizePersistentText returns valid text bounded at a complete UTF-8 rune.
// Invalid byte sequences are removed and outer whitespace is trimmed first.
// maxBytes counts bytes, and a nonpositive value disables the length cap.
//
// A positive cap keeps the largest valid prefix within that byte budget, which
// can use fewer bytes than requested when the next rune would cross it. The
// returned prefix can end with whitespace exposed by truncation; the earlier
// trim is not repeated after shortening the value.
func NormalizePersistentText(value string, maxBytes int) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, ""))
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	// A byte limit can cut the final multibyte rune. Back off its incomplete
	// sequence so later persistence and session-binding consumers receive valid UTF-8.
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
