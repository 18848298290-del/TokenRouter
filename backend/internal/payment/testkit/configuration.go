package testkit

import (
	"log/slog"
	"os"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	paymentpostgres "github.com/TokenFlux/TokenRouter/internal/payment/postgres"
	paymentadapter "github.com/TokenFlux/TokenRouter/internal/payment/provider"
)

// Configuration builds payment configuration with process environment lookup and provider validation.
func Configuration(client *dbent.Client, settings payment.ConfigurationSettings, key []byte) *payment.ConfigService {
	var store payment.ConfigurationStore
	if client != nil {
		store = paymentpostgres.NewInstanceStore(client)
	}
	plans := billing.NewPlans(billingpostgres.NewPlanStore(client), paymentpostgres.NewInstanceStore(client))
	return payment.NewConfigService(store, settings, key, plans, payment.ConfigurationRuntime{CreateProvider: paymentadapter.CreateProvider, LookupEnv: os.LookupEnv, Warn: slog.Warn})
}
