package payment

import (
	"context"
	"sync"
)

// A payment method can move between providers while old orders still refer to
// the merchant that accepted their payment. ProviderBindings reads the saved
// instance or snapshot identity, then constructs an adapter from that
// instance's current configuration. Legacy orders require the resolution rules
// in provider_bindings.go to establish a usable instance or registry choice.
//
// Lazy registry loading and refresh share a mutex and readiness flag.
// Construction receives their initial dependencies without loading or starting
// work; alreadyLoaded lets controlled callers use a prepared registry. Loading
// failures keep later attempts possible, while accepted candidate lists are
// published through the registry's complete-map replacement. The factory
// callbacks and store remain responsible for their own request and persistence
// behavior.

// BindingStore reads persisted instance and order identity for provider
// resolution.
type BindingStore interface {
	// Instance reads the addressed payment instance, including a disabled
	// historical instance.
	Instance(context.Context, int64) (*ProviderInstance, error)
	// ListInstances returns instances selected by the requested filters for
	// historical lookup or candidate construction. Legacy resolution includes
	// disabled rows; webhook enumeration requests enabled rows in configured
	// order.
	ListInstances(context.Context, InstanceFilter) ([]*ProviderInstance, error)
	// CountEnabledInstances counts currently enabled instances for an exact
	// provider key. Registry resolution requires this query to succeed and
	// rejects more than one candidate before using a legacy binding.
	CountEnabledInstances(context.Context, string) (int, error)
	// OrderByTradeNumber reads an exact stored merchant number before candidate
	// verification. GetWebhookProviders uses the returned order to resolve a
	// pinned instance; notification processing performs its own later order
	// lookup.
	OrderByTradeNumber(context.Context, string) (*Order, error)
	// IsNotFound classifies storage absence for pinned and legacy instance
	// lookup.
	IsNotFound(error) bool
}

// BindingRuntime supplies adapter construction for historical orders and
// registry publication.
type BindingRuntime struct {
	// Factory constructs the adapter for a resolved order or webhook instance. It
	// receives that instance's current decrypted configuration and remains
	// caller-supplied when those paths are used.
	Factory func(string, string, map[string]string) (Provider, error)
	// RegistryFactory constructs each enabled candidate during registry loading.
	// LoadProviders skips an individual factory error, while failure to list
	// instances retains the previously published map.
	RegistryFactory func(string, string, map[string]string) (Provider, error)
	Warn            func(string, ...any) // provider resolution warning logger
}

// ProviderBindings resolves order-specific adapters and coordinates provider
// registry initialization.
type ProviderBindings struct {
	// providerMu and providersLoaded coordinate lazy loading and refresh. Both
	// loading paths hold this mutex while building candidates, then publish their
	// accepted list through Registry.Replace.
	providerMu sync.Mutex // provider refresh serialization mutex
	// providersLoaded starts from the constructor's alreadyLoaded argument.
	// Successful publication sets it true; a checked refresh list failure clears
	// it so later EnsureProviders can try loading again.
	providersLoaded bool           // lazy registry readiness flag
	store           BindingStore   // historical identity persistence
	registry        *Registry      // shared method binding registry
	loadBalancer    LoadBalancer   // instance configuration reader
	runtime         BindingRuntime // injected adapter construction operations
}

// NewProviderBindings retains the store, registry, selector, and construction
// callbacks as supplied. alreadyLoaded skips lazy registry population until
// refresh clears that state, so callers passing true provide the registry
// state their lookup paths require.
func NewProviderBindings(store BindingStore, registry *Registry, balancer LoadBalancer, runtime BindingRuntime, alreadyLoaded bool) *ProviderBindings {
	return &ProviderBindings{store: store, registry: registry, loadBalancer: balancer, runtime: runtime, providersLoaded: alreadyLoaded}
}

// warn forwards binding diagnostics through the optional warning callback.
// Missing logging support discards the event, while provider loading and
// resolution keep the result chosen by their calling operation.
func (s *ProviderBindings) warn(message string, attrs ...any) {
	if s.runtime.Warn != nil {
		s.runtime.Warn(message, attrs...)
	}
}
