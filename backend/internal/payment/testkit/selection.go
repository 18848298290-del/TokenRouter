package testkit

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/payment"
)

// selection.go records provider and method selections in a load-balancer
// fixture for visible-routing tests.
// Visible payment routing chooses which provider key reaches the underlying
// selector. CaptureLoadBalancer records that key and payment method, then
// returns a minimal selection, so scenarios can inspect routing decisions
// before any configured adapter or payment request is needed.

// CaptureLoadBalancer records the provider and method forwarded by visible
// routing tests.
type CaptureLoadBalancer struct {
	// LastProviderKey and LastPaymentType are replaced together by each
	// synchronous SelectInstance call. Tests inspect them after that call
	// returns; this fixture adds no synchronization for concurrent selection.
	LastProviderKey string // last requested provider key
	LastPaymentType string // last requested payment method
}

// GetInstanceConfig returns a new writable empty configuration for any
// instance ID. The fixture ignores context and performs no persistence read,
// letting routing tests avoid constructing a configured provider adapter.
func (c *CaptureLoadBalancer) GetInstanceConfig(context.Context, int64) (map[string]string, error) {
	return map[string]string{}, nil
}

// SelectInstance records the supplied provider key and payment type, then
// returns them in a new selection. Strategy and amount are ignored; no
// instance ID or configuration is allocated by this routing fixture.
func (c *CaptureLoadBalancer) SelectInstance(_ context.Context, providerKey string, paymentType payment.PaymentType, _ payment.Strategy, _ float64) (*payment.InstanceSelection, error) {
	c.LastProviderKey = providerKey
	c.LastPaymentType = paymentType
	return &payment.InstanceSelection{ProviderKey: providerKey, SupportedTypes: paymentType}, nil
}
