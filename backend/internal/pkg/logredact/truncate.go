package logredact

import (
	"strings"
	"unicode/utf8"
)

// truncate.go truncates diagnostic strings and byte slices for bounded log
// output.
// Log storage limits are measured in bytes, so cutting a string at its limit
// can leave a partial UTF-8 character. TruncateUTF8 backs off the cut until the
// prefix is valid UTF-8, while TruncateUTF8Value first removes surrounding
// whitespace. Inputs already within the limit are returned as supplied,
// including malformed UTF-8.
//
// Single-line diagnostic output has a separate order: TruncateLine limits the
// supplied byte slice before replacing carriage returns and line feeds with
// visible escapes. Those escapes can increase the final length beyond the
// input-byte limit. It retains other bytes as provided, so callers choose the
// UTF-8 helper when they need a valid textual prefix.

// TruncateUTF8Value trims surrounding whitespace and delegates byte truncation
// to TruncateUTF8. Nonpositive limits produce empty text; truncation can expose
// whitespace inside the previously trimmed input because trimming precedes the
// byte cut.
func TruncateUTF8Value(value string, maxLen int) string {
	value = strings.TrimSpace(value)
	return TruncateUTF8(value, maxLen)
}

// TruncateUTF8 returns a byte-limited text prefix and repairs partial UTF-8 at
// a cut. Nonpositive limits return empty text, while input whose byte length
// already fits is returned unchanged, even if that input contains invalid
// UTF-8.
//
// For longer input, the first maxLen bytes are selected and trailing bytes are
// removed until that entire prefix is valid. For A你B, a three-byte cut backs
// off to A because the Chinese character needs three bytes after the leading A.
// Whitespace is retained throughout this helper.
func TruncateUTF8(value string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(value) <= maxLen {
		return value
	}
	// The byte cut can end within a multibyte rune or retain earlier invalid bytes.
	// Back off until the whole prefix is valid before handing text to log consumers.
	value = value[:maxLen]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

// TruncateLine limits input bytes and escapes CR and LF for single-line
// diagnostic text. Nonpositive maxBytes selects 2048 bytes; escape expansion
// can exceed that limit, and the byte cut can split UTF-8.
func TruncateLine(b []byte, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = 2048
	}
	if len(b) > maxBytes {
		// Bound captured payload bytes before newline escapes can expand their length.
		b = b[:maxBytes]
	}
	s := string(b)
	// Make embedded line breaks visible so one captured upstream body cannot create
	// additional log lines; expansion occurs after the captured byte prefix is chosen.
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	return s
}
