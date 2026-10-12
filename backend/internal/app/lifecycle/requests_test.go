package lifecycle

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// HTTP shutdown does not wait for every application handler tail, and hijacked
// connections need separate closure. Live-server fixtures keep handlers active
// behind release signals, exposing ResponseWriter interfaces and observing
// manager completion only after tail or connection cleanup has finished.

// TestRequestsWaitIncludesHandlerTail keeps a flushed HTTP handler active
// after listener cancellation. Request waiting must stay pending until the
// handler is released, while the passed-through writer retains its Flush
// capability.
func TestRequestsWaitIncludesHandlerTail(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	address := make(chan string, 1)
	server := &http.Server{Addr: "127.0.0.1:0", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Flush initial output so http.Get can return while this handler remains
		// active behind release; output errors are not the fixture observation.
		_, _ = w.Write([]byte("started"))
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("缺少 Flush 能力")
			return
		}
		flusher.Flush()
		close(entered)
		<-release
	}), BaseContext: func(ln net.Listener) context.Context {
		// Publish the actual ephemeral listener address for the client. Buffering
		// lets Serve finish its base-context callback before the test receives it.
		address <- ln.Addr().String()
		return context.Background()
	}}
	manager := New()
	TrackRequests(server, manager)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	// Serve progresses independently while the client enters its handler. Buffer
	// its result so HTTP shutdown can complete before this fixture receives it.
	go func() { served <- Serve(ctx, server) }()
	response, err := http.Get("http://" + <-address)
	require.NoError(t, err)
	// Attempt response cleanup even on an assertion exit. The fixture discards
	// close errors and observes the explicit Serve and manager stop results instead.
	defer func() { _ = response.Body.Close() }()
	<-entered
	cancel()
	closed := make(chan error, 1)
	// Run the manager drain concurrently with the blocked handler. Its result
	// must remain pending until release lets the counted handler finish its tail.
	go func() { closed <- manager.Stop(context.Background()) }()
	select {
	case <-closed:
		t.Fatal("handler 尾部清理未完成")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-closed)
	require.NoError(t, <-served)
}

// TestRequestsCloseHijackedConnectionsBeforeWaiting upgrades one connection
// and blocks the handler reading it. The writer must retain Hijack, HTTP
// serving must end, and manager cleanup must close the connection so request
// waiting finishes.
func TestRequestsCloseHijackedConnectionsBeforeWaiting(t *testing.T) {
	address := make(chan string, 1)
	hijacked := make(chan struct{})
	server := &http.Server{Addr: "127.0.0.1:0", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("缺少 Hijack 能力")
			return
		}
		conn, rw, err := hijacker.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		// The handler owns cleanup of its hijacked connection. Attempt final
		// closure and ignore close errors after the read loop has been unblocked.
		defer func() { _ = conn.Close() }()
		// Send and flush the raw upgrade response so the client can finish
		// ReadResponse before this fixture blocks in the hijacked connection read loop.
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: example\r\n\r\n")
		_ = rw.Flush()
		// Announce the completed upgrade before blocking on connection reads.
		// The test then cancels serving and starts the manager's hijack-close phase.
		close(hijacked)
		// Manager closure unblocks this read loop. Its I/O error is discarded
		// because the fixture observes cleanup completion through the request barrier.
		_, _ = io.Copy(io.Discard, conn)
	}), BaseContext: func(ln net.Listener) context.Context {
		// Publish the actual ephemeral listener address for the client. Buffering
		// lets Serve finish its base-context callback before the test receives it.
		address <- ln.Addr().String()
		return context.Background()
	}}
	manager := New()
	TrackRequests(server, manager)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	// Serve progresses independently while the client enters its handler. Buffer
	// its result so HTTP shutdown can complete before this fixture receives it.
	go func() { served <- Serve(ctx, server) }()
	conn, err := net.Dial("tcp", <-address)
	require.NoError(t, err)
	// Release the client endpoint during fixture cleanup, discarding close
	// errors after assertions observe serving and manager shutdown results.
	defer func() { _ = conn.Close() }()
	_, err = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: localhost\r\nConnection: Upgrade\r\nUpgrade: example\r\n\r\n")
	require.NoError(t, err)
	_, err = http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	<-hijacked
	cancel()
	require.NoError(t, <-served)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	require.NoError(t, manager.Stop(stopCtx))
}

// TestRequestsCloseLateHijack reports a synthetic hijack after the manager has
// stopped. ConnState must close that late connection, observable as EOF on its
// paired endpoint rather than an open pipe waiting for input.
func TestRequestsCloseLateHijack(t *testing.T) {
	manager := New()
	server := &http.Server{}
	TrackRequests(server, manager)
	require.NoError(t, manager.Stop(context.Background()))
	serverSide, clientSide := net.Pipe()
	// Both pipe endpoints belong to this fixture. Attempt their closure on
	// return and discard cleanup errors after the late-hijack EOF assertion.
	defer func() { _ = serverSide.Close(); _ = clientSide.Close() }()
	require.NoError(t, clientSide.SetReadDeadline(time.Now().Add(time.Second)))
	server.ConnState(serverSide, http.StateHijacked)
	_, err := clientSide.Read(make([]byte, 1))
	require.ErrorIs(t, err, io.EOF)
}
