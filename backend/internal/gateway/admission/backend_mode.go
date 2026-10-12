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

const BackendModeKey = "backend_mode_enabled"

// BackendModeReader reads the gateway backend-mode setting.
type BackendModeReader interface {
	GetValue(context.Context, string) (string, error)
}

type backendModeSnapshot struct {
	enabled bool
	expires time.Time
}

// BackendMode caches the setting and uses a generation check to coordinate refreshes with publication.
type BackendMode struct {
	reader     BackendModeReader
	warn       func(string, ...any)
	cached     atomic.Pointer[backendModeSnapshot]
	mu         sync.Mutex
	generation uint64
	flight     singleflight.Group
}

func NewBackendMode(reader BackendModeReader, warn func(string, ...any)) *BackendMode {
	return &BackendMode{reader: reader, warn: warn}
}

// Publish replaces the cached value and invalidates older refreshes. Call it after persistence succeeds.
func (m *BackendMode) Publish(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generation++
	m.cached.Store(&backendModeSnapshot{enabled: enabled, expires: time.Now().Add(time.Minute)})
	m.flight.Forget(BackendModeKey)
}

// Enabled uses a separate five-second read budget and caches successful reads for one minute, failures for five seconds.
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
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		value, err := m.reader.GetValue(readCtx, BackendModeKey)
		ttl := time.Minute
		if err != nil && !errors.Is(err, settings.ErrSettingNotFound) {
			ttl = 5 * time.Second
			if m.warn != nil {
				m.warn("failed to get backend_mode_enabled setting", "error", err)
			}
		}
		next := &backendModeSnapshot{enabled: err == nil && value == "true", expires: time.Now().Add(ttl)}
		m.mu.Lock()
		defer m.mu.Unlock()
		if generation == m.generation {
			m.cached.Store(next)
		}
		return m.cached.Load(), nil
	})
	// Prefer a completed administrative publication even for callers waiting on singleflight.
	if cached := m.cached.Load(); cached != nil {
		return cached.enabled
	}
	cached, _ := result.(*backendModeSnapshot)
	return cached != nil && cached.enabled
}

// IsBackendModeEnabled reads the cached setting for identity HTTP handlers.
func (m *BackendMode) IsBackendModeEnabled(ctx context.Context) bool { return m.Enabled(ctx) }
