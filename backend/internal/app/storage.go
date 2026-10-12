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

// provideCalendar captures the timezone initialized by bootstrap; the Ent dependency enforces assembly order.
func provideCalendar(_ *ent.Client) timezone.Calendar {
	return timezone.NewCalendar(time.Local)
}

// databaseAdvisoryLease binds the database pool; callers choose lock identities and handle acquisition failure.
func databaseAdvisoryLease(db *sql.DB) func(context.Context, string) (func(), bool) {
	if db == nil {
		return nil
	}
	return func(ctx context.Context, key string) (func(), bool) {
		return postgres.TryAcquireDBAdvisoryLock(ctx, db, postgres.HashAdvisoryLockID(key))
	}
}

func provideEnt(ctx context.Context, cfg *config.Config, manager *lifecycle.Manager) (*ent.Client, error) {
	client, _, err := bootstrap.InitEnt(ctx, cfg)
	if err != nil {
		return nil, err
	}
	manager.Register(lifecycle.Hook{Name: "Ent", StartOrder: -100, StopOrder: 910, Stop: func(context.Context) error { return client.Close() }})
	return client, nil
}

func provideRedis(ctx context.Context, cfg *config.Config, manager *lifecycle.Manager) (*redis.Client, error) {
	client := bootstrap.InitRedis(cfg)
	// Migrate runtime keys before constructing workers or accepting traffic so limits use the migrated names.
	migrationCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	if err := redisinfra.MigrateProviderNames(migrationCtx, client); err != nil {
		_ = client.Close()
		return nil, err
	}
	manager.Register(lifecycle.Hook{Name: "Redis", StartOrder: -90, StopOrder: 900, Stop: func(context.Context) error { return client.Close() }})
	return client, nil
}

// provideSettingsStore creates the shared version, notifications, and update coordinator.
// @project-doc docs/interfaces/configuration.md#runtime_settings
func provideSettingsStore(client *ent.Client) *settings.Store {
	return settings.New(settingspostgres.NewSettingRepository(client))
}

// provideIdempotencyRepository uses the application SQL pool for idempotency storage.
func provideIdempotencyRepository(db *sql.DB) idempotency.IdempotencyRepository {
	return idempotencypostgres.NewIdempotencyRepository(db)
}

// provideSQLDB returns the pool owned and closed by Ent.
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
