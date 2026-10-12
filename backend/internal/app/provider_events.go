package app

import (
	"context"
	"database/sql"
	"log"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	egresspostgres "github.com/TokenFlux/TokenRouter/internal/egress/postgres"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/infra/timingwheel"
	acctcore "github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
	routingpostgres "github.com/TokenFlux/TokenRouter/internal/routing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	schedulerpostgres "github.com/TokenFlux/TokenRouter/internal/scheduler/postgres"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/rediscache/codec"
)

type providerRecordsReader interface {
	GetByID(context.Context, int64) (*acctcore.Record, error)
	GetByIDs(context.Context, []int64) ([]*acctcore.Record, error)
}

type providerSchedulerEvents struct{ publisher scheduler.SnapshotPublisher }

// provideProviderDeferred binds the provider store and timing wheel for deferred writes.
func provideProviderDeferred(store *providerpostgres.ProviderStore, wheel *timingwheel.Wheel) *acctcore.DeferredService {
	return acctcore.NewDeferredService(store, wheel, acctcore.DeferredOptions{Interval: 10 * time.Second, Now: time.Now, Observe: log.Printf})
}

// newProviderEvents connects provider writes to the scheduler outbox and snapshot publisher.
func newProviderEvents(reader providerRecordsReader, cache scheduler.SnapshotCache) providerpostgres.ProviderEvents {
	return providerSchedulerEvents{publisher: scheduler.SnapshotPublisher{
		Cache: cache, Diagnostics: scheduler.Diagnostics{
			Logf: logging.LegacyPrintf, Event: logging.Event,
		},

		Read: func(ctx context.Context, id int64) (scheduler.SnapshotProvider, error) {
			value, err := reader.GetByID(ctx, id)
			return codec.WrapRecord(value), err
		},
		ReadMany: func(ctx context.Context, ids []int64) ([]scheduler.SnapshotProvider, error) {
			value, err := reader.GetByIDs(ctx, ids)
			return wrapSchedulerRecordPointers(value), err
		},
	}}
}

func (b providerSchedulerEvents) Write(ctx context.Context, exec postgresinfra.Executor, event providerpostgres.ProviderEvent, id, group *int64, payload any) error {
	return schedulerpostgres.EnqueueSchedulerChange(ctx, exec, providerSchedulerEventName(event), id, group, payload)
}

func (providerSchedulerEvents) GroupPayload(ids []int64) any { return scheduler.GroupPayload(ids) }

func (b providerSchedulerEvents) SyncOne(ctx context.Context, id int64) { b.publisher.Publish(ctx, id) }

func (b providerSchedulerEvents) SyncMany(ctx context.Context, ids []int64) {
	b.publisher.PublishMany(ctx, ids)
}

func (b providerSchedulerEvents) Drop(ctx context.Context, id int64) { b.publisher.Drop(ctx, id) }

func (providerSchedulerEvents) Name(event providerpostgres.ProviderEvent) string {
	return providerSchedulerEventName(event)
}

func providerSchedulerEventName(event providerpostgres.ProviderEvent) string {
	switch event {
	case providerpostgres.ProviderChanged:
		return scheduler.SchedulerOutboxEventProviderChanged
	case providerpostgres.ProviderGroupsChanged:
		return scheduler.SchedulerOutboxEventProviderGroupsChanged
	case providerpostgres.ProviderLastUsed:
		return scheduler.SchedulerOutboxEventProviderLastUsed
	case providerpostgres.ProviderBulkChanged:
		return scheduler.SchedulerOutboxEventProviderBulkChanged
	default:
		panic("未知提供商事件")
	}
}

// providerUsageEvents binds change events and snapshot publication for accumulated provider usage.
func providerUsageEvents(reader providerRecordsReader, cache scheduler.SnapshotCache, exec postgresinfra.Executor) billingpostgres.ProviderUsageOptions {
	events := newProviderEvents(reader, cache)
	return billingpostgres.ProviderUsageOptions{
		Changed: func(ctx context.Context, id int64) error {
			return events.Write(ctx, exec, providerpostgres.ProviderChanged, &id, nil, nil)
		},
		Sync: events.SyncOne,
		Observe: func(format string, args ...any) {
			logging.LegacyPrintf("repository.provider", format, args...)
		},
	}
}

func wrapSchedulerRecordPointers(values []*acctcore.Record) []scheduler.SnapshotProvider {
	if values == nil {
		return nil
	}
	out := make([]scheduler.SnapshotProvider, len(values))
	for i, value := range values {
		out[i] = codec.WrapRecord(value)
	}
	return out
}

// provideProviderStore binds record conversion and change events to the shared provider store.
func provideProviderStore(client *dbent.Client, db *sql.DB, cache scheduler.SnapshotCache) *providerpostgres.ProviderStore {
	store := providerpostgres.NewProviderStore(client, db, providerpostgres.ProviderStoreOptions{
		Group: func(g *dbent.Group) *accessview.GroupConfig {
			return (*accessview.GroupConfig)(routingpostgres.GroupFromEnt(g))
		},
		OllamaIdentity: acctcore.IsOllamaCloudUsageProvider,
		Proxy:          egresspostgres.ProxyEntity, Now: time.Now, LoadLocation: time.LoadLocation,
		Observe: func(format string, args ...any) { logging.LegacyPrintf("repository.provider", format, args...) },
	})
	store.SetEvents(newProviderEvents(store, cache))
	return store
}

func provideExecutionProviderStore(store *providerpostgres.ProviderStore, usage *billingpostgres.ProviderUsageStore) gatewayprovider.ExecutionProviderStore {
	return &executionProviderStore{data: store, usage: usage}
}
