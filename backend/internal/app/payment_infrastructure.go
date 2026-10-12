package app

import (
	"log/slog"
	"os"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	paymenthttp "github.com/TokenFlux/TokenRouter/internal/payment/httpapi"
	paymentpostgres "github.com/TokenFlux/TokenRouter/internal/payment/postgres"
	paymentadapter "github.com/TokenFlux/TokenRouter/internal/payment/provider"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
)

func providePaymentHTTP(runtime *payment.Runtime, cfg *payment.ConfigService, plans *billing.Plans) *paymenthttp.PaymentHandler {
	return paymenthttp.NewPaymentHandler(runtime, cfg, plans)
}

func providePaymentAdminHTTP(runtime *payment.Runtime, cfg *payment.ConfigService, plans *billing.Plans, calendar timezone.Calendar) *paymenthttp.AdminHandler {
	return paymenthttp.NewAdminHandler(runtime, cfg, plans, calendar)
}

func providePaymentWebhookHTTP(runtime *payment.Runtime, registry *payment.Registry) *paymenthttp.PaymentWebhookHandler {
	return paymenthttp.NewPaymentWebhookHandler(runtime, registry)
}

// providePaymentEncryptionKey uses the explicitly configured TOTP encryption key for payment encryption.
// Missing or automatically generated keys yield nil and disable payment features that require encryption.
// Invalid hex or length in an explicitly configured key returns an error to application startup.
func providePaymentEncryptionKey(cfg *config.Config) (payment.EncryptionKey, error) {
	if cfg == nil {
		slog.Warn("payment encryption key not configured — encrypted payment config and resume signing will be unavailable")
		return nil, nil
	}
	key, warning, err := payment.ConfiguredEncryptionKey(cfg.Totp.EncryptionKey, cfg.Totp.EncryptionKeyConfigured)
	if warning != "" {
		slog.Warn(warning)
	}
	return key, err
}

// providePaymentRegistry creates the payment provider registry.
// Providers are registered during operation.
func providePaymentRegistry() *payment.Registry {
	return payment.NewRegistry()
}

// providePaymentLoadBalancer binds the payment instance store and encryption key.
func providePaymentLoadBalancer(store *paymentpostgres.InstanceStore, key payment.EncryptionKey) *payment.DefaultLoadBalancer {
	return payment.NewDefaultLoadBalancer(store, []byte(key), payment.SelectionRuntime{Observe: paymentSelectionLog})
}

func paymentSelectionLog(level, message string, attrs ...any) {
	if level == "warn" {
		slog.Warn(message, attrs...)
	} else {
		slog.Info(message, attrs...)
	}
}

func providePaymentConfigCore(store *paymentpostgres.InstanceStore, settings settingscore.Repository, key payment.EncryptionKey, plans *billing.Plans) *payment.ConfigService {
	return payment.NewConfigService(store, settings, []byte(key), plans, payment.ConfigurationRuntime{CreateProvider: paymentadapter.CreateProvider, LookupEnv: os.LookupEnv, Warn: slog.Warn})
}
