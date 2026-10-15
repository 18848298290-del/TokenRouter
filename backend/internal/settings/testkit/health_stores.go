package testkit

import (
	"context"
	"maps"
	"sync"

	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
)

// health_stores.go provides in-memory settings storage with injectable read
// failures for tests.
// Settings reader tests need controllable storage errors and observable read
// counts while their runtime caches can progress concurrently. This fixture
// protects saved strings and read-count changes with one mutex, returning copied
// maps to keep assertion-side changes separate from stored values. GetValue
// can inject a failure and treats absence as empty text; Get reports absence
// through the settings sentinel.

// Memory holds settings fixture state and synchronized raw-value read observations.
type Memory struct {
	// Mu protects Data, GetValueErr, and GetValueCalls in all fixture methods.
	// Tests that mutate or inspect these exposed fields while calls can run
	// hold the same mutex, and keep the Memory value at a stable address.
	Mu            sync.Mutex        // fixture state access lock
	Data          map[string]string // saved raw setting strings
	GetValueErr   error             // injected raw-value read failure
	GetValueCalls int               // observed GetValue invocation count
}

// NewMemory constructs the fixture with an allocated empty setting map.
func NewMemory() *Memory {
	return &Memory{Data: make(map[string]string)}
}

// Get returns a new core record containing the saved key and value under
// the fixture lock. An absent key returns ErrSettingNotFound; contexts are ignored
// and record identifiers and timestamps stay at their zero values.
func (m *Memory) Get(_ context.Context, key string) (*settingscore.Setting, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	v, ok := m.Data[key]
	if !ok {
		return nil, settingscore.ErrSettingNotFound
	}
	return &settingscore.Setting{Key: key, Value: v}, nil
}

// GetValue increments the call count and returns the configured error before
// reading saved text. Missing keys return empty text with nil error; this fixture
// ignores context cancellation and differs from production absence classification.
func (m *Memory) GetValue(_ context.Context, key string) (string, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	// Count attempted reads before injected failure selection so cache tests
	// can observe requests that reached storage even when their result was unavailable.
	m.GetValueCalls++
	if m.GetValueErr != nil {
		return "", m.GetValueErr
	}
	v, ok := m.Data[key]
	if !ok {
		return "", nil
	}
	return v, nil
}

// Set replaces one saved string under the fixture mutex. Its context is
// ignored, and the fixture has no injected write failure; NewMemory supplies the
// map required for these in-place assignments.
func (m *Memory) Set(_ context.Context, key, value string) error {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	m.Data[key] = value
	return nil
}

// GetMultiple returns a new map of saved requested values under the fixture
// lock. Absent keys are omitted, contexts are ignored, and callers can change
// the returned map independently of stored entries.
func (m *Memory) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	result := make(map[string]string)
	for _, k := range keys {
		if v, ok := m.Data[k]; ok {
			result[k] = v
		}
	}
	return result, nil
}

// SetMultiple copies a batch into saved fixture state under one mutex hold.
// The context is ignored and no write failure is injected, so synchronized
// fixture readers observe the map before or after that completed copy.
func (m *Memory) SetMultiple(_ context.Context, settings map[string]string) error {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	maps.Copy(m.Data, settings)
	return nil
}

// GetAll copies the saved key/value map while holding the fixture mutex.
// Contexts are ignored, and callers can change the returned map while retaining
// the fixture's separately stored string entries.
func (m *Memory) GetAll(_ context.Context) (map[string]string, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	result := make(map[string]string, len(m.Data))
	maps.Copy(result, m.Data)
	return result, nil
}

// Delete removes a fixture key while holding its state mutex. Absence succeeds,
// contexts are ignored, and the fixture supplies no deletion-error path for its
// caller to distinguish from ordinary cleanup.
func (m *Memory) Delete(_ context.Context, key string) error {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	delete(m.Data, key)
	return nil
}
