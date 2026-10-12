package httpclient

import (
	"errors"
	"fmt"
	"io"
)

// DefaultResponseReadMaxBytes is the default limit for buffered upstream responses.
const DefaultResponseReadMaxBytes int64 = 128 * 1024 * 1024

var ErrResponseBodyTooLarge = errors.New("upstream response body too large")

// ReadResponseBodyLimited reads at most maxBytes+1 bytes to detect overflow.
// Nonpositive limits use DefaultResponseReadMaxBytes. Nil readers, read errors,
// and overflow return an error with no body. The caller closes reader.
func ReadResponseBodyLimited(reader io.Reader, maxBytes int64) ([]byte, error) {
	if reader == nil {
		return nil, errors.New("response body is nil")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultResponseReadMaxBytes
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("%w: limit=%d", ErrResponseBodyTooLarge, maxBytes)
	}
	return body, nil
}
