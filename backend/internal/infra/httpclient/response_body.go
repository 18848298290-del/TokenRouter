package httpclient

import (
	"errors"
	"fmt"
	"io"
)

// Buffered upstream bodies need a size bound before protocol decoders
// allocate from the complete response. Reading one extra byte distinguishes
// an exactly fitting body from overflow; failures return no body and leave
// reader cleanup with its caller.

// DefaultResponseReadMaxBytes bounds buffered upstream responses when callers
// supply a nonpositive size, before the complete body reaches protocol decoding.
const DefaultResponseReadMaxBytes int64 = 128 * 1024 * 1024

// ErrResponseBodyTooLarge identifies overflow so callers can distinguish the
// configured response size limit from failures reading the underlying body.
var ErrResponseBodyTooLarge = errors.New("upstream response body too large")

// ReadResponseBodyLimited returns the complete body when it fits maxBytes,
// reading one extra byte to detect overflow. Positive limits must leave room
// for maxBytes+1 in int64. Nonpositive limits select
// DefaultResponseReadMaxBytes; nil readers, read errors, and overflow return
// an error with no body. The caller owns closing the reader.
func ReadResponseBodyLimited(reader io.Reader, maxBytes int64) ([]byte, error) {
	if reader == nil {
		return nil, errors.New("response body is nil")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultResponseReadMaxBytes
	}
	// One extra byte distinguishes overflow from an exactly fitting response,
	// allowing callers to reject oversized input before their complete-body decode.
	body, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("%w: limit=%d", ErrResponseBodyTooLarge, maxBytes)
	}
	return body, nil
}
