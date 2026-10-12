package session

import (
	"sync"
	"sync/atomic"
	"time"
)

// CodexTurnOrigins records the issuing provider by turn-state digest.
// HTTP registers staged headers after committing them and uses ownership to strip replayed state on provider switches.
type CodexTurnOrigins struct {
	origins sync.Map
	writes  atomic.Uint64
	now     func() time.Time
}
type codexTurnOrigin struct {
	providerID int64
	expiresAt  time.Time
}

func NewCodexTurnOrigins(now func() time.Time) *CodexTurnOrigins {
	if now == nil {
		now = time.Now
	}
	return &CodexTurnOrigins{now: now}
}

// Record stores ownership using the supplied TTL and sweeps expired entries every 256 writes.
func (s *CodexTurnOrigins) Record(seed string, id int64, ttl time.Duration) {
	if s == nil || seed == "" || id <= 0 {
		return
	}
	s.origins.Store(seed, codexTurnOrigin{providerID: id, expiresAt: s.now().Add(ttl)})
	if s.writes.Add(1)%256 != 0 {
		return
	}
	now := s.now()
	s.origins.Range(func(key, value any) bool {
		origin, ok := value.(codexTurnOrigin)
		if !ok || (!origin.expiresAt.IsZero() && now.After(origin.expiresAt)) {
			s.origins.Delete(key)
		}
		return true
	})
}

// Owner returns an unexpired issuing provider. Missing or expired ownership returns false.
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
		s.origins.Delete(seed)
		return 0, false
	}
	return origin.providerID, true
}
