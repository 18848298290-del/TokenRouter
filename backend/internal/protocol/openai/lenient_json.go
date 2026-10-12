package openai

import "fmt"

// Some request bodies include a leading UTF-8 BOM or raw control bytes inside
// quoted strings. Compatibility normalization removes the BOM and escapes those
// bytes while retaining the surrounding request spelling; the pass does not
// establish that the complete result is valid JSON.
//
// Quote and backslash state determine which bytes belong to a string. A changed
// result allocates separate output, while unchanged content can retain a view
// into the supplied body. Each control byte expands into six ASCII escape bytes,
// so the size limit applies both before scanning and during output growth.
// Callers receive BodyLimitError for either limit violation and perform their
// ordinary JSON decoding after this compatibility pass succeeds.

const (
	// jsonUTF8BOMLen is the length used to recognize and remove the UTF-8
	// byte-order marker before normalized JSON size accounting begins.
	jsonUTF8BOMLen = 3
	// maxDecompressedBodySize supplies the normalized-body byte limit when
	// callers configure a nonpositive bound for the lenient compatibility pass.
	maxDecompressedBodySize = 64 << 20
)

// BodyLimitError reports a normalized body size limit violation for the HTTP
// adapter to translate.
type BodyLimitError struct {
	Limit int64 /* bytes */
}

// Error returns the normalized JSON size limit in the adapter's error message.
func (e *BodyLimitError) Error() string {
	return fmt.Sprintf("normalized JSON body exceeds %d bytes", e.Limit)
}

// NormalizeLenientJSONRequestBody removes a leading BOM and escapes control
// bytes encountered inside quoted strings, including DEL. A nonpositive limit
// selects 64 MiB; an oversized input or expanding output returns BodyLimitError.
//
// Quote and escape state are tracked while the bytes are scanned, but overall
// JSON validity remains the caller's responsibility. Repaired output uses a
// separate buffer and leaves input unchanged; an unrepaired result can share
// the supplied body's storage after BOM removal. Callers finish reading any
// borrowed result before modifying that underlying storage.
func NormalizeLenientJSONRequestBody(body []byte, maxNormalizedBytes int64) ([]byte, error) {
	if maxNormalizedBytes <= 0 {
		maxNormalizedBytes = maxDecompressedBodySize
	}

	body = trimUTF8BOM(body)
	if len(body) == 0 {
		return body, nil
	}
	if int64(len(body)) > maxNormalizedBytes {
		return nil, &BodyLimitError{Limit: maxNormalizedBytes}
	}

	var out []byte
	inString := false
	escaped := false
	for i, b := range body {
		if inString && isJSONControlByte(b) {
			if out == nil {
				// Allocate only after finding a byte that needs expansion. Reserve
				// its six-byte escape within the configured limit while copying the
				// untouched prefix once.
				capHint := len(body) + 6
				if int64(capHint) > maxNormalizedBytes {
					capHint = int(maxNormalizedBytes)
				}
				out = make([]byte, 0, capHint)
				out = append(out, body[:i]...)
			}
			if int64(len(out)+6) > maxNormalizedBytes {
				// One control byte expands to six ASCII bytes. Check that growth
				// before appending so the repaired body cannot pass its configured
				// normalized-size limit.
				return nil, &BodyLimitError{Limit: maxNormalizedBytes}
			}
			out = appendJSONUnicodeEscape(out, b)
			escaped = false
			continue
		}

		switch {
		case escaped:
			escaped = false
		case inString && b == '\\':
			escaped = true
		case b == '"':
			inString = !inString
		}

		if out != nil {
			// After the first repair, ordinary suffix bytes also grow the new
			// buffer. Apply the same output limit through the remainder of the request.
			if int64(len(out)+1) > maxNormalizedBytes {
				return nil, &BodyLimitError{Limit: maxNormalizedBytes}
			}
			out = append(out, b)
		}
	}
	if out != nil {
		return out, nil
	}
	return body, nil
}

// trimUTF8BOM returns body after a leading UTF-8 byte-order marker when present.
// The returned slice shares input storage; nonmatching or short input is returned
// unchanged for the caller's ordinary JSON handling.
func trimUTF8BOM(body []byte) []byte {
	// The UTF-8 marker precedes the JSON value rather than belonging to it.
	// Return the suffix so ordinary decoding starts at JSON while retaining
	// the caller's byte storage.
	if len(body) >= jsonUTF8BOMLen && body[0] == 0xef && body[1] == 0xbb && body[2] == 0xbf {
		return body[jsonUTF8BOMLen:]
	}
	return body
}

// isJSONControlByte reports whether the string compatibility pass escapes this byte.
func isJSONControlByte(b byte) bool {
	// The compatibility pass includes DEL beyond the lower control range;
	// both classes enter the same six-byte Unicode escape encoder during scanning.
	return b < 0x20 || b == 0x7f
}

// appendJSONUnicodeEscape appends b as a lowercase six-byte \u00xx escape.
// The caller checks normalized output size beforehand and supplies a byte
// selected by the compatibility control-byte predicate; the returned slice can
// reuse dst's backing storage.
func appendJSONUnicodeEscape(dst []byte, b byte) []byte {
	const hex = "0123456789abcdef"
	// Render the selected byte as ASCII escape digits so the compatibility
	// pass can represent that byte explicitly inside the forwarded JSON string.
	return append(dst, '\\', 'u', '0', '0', hex[b>>4], hex[b&0x0f])
}
