package testkit

import "github.com/TokenFlux/TokenRouter/internal/payment"

// StaticProvider supplies provider identity and payment types. Unconfigured calls through a nil embedded
// Provider panic.
type StaticProvider struct {
	payment.Provider
	Key   string
	Types []payment.PaymentType
}

func (p StaticProvider) Name() string                          { return p.Key }
func (p StaticProvider) ProviderKey() string                   { return p.Key }
func (p StaticProvider) SupportedTypes() []payment.PaymentType { return p.Types }
