package lifecycle

import (
	"context"
	"net"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// A server cannot acquire a listening address already held by another
// listener. The fixture reserves an ephemeral loopback port and asks Serve to
// bind that same address, checking that listener failure reaches its caller
// before serving begins.

// TestServeReturnsListenerFailure reserves a loopback address before invoking
// Serve on it. The second bind must return an error, protecting startup callers
// from a hidden listener-acquisition failure.
func TestServeReturnsListenerFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	// Keep the port reserved through the conflicting bind. Deferred cleanup
	// attempts to close the fixture listener and discards any listener-close error.
	defer func() { _ = ln.Close() }()
	require.Error(t, Serve(context.Background(), &http.Server{Addr: ln.Addr().String()}))
}
