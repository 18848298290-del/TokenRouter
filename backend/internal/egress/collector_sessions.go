package egress

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"
)

// A temporary capture session owns a bounded sequence of retained record
// pointers and an expiration timestamp. Reads and writes prune sessions only
// after the clock passes expiration, so a session remains readable at the exact
// timestamp and rejects the next later instant.
//
// One mutex serializes session creation, lookup, pruning, and list membership
// changes. Append retains the newest arrivals before enforcing the record cap,
// while List copies the pointer slice and orders it by CapturedAt. Captured
// records themselves remain shared with producers and readers; changing those
// objects after insertion would bypass the membership lock and alter later
// inspection results.

// CaptureSessions manages capture sessions, expiration, and record limits.
type CaptureSessions struct {
	// mu protects states and calls to now during session operations. The
	// constructor retains now; callbacks should return promptly while the session
	// lock is held.
	mu     sync.Mutex                 // session membership serialization lock
	states map[string]*captureSession // token-indexed retained session records
	now    func() time.Time           // session expiration clock callback
}

// captureSession retains one token's expiration time and capture record
// pointers under the owner lock.
type captureSession struct {
	token     string    // session map identity token
	expiresAt time.Time // strict expiration cutoff time
	// records is changed under CaptureSessions.mu, but its pointed-to captures
	// remain shared. Producers and readers treat each inserted capture and its
	// nested collections as immutable.
	records []*TLSFingerprintCaptureRecord // arrival-retained shared capture pointers
}

// NewCaptureSessions constructs an empty capture-session map using the supplied
// clock. Nil selects time.Now, and later operations invoke the retained clock
// while holding the session membership lock.
func NewCaptureSessions(now func() time.Time) *CaptureSessions {
	if now == nil {
		now = time.Now
	}
	return &CaptureSessions{states: make(map[string]*captureSession), now: now}
}

// prune removes sessions whose expiration precedes now from the owner's map.
// The caller holds mu; a timestamp equal to expiresAt retains the session for
// capture lookup at that boundary.
func (s *CaptureSessions) prune(now time.Time) {
	for token, state := range s.states {
		if now.After(state.expiresAt) {
			delete(s.states, token)
		}
	}
}

// state prunes sessions using the retained clock and returns the shared
// matching state. The caller holds mu, and missing or expired tokens return the
// same session-not-found error.
func (s *CaptureSessions) state(token string) (*captureSession, error) {
	s.prune(s.now())
	state := s.states[token]
	if state == nil {
		return nil, errors.New("capture session not found or expired")
	}
	return state, nil
}

// Create allocates a random token, stores an empty capture session, and returns
// its URL and certificate authority. The caller provides a positive effective
// ttl and a normalized base URL; ttl is otherwise applied as supplied.
//
// Pruning, token creation, expiry calculation, and publication occur under the
// session mutex. The retained clock can run twice while creating a session, once
// for pruning and once for expiration, so a changing clock can give those
// operations different observations. Entropy failure follows the pinned
// crypto/rand behavior; the URL contains the exact token stored in the map.
func (s *CaptureSessions) Create(baseURL, caPEM string, ttl time.Duration) (*TLSFingerprintCollectorSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(s.now())
	token, err := randomHexToken(24)
	if err != nil {
		return nil, err
	}
	expires := s.now().Add(ttl)
	s.states[token] = &captureSession{token: token, expiresAt: expires}
	return &TLSFingerprintCollectorSession{Token: token, ExpiresAt: expires, CaptureURL: baseURL + "/capture/" + token, CAPEM: caPEM}, nil
}

// Append prepends a shared capture record and retains at most max arrivals for
// the identified session. max needs to be nonnegative, and zero retains none.
//
// Session lookup, expiration pruning, insertion, and truncation use one
// membership lock so the cap applies to the just-updated sequence. The function
// does not copy or validate record, so producers leave captures immutable and
// supply nonnil records before List sorts their timestamps. Retention follows
// arrival order, while later List presentation follows CapturedAt.
func (s *CaptureSessions) Append(token string, record *TLSFingerprintCaptureRecord, max int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.state(token)
	if err != nil {
		return err
	}
	state.records = append([]*TLSFingerprintCaptureRecord{record}, state.records...)
	// Bound retained arrivals after inserting the newest capture, so a full
	// session discards older arrivals even when their CapturedAt values sort later.
	if len(state.records) > max {
		state.records = state.records[:max]
	}
	return nil
}

// List returns a new pointer slice ordered by decreasing CapturedAt for a live
// session. Missing or expired tokens return an error. The membership mutex
// remains held through pruning, copying, and sorting, while capture records and
// their nested values remain shared and immutable.
//
// Append retains records by arrival order before the cap is enforced, whereas
// this presentation sorts the surviving records by capture time. A timestamp
// cannot restore an older discarded arrival. Producers supply nonnil records
// before insertion, and readers leave their fields unchanged because the lock
// protects membership rather than subsequent external object mutation.
func (s *CaptureSessions) List(token string) ([]*TLSFingerprintCaptureRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.state(token)
	if err != nil {
		return nil, err
	}
	out := make([]*TLSFingerprintCaptureRecord, len(state.records))
	copy(out, state.records)
	sort.Slice(out, func(i, j int) bool { return out[i].CapturedAt.After(out[j].CapturedAt) })
	return out, nil
}

// Delete removes the token and its retained pointer sequence under the
// membership lock. A missing token has no effect, and capture objects already
// held by a reader remain available to that reader.
func (s *CaptureSessions) Delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.states, token)
}

// randomHexToken allocates bytesLen random bytes and returns their lowercase
// hexadecimal encoding. The length needs to be nonnegative; the pinned Go
// crypto/rand.Read fills the buffer or terminates on entropy failure rather
// than returning a recoverable read error.
func randomHexToken(bytesLen int) (string, error) {
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
