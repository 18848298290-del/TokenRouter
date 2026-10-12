package testkit

import (
	"context"
	"log/slog"
	"os"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	paymentpostgres "github.com/TokenFlux/TokenRouter/internal/payment/postgres"
	paymentadapter "github.com/TokenFlux/TokenRouter/internal/payment/provider"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
)

// Fulfillment builds payment fulfillment with database storage, entitlements, and audit logging.
func Fulfillment(client *dbent.Client, redeem payment.FulfillmentRedeemer, subscriptions payment.FulfillmentSubscriptions, rebates *promotion.AffiliateService) *payment.Fulfillment {
	return fulfillment(client, redeem, subscriptions, rebates, nil, false)
}

// Lifecycle builds order lifecycle handling with fulfillment and payment resume services.
func Lifecycle(client *dbent.Client, registry *payment.Registry, redeem payment.FulfillmentRedeemer, subscriptions payment.FulfillmentSubscriptions, resume *payment.PaymentResumeService, loaded bool) *payment.OrderLifecycle {
	if resume == nil {
		resume = Resume(nil)
	}
	return payment.NewOrderLifecycle(fulfillment(client, redeem, subscriptions, nil, registry, loaded), resume, nil)
}

func fulfillment(client *dbent.Client, redeem payment.FulfillmentRedeemer, subscriptions payment.FulfillmentSubscriptions, rebates *promotion.AffiliateService, registry *payment.Registry, loaded bool) *payment.Fulfillment {
	auditStore := paymentpostgres.NewRefundStore(client, nil)
	audit := func(ctx context.Context, id int64, action, operator string, detail map[string]any) {
		if err := auditStore.AppendObservation(ctx, id, action, operator, detail); err != nil {
			slog.Error("audit log failed", "orderID", id, "action", action, "error", err)
		}
	}
	runtime := payment.FulfillmentRuntime{Audit: audit}
	if rebates != nil {
		runtime.RebateEnabled = rebates.IsEnabled
	}
	store := paymentpostgres.NewOrderStore(client, paymentpostgres.OrderStoreRuntime{
		Audit: audit,
		Rebates: func(*dbent.Tx) paymentpostgres.OrderRebates {
			return rebates
		},
	})
	bindings := payment.NewProviderBindings(paymentpostgres.NewInstanceStore(client), registry, nil, payment.BindingRuntime{Factory: paymentadapter.CreateProvider}, loaded)
	return payment.NewFulfillment(store, bindings, registry, redeem, subscriptions, runtime)
}

// Resume selects the signing key from the environment and accepts tokens signed with the supplied legacy key.
func Resume(legacyKey []byte) *payment.PaymentResumeService {
	key, fallbacks := payment.ResolvePaymentResumeSigningKeys(os.Getenv("PAYMENT_RESUME_SIGNING_KEY"), legacyKey)
	return payment.NewPaymentResumeService(key, fallbacks...)
}
