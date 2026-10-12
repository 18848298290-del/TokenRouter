package httpclient

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// Buffered-body fixtures use short in-memory payloads to isolate the
// configured byte budget. An exactly fitting payload is returned intact; a
// larger payload returns no bytes and an error recognizable through
// ErrResponseBodyTooLarge before protocol decoding can consume it.

// TestReadUpstreamResponseBodyLimited compares an exactly fitting payload
// with an oversized payload. It requires complete bytes for the former and no
// body plus the overflow sentinel for the latter.
func TestReadUpstreamResponseBodyLimited(t *testing.T) {
	t.Run("within limit", func(t *testing.T) {
		body, err := ReadResponseBodyLimited(bytes.NewReader([]byte("ok")), 2)
		require.NoError(t, err)
		require.Equal(t, []byte("ok"), body)
	})

	t.Run("exceeds limit", func(t *testing.T) {
		body, err := ReadResponseBodyLimited(bytes.NewReader([]byte("toolong")), 3)
		require.Nil(t, body)
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrResponseBodyTooLarge))
	})
}
