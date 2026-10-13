package testkit

import "github.com/TokenFlux/TokenRouter/internal/payment"

// Registry identity scenarios need configured provider keys and supported
// methods while payment operations stay outside the scenario. StaticProvider
// overrides those identity methods and delegates other operations through its
// embedded interface, which needs an implementation when invoked.

// StaticProvider returns configured identity and method values for registry
// tests.
type StaticProvider struct {
	// Provider supplies any operation not overridden by this fixture. A nil
	// embedded interface panics when a caller invokes one of those delegated
	// payment operations.
	payment.Provider
	Key string // fixture provider identity
	// Types is returned directly by SupportedTypes and shares its backing slice
	// with the caller. Registry tests keep the supplied method list stable while
	// using this provider.
	Types []payment.PaymentType
}

// Name returns the configured key as the fixture display name.
func (p StaticProvider) Name() string { return p.Key }

// ProviderKey returns the configured key used by registry identity lookup.
func (p StaticProvider) ProviderKey() string { return p.Key }

// SupportedTypes returns the configured payment-method slice as supplied by
// the test. Its backing storage is shared, so changes affect later registry
// registration or replacement calls using this fixture.
func (p StaticProvider) SupportedTypes() []payment.PaymentType { return p.Types }
