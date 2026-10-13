//go:build integration

package apikey_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keypostgres "github.com/TokenFlux/TokenRouter/internal/apikey/postgres"
	"github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// Concurrency and RPM limits are independent persisted key fields that
// authentication needs to load. The migrated PostgreSQL fixture checks partial
// updates, zero clearing, outbox records, negative-value rejection, and direct
// reapplication of the request-limit migration.

// TestRequestLimitsPersistence reapplies the request-limit migration and edits
// each limit independently in PostgreSQL. Authentication reloads reflect the
// selected field, both updates enqueue invalidation, and negative RPM fails
// storage validation.
func TestRequestLimitsPersistence(t *testing.T) {
	db, client := keyDatabase(t)
	ctx := context.Background()
	// keyDatabase already applied migrations. Repeat the runner and then the SQL
	// migration body to check reapplication against an existing request-limit
	// schema.
	require.NoError(t, postgres.ApplyMigrations(ctx, db, migrations.FS))
	migration, err := migrations.FS.ReadFile("288_api_key_request_limits.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	user, err := client.User.Create().SetEmail("request-limits@example.com").SetPasswordHash("test").Save(ctx)
	require.NoError(t, err)
	repo := keypostgres.NewKeyStore(client, db, nil)
	key := &apikey.APIKey{UserID: user.ID, Key: "sk-request-limits", Name: "limits", Status: "active", ConcurrencyLimit: 2, RPMLimit: 10}
	require.NoError(t, repo.Create(ctx, key))
	loaded, err := repo.GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	require.Equal(t, 2, loaded.ConcurrencyLimit)
	require.Equal(t, 10, loaded.RPMLimit)
	// Clear prior outbox records before the two independent edits so the final
	// count measures their invalidations, then supply a deliberately stale
	// concurrency value.
	_, err = db.ExecContext(ctx, "DELETE FROM auth_cache_invalidation_outbox")
	require.NoError(t, err)
	key.ConcurrencyLimit = 99
	key.RPMLimit = 0
	require.NoError(t, repo.Update(ctx, key, apikey.APIKeyUpdateFields{RPMLimit: true}))
	loaded, err = repo.GetByID(ctx, key.ID)
	require.NoError(t, err)
	require.Equal(t, 2, loaded.ConcurrencyLimit)
	require.Zero(t, loaded.RPMLimit)
	key.ConcurrencyLimit = 0
	require.NoError(t, repo.Update(ctx, key, apikey.APIKeyUpdateFields{ConcurrencyLimit: true}))
	loaded, err = repo.GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	require.Zero(t, loaded.ConcurrencyLimit)
	// Count the invalidations after both updates; a cleared limit still changes
	// authentication data and therefore needs a persisted event for other
	// instances.
	var invalidations int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM auth_cache_invalidation_outbox").Scan(&invalidations))
	require.Equal(t, 2, invalidations)
	// The database constraint rejects negative limits even when HTTP validation is
	// bypassed.
	_, err = db.ExecContext(ctx, "UPDATE api_keys SET rpm_limit=-1 WHERE id=$1", key.ID)
	require.Error(t, err)
	var count int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations WHERE filename=$1", "288_api_key_request_limits.sql").Scan(&count))
	require.Equal(t, 1, count)
}
