package bootstrap

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	ip "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
)

// Setup tests the PostgreSQL maintenance database before accessing a target
// that may not exist. It checks for the target name, creates the database when
// absent, closes the maintenance pool, and opens the target to verify its
// access separately. Creation survives a later target connection failure
// because these steps are not one transaction.
//
// Maintenance work and target verification each have a fresh five-second
// context. Setup migration and administrator creation use their own temporary
// pools and operation budgets. These adapters close acquired pools on return,
// with close errors either logged or discarded as stated by their entry
// points. HTTP and CLI setup validate the host, port, user, database name, and
// SSL mode before probing; automatic setup passes its environment values directly.
// Callers provide a valid database identifier and libpq-compatible field text
// because these adapters interpolate the supplied values.

// SetupRedisConfig carries temporary Redis probe settings from setup input.
type SetupRedisConfig struct {
	Host     string `json:"host" yaml:"host"`         // connection server hostname
	Port     int    `json:"port" yaml:"port"`         // connection server port
	Username string `json:"username" yaml:"username"` // Redis authentication username
	Password string `json:"password" yaml:"password"` // connection authentication secret
	DB       int    `json:"db" yaml:"db"`             // selected Redis database
	// EnableTLS applies TLS 1.2 or newer and verifies the configured Host
	// as the server name when the setup Redis connection is probed.
	EnableTLS bool `json:"enable_tls" yaml:"enable_tls"`
}

// SetupDatabaseConfig carries PostgreSQL credentials and target settings for setup operations.
type SetupDatabaseConfig struct {
	Host     string `json:"host" yaml:"host"`         // connection server hostname
	Port     int    `json:"port" yaml:"port"`         // connection server port
	User     string `json:"user" yaml:"user"`         // PostgreSQL authentication username
	Password string `json:"password" yaml:"password"` // connection authentication secret
	DBName   string `json:"dbname" yaml:"dbname"`     // target database identifier
	SSLMode  string `json:"sslmode" yaml:"sslmode"`   // libpq TLS mode
}

// BuildPostgresDSN interpolates setup connection fields into a libpq keyword
// DSN using the supplied dbName. Values are inserted as supplied, so callers
// provide text valid for that format; this helper performs no quoting or
// validation.
func BuildPostgresDSN(cfg *SetupDatabaseConfig, dbName string) string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, dbName, cfg.SSLMode,
	)
}

// BuildDatabaseConnectionDSNs returns maintenance and target DSNs using the
// same credentials. The first names postgres so setup can check or create
// cfg.DBName; the second names that configured target for its independent
// connectivity probe.
func BuildDatabaseConnectionDSNs(cfg *SetupDatabaseConfig) (bootstrapDSN, targetDSN string) {
	return BuildPostgresDSN(cfg, "postgres"), BuildPostgresDSN(cfg, cfg.DBName)
}

// TestSetupDatabaseConnection probes maintenance access, creates cfg.DBName
// when absent, then probes target access. Callers validate the target
// identifier before CREATE DATABASE interpolation; this function performs no
// identifier validation.
//
// Maintenance ping, existence lookup, and creation share one five-second
// context. The target pool receives a separate five-second probe after the
// maintenance pool is closed. A database successfully created before a later
// failure remains present. Pool-close errors are logged and do not replace the
// connectivity or creation result returned to setup.
func TestSetupDatabaseConnection(cfg *SetupDatabaseConfig) error {
	// The target can be absent during first setup. Probe the maintenance
	// database before its existence query and creation, using separate target access afterward.
	defaultDSN, targetDSN := BuildDatabaseConnectionDSNs(cfg)

	db, err := postgresinfra.Open(defaultDSN, false)
	if err != nil {
		return fmt.Errorf("failed to connect to PostgreSQL: %w", err)
	}

	// Retain maintenance-pool ownership until return or the explicit close clears
	// db. Deferred close failures are logged while the probe result remains authoritative.
	defer func() {
		if db == nil {
			return
		}
		if err := db.Close(); err != nil {
			logger.LegacyPrintf("setup", "failed to close postgres connection: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping failed: %w", err)
	}

	var exists bool
	row := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", cfg.DBName)
	if err := row.Scan(&exists); err != nil {
		return fmt.Errorf("failed to check database existence: %w", err)
	}

	if !exists {
		// CREATE interpolates the target identifier, so callers validate DBName
		// before probing. HTTP and CLI setup do so; automatic setup supplies raw environment input.
		_, err := db.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE %s", cfg.DBName))
		if err != nil {
			return fmt.Errorf("failed to create database '%s': %w", cfg.DBName, err)
		}
		logger.LegacyPrintf("setup", "Database '%s' created successfully", cfg.DBName)
	}

	// Release maintenance access before acquiring the target pool, then verify
	// target connectivity independently. Close failures are logged while target probing proceeds.
	if err := db.Close(); err != nil {
		logger.LegacyPrintf("setup", "failed to close postgres connection: %v", err)
	}
	db = nil

	targetDB, err := postgresinfra.Open(targetDSN, false)
	if err != nil {
		return fmt.Errorf("failed to connect to database '%s': %w", cfg.DBName, err)
	}

	// The target pool belongs to this independent connectivity probe. Close it
	// on every return, logging failures while retaining the probe's result.
	defer func() {
		if err := targetDB.Close(); err != nil {
			logger.LegacyPrintf("setup", "failed to close postgres connection: %v", err)
		}
	}()

	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	if err := targetDB.PingContext(ctx2); err != nil {
		return fmt.Errorf("ping target database failed: %w", err)
	}

	return nil
}

// TestSetupRedisConnection pings a temporary Redis client within a five-second
// context. Enabled TLS selects TLS 1.2 or newer and cfg.Host as ServerName;
// the acquired client is closed on return, with close failures logged.
func TestSetupRedisConnection(cfg *SetupRedisConfig) error {
	opts := &redis.Options{
		Addr:     fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Username: cfg.Username,
		Password: cfg.Password,
		DB:       cfg.DB,
	}

	// Match normal bootstrap's TLS floor and server-name selection, letting this
	// setup probe check the same configured TLS connection used after installation.
	if cfg.EnableTLS {
		opts.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: cfg.Host,
		}
	}

	rdb := redis.NewClient(opts)
	// The temporary Redis client stays within this probe. Close it on return
	// and log cleanup failures while preserving the connection-check result.
	defer func() {
		if err := rdb.Close(); err != nil {
			logger.LegacyPrintf("setup", "failed to close redis client: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("ping failed: %w", err)
	}

	return nil
}

// InitializeSetupDatabase applies embedded migrations over a temporary target
// pool with a timeout derived from ctx. It attempts pool closure on return and
// discards close errors; migration or connection errors determine the returned
// result.
func InitializeSetupDatabase(ctx context.Context, cfg *SetupDatabaseConfig, timeout time.Duration) error {
	db, err := postgresinfra.Open(BuildPostgresDSN(cfg, cfg.DBName), false)
	if err != nil {
		return err
	}
	// This migration helper owns its temporary target pool. Attempt closure on
	// every return; close errors are discarded and the migration result is retained.
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return ApplyMigrations(ctx, db)
}

// InitializeSetupAdmin opens a temporary target pool and delegates bootstrap
// administrator creation within five seconds derived from ctx. It returns the
// creation flag and decision reason from identity storage, and discards
// pool-close errors on return.
func InitializeSetupAdmin(ctx context.Context, cfg *SetupDatabaseConfig, input identity.InitialAdminInput, password func() (string, error)) (bool, string, error) {
	db, err := postgresinfra.Open(BuildPostgresDSN(cfg, cfg.DBName), false)
	if err != nil {
		return false, "", err
	}
	// This administrator helper owns its temporary target pool. Attempt closure
	// on every return and retain identity storage's creation result as the outcome.
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return CreateInitialAdmin(ctx, db, input, password)
}

// CreateInitialAdmin delegates to identity storage using the supplied pool,
// which remains caller-owned. The result reports whether an administrator was
// created and the decision reason. When creation proceeds, input.Now must be
// callable; an empty or whitespace-only password also requires a callable
// password generator. Existing-user decisions return before invoking either
// callback.
func CreateInitialAdmin(ctx context.Context, db *sql.DB, input identity.InitialAdminInput, password func() (string, error)) (bool, string, error) {
	return ip.CreateInitialAdmin(ctx, db, input, password)
}
