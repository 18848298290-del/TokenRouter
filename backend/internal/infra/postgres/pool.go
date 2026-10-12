package postgres

import (
	"database/sql"
	"log/slog"
	"time"
)

const (
	defaultConnMaxLifetime = 30 * time.Minute
	defaultConnMaxIdleTime = 5 * time.Minute
	maxConfiguredConnAge   = 24 * time.Hour
)

type PoolSettings struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// PoolOptions contains database pool configuration read at startup.
type PoolOptions struct {
	MaxOpenConns           int
	MaxIdleConns           int
	ConnMaxLifetimeMinutes int
	ConnMaxIdleTimeMinutes int
}

func ResolvePoolSettings(cfg PoolOptions) PoolSettings {
	return PoolSettings{
		MaxOpenConns:    cfg.MaxOpenConns,
		MaxIdleConns:    cfg.MaxIdleConns,
		ConnMaxLifetime: clampDBPoolDuration("database.conn_max_lifetime_minutes", cfg.ConnMaxLifetimeMinutes, defaultConnMaxLifetime),
		ConnMaxIdleTime: clampDBPoolDuration("database.conn_max_idle_time_minutes", cfg.ConnMaxIdleTimeMinutes, defaultConnMaxIdleTime),
	}
}

func clampDBPoolDuration(key string, minutes int, fallback time.Duration) time.Duration {
	if minutes <= 0 || minutes > int(maxConfiguredConnAge/time.Minute) {
		slog.Warn("database connection pool duration clamped",
			"key", key,
			"before", minutes,
			"after", int(fallback/time.Minute),
		)
		return fallback
	}

	return time.Duration(minutes) * time.Minute
}

func ApplyPoolSettings(db *sql.DB, cfg PoolOptions) {
	settings := ResolvePoolSettings(cfg)
	db.SetMaxOpenConns(settings.MaxOpenConns)
	db.SetMaxIdleConns(settings.MaxIdleConns)
	db.SetConnMaxLifetime(settings.ConnMaxLifetime)
	db.SetConnMaxIdleTime(settings.ConnMaxIdleTime)

	slog.Info("database connection pool configured",
		slog.Group("effective",
			slog.Int("max_open", settings.MaxOpenConns),
			slog.Int("max_idle", settings.MaxIdleConns),
			slog.Duration("max_lifetime", settings.ConnMaxLifetime),
			slog.Duration("max_idle_time", settings.ConnMaxIdleTime),
		),
	)
}
