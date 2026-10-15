package app

import (
	"context"
	"database/sql"
	"errors"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/app/bootstrap"
	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/idempotency"
	idempotencypostgres "github.com/TokenFlux/TokenRouter/internal/idempotency/postgres"
	"github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	redisinfra "github.com/TokenFlux/TokenRouter/internal/infra/redis"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	settingspostgres "github.com/TokenFlux/TokenRouter/internal/settings/postgres"
)

// storage.go constructs shared database and Redis resources, registers their
// cleanup, and supplies settings storage. Redis runtime-key migration finishes
// before the client is returned to worker constructors, and calendar capture
// depends on Ent bootstrap so business date calculations use the initialized
// process timezone.

// provideCalendar captures bootstrap's process location in the shared calendar.
// The Ent argument is a Wire ordering dependency, requiring database bootstrap
// before this constructor reads time.Local.
func provideCalendar(_ *ent.Client) timezone.Calendar {
	return timezone.NewCalendar(time.Local)
}

// databaseAdvisoryLease returns nil when no SQL pool is supplied. Its callback
// hashes the requested lock key, and callers handle the acquisition flag and
// invoke the returned release after owned work finishes.
func databaseAdvisoryLease(db *sql.DB) func(context.Context, string) (func(), bool) {
	if db == nil {
		return nil
	}
	return func(ctx context.Context, key string) (func(), bool) {
		return postgres.TryAcquireDBAdvisoryLock(ctx, db, postgres.HashAdvisoryLockID(key))
	}
}

// provideEnt runs Ent bootstrap and registers the acquired client for lifecycle
// closure. Call before manager startup; bootstrap failure returns immediately,
// while the registered client owns the shared SQL pool released at level 910.
func provideEnt(ctx context.Context, cfg *config.Config, manager *lifecycle.Manager) (*ent.Client, error) {
	client, _, err := bootstrap.InitEnt(ctx, cfg)
	if err != nil {
		return nil, err
	}
	manager.Register(lifecycle.Hook{Name: "Ent", StartOrder: -100, StopOrder: 910, Stop: func(context.Context) error { return client.Close() }})
	return client, nil
}

// provideRedis constructs Redis and passes a two-minute child context to
// runtime-key migration. Socket operations retain the client timeout policy
// established by bootstrap.InitRedis, so this context is not a strict I/O
// deadline. Migration failure closes the unregistered client and returns its
// cause; success registers Redis closure before returning the client to
// dependent constructors.
func provideRedis(ctx context.Context, cfg *config.Config, manager *lifecycle.Manager) (*redis.Client, error) {
	client := bootstrap.InitRedis(cfg)
	// Migrate runtime keys before returning Redis to dependent constructors.
	// Workers and traffic then read the migrated names instead of mixing old and current limit namespaces.
	migrationCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	if err := redisinfra.MigrateProviderNames(migrationCtx, client); err != nil {
		// Migration failed before lifecycle ownership was registered. Close this
		// acquired client locally and retain the migration error, discarding any separate close failure.
		_ = client.Close()
		return nil, err
	}
	manager.Register(lifecycle.Hook{Name: "Redis", StartOrder: -90, StopOrder: 900, Stop: func(context.Context) error { return client.Close() }})
	return client, nil
}

// provideSettingsStore constructs shared settings storage, version,
// notifications, and update coordination.
// @project-doc docs/interfaces/configuration.md#runtime_settings
func provideSettingsStore(client *ent.Client) *settings.Store {
	return settings.New(settingspostgres.NewSettingRepository(client))
}

// provideIdempotencyRepository binds command idempotency persistence to the
// application SQL pool.
func provideIdempotencyRepository(db *sql.DB) idempotency.IdempotencyRepository {
	return idempotencypostgres.NewIdempotencyRepository(db)
}

// provideSQLDB returns the pool exposed by the Ent SQL driver. Nil client or
// another driver type returns an error; callers borrow this pool while Ent
// retains its lifecycle ownership and closure.
func provideSQLDB(client *ent.Client) (*sql.DB, error) {
	if client == nil {
		return nil, errors.New("nil ent client")
	}
	driver, ok := client.Driver().(*entsql.Driver)
	if !ok {
		return nil, errors.New("ent driver does not expose *sql.DB")
	}
	return driver.DB(), nil
}
