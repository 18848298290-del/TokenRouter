package batchimage

import (
	"strings"
)

// Batch backends are selected by their exact registered names. Registration
// ignores blank names and a nil interface value, and later entries replace
// earlier entries with the same name; public selection and result access
// share this lookup.

type (
	// NamedProvider supplies the exact lookup name of a batch execution backend.
	NamedProvider interface {
		// Name returns the exact backend name used as the registration lookup key.
		Name() string
	}
	// Registry retains named batch backend instances for shared
	// construction-time registration.
	Registry[T NamedProvider] struct {
		providers map[string]T /* exact backend registrations */
	}
)

// NewRegistry records each provider under the name returned by its Name
// method. Blank names and nil interface values are skipped, and the last
// registration for an exact name replaces earlier entries.
func NewRegistry[T NamedProvider](providers ...T) *Registry[T] {
	r := &Registry[T]{providers: make(map[string]T, len(providers))}
	for _, p := range providers {
		if any(p) == nil || strings.TrimSpace(p.Name()) == "" {
			continue
		}
		r.providers[p.Name()] = p
	}
	return r
}

// Get returns the provider registered under the exact supplied name. A nil
// registry or missing name returns the zero value and false, allowing callers
// to test backend availability.
func (r *Registry[T]) Get(name string) (T, bool) {
	var empty T
	if r == nil {
		return empty, false
	}
	p, ok := r.providers[name]
	return p, ok
}

// MustGet returns the provider registered under the exact supplied name.
// Missing entries, including lookup through a nil registry, return
// ErrBatchImageInvalidProvider so callers can reject the backend.
func (r *Registry[T]) MustGet(name string) (T, error) {
	p, ok := r.Get(name)
	if !ok {
		return p, ErrBatchImageInvalidProvider
	}
	return p, nil
}
