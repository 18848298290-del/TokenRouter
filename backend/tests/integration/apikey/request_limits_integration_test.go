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

// TestRequestLimitsPersistence checks migration reapplication, partial updates, and authentication reads.
func TestRequestLimitsPersistence(t *testing.T) {
	db, client := keyDatabase(t)
	ctx := context.Background()
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
	// An RPM-only edit must not overwrite the stored concurrency limit with stale data.
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
	// Each independent limit update must enqueue authentication cache invalidation.
	var invalidations int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM auth_cache_invalidation_outbox").Scan(&invalidations))
	require.Equal(t, 2, invalidations)
	// The database constraint rejects negative limits even when HTTP validation is bypassed.
	_, err = db.ExecContext(ctx, "UPDATE api_keys SET rpm_limit=-1 WHERE id=$1", key.ID)
	require.Error(t, err)
	var count int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations WHERE filename=$1", "288_api_key_request_limits.sql").Scan(&count))
	require.Equal(t, 1, count)
}
