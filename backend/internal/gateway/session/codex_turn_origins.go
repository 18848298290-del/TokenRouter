package session

import (
	"sync"
	"sync/atomic"
	"time"
)

// Codex turn state is opaque, so HTTP filtering remembers the latest provider
// for an API Key and original client-session pair. CodexTurnStateHeaders supplies
// a seed containing the decimal key ID, a NUL separator, and the inbound session.
// Recording remembers a provider ID for that pair; the index does not identify
// individual state values or confirm that a client received a response header.
//
// Entries expire strictly after their deadline and are removed on lookup or
// periodic record-side sweeps. The map and write counter support concurrent
// operations, but load, expiration testing, and deletion remain separate. Cleanup
// of an older value can therefore remove a replacement recorded during that
// sequence. An injected clock must support concurrent calls.

// CodexTurnOrigins holds process-local provider ownership indexed by caller-built
// key/session seeds. HTTP uses unknown ownership to allow replay and a different
// known owner to remove the echoed state.
type CodexTurnOrigins struct {
	origins sync.Map      // caller seed to provider ownership
	writes  atomic.Uint64 // accepted records since construction
	// now supplies concurrent ownership time; injected mutable clock state
	// requires caller synchronization.
	now func() time.Time
}

// codexTurnOrigin records one provider ID with its ownership expiration deadline.
type codexTurnOrigin struct {
	providerID int64 // most recently recorded provider
	// expiresAt is inclusive for ownership lookup; a zero deadline keeps the
	// loaded origin valid indefinitely.
	expiresAt time.Time
}

// NewCodexTurnOrigins constructs an empty shared ownership index for HTTP adapters.
// A nil clock uses time.Now; a supplied clock must support concurrent calls from
// ownership reads and recording sweeps.
func NewCodexTurnOrigins(now func() time.Time) *CodexTurnOrigins {
	if now == nil {
		now = time.Now
	}
	return &CodexTurnOrigins{now: now}
}

// Record replaces the provider remembered for seed and assigns the supplied TTL.
// A nil index, empty seed, or nonpositive provider ID is ignored. HTTP callers use
// one seed per API Key and original session, so another record replaces that
// pair's previously remembered provider.
//
// Every 256 accepted records triggers an expiration sweep using one clock reading.
// The sweep deletes invalid values and entries strictly past their deadlines;
// nonpositive TTLs are accepted. Storage and sweeping are separate map operations,
// so expiration cleanup can remove a concurrently replaced entry. Recording does
// not establish successful response delivery.
func (s *CodexTurnOrigins) Record(seed string, id int64, ttl time.Duration) {
	if s == nil || seed == "" || id <= 0 {
		return
	}
	// Overwrite the pair's provider before incrementing the sweep count so
	// the triggering record is visible when periodic cleanup traverses the map.
	s.origins.Store(seed, codexTurnOrigin{providerID: id, expiresAt: s.now().Add(ttl)})
	// Amortize whole-map expiration scans across accepted records; lookup-side
	// removal still handles a stale entry between these periodic sweeps.
	if s.writes.Add(1)%256 != 0 {
		return
	}
	now := s.now()
	s.origins.Range(func(key, value any) bool {
		origin, ok := value.(codexTurnOrigin)
		if !ok || (!origin.expiresAt.IsZero() && now.After(origin.expiresAt)) {
			// Delete the expired snapshot using its key; a concurrent replacement
			// may also be removed because Range and Delete are separate operations.
			s.origins.Delete(key)
		}
		return true
	})
}

// Owner returns the most recently stored provider for a live seed, or false when
// ownership is unavailable. A nil index, empty seed, missing value, or expired
// entry produces no owner. The exact expiration instant is accepted, and a zero
// deadline is treated as unbounded.
//
// Invalid and expired loaded values trigger deletion. Loading, checking, and
// deleting are separate concurrent map operations, so cleanup can remove a new
// record installed after the old value was read. Callers such as Guard treat
// missing ownership as permission to defer state validation to upstream.
func (s *CodexTurnOrigins) Owner(seed string) (int64, bool) {
	if s == nil || seed == "" {
		return 0, false
	}
	value, ok := s.origins.Load(seed)
	if !ok {
		return 0, false
	}
	origin, ok := value.(codexTurnOrigin)
	if !ok || (!origin.expiresAt.IsZero() && s.now().After(origin.expiresAt)) {
		// Retire stale ownership so subsequent guards defer to upstream; this
		// separate delete may also remove a replacement recorded after Load.
		s.origins.Delete(seed)
		return 0, false
	}
	return origin.providerID, true
}
