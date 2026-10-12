//go:build integration

package app

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	routingpostgres "github.com/TokenFlux/TokenRouter/internal/routing/postgres"
	schedulerpostgres "github.com/TokenFlux/TokenRouter/internal/scheduler/postgres"
	schedulerredis "github.com/TokenFlux/TokenRouter/internal/scheduler/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/rediscache/codec"
	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// Snapshot replay uses production assembly and the provider outbox publisher.
var (
	NewSnapshotForTest       = provideSchedulerSnapshot
	NewProviderEventsForTest = newProviderEvents
)

// TestSchedulerSnapshotOutboxReplay checks that outbox replay updates the cached last-used timestamp.
func TestSchedulerSnapshotOutboxReplay(t *testing.T) {
	f := newDatabaseFixture(t)
	ctx := t.Context()
	container, err := rediscontainer.Run(ctx, "redis:8.4-alpine")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	address, err := container.Endpoint(ctx, "")
	require.NoError(t, err)
	rdb := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	_, err = f.db.ExecContext(ctx, "TRUNCATE scheduler_outbox")
	require.NoError(t, err)
	store := providerpostgres.NewProviderStore(f.client, f.db, providerpostgres.ProviderStoreOptions{})
	cache := schedulerredis.NewSnapshotCache(rdb, codec.ProviderCodec{})
	store.SetEvents(NewProviderEventsForTest(store, nil))
	outbox := schedulerpostgres.NewSchedulerOutboxRepository(f.db)
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.OutboxPollIntervalSeconds = 1
	cfg.Gateway.Scheduling.DbFallbackEnabled = true
	value := &provider.Record{Name: "outbox-replay-" + time.Now().Format("150405.000000"), Platform: provider.PlatformOpenAI, Type: provider.ProviderTypeAPIKey, Status: provider.StatusActive, Schedulable: true, Concurrency: 3, Priority: 1, Credentials: map[string]any{}, Extra: map[string]any{}}
	require.NoError(t, store.Create(ctx, value))
	require.NoError(t, cache.SetProvider(ctx, codec.WrapRecord(value)))
	groups := routingpostgres.NewGroupStore(f.client, f.db, routingpostgres.GroupStoreOptions{})
	runtime := NewSnapshotForTest(cache, outbox, store, groups, cfg)
	runtime.Start()
	t.Cleanup(runtime.Stop)
	require.NoError(t, store.UpdateLastUsed(ctx, value.ID))
	updated, err := store.GetByID(ctx, value.ID)
	require.NoError(t, err)
	require.NotNil(t, updated.LastUsedAt)
	expected := updated.LastUsedAt.Unix()
	require.Eventually(t, func() bool {
		cached, err := cache.GetProvider(ctx, value.ID)
		if err != nil || cached == nil {
			return false
		}
		record, err := codec.RecordValue(cached)
		return err == nil && record != nil && record.LastUsedAt != nil && record.LastUsedAt.Unix() == expected
	}, 5*time.Second, 100*time.Millisecond)
}
