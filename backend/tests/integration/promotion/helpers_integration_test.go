//go:build integration

package promotion_test

import (
	"database/sql"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
)

// testStore creates an isolated test database and Ent client.
func testStore(t *testing.T) (*dbent.Client, *sql.DB) {
	t.Helper()
	db := databaseSuite.New(t)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return client, db
}

// testEntClient supplies an isolated database for tests that commit transactions.
func testEntClient(t *testing.T) *dbent.Client {
	t.Helper()
	client, _ := testStore(t)
	return client
}
