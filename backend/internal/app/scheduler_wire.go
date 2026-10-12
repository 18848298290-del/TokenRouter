//go:build wireinject

package app

import (
	"github.com/google/wire"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	schedulerpostgres "github.com/TokenFlux/TokenRouter/internal/scheduler/postgres"
	schedulerredis "github.com/TokenFlux/TokenRouter/internal/scheduler/rediscache"
)

// Provider scheduling reads snapshots through a SnapshotCache interface bound
// to the Redis implementation. These providers connect SnapshotService's
// cache to persistent outbox access and supply the concurrency, session, and
// rate-limit adapters used by request admission.

// schedulerProviders supplies snapshot storage, selection dependencies, and
// admission services, including the Redis SnapshotCache interface binding.
var schedulerProviders = wire.NewSet(provideSelectionSnapshots, provideSelectionReads, provideSelectionShared, provideSelectionFreeQuota, provideSelectionModelTransient, provideSelectionProxyCircuit, provideGenericSelection, provideCompatibleSelection, provideGeminiSelection, provideSchedulerSharedState, provideUpstreamHealth, provideSchedulerDiagnosticsHTTP, provideSchedulerCache,
	wire.Bind(new(scheduler.SnapshotCache), new(*schedulerredis.SnapshotCache)),
	provideSchedulerSnapshot,
	provideConcurrencyCache, provideConcurrency, provideSessionCache, provideMessageQueue,
	schedulerredis.NewRPMCache, schedulerredis.NewUserRPMCache, schedulerredis.NewUserMsgQueueCache,
	schedulerpostgres.NewSchedulerOutboxRepository)
