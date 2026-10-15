package codeassist

import "strings"

// sanitize.go shortens inline base64 data and caps Code Assist diagnostic body
// text for logging.
// Code Assist failures can contain long inline data URLs that overwhelm log
// messages. Body shortening replaces base64 tails before applying a
// byte-length cap, while credential redaction remains the responsibility of
// callers that expose the diagnostic text.

// maxLogBodyLen caps shortened error-body bytes before the truncation marker
// is appended by SanitizeBodyForLogs.
const maxLogBodyLen = 2048

// SanitizeBodyForLogs shortens data-URL base64 tails and then caps body bytes
// before appending a marker. Credential fields remain present, and byte
// slicing can split UTF-8; callers apply redaction suitable for their
// destination.
func SanitizeBodyForLogs(body string) string {
	body = truncateBase64InMessage(body)
	// Cap the shortened body before appending its marker so a remaining large
	// error message cannot dominate the Code Assist failure diagnostic.
	if len(body) > maxLogBodyLen {
		body = body[:maxLogBodyLen] + "...[truncated]"
	}
	return body
}

// truncateBase64InMessage retains the first fifty alphabet bytes after each
// literal ;base64, marker and replaces longer runs with a truncation marker.
// It recognizes encoded text syntactically and leaves other message content
// unchanged.
func truncateBase64InMessage(message string) string {
	// Keep a small recognizable prefix for data-URL diagnostics.
	const maxBase64Length = 50

	result := message
	offset := 0
	for {
		idx := strings.Index(result[offset:], ";base64,")
		if idx == -1 {
			break
		}
		actualIdx := offset + idx
		start := actualIdx + len(";base64,")

		end := start
		for end < len(result) && isBase64Char(result[end]) {
			end++
		}

		// Resume scanning after the inserted marker so later data URLs can also be
		// shortened without treating replacement text as part of the source
		// encoding.
		if end-start > maxBase64Length {
			result = result[:start+maxBase64Length] + "...[truncated]" + result[end:]
			offset = start + maxBase64Length + len("...[truncated]")
			continue
		}
		offset = end
	}

	return result
}

// isBase64Char recognizes ASCII bytes in the standard base64 alphabet
// including padding.
func isBase64Char(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '+' || c == '/' || c == '='
}
