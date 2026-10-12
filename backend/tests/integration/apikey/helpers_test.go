package apikey_test

import (
	"context"
	"database/sql"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/enttest"
	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/apikey/postgres"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	usagepostgres "github.com/TokenFlux/TokenRouter/internal/usage/postgres"
)

// newAPIKeyRepoSQLite creates an in-memory SQLite API key store with foreign keys enabled.
func newAPIKeyRepoSQLite(t *testing.T) (*postgres.KeyStore, *dbent.Client) {
	t.Helper()

	db, err := sql.Open("sqlite", "file:api_key_repo_last_used?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	return newKeyStoreFixture(client, db), client
}

// mustCreateAPIKeyRepoUser creates an active user for API key storage tests.
func mustCreateAPIKeyRepoUser(t *testing.T, ctx context.Context, client *dbent.Client, email string) *identity.User {
	t.Helper()
	u, err := client.User.Create().
		SetEmail(email).
		SetPasswordHash("test-password-hash").
		SetRole(identity.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	return identitypostgres.UserFromEntity(u)
}

// newLimitedAPIKey constructs an API key fixture for user-limit tests.
func newLimitedAPIKey(userID int64, key, status string) *apikey.APIKey {
	return &apikey.APIKey{
		UserID: userID,
		Key:    key,
		Name:   key,
		Status: status,
	}
}

// newKeyStoreFixture uses the supplied executor for both key storage and batched usage totals.
func newKeyStoreFixture(client *dbent.Client, exec postgresinfra.Executor) *postgres.KeyStore {
	return postgres.NewKeyStoreWithSQL(client, exec, func(ctx context.Context, ids []int64) (map[int64]float64, error) {
		return usagepostgres.ReadAPIKeyUsageTotals(ctx, exec, nil, ids)
	})
}
