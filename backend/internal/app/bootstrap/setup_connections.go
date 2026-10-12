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

type SetupRedisConfig struct {
	Host      string `json:"host" yaml:"host"`
	Port      int    `json:"port" yaml:"port"`
	Username  string `json:"username" yaml:"username"`
	Password  string `json:"password" yaml:"password"`
	DB        int    `json:"db" yaml:"db"`
	EnableTLS bool   `json:"enable_tls" yaml:"enable_tls"`
}
type SetupDatabaseConfig struct {
	Host     string `json:"host" yaml:"host"`
	Port     int    `json:"port" yaml:"port"`
	User     string `json:"user" yaml:"user"`
	Password string `json:"password" yaml:"password"`
	DBName   string `json:"dbname" yaml:"dbname"`
	SSLMode  string `json:"sslmode" yaml:"sslmode"`
}

func BuildPostgresDSN(cfg *SetupDatabaseConfig, dbName string) string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, dbName, cfg.SSLMode,
	)
}

func BuildDatabaseConnectionDSNs(cfg *SetupDatabaseConfig) (bootstrapDSN, targetDSN string) {
	return BuildPostgresDSN(cfg, "postgres"), BuildPostgresDSN(cfg, cfg.DBName)
}

// TestSetupDatabaseConnection checks connectivity and creates the target database when absent.
func TestSetupDatabaseConnection(cfg *SetupDatabaseConfig) error {
	// Connect to the maintenance database first because the target may not exist yet.
	defaultDSN, targetDSN := BuildDatabaseConnectionDSNs(cfg)

	db, err := postgresinfra.Open(defaultDSN, false)
	if err != nil {
		return fmt.Errorf("failed to connect to PostgreSQL: %w", err)
	}

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
		// Callers validate the database identifier before interpolation; SQL parameters bind values.
		_, err := db.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE %s", cfg.DBName))
		if err != nil {
			return fmt.Errorf("failed to create database '%s': %w", cfg.DBName, err)
		}
		logger.LegacyPrintf("setup", "Database '%s' created successfully", cfg.DBName)
	}

	// Reconnect to the target database to verify access.
	if err := db.Close(); err != nil {
		logger.LegacyPrintf("setup", "failed to close postgres connection: %v", err)
	}
	db = nil

	targetDB, err := postgresinfra.Open(targetDSN, false)
	if err != nil {
		return fmt.Errorf("failed to connect to database '%s': %w", cfg.DBName, err)
	}

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

// TestSetupRedisConnection checks Redis connectivity.
func TestSetupRedisConnection(cfg *SetupRedisConfig) error {
	opts := &redis.Options{
		Addr:     fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Username: cfg.Username,
		Password: cfg.Password,
		DB:       cfg.DB,
	}

	if cfg.EnableTLS {
		opts.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: cfg.Host,
		}
	}

	rdb := redis.NewClient(opts)
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

// InitializeSetupDatabase applies migrations and closes its connection before returning.
func InitializeSetupDatabase(ctx context.Context, cfg *SetupDatabaseConfig, timeout time.Duration) error {
	db, err := postgresinfra.Open(BuildPostgresDSN(cfg, cfg.DBName), false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return ApplyMigrations(ctx, db)
}

// InitializeSetupAdmin uses a separate connection with a five-second operation budget.
func InitializeSetupAdmin(ctx context.Context, cfg *SetupDatabaseConfig, input identity.InitialAdminInput, password func() (string, error)) (bool, string, error) {
	db, err := postgresinfra.Open(BuildPostgresDSN(cfg, cfg.DBName), false)
	if err != nil {
		return false, "", err
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return CreateInitialAdmin(ctx, db, input, password)
}

// CreateInitialAdmin delegates administrator creation to identity storage.
func CreateInitialAdmin(ctx context.Context, db *sql.DB, input identity.InitialAdminInput, password func() (string, error)) (bool, string, error) {
	return ip.CreateInitialAdmin(ctx, db, input, password)
}
