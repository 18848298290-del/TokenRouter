package payment

import (
	"sync"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// registry.go maintains synchronized payment-method bindings and publishes
// complete replacement provider maps.
// Several payment methods can resolve to the same provider, while registering
// another provider can replace individual method bindings. The registry
// synchronizes that method map and returns shared provider values; each
// adapter retains responsibility for its own configuration and request state.
// Exact provider-key lookup scans the registered values, so multiple matching
// instances have no stable map-order preference.
//
// ProviderBindings builds a replacement list before calling Replace. Replace
// gathers all reported methods before locking, then publishes the complete map
// in one assignment. Existing readers can continue using the old bindings
// during candidate construction, and previously returned providers remain
// usable according to their adapter rules after replacement or clearing.

// ErrProviderNotFound reports missing method or provider-key registration to
// lookup callers and webhook acknowledgement policy.
var ErrProviderNotFound = infraerrors.NotFound("PROVIDER_NOT_FOUND", "payment provider not registered")

// Registry maps payment methods to shared provider instances under a
// read/write mutex. Construct it with NewRegistry before registering providers
// so its method map is initialized.
type Registry struct {
	mu sync.RWMutex // method map synchronization
	// providers holds shared provider values. Register and Replace overwrite
	// method entries under mu; each provider owns synchronization for its own
	// operations and configuration.
	providers map[PaymentType]Provider // registered method providers
}

// NewRegistry returns an empty registry ready for provider registration.
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[PaymentType]Provider),
	}
}

// Register assigns the nonnil provider to each method it reports, replacing
// existing bindings for those methods. The provider remains shared, and its
// SupportedTypes call runs while the registry write lock is held.
func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// The writer lock covers every reported method so concurrent lookups cannot
	// observe this provider's registration halfway through its method list.
	for _, t := range p.SupportedTypes() {
		r.providers[t] = p
	}
}

// GetProvider returns the shared provider registered for an exact payment
// method. A missing method returns ErrProviderNotFound; callers use the
// provider after the registry read lock has been released.
func (r *Registry) GetProvider(t PaymentType) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[t]
	if !ok {
		return nil, ErrProviderNotFound
	}
	return p, nil
}

// GetProviderByKey returns a shared registered provider whose key exactly
// matches key. Missing keys return ErrProviderNotFound, and map iteration
// gives no stable instance preference when several entries match.
func (r *Registry) GetProviderByKey(key string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.providers {
		if p.ProviderKey() == key {
			return p, nil
		}
	}
	return nil, ErrProviderNotFound
}

// GetProviderKey returns the key reported by the provider for an exact method.
// Missing methods return an empty string, which historical-order callers use
// to continue their identity lookup.
func (r *Registry) GetProviderKey(t PaymentType) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[t]
	if !ok {
		return ""
	}
	return p.ProviderKey()
}

// SupportedTypes returns a new slice of the currently registered method keys.
// Their order follows map iteration; modifying the slice leaves registry
// membership unchanged, while the provider values remain shared.
func (r *Registry) SupportedTypes() []PaymentType {
	r.mu.RLock()
	defer r.mu.RUnlock()
	types := make([]PaymentType, 0, len(r.providers))
	for t := range r.providers {
		types = append(types, t)
	}
	return types
}

// Clear replaces the registry method map with an empty map under the write
// lock. Previously returned providers remain available to their callers;
// clearing membership does not close provider resources.
func (r *Registry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Replace membership while holding the lookup mutex. Existing provider
	// references stay with their callers after future lookups start using the
	// empty map.
	r.providers = make(map[PaymentType]Provider)
}

// Replace publishes the method bindings reported by a complete nonnil provider
// list. Later providers in the supplied list win duplicate methods, and the
// providers themselves remain shared with callers.
//
// It builds the replacement before taking the write lock, so SupportedTypes
// work can finish while readers continue using the old map. One locked map
// assignment publishes every accepted binding together; provider-list loading
// and validation remain the responsibility of ProviderBindings.
func (r *Registry) Replace(providers []Provider) {
	next := make(map[PaymentType]Provider)
	for _, provider := range providers {
		for _, kind := range provider.SupportedTypes() {
			next[kind] = provider
		}
	}
	// Candidate methods have all been collected. Swap their map under the same
	// mutex used by lookups so readers observe one completed set.
	r.mu.Lock()
	r.providers = next
	r.mu.Unlock()
}
