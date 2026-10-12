//go:build wireinject

package app

import (
	"github.com/google/wire"

	"github.com/TokenFlux/TokenRouter/internal/payment"
	paymentpostgres "github.com/TokenFlux/TokenRouter/internal/payment/postgres"
)

var (
	// paymentAssemblyProviders groups Wire providers for the payment module.
	paymentAssemblyProviders = wire.NewSet(
		providePaymentExpiry,
		providePaymentHTTP,
		providePaymentAdminHTTP,
		providePaymentWebhookHTTP,
		providePaymentConfigCore,
		providePaymentRuntime,
		paymentProviders,
	)

	// paymentProviders groups payment persistence, encryption, and load-balancing dependencies.
	// Constructors live in app files compiled without the wireinject tag.
	paymentProviders = wire.NewSet(
		paymentpostgres.NewInstanceStore,
		providePaymentEncryptionKey,
		providePaymentRegistry,
		providePaymentLoadBalancer,
		wire.Bind(new(payment.LoadBalancer), new(*payment.DefaultLoadBalancer)),
	)
)
