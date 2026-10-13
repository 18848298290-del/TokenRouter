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

// SQLite fixtures exercise key persistence and usage lookups through the Ent
// schema after issuing the foreign-key PRAGMA. The fixed shared-memory database
// name supports these sequential cases; overlapping helper lifetimes can refer
// to the same database.

// newAPIKeyRepoSQLite opens the named shared-memory SQLite database and issues
// the foreign-key PRAGMA before creating the Ent schema. Test cleanup closes
// both the Ent client and SQL pool; callers keep their lifetimes within the
// test.
func newAPIKeyRepoSQLite(t *testing.T) (*postgres.KeyStore, *dbent.Client) {
	t.Helper()

	db, err := sql.Open("sqlite", "file:api_key_repo_last_used?mode=memory&cache=shared")
	require.NoError(t, err)
	// The shared-memory database survives while connections remain open. Register
	// pool cleanup before Ent cleanup so the later client callback runs first.
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	// Ent closes its shared SQL driver first during cleanup. The pool callback
	// then tolerates repeated closure by discarding its return value.
	t.Cleanup(func() { _ = client.Close() })

	return newKeyStoreFixture(client, db), client
}

// mustCreateAPIKeyRepoUser inserts an active ordinary user and returns its
// identity view after successful persistence. Errors fail the test, so callers
// receive a usable owner for key fixtures.
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

// newLimitedAPIKey returns an unpersisted key whose name matches its credential
// and whose status is supplied by the caller.
func newLimitedAPIKey(userID int64, key, status string) *apikey.APIKey {
	return &apikey.APIKey{
		UserID: userID,
		Key:    key,
		Name:   key,
		Status: status,
	}
}

// newKeyStoreFixture retains client and exec for key operations and batched
// usage-total reads. The caller owns both dependencies and supplies matching
// transaction scope when reads must observe uncommitted fixture writes.
func newKeyStoreFixture(client *dbent.Client, exec postgresinfra.Executor) *postgres.KeyStore {
	return postgres.NewKeyStoreWithSQL(client, exec, func(ctx context.Context, ids []int64) (map[int64]float64, error) {
		return usagepostgres.ReadAPIKeyUsageTotals(ctx, exec, nil, ids)
	})
}
