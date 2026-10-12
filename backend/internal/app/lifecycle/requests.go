package lifecycle

import (
	"context"
	"net"
	"net/http"
	"sync"
)

// Requests tracks handlers until they return, including usage collection after a client disconnects.
// ResponseWriter passes through unchanged so handlers retain Flush, Hijack, and HTTP/2 capabilities.
type Requests struct {
	mu             sync.Mutex
	sealed         bool
	wg             sync.WaitGroup
	done           chan struct{}
	hijacked       map[net.Conn]struct{}
	closingHijacks bool
}

type requestConnectionKey struct{}

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
		r.mu.Lock()
		closeHijack := state == http.StateHijacked && r.closingHijacks
		if state == http.StateHijacked {
			r.hijacked[conn] = struct{}{}
		}
		if state == http.StateClosed {
			delete(r.hijacked, conn)
		}
		r.mu.Unlock()
		// Connections hijacked after the shutdown snapshot also need disconnection cleanup.
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
		r.mu.Lock()
		if r.sealed {
			r.mu.Unlock()
			http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
			return
		}
		r.wg.Add(1)
		r.mu.Unlock()
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
	// net/http Shutdown leaves hijacked connections open; closing them triggers handler cleanup.
	manager.Register(Hook{Name: "HTTPHijackedConnections", StartOrder: 990, StopOrder: 10, Stop: func(context.Context) error {
		r.mu.Lock()
		r.closingHijacks = true
		connections := make([]net.Conn, 0, len(r.hijacked))
		for conn := range r.hijacked {
			connections = append(connections, conn)
		}
		r.mu.Unlock()
		for _, conn := range connections {
			_ = conn.Close()
		}
		return nil
	}})
	manager.Register(Hook{Name: "HTTPRequests", StartOrder: 985, StopOrder: 15, Stop: r.Wait})
}

// Wait seals request admission and waits for handlers to finish cleanup. Call it after stopping the listener.
func (r *Requests) Wait(ctx context.Context) error {
	r.mu.Lock()
	if r.done == nil {
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
