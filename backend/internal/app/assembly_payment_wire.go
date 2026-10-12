//go:build wireinject

package app

import (
	"github.com/google/wire"

	"github.com/TokenFlux/TokenRouter/internal/payment"
	paymentpostgres "github.com/TokenFlux/TokenRouter/internal/payment/postgres"
)

// Payment runtime, public endpoints, and administrator handlers share
// persisted provider instances and the application provider registry. Wire
// binds DefaultLoadBalancer to payment.LoadBalancer, so order processing
// receives selection backed by the same instance store and configured
// encryption key.

var (
	// paymentAssemblyProviders supplies payment runtime, expiry handling, and
	// public, administrator, and webhook HTTP constructors for application routes.
	paymentAssemblyProviders = wire.NewSet(
		providePaymentExpiry,
		providePaymentHTTP,
		providePaymentAdminHTTP,
		providePaymentWebhookHTTP,
		providePaymentConfigCore,
		providePaymentRuntime,
		paymentProviders,
	)

	// paymentProviders supplies persisted instances, encryption, and registry
	// construction, then binds the default payment load balancer.
	paymentProviders = wire.NewSet(
		paymentpostgres.NewInstanceStore,
		providePaymentEncryptionKey,
		providePaymentRegistry,
		providePaymentLoadBalancer,
		wire.Bind(new(payment.LoadBalancer), new(*payment.DefaultLoadBalancer)),
	)
)
