package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/config"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// Database initialization fixes the process timezone before constructing the
// shared PostgreSQL pool. Migrations run before persistent system secrets are
// loaded, and complete configuration validation runs after those secrets have
// populated cfg. A ten-minute context bounds migration retries and secret
// initialization while retaining the caller's cancellation signal.
//
// Ent and direct SQL consumers use the same pool, so successful construction
// transfers closing responsibility to the returned Ent client. Failed
// initialization closes the acquired pool and joins any close error with the
// original failure. This prevents a migration or secret failure from leaving
// an application connection pool open during aborted startup.

// InitEnt initializes the timezone and database, then returns an Ent client and
// its shared SQL pool. Migrations complete before secret loading updates cfg,
// and full configuration validation follows that update.
//
// Migration retries and secret initialization share a ten-minute budget derived
// from ctx. The caller closes the Ent client on success, which also closes the
// pool used by direct SQL consumers. Failure closes an acquired pool before
// returning and retains cleanup errors in the returned error chain.
func InitEnt(ctx context.Context, cfg *config.Config) (_ *ent.Client, _ *sql.DB, resultErr error) {
	if err := InitTimezone(cfg.Timezone); err != nil {
		return nil, nil, err
	}

	// Pass the configured timezone to PostgreSQL through the DSN.
	dsn := cfg.Database.DSNWithTimezone(cfg.Timezone)

	db, err := postgresinfra.Open(dsn, cfg.Server.EnableServerTiming)
	if err != nil {
		return nil, nil, err
	}
	// Construction has not transferred ownership while a named error is present.
	// Close the pool and join cleanup failures so the initialization cause remains
	// observable through the returned error chain.
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, db.Close())
		}
	}()
	drv := entsql.OpenDB(dialect.Postgres, db)
	applyDBPoolSettings(drv.DB(), cfg)

	// Bound retries and secret initialization together so repeated migration
	// failures cannot consume a fresh full deadline before secret loading starts.
	migrationCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := postgresinfra.InitializeWithRetry(migrationCtx, func(ctx context.Context) error {
		return postgresinfra.ApplyMigrations(ctx, drv.DB(), migrations.FS)
	}); err != nil {
		return nil, nil, err
	}

	client := ent.NewClient(ent.Driver(drv))

	// Persistent secrets use tables installed by migrations, so resolve them
	// after schema initialization and before validation requires the JWT secret.
	if err := ensureBootstrapSecrets(migrationCtx, client, cfg); err != nil {
		return nil, nil, err
	}

	// Bootstrap loading permits a missing persisted secret. Validate cfg after
	// secret resolution so the complete application receives a usable secret.
	if err := cfg.Validate(); err != nil {
		return nil, nil, fmt.Errorf("validate config after secret bootstrap: %w", err)
	}

	return client, drv.DB(), nil
}

// postgresPoolOptions converts deployment database configuration to pool settings.
func postgresPoolOptions(cfg *config.Config) postgresinfra.PoolOptions {
	return postgresinfra.PoolOptions{
		MaxOpenConns:           cfg.Database.MaxOpenConns,
		MaxIdleConns:           cfg.Database.MaxIdleConns,
		ConnMaxLifetimeMinutes: cfg.Database.ConnMaxLifetimeMinutes,
		ConnMaxIdleTimeMinutes: cfg.Database.ConnMaxIdleTimeMinutes,
	}
}

// applyDBPoolSettings applies deployment connection limits to the shared SQL
// pool. PostgreSQL infrastructure resolves nonpositive or excessive connection
// ages to its default lifetimes before configuring this pool.
func applyDBPoolSettings(db *sql.DB, cfg *config.Config) {
	postgresinfra.ApplyPoolSettings(db, postgresPoolOptions(cfg))
}

// ApplyMigrations runs the embedded migration files through the PostgreSQL
// migration implementation using db and ctx. The caller supplies and owns the
// database pool and receives migration failures directly.
func ApplyMigrations(ctx context.Context, db *sql.DB) error {
	return postgresinfra.ApplyMigrations(ctx, db, migrations.FS)
}
