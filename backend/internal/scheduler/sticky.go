package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cespare/xxhash/v2"

	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// Session affinity is stored under a group and a platform-supplied prefix,
// with current and optional legacy hashes naming the same logical session. A
// successful current lookup wins; legacy lookup can recover a positive binding
// after a current miss or error. Independent counters record those read
// attempts and successful dual writes for process-local observation.
//
// Writes install the current key before an optional shorter-lived legacy key,
// so a legacy failure can follow a committed current binding. Refresh and
// deletion return the current operation's error while treating legacy work as
// best effort. Escape decisions separately compare shared provider feedback
// with the request's policy and return a reason without mutating the persisted
// binding, leaving the owning selector to choose another eligible provider for
// that request.

// StickyCache reads and writes provider affinity under a group-scoped session
// key.
type StickyCache interface {
	// GetSessionProviderID returns the provider ID stored for a group and session
	// key. Missing or failed lookups are interpreted by the owning selector,
	// which can continue selection or attempt legacy-key recovery.
	GetSessionProviderID(context.Context, int64, string) (int64, error)
	// SetSessionProviderID stores provider affinity with the supplied lifetime.
	// Callers pass their platform prefix and group namespace; a zero Redis
	// lifetime leaves the key persistent under the adapter's SET behavior.
	SetSessionProviderID(context.Context, int64, string, int64, time.Duration) error
	// RefreshSessionTTL renews the supplied binding key's lifetime when it
	// exists. The Redis adapter's missing key is a successful no-op, and callers
	// decide whether a refresh error affects selection.
	RefreshSessionTTL(context.Context, int64, string, time.Duration) error
	// DeleteSessionProviderID removes the supplied group/session binding key. The
	// Redis adapter accepts a missing key, while selectors invoke deletion after
	// binding eligibility changes or the corresponding provider becomes
	// unavailable.
	DeleteSessionProviderID(context.Context, int64, string) error
}

// StickyStats records independent process-local observations of legacy lookup
// and dual-write activity.
type StickyStats struct {
	readFallbackTotal atomic.Int64 // legacy recovery lookup attempts
	readFallbackHit   atomic.Int64 // positive legacy recovery hits
	dualWriteTotal    atomic.Int64 // successful legacy companion writes
}

// StickyOptions controls key prefixes, legacy compatibility, and
// companion-binding lifetime defaults.
type StickyOptions struct {
	Prefix          string // platform session key prefix
	ReadLegacy      bool   // legacy lookup recovery switch
	DualWriteLegacy bool   // legacy companion write switch
	// DefaultTTL supplies the legacy companion lifetime when the requested
	// duration is nonpositive. LegacyTTL subsequently caps that chosen value at
	// ten minutes; current-key writes retain the supplied duration.
	DefaultTTL time.Duration // legacy default key lifetime
}

// StickySession applies shared cache operations to one platform's current and
// legacy session keys.
type StickySession struct {
	cache   StickyCache   // shared group affinity cache
	options StickyOptions // configured compatibility and key rules
	// stats is required when a legacy lookup or successful dual write records an
	// observation. NewStickySession retains the supplied pointer, so those
	// observations remain shared across callers.
	stats *StickyStats // shared legacy compatibility counters
}

// Snapshot reads legacy lookup attempts, positive hits, and successful
// companion writes. The counters are loaded independently, so callers receive
// an observational sample rather than one atomic tuple during concurrent
// updates.
func (s *StickyStats) Snapshot() (int64, int64, int64) {
	return s.readFallbackTotal.Load(), s.readFallbackHit.Load(), s.dualWriteTotal.Load()
}

// NewStickySession binds a shared cache, compatibility options, and
// observation counters. It retains these supplied values; callers provide
// nonnil counters whenever enabled legacy paths can record reads or companion
// writes.
func NewStickySession(cache StickyCache, options StickyOptions, stats *StickyStats) *StickySession {
	return &StickySession{cache: cache, options: options, stats: stats}
}

// SessionKey trims a current session hash and applies the configured platform
// prefix. Empty or whitespace-only hashes produce an empty key so affinity
// operations can skip requests lacking a usable session identity.
func (s *StickySession) SessionKey(hash string) string {
	v := strings.TrimSpace(hash)
	if v == "" {
		return ""
	}
	return s.options.Prefix + v
}

// LegacyKey trims a legacy hash and returns its prefixed compatibility key. An
// empty legacy hash or a key equal to SessionKey returns empty, preventing a
// duplicate compatibility operation on the current key.
func (s *StickySession) LegacyKey(hash, legacy string) string {
	v := strings.TrimSpace(legacy)
	if v == "" {
		return ""
	}
	key := s.options.Prefix + v
	if key == s.SessionKey(hash) {
		return ""
	}
	return key
}

// LegacyTTL selects the compatibility key's lifetime from the supplied
// duration or DefaultTTL. Durations above ten minutes are capped, while a
// nonpositive configured default can remain nonpositive after that selection.
func (s *StickySession) LegacyTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		ttl = s.options.DefaultTTL
	}
	// Long current-key lifetimes still give legacy companions a short retention window.
	if ttl > 10*time.Minute {
		return 10 * time.Minute
	}
	return ttl
}

// DeriveSessionHashes returns current xxhash hexadecimal and legacy SHA256
// hexadecimal session identities from one trimmed value. Empty or
// whitespace-only input returns two empty strings, allowing both affinity
// paths to recognize an absent session.
//
// The same normalized seed supplies both encodings, so surrounding whitespace
// cannot select different logical sessions depending on which representation
// is read. Callers carry both results into compatibility lookup and companion
// writes; prefixes and group namespaces are added by StickySession or the
// owning selector.
func DeriveSessionHashes(sessionID string) (currentHash string, legacyHash string) {
	normalized := strings.TrimSpace(sessionID)
	if normalized == "" {
		return "", ""
	}

	currentHash = fmt.Sprintf("%016x", xxhash.Sum64String(normalized))
	sum := sha256.Sum256([]byte(normalized))
	legacyHash = hex.EncodeToString(sum[:])
	return currentHash, legacyHash
}

// Get reads a positive current binding, then optionally recovers from the
// distinct legacy key. Missing cache or current key returns zero, while an
// unsuccessful legacy read leaves the original current result and error
// available.
//
// A legacy positive hit replaces a missing or failed current lookup and
// increments its hit counter. Legacy attempts increment a separate counter, so
// failed compatibility reads remain observable even when their own error is
// suppressed. The method returns the binding it found; the owning selector
// still checks provider eligibility before using that affinity.
func (s *StickySession) Get(ctx context.Context, groupID int64, sessionHash, legacyHash string) (int64, error) {
	if s == nil || s.cache == nil {
		return 0, nil
	}

	primaryKey := s.SessionKey(sessionHash)
	if primaryKey == "" {
		return 0, nil
	}

	providerID, err := s.cache.GetSessionProviderID(ctx, groupID, primaryKey)
	if err == nil && providerID > 0 {
		return providerID, nil
	}
	if !s.options.ReadLegacy {
		return providerID, err
	}

	legacyKey := s.LegacyKey(sessionHash, legacyHash)
	if legacyKey == "" {
		return providerID, err
	}

	// A current error or absent positive binding can still be recovered from
	// compatibility state. Record the attempt before querying the distinct legacy key.
	s.stats.readFallbackTotal.Add(1)
	legacyProviderID, legacyErr := s.cache.GetSessionProviderID(ctx, groupID, legacyKey)
	if legacyErr == nil && legacyProviderID > 0 {
		s.stats.readFallbackHit.Add(1)
		return legacyProviderID, nil
	}
	// An unsuccessful legacy lookup supplies no replacement binding. Retain the
	// current result and error so compatibility failure does not hide that primary observation.
	return providerID, err
}

// Set writes current affinity and then an optional distinct legacy companion
// binding. Missing cache, absent session key, or nonpositive provider ID skips
// the operation. The primary write uses the supplied lifetime and the
// companion uses LegacyTTL.
//
// Current publication precedes compatibility publication, and a legacy write
// error can follow a successful current write. That failure is returned to the
// caller rather than rolling back primary affinity. The companion counter
// increases after successful legacy publication, so it records completed dual
// writes rather than attempted primary writes.
func (s *StickySession) Set(ctx context.Context, groupID int64, sessionHash, legacyHash string, providerID int64, ttl time.Duration) error {
	if s == nil || s.cache == nil || providerID <= 0 {
		return nil
	}
	primaryKey := s.SessionKey(sessionHash)
	if primaryKey == "" {
		return nil
	}

	// Current-key readers stop at a positive hit. Publish that key before its
	// legacy companion so a companion-write failure still leaves the preferred
	// lookup pointing to this provider.
	if err := s.cache.SetSessionProviderID(ctx, groupID, primaryKey, providerID, ttl); err != nil {
		return err
	}

	if !s.options.DualWriteLegacy {
		return nil
	}
	legacyKey := s.LegacyKey(sessionHash, legacyHash)
	if legacyKey == "" {
		return nil
	}
	if err := s.cache.SetSessionProviderID(ctx, groupID, legacyKey, providerID, s.LegacyTTL(ttl)); err != nil {
		return err
	}
	s.stats.dualWriteTotal.Add(1)
	return nil
}

// Refresh renews current affinity and optionally its distinct legacy
// companion. A nil receiver, missing cache, or empty current key completes
// successfully without cache work. The requested lifetime applies to the
// current key, while either compatibility option enables legacy upkeep with
// LegacyTTL.
//
// The current refresh runs before legacy upkeep, even when it reports an
// error. Legacy errors are suppressed and the primary error is returned, so
// success describes the primary operation; it can accompany an absent or
// unrefreshed companion. The Redis adapter accepts a missing current key as a
// successful no-op.
func (s *StickySession) Refresh(ctx context.Context, groupID int64, sessionHash, legacyHash string, ttl time.Duration) error {
	if s == nil || s.cache == nil {
		return nil
	}
	primaryKey := s.SessionKey(sessionHash)
	if primaryKey == "" {
		return nil
	}

	err := s.cache.RefreshSessionTTL(ctx, groupID, primaryKey, ttl)
	if !s.options.ReadLegacy && !s.options.DualWriteLegacy {
		return err
	}

	legacyKey := s.LegacyKey(sessionHash, legacyHash)
	if legacyKey != "" {
		// When the current key was renewed, Get can reuse its binding even if legacy
		// renewal fails. Return the primary error so the selector sees that
		// current-key outcome.
		_ = s.cache.RefreshSessionTTL(ctx, groupID, legacyKey, s.LegacyTTL(ttl))
	}
	return err
}

// Delete removes current affinity and optionally its distinct legacy
// companion. A nil receiver, missing cache, or empty current key skips cache
// work. Either compatibility option enables deletion of a distinct legacy key,
// using the same group namespace.
//
// The current deletion runs first and its error is retained while legacy
// deletion is attempted. Legacy errors are suppressed, so a successful current
// deletion can leave the companion key present. Callers that encounter that
// remaining compatibility binding still perform provider eligibility checks
// when recovering through Get.
func (s *StickySession) Delete(ctx context.Context, groupID int64, sessionHash, legacyHash string) error {
	if s == nil || s.cache == nil {
		return nil
	}
	primaryKey := s.SessionKey(sessionHash)
	if primaryKey == "" {
		return nil
	}

	err := s.cache.DeleteSessionProviderID(ctx, groupID, primaryKey)
	if !s.options.ReadLegacy && !s.options.DualWriteLegacy {
		return err
	}

	legacyKey := s.LegacyKey(sessionHash, legacyHash)
	if legacyKey != "" {
		// Try legacy removal even after a primary error because Get can recover from
		// a later primary miss or failure. Ignore this cleanup error so it cannot
		// replace the primary deletion result.
		_ = s.cache.DeleteSessionProviderID(ctx, groupID, legacyKey)
	}
	return err
}

// ShouldEscapeSticky compares shared provider feedback with the supplied
// request policy and returns the observed values and escape reason. Disabled
// policy, missing stats, or nonpositive IDs produce no escape.
//
// A positive TTFT sample is tested first and takes precedence when both
// thresholds are exceeded. Threshold equality remains accepted because
// comparisons are strict. Error rate starts from the runtime's unobserved zero
// baseline, while missing TTFT does not trigger latency escape; the method
// reports the decision for this request and leaves binding updates to its
// caller.
func ShouldEscapeSticky(stats *RuntimeStats, providerID int64, cfg policy.StickyEscapeConfig) (reason string, errorRate float64, ttft float64, shouldEscape bool) {
	if !cfg.Enabled || stats == nil || providerID <= 0 {
		return "", 0, 0, false
	}
	errorRate, ttft, hasTTFT := stats.Snapshot(providerID)
	if hasTTFT && ttft > cfg.TtftMs {
		return "ttft", errorRate, ttft, true
	}
	if errorRate > cfg.ErrorRate {
		return "error_rate", errorRate, ttft, true
	}
	return "", errorRate, ttft, false
}
