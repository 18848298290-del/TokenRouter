package lifecycle

import (
	"context"
	"net"
	"net/http"
	"sync"
)

// requests.go tracks HTTP handler completion and closes hijacked connections
// during application shutdown. HTTP handlers can retain work after a client
// disconnects, so HTTP listener shutdown does not establish that their cleanup
// has finished. The server wrapper counts each admitted handler through its
// return while passing ResponseWriter through unchanged. Flush, Hijack, and
// other writer interfaces remain available to the handler.
//
// Hijacked connections need a separate close phase because net/http shutdown
// leaves them open. ConnState tracks those connections under the same mutex
// used to start the shutdown snapshot. A closing flag also catches hijacks
// reported after that snapshot. Request waiting seals admission before
// WaitGroup.Wait begins, then waits for all counted handler returns within the
// caller's context.

// Requests counts handler lifetimes and tracks connections needing hijack shutdown.
type Requests struct {
	// mu serializes sealed and wg registration with creation of done. Sealing
	// prevents new registrations before its waiter can observe an empty group;
	// done closes after the counted handlers return.
	mu     sync.Mutex     // request admission lock
	sealed bool           // new handlers rejected
	wg     sync.WaitGroup // admitted handler lifetimes
	done   chan struct{}  // handler group completion
	// hijacked and closingHijacks share mu across ConnState and shutdown. The
	// close snapshot sets closingHijacks first, so late hijacks receive another
	// close attempt; handler return or StateClosed removes tracked entries.
	hijacked       map[net.Conn]struct{} // reported hijacked connections
	closingHijacks bool                  // hijack close phase begun
}

// requestConnectionKey associates a server connection with handler context for hijack cleanup.
type requestConnectionKey struct{}

// TrackRequests wraps server callbacks and its Handler to count requests
// through handler return. It chains existing ConnContext and ConnState
// callbacks, stores the connection in a private context key, and uses
// DefaultServeMux when Handler is nil.
//
// The manager receives a hijacked-connection close hook before its
// request-wait hook. Closing marks the snapshot phase before copying
// connections, allowing later hijacks to be closed by ConnState as well. The
// handler releases its count after cleanup. Call during construction before
// serving HTTP and before manager startup or shutdown; ResponseWriter itself
// remains unwrapped.
func TrackRequests(server *http.Server, manager *Manager) {
	r := &Requests{hijacked: make(map[net.Conn]struct{})}
	previousContext := server.ConnContext
	server.ConnContext = func(ctx context.Context, conn net.Conn) context.Context {
		if previousContext != nil {
			ctx = previousContext(ctx, conn)
		}
		return context.WithValue(ctx, requestConnectionKey{}, conn)
	}
	previousState := server.ConnState
	server.ConnState = func(conn net.Conn, state http.ConnState) {
		// Publish connection transitions under the snapshot mutex before chaining
		// the prior observer, so shutdown sees the same map and late-hijack flag.
		r.mu.Lock()
		closeHijack := state == http.StateHijacked && r.closingHijacks
		if state == http.StateHijacked {
			r.hijacked[conn] = struct{}{}
		}
		if state == http.StateClosed {
			delete(r.hijacked, conn)
		}
		r.mu.Unlock()
		// A hijack reported after the snapshot still needs a close attempt.
		// This callback has no error result; request waiting observes any unfinished handler.
		if closeHijack {
			_ = conn.Close()
		}
		if previousState != nil {
			previousState(conn, state)
		}
	}
	next := server.Handler
	if next == nil {
		next = http.DefaultServeMux
	}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Test sealing and register the handler under Wait's admission mutex.
		// This keeps a new Add from escaping the group that shutdown waits on.
		r.mu.Lock()
		if r.sealed {
			r.mu.Unlock()
			http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
			return
		}
		r.wg.Add(1)
		r.mu.Unlock()
		// Remove the connection after handler cleanup, then release its request
		// count so the wait barrier covers the handler's full return path.
		defer func() {
			if conn, ok := req.Context().Value(requestConnectionKey{}).(net.Conn); ok {
				r.mu.Lock()
				delete(r.hijacked, conn)
				r.mu.Unlock()
			}
			r.wg.Done()
		}()
		next.ServeHTTP(w, req)
	})
	// net/http shutdown leaves hijacks open. Set the closing phase before
	// snapshotting connections so later ConnState reports also receive close attempts.
	manager.Register(Hook{Name: "HTTPHijackedConnections", StartOrder: 990, StopOrder: 10, Stop: func(context.Context) error {
		r.mu.Lock()
		r.closingHijacks = true
		connections := make([]net.Conn, 0, len(r.hijacked))
		for conn := range r.hijacked {
			connections = append(connections, conn)
		}
		r.mu.Unlock()
		// Attempt every captured close and discard individual close errors. The
		// subsequent request barrier still reports handlers that do not finish cleanup.
		for _, conn := range connections {
			_ = conn.Close()
		}
		return nil
	}})
	manager.Register(Hook{Name: "HTTPRequests", StartOrder: 985, StopOrder: 15, Stop: r.Wait})
}

// Wait seals request admission and waits for the counted handlers to return,
// or returns ctx's error. Call after stopping the listener so handler shutdown
// and cleanup can proceed. A timeout leaves the shared completion wait
// running, and later calls can wait again.
//
// The admission mutex covers sealing and creation of done before the WaitGroup
// waiter starts, preventing new handler registrations from racing a zero-count
// Wait. The completion channel closes after every admitted handler runs its
// deferred release. Subsequent callers reuse that same signal rather than
// creating another request group.
func (r *Requests) Wait(ctx context.Context) error {
	r.mu.Lock()
	if r.done == nil {
		// Seal new handler registration before starting the WaitGroup waiter.
		// Its shared completion signal stays usable after one caller's timeout.
		r.sealed = true
		r.done = make(chan struct{})
		go func() { r.wg.Wait(); close(r.done) }()
	}
	done := r.done
	r.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
