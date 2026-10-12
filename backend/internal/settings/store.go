package settings

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

var ErrSettingNotFound = apperror.NotFound("SETTING_NOT_FOUND", "setting not found")

// Setting represents a row in the settings table.
type Setting struct {
	ID        int64
	Key       string
	Value     string
	UpdatedAt time.Time
}

// Repository reads and writes settings. Implementations must commit batch writes atomically.
type Repository interface {
	Get(context.Context, string) (*Setting, error)
	GetValue(context.Context, string) (string, error)
	Set(context.Context, string, string) error
	GetMultiple(context.Context, []string) (map[string]string, error)
	SetMultiple(context.Context, map[string]string) error
	GetAll(context.Context) (map[string]string, error)
	Delete(context.Context, string) error
}

// ConditionalRepository compares expected values and commits the complete batch atomically.
type ConditionalRepository interface {
	Repository
	CompareAndSetMultiple(context.Context, map[string]string, map[string]*string) error
}

type subscription struct {
	active atomic.Bool
	id     uint64
	fn     func()
}

// Store provides setting reads, writes, and subscriptions. Calling modules own validation and domain caches.
// Call NotifyUpdated after persistence and cache refresh complete.
// @project-doc docs/interfaces/configuration.md#runtime_settings
type Store struct {
	updates     *Updates
	repo        Repository
	mu          sync.RWMutex
	version     string
	nextID      uint64
	subscribers []*subscription
}

// New returns repo when it is already a Store; otherwise it wraps repo in a Store.
func New(repo Repository) *Store {
	if store, ok := repo.(*Store); ok {
		return store
	}
	return &Store{repo: repo, updates: newUpdates(repo)}
}

func (s *Store) Get(ctx context.Context, key string) (*Setting, error) { return s.repo.Get(ctx, key) }

func (s *Store) GetValue(ctx context.Context, key string) (string, error) {
	return s.repo.GetValue(ctx, key)
}

func (s *Store) Set(ctx context.Context, key, value string) error { return s.repo.Set(ctx, key, value) }

func (s *Store) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	return s.repo.GetMultiple(ctx, keys)
}

func (s *Store) SetMultiple(ctx context.Context, values map[string]string) error {
	return s.repo.SetMultiple(ctx, values)
}
func (s *Store) GetAll(ctx context.Context) (map[string]string, error) { return s.repo.GetAll(ctx) }
func (s *Store) Delete(ctx context.Context, key string) error          { return s.repo.Delete(ctx, key) }

// SetVersion sets the application version.
func (s *Store) SetVersion(version string) {
	s.mu.Lock()
	s.version = version
	s.mu.Unlock()
}

func (s *Store) Version() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version
}

// Subscribe registers a synchronous callback in registration order and returns an idempotent unsubscribe
// function. A callback already claimed by NotifyUpdated may still finish after unsubscribe.
func (s *Store) Subscribe(fn func()) func() {
	if fn == nil {
		return func() {}
	}
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	sub := &subscription{id: id, fn: fn}
	sub.active.Store(true)
	s.subscribers = append(s.subscribers, sub)
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			for i, sub := range s.subscribers {
				if sub.id == id {
					sub.active.Store(false)
					s.subscribers = append(s.subscribers[:i], s.subscribers[i+1:]...)
					return
				}
			}
		})
	}
}

// NotifyUpdated invokes active subscribers. Call it after persistence and cache refresh.
// Callbacks execute outside the lock so they can unsubscribe themselves or read settings.
func (s *Store) NotifyUpdated() {
	s.mu.RLock()
	subs := append([]*subscription(nil), s.subscribers...)
	s.mu.RUnlock()
	for _, sub := range subs {
		if sub.active.Load() {
			sub.fn()
		}
	}
}

// Updates returns this Store's combined settings update coordinator.
func (s *Store) Updates() *Updates { return s.updates }

// StringOrDefault returns a nonempty stored value or fallback. Whitespace is retained.
func StringOrDefault(values map[string]string, key, fallback string) string {
	if value, ok := values[key]; ok && value != "" {
		return value
	}
	return fallback
}

// IsExplicitFalse recognizes false, 0, off, and disabled after trimming and case normalization.
func IsExplicitFalse(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "false", "0", "off", "disabled":
		return true
	default:
		return false
	}
}
