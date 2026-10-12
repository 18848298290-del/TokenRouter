package openai

import "fmt"

const (
	jsonUTF8BOMLen          = 3
	maxDecompressedBodySize = 64 << 20
)

// BodyLimitError reports a normalized body size limit violation for the HTTP adapter to translate.
type BodyLimitError struct{ Limit int64 }

func (e *BodyLimitError) Error() string {
	return fmt.Sprintf("normalized JSON body exceeds %d bytes", e.Limit)
}

// NormalizeLenientJSONRequestBody escapes raw control bytes inside JSON strings
// and enforces the resulting size limit. A nonpositive limit defaults to 64 MiB.
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
				capHint := len(body) + 6
				if int64(capHint) > maxNormalizedBytes {
					capHint = int(maxNormalizedBytes)
				}
				out = make([]byte, 0, capHint)
				out = append(out, body[:i]...)
			}
			if int64(len(out)+6) > maxNormalizedBytes {
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

// trimUTF8BOM removes a leading UTF-8 BOM from a JSON request body.
func trimUTF8BOM(body []byte) []byte {
	if len(body) >= jsonUTF8BOMLen && body[0] == 0xef && body[1] == 0xbb && body[2] == 0xbf {
		return body[jsonUTF8BOMLen:]
	}
	return body
}

// isJSONControlByte reports whether this compatibility pass escapes the byte inside a string.
func isJSONControlByte(b byte) bool {
	return b < 0x20 || b == 0x7f
}

// appendJSONUnicodeEscape appends a control byte as a \u00xx JSON escape.
func appendJSONUnicodeEscape(dst []byte, b byte) []byte {
	const hex = "0123456789abcdef"
	return append(dst, '\\', 'u', '0', '0', hex[b>>4], hex[b&0x0f])
}
