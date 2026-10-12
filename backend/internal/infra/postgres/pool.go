package postgres

import (
	"database/sql"
	"log/slog"
	"time"
)

// Database connection counts and age limits enter through startup
// configuration before sql.DB creates its pool. Connection ages outside one
// minute through twenty-four hours select bounded defaults with a warning;
// connection counts are passed through to database/sql's own pool rules.

// Connection age defaults limit stale sessions when startup values are outside
// the accepted minute range; each invalid age is replaced independently.
const (
	defaultConnMaxLifetime = 30 * time.Minute
	defaultConnMaxIdleTime = 5 * time.Minute
	maxConfiguredConnAge   = 24 * time.Hour
)

// PoolSettings holds connection counts and resolved age durations for sql.DB.
type PoolSettings struct {
	MaxOpenConns int // SQL open connection limit
	MaxIdleConns int // SQL idle connection limit
	// ConnMaxLifetime and ConnMaxIdleTime are resolved positive age durations,
	// each replacing an invalid startup minute value with its own default.
	ConnMaxLifetime time.Duration // total connection age
	ConnMaxIdleTime time.Duration // unused connection age
}

// PoolOptions contains database pool configuration read at startup.
type PoolOptions struct {
	MaxOpenConns int // requested open connection limit
	MaxIdleConns int // requested idle connection limit
	// ConnMaxLifetimeMinutes and ConnMaxIdleTimeMinutes are startup minute
	// inputs; ResolvePoolSettings replaces each outside one through 1440 with
	// the corresponding default duration.
	ConnMaxLifetimeMinutes int // total age in minutes
	ConnMaxIdleTimeMinutes int // idle age in minutes
}

// ResolvePoolSettings converts startup age values from minutes to durations
// while retaining configured connection counts. Invalid or nonpositive age
// values use their respective defaults and emit warnings; counts remain
// subject to database/sql rules when applied.
func ResolvePoolSettings(cfg PoolOptions) PoolSettings {
	return PoolSettings{
		MaxOpenConns:    cfg.MaxOpenConns,
		MaxIdleConns:    cfg.MaxIdleConns,
		ConnMaxLifetime: clampDBPoolDuration("database.conn_max_lifetime_minutes", cfg.ConnMaxLifetimeMinutes, defaultConnMaxLifetime),
		ConnMaxIdleTime: clampDBPoolDuration("database.conn_max_idle_time_minutes", cfg.ConnMaxIdleTimeMinutes, defaultConnMaxIdleTime),
	}
}

// clampDBPoolDuration converts a valid one-to-1440-minute value into a
// duration. Other values return fallback and log key with the supplied and
// selected minute counts for startup diagnostics.
func clampDBPoolDuration(key string, minutes int, fallback time.Duration) time.Duration {
	// Startup age settings outside the supported range select a known default
	// before minute multiplication, bounding retained sessions and the converted duration.
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

// ApplyPoolSettings resolves cfg and applies its connection counts and age
// durations to db. Pass a non-nil handle; the logged effective values
// describe the resolved inputs passed to database/sql before that library
// adjusts pool counts.
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
