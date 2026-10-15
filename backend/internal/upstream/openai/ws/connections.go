package ws

import (
	"errors"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// connections.go owns lazy WebSocket pool and dialer initialization and
// serializes pool updates with shutdown.
// WebSocket resources create their shared pool on first use instead of starting
// workers during construction. Shutdown records the closed state before
// stopping an existing pool, so later access can return that same stopped pool
// but cannot create another one.
//
// Option updates and shutdown serialize through updateMu, while poolMu protects
// owner publication and pool lookup. Updating releases poolMu before pool
// reconciliation can close sockets, allowing other callers to retrieve the pool
// while a slow close handshake progresses. Options are stored as an owner copy
// before the pool applies them; callers use the methods for mutation and
// synchronize direct reads of the exported pointer. A supplied pool is reused
// with its existing start state, while the dialer is lazily initialized
// independently.

// OpenAIWSConnections owns lazy pool creation, option publication, and shared
// dialer access.
type OpenAIWSConnections struct {
	Options    *WSPoolOptions        // owner option snapshot pointer
	dialer     openai.WSClientDialer // shared WebSocket transport dialer
	dialerOnce sync.Once             // lazy dialer initialization guard
	pool       *WSConnPool           // shared Responses connection pool
	poolOnce   sync.Once             // lazy pool initialization guard
	poolMu     sync.Mutex            // owner pool publication mutex
	// updateMu serializes option application and Close. poolMu protects Options,
	// pool, poolOnce creation, and closed while allowing socket cleanup outside that lock.
	updateMu sync.Mutex
	closed   bool // pool creation shutdown flag
}

// NewOpenAIWSConnections retains options and dialer and optionally the first
// supplied pool. Callers keep the initial options immutable; a created pool
// starts on Pool access, while a supplied pool retains its existing lifecycle
// state.
func NewOpenAIWSConnections(options *WSPoolOptions, dialer openai.WSClientDialer, pools ...*WSConnPool) *OpenAIWSConnections {
	value := &OpenAIWSConnections{Options: options, dialer: dialer}
	if len(pools) > 0 {
		value.pool = pools[0]
	}
	return value
}

// Pool returns the shared pool, creating and starting it once when the owner is
// open. Nil owners return nil; after Close it returns the existing stopped pool
// or nil and cannot create a replacement.
func (s *OpenAIWSConnections) Pool() *WSConnPool {
	if s == nil {
		return nil
	}
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	if s.closed {
		return s.pool
	}
	s.poolOnce.Do(func() {
		if s.pool == nil {
			s.pool = NewWSConnPool(s.Options)
			s.pool.Start()
		}
	})
	return s.pool
}

// Dialer returns the supplied or lazily created default shared dialer. Nil
// owners return nil, and pool shutdown does not disable dialer lookup; callers
// retain the dialer through their resource owner.
func (s *OpenAIWSConnections) Dialer() openai.WSClientDialer {
	if s == nil {
		return nil
	}
	s.dialerOnce.Do(func() {
		if s.dialer == nil {
			s.dialer = openai.NewDefaultWSClientDialer()
		}
	})
	return s.dialer
}

// InvalidateProvider resolves the shared pool and delegates provider clearing
// when one exists. Resolving an open owner can create/start the pool first; nil
// owners and closed owners without a pool have no resource to clear.
func (s *OpenAIWSConnections) InvalidateProvider(providerID int64) {
	if pool := s.Pool(); pool != nil {
		pool.ClearProvider(providerID)
	}
}

// Close records shutdown and stops an existing pool while preventing new pool
// creation. Nil owners are ignored and repeated calls retain the same
// stopped-resource state. updateMu serializes this operation against option
// changes.
//
// poolMu is released before stopping the pool, so concurrent lookups can
// retrieve its existing pointer while workers, acquisitions, and socket cleanup
// finish. The pool itself owns those shutdown waits and active-lease cleanup.
// Later Pool calls return that stopped pool or nil; independent Dialer access
// remains available.
func (s *OpenAIWSConnections) Close() {
	if s == nil {
		return
	}
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	s.poolMu.Lock()
	s.closed = true
	pool := s.pool
	// Release owner lookup protection before the pool can close sockets, so a
	// slow handshake does not block Pool access while updateMu still serializes lifecycle work.
	s.poolMu.Unlock()
	if pool != nil {
		pool.Close()
	}
}

// UpdateOptions stores a copied option value and asks an existing pool to apply
// it. The receiver must be nonnil; a closed owner returns an error before
// publication. updateMu serializes complete updates and shutdown.
//
// The owner releases poolMu before pool reconciliation can close old
// connections, keeping lookup available during slow handshakes. It stores
// Options before calling the pool, so a pool-level error can leave the owner
// copy changed even though the caller receives failure. Without an existing
// pool the copy supplies future lazy construction, and successful existing-pool
// updates finish before the update lock is released.
func (s *OpenAIWSConnections) UpdateOptions(options WSPoolOptions) error {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	s.poolMu.Lock()
	if s.closed {
		s.poolMu.Unlock()
		return errors.New("responses websocket connections are stopped")
	}
	s.Options = &options
	pool := s.pool
	// Release owner lookup protection before the pool can close sockets, so a
	// slow handshake does not block Pool access while updateMu still serializes lifecycle work.
	s.poolMu.Unlock()
	if pool != nil {
		return pool.UpdateOptions(options)
	}
	return nil
}
