package httpclient

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Borrowed SSE storage is exercised through a normal get, mutation, and
// return followed by a nil return. The test checks the fixed capacity and
// usable pointer, leaving sync.Pool's reuse and eviction choices outside its
// assertions.

// TestSSEScannerBuf64KPool_GetPutDoesNotPanic borrows fixed scanner storage,
// writes a byte, and returns it before returning nil. It requires a non-nil
// pointer and the configured capacity, with both returns completing normally.
func TestSSEScannerBuf64KPool_GetPutDoesNotPanic(t *testing.T) {
	buf := GetSSEScannerBuf64K()
	require.NotNil(t, buf)
	require.Equal(t, SSEScannerBuf64KSize, len(buf[:]))

	buf[0] = 1
	PutSSEScannerBuf64K(buf)

	// Returning a nil buffer is safe.
	PutSSEScannerBuf64K(nil)
}
