package moderation

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// Gateway Cyber session blocking reads enablement and lifetime together from a
// published entry, while each reload fetches the two database keys separately.
// A valid entry is reused for sixty seconds; an uncertain enablement read
// disables blocking for five seconds, and a TTL read or parse failure selects
// one hour.
//
// Concurrent cache misses share one singleflight callback. That callback
// retains the first caller context values and detaches its cancellation before
// applying a database deadline. Waiting callers therefore share completion of
// the reload, and their individual cancellation does not interrupt Do. Atomic
// publication keeps readers from observing a partially assembled cache entry.

// Cyber runtime reads use separate successful-cache, failed-enable-read, and
// database budgets to control reload frequency and detached storage work.
const (
	cyberSessionBlockRuntimeCacheTTL  = 60 * time.Second
	cyberSessionBlockRuntimeErrorTTL  = 5 * time.Second
	cyberSessionBlockRuntimeDBTimeout = 5 * time.Second
)

// RuntimeSettings publishes cached Cyber session enablement and lifetime for
// gateway session-block reads.
type RuntimeSettings struct {
	settingRepo RuntimeSettingsStore // runtime setting value reader
	notFound    error                // recognized missing setting error
	// cyberSessionBlockCache and cyberSessionBlockSF coordinate publication and
	// reload. Entries stay immutable after atomic publication; singleflight
	// waiters share the first loader result and wait until its callback returns.
	cyberSessionBlockCache atomic.Value       // immutable published runtime entry
	cyberSessionBlockSF    singleflight.Group // shared storage read group
}

// RuntimeSettingsStore reads individual settings for the cached Cyber
// session-block gateway configuration.
type RuntimeSettingsStore interface {
	// GetValue returns a stored value or a read error for one key. The reader
	// compares enablement failures with its configured missing-setting error and
	// supplies a detached context with a database deadline.
	GetValue(context.Context, string) (string, error)
}

// cachedCyberSessionBlockRuntime is one immutable enablement and lifetime entry
// published to gateway readers.
type cachedCyberSessionBlockRuntime struct {
	// enabled, ttl, and expiresAt enter one immutable cache entry. ttl supplies
	// the session-block store lifetime, while expiresAt controls how long this
	// settings pair is reused.
	enabled   bool          // session blocking effective switch
	ttl       time.Duration // converted session entry lifetime
	expiresAt int64         // Unix nanoseconds
}

// NewRuntimeSettings binds the setting reader and its missing-key error for
// lazy Cyber configuration reads. Construction performs no storage work; a nil
// reader later returns disabled with a one-hour session lifetime.
func NewRuntimeSettings(repo RuntimeSettingsStore, notFound error) *RuntimeSettings {
	return &RuntimeSettings{settingRepo: repo, notFound: notFound}
}

// GetCyberSessionBlockRuntime returns cached session enablement and lifetime,
// using disabled and one hour when no reader is available. Nil context selects
// Background. The first uncached caller supplies context values to a detached
// five-second storage context; singleflight.Do waiters stay blocked until that
// read completes even if their own contexts cancel.
//
// An enablement read error other than the configured missing-key error
// publishes disabled for five seconds. Missing enablement is disabled, while a
// failed TTL read, malformed integer, or nonpositive stored seconds uses one
// hour in the ordinary sixty-second entry. The two keys are read separately and
// can observe different stored update moments.
//
// Positive stored seconds are multiplied into time.Duration without an
// upper-range check. Overflow can yield a nonpositive duration that is cached
// as supplied; gateway session.CyberBlocks.Runtime forwards that value to its
// caller.
func (s *RuntimeSettings) GetCyberSessionBlockRuntime(ctx context.Context) (bool, time.Duration) {
	if s == nil || s.settingRepo == nil {
		return false, time.Hour
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if cached, ok := s.cyberSessionBlockCache.Load().(*cachedCyberSessionBlockRuntime); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.enabled, cached.ttl
		}
	}
	// Recheck inside the shared load because another caller may publish between
	// the outer test and singleflight admission. Every callback path returns nil
	// error; storage failures are handled within its entry selection, so the
	// discarded Do error does not contain them.
	result, _, _ := s.cyberSessionBlockSF.Do("cyber_session_block_runtime", func() (any, error) {
		if cached, ok := s.cyberSessionBlockCache.Load().(*cachedCyberSessionBlockRuntime); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached, nil
			}
		}
		// Detach cancellation from the first request so a canceled caller does not
		// abandon the shared reload that other gateway requests are still awaiting.
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cyberSessionBlockRuntimeDBTimeout)
		defer cancel()

		enabledVal, enabledErr := s.settingRepo.GetValue(dbCtx, SettingKeyCyberSessionBlockEnabled)
		ttlVal, ttlErr := s.settingRepo.GetValue(dbCtx, SettingKeyCyberSessionBlockTTLSeconds)
		if enabledErr != nil && !errors.Is(enabledErr, s.notFound) {
			// Disable session blocking after an uncertain enablement read, and publish
			// a short retry entry so that database recovery can be observed sooner.
			slog.Warn("failed to get cyber session block setting", "error", enabledErr)
			entry := &cachedCyberSessionBlockRuntime{
				enabled:   false,
				ttl:       time.Hour,
				expiresAt: time.Now().Add(cyberSessionBlockRuntimeErrorTTL).UnixNano(),
			}
			s.cyberSessionBlockCache.Store(entry)
			return entry, nil
		}

		// A TTL read or parse failure keeps the default session lifetime while
		// enablement still follows its separate setting read in this reload.
		ttl := time.Hour
		if ttlErr == nil {
			if seconds, err := strconv.Atoi(strings.TrimSpace(ttlVal)); err == nil && seconds > 0 {
				// Multiply accepted whole seconds into nanoseconds using signed duration
				// arithmetic; no upper-range check occurs before the converted value enters
				// the published settings entry.
				ttl = time.Duration(seconds) * time.Second
			}
		}
		entry := &cachedCyberSessionBlockRuntime{
			enabled:   enabledErr == nil && strings.TrimSpace(enabledVal) == "true",
			ttl:       ttl,
			expiresAt: time.Now().Add(cyberSessionBlockRuntimeCacheTTL).UnixNano(),
		}
		// Publish the complete immutable pair after both reads so cache-hit callers
		// see one entry, including its matching expiry, through the atomic value.
		s.cyberSessionBlockCache.Store(entry)
		return entry, nil
	})
	if entry, ok := result.(*cachedCyberSessionBlockRuntime); ok && entry != nil {
		return entry.enabled, entry.ttl
	}
	// An unexpected shared-load result uses the same disabled default as an absent
	// reader; the public return shape has no error channel for this condition.
	return false, time.Hour
}
