//go:build wireinject

package app

import (
	"github.com/google/wire"

	batchpostgres "github.com/TokenFlux/TokenRouter/internal/batchimage/postgres"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	billingredis "github.com/TokenFlux/TokenRouter/internal/billing/rediscache"
	creativepostgres "github.com/TokenFlux/TokenRouter/internal/creative/postgres"
	routingpostgres "github.com/TokenFlux/TokenRouter/internal/routing/postgres"
)

// module_storage_wire.go registers task storage constructors and billing
// repository bindings for Wire assembly. Creative runs keep persistent metadata
// and outbox actions in PostgreSQL and temporary payloads in Redis. This
// storage set supplies both kinds of construction, together with batch queues,
// download limiting, and billing repository bindings consumed by their task and
// funding services.

// moduleStorageProviders supplies task and billing storage constructors and
// binds the billing repository interfaces to PostgreSQL implementations.
var moduleStorageProviders = wire.NewSet(
	provideCreativeQueue,
	provideCreativeTransientStore,
	provideBatchQueue,
	provideBatchDownloadLimiter,
	billingpostgres.NewUserSubscriptionRepository,
	wire.Bind(new(billing.UserSubscriptionRepository), new(*billingpostgres.SubscriptionStore)),
	billingpostgres.NewUserGroupRateRepository,
	wire.Bind(new(billing.UserGroupRateRepository), new(*billingpostgres.GroupRateStore)),
	billingpostgres.NewRedeemCodeRepository,
	wire.Bind(new(billing.RedeemCodeRepository), new(*billingpostgres.RedeemStore)),
	billingredis.NewRedeemCache,
	wire.Bind(new(billing.RedeemCache), new(*billingredis.RedeemCache)),
	batchpostgres.NewBatchImageRepository,
	creativepostgres.NewCreativeRunRepository,
	creativepostgres.NewCreativeRunOutboxRepository,
	routingpostgres.NewGroupAvailabilityProbeRepository,
)
