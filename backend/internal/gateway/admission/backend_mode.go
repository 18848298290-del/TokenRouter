package admission

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// Backend-mode admission reads are cached so identity handlers can consult the
// setting without a database read on each request. Successful reads and missing
// settings expire after one minute; other read failures produce a disabled
// snapshot with a five-second lifetime. A refresh uses an independent timeout
// so cancellation of the first waiting request does not determine other callers'
// setting value.
//
// Administrative publication can finish while an older refresh is reading the
// database. The generation protected by mu prevents that older value from
// replacing the published snapshot, and readers prefer the latest stored value
// after the shared refresh completes.

// BackendModeKey identifies the persisted toggle read by identity admission and
// published after administrative settings updates succeed.
const BackendModeKey = "backend_mode_enabled"

// BackendModeReader supplies persisted setting values for backend-mode admission.
type BackendModeReader interface {
	// GetValue reads the named setting using the supplied context. A missing value
	// returns settings.ErrSettingNotFound so BackendMode can distinguish an
	// unconfigured toggle from a temporary storage failure.
	GetValue(context.Context, string) (string, error)
}

// backendModeSnapshot is an immutable setting value with its read deadline.
type backendModeSnapshot struct {
	enabled bool      // effective backend-mode toggle
	expires time.Time // cache validity deadline
}

// BackendMode caches the setting and uses a generation check to coordinate refreshes with publication.
type BackendMode struct {
	reader BackendModeReader // persisted setting source
	// warn receives refresh errors other than missing settings. A nil callback
	// disables these diagnostics while the refresh still publishes its disabled snapshot.
	warn func(string, ...any)
	// cached points to immutable snapshots. Atomic replacement lets valid cache
	// hits proceed while another caller refreshes or publishes the setting.
	cached atomic.Pointer[backendModeSnapshot]
	// mu and generation order publication against refresh completion. Refreshes
	// capture generation before reading and can publish their result only while
	// that generation still matches.
	mu         sync.Mutex         // generation update guard
	generation uint64             // completed publication version
	flight     singleflight.Group // shared setting refresh
}

// NewBackendMode constructs a cache backed by reader and an optional warning
// callback. Supply a usable reader before calling Enabled; construction performs
// no setting read and leaves the cache empty.
func NewBackendMode(reader BackendModeReader, warn func(string, ...any)) *BackendMode {
	return &BackendMode{reader: reader, warn: warn}
}

// Publish installs a backend-mode value after the administrative update has
// successfully persisted. The value remains valid for one minute, so subsequent
// reads can use it immediately.
//
// Increasing the generation prevents a refresh that started before this call
// from replacing the snapshot when its database read finishes. Forgetting the
// singleflight key lets a later expired-cache read start a fresh refresh while
// callers of the older refresh still obtain the current stored value.
func (m *BackendMode) Publish(enabled bool) {
	// Serialize the generation advance and snapshot replacement so an in-flight
	// refresh cannot publish a stale database value between these two operations.
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generation++
	m.cached.Store(&backendModeSnapshot{enabled: enabled, expires: time.Now().Add(time.Minute)})
	m.flight.Forget(BackendModeKey)
}

// Enabled reports whether the cached or freshly read backend-mode setting is
// exactly "true". Pass a nonnil context; its values reach the database read, but
// its cancellation and deadline are replaced by a five-second read budget.
//
// Concurrent expired-cache reads share one refresh. Successful reads and missing
// settings are cached for one minute, while other read failures produce disabled
// snapshots that expire after five seconds. A publication that advances generation
// during the read wins over that refresh, including for callers already waiting
// on its singleflight result.
func (m *BackendMode) Enabled(ctx context.Context) bool {
	if cached := m.cached.Load(); cached != nil && time.Now().Before(cached.expires) {
		return cached.enabled
	}
	result, _, _ := m.flight.Do(BackendModeKey, func() (any, error) {
		m.mu.Lock()
		generation := m.generation
		cached := m.cached.Load()
		m.mu.Unlock()
		if cached != nil && time.Now().Before(cached.expires) {
			return cached, nil
		}
		// A shared read serves other waiting requests too. Detach the initiating
		// request's cancellation and bound the database operation with its own timeout.
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		value, err := m.reader.GetValue(readCtx, BackendModeKey)
		ttl := time.Minute
		if err != nil && !errors.Is(err, settings.ErrSettingNotFound) {
			// Storage errors disable the mode temporarily. A short lifetime allows
			// recovery to become visible sooner than an ordinary successful read.
			ttl = 5 * time.Second
			if m.warn != nil {
				m.warn("failed to get backend_mode_enabled setting", "error", err)
			}
		}
		next := &backendModeSnapshot{enabled: err == nil && value == "true", expires: time.Now().Add(ttl)}
		m.mu.Lock()
		defer m.mu.Unlock()
		// An administrative publication advances generation while the read is
		// unlocked, so discard this read's value when a newer publication exists.
		if generation == m.generation {
			m.cached.Store(next)
		}
		return m.cached.Load(), nil
	})
	// A publication may finish after the shared read returns. Load its snapshot
	// again so waiting callers observe that completed administrative update.
	if cached := m.cached.Load(); cached != nil {
		return cached.enabled
	}
	cached, _ := result.(*backendModeSnapshot)
	return cached != nil && cached.enabled
}

// IsBackendModeEnabled delegates identity-handler admission reads to the shared
// backend-mode cache. It uses Enabled's independent refresh timeout and publication
// ordering, with missing settings or failed reads producing disabled snapshots.
func (m *BackendMode) IsBackendModeEnabled(ctx context.Context) bool { return m.Enabled(ctx) }
