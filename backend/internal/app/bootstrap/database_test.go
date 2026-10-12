package bootstrap

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

// Database pool age settings need normalization before they become SQL
// durations. Fixtures compare zero, negative, accepted, and over-limit minute
// values, then inspect a constructed pool to check that its configured
// open-connection maximum reaches database/sql.

// TestPostgresPoolOptionsResolveSettings passes several minute-based age
// settings through bootstrap options and PostgreSQL normalization. Each case
// must retain connection counts and resolve the expected lifetime and idle-age
// durations.
func TestPostgresPoolOptionsResolveSettings(t *testing.T) {
	tests := []struct {
		name                string        // displayed age-setting case
		connMaxLifetime     int           // configured lifetime minutes
		connMaxIdleTime     int           // configured idle minutes
		wantMaxLifetime     time.Duration // expected connection lifetime
		wantConnMaxIdleTime time.Duration // expected connection idle age
	}{
		{
			name:                "zero values fall back to safe defaults",
			connMaxLifetime:     0,
			connMaxIdleTime:     0,
			wantMaxLifetime:     30 * time.Minute,
			wantConnMaxIdleTime: 5 * time.Minute,
		},
		{
			name:                "negative values fall back to safe defaults",
			connMaxLifetime:     -1,
			connMaxIdleTime:     -5,
			wantMaxLifetime:     30 * time.Minute,
			wantConnMaxIdleTime: 5 * time.Minute,
		},
		{
			name:                "reasonable values pass through",
			connMaxLifetime:     15,
			connMaxIdleTime:     3,
			wantMaxLifetime:     15 * time.Minute,
			wantConnMaxIdleTime: 3 * time.Minute,
		},
		// One minute beyond twenty-four hours triggers the pool normalizer's defaults.
		{
			name:                "values over twenty four hours fall back to safe defaults",
			connMaxLifetime:     24*60 + 1,
			connMaxIdleTime:     24*60 + 1,
			wantMaxLifetime:     30 * time.Minute,
			wantConnMaxIdleTime: 5 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Database: config.DatabaseConfig{
					MaxOpenConns:           50,
					MaxIdleConns:           10,
					ConnMaxLifetimeMinutes: tt.connMaxLifetime,
					ConnMaxIdleTimeMinutes: tt.connMaxIdleTime,
				},
			}

			settings := postgres.ResolvePoolSettings(postgresPoolOptions(cfg))
			require.Equal(t, 50, settings.MaxOpenConns)
			require.Equal(t, 10, settings.MaxIdleConns)
			require.Equal(t, tt.wantMaxLifetime, settings.ConnMaxLifetime)
			require.Equal(t, tt.wantConnMaxIdleTime, settings.ConnMaxIdleTime)
		})
	}
}

// TestApplyDBPoolSettings constructs a SQL pool and applies bootstrap
// connection settings. The pool statistics must expose the configured maximum
// open-connection count after the configuration helper runs.
func TestApplyDBPoolSettings(t *testing.T) {
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			MaxOpenConns:           40,
			MaxIdleConns:           8,
			ConnMaxLifetimeMinutes: 15,
			ConnMaxIdleTimeMinutes: 3,
		},
	}

	db, err := sql.Open("postgres", "host=127.0.0.1 port=5432 user=postgres sslmode=disable")
	require.NoError(t, err)
	// The fixture owns this unopened SQL pool. Attempt closure at cleanup and
	// discard its close result after inspecting the applied pool configuration.
	t.Cleanup(func() {
		_ = db.Close()
	})

	applyDBPoolSettings(db, cfg)
	stats := db.Stats()
	require.Equal(t, 40, stats.MaxOpenConnections)
}
