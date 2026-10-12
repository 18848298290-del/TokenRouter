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

// InitEnt sets the process timezone, runs SQL migrations, and returns an Ent client and its shared SQL pool.
//
// Timezone and connection settings come from cfg. Migration retries and secret bootstrap
// share a ten-minute budget.
//
// The caller closes the returned Ent client, which also closes the underlying SQL pool.
//
// The SQL pool supports direct queries. Initialization failures close the acquired pool before returning.
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
	// Close the acquired pool on failure; the returned Ent client owns it on success.
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, db.Close())
		}
	}()
	drv := entsql.OpenDB(dialect.Postgres, db)
	applyDBPoolSettings(drv.DB(), cfg)

	// Migrations, retry delays, and secret bootstrap share this deadline.
	migrationCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := postgresinfra.InitializeWithRetry(migrationCtx, func(ctx context.Context) error {
		return postgresinfra.ApplyMigrations(ctx, drv.DB(), migrations.FS)
	}); err != nil {
		return nil, nil, err
	}

	client := ent.NewClient(ent.Driver(drv))

	// Load persistent system secrets from configuration or the database.
	if err := ensureBootstrapSecrets(migrationCtx, client, cfg); err != nil {
		return nil, nil, err
	}

	// Validate the complete configuration after filling in persistent secrets.
	if err := cfg.Validate(); err != nil {
		return nil, nil, fmt.Errorf("validate config after secret bootstrap: %w", err)
	}

	return client, drv.DB(), nil
}

// postgresPoolOptions converts database configuration to pool settings.
func postgresPoolOptions(cfg *config.Config) postgresinfra.PoolOptions {
	return postgresinfra.PoolOptions{
		MaxOpenConns:           cfg.Database.MaxOpenConns,
		MaxIdleConns:           cfg.Database.MaxIdleConns,
		ConnMaxLifetimeMinutes: cfg.Database.ConnMaxLifetimeMinutes,
		ConnMaxIdleTimeMinutes: cfg.Database.ConnMaxIdleTimeMinutes,
	}
}

// applyDBPoolSettings configures the SQL connection pool.
func applyDBPoolSettings(db *sql.DB, cfg *config.Config) {
	postgresinfra.ApplyPoolSettings(db, postgresPoolOptions(cfg))
}

// ApplyMigrations applies the embedded SQL migrations.
func ApplyMigrations(ctx context.Context, db *sql.DB) error {
	return postgresinfra.ApplyMigrations(ctx, db, migrations.FS)
}
