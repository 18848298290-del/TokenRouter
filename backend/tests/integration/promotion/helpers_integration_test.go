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

// Promotion fixtures need committed PostgreSQL writes and multiple connections
// while each case retains an isolated dataset. testStore borrows a newly cloned
// migrated database from databaseSuite and wraps its pool with Ent, registering
// wrapper cleanup before the pool is closed and its clone dropped.

// testStore returns Ent and SQL access to an independent migrated PostgreSQL
// clone. Test cleanup checks Ent closure before the suite closes its pool and
// forcibly drops that clone.
func testStore(t *testing.T) (*dbent.Client, *sql.DB) {
	t.Helper()
	db := databaseSuite.New(t)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	// Register Ent closure after cloned-database cleanup so the wrapper
	// closes its shared SQL pool first, and the suite later checks pool
	// cleanup before dropping its clone.
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return client, db
}

// testEntClient returns the Ent wrapper for a newly cloned promotion database
// and discards its SQL handle. The testStore cleanup still owns wrapper
// closure, pool release, and clone deletion.
func testEntClient(t *testing.T) *dbent.Client {
	t.Helper()
	client, _ := testStore(t)
	return client
}
