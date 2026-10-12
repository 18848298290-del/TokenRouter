//go:build wireinject

package app

import (
	"github.com/google/wire"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	billinghttpapi "github.com/TokenFlux/TokenRouter/internal/billing/httpapi"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	billingredis "github.com/TokenFlux/TokenRouter/internal/billing/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// billingAssemblyProviders groups Wire providers for the billing module.
var billingAssemblyProviders = wire.NewSet(
	wire.Bind(new(identity.DefaultSubscriptionAssigner), new(*billing.SubscriptionService)),
	wire.Bind(new(completion.Store), new(*billingpostgres.SettlementStore)),
	provideBalanceNotifications,
	provideProviderUsage,
	provideGroupRateAdmin,
	provideBillingCalculator,
	provideBillingPriceResolver,
	provideSubscriptionExpiry,
	provideBillingPlans,
	wire.Bind(new(billinghttpapi.RedeemAdministrator), new(*billing.RedeemAdmin)),
	provideRedeemAdministration,
	provideBalanceAdjuster,
	provideBillingRedeem,
	provideBillingEligibility,
	provideWindowCostCache,
	provideBillingSubscriptions,
	provideSettlementStore,
	provideBillingFunds,
	billingredis.NewBillingCache,
	wire.Bind(new(billing.BillingCache), new(*billingredis.Cache)),
)
