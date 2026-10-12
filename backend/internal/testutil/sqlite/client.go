package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/enttest"
)

// Ent unit fixtures use a named in-memory SQLite database with shared-cache
// access. The name derives from the test name with slash and space replacement,
// so overlapping callers with the same normalized name can refer to the same
// database until their connections close.

// NewClient constructs an Ent SQLite fixture and fails the test on setup
// errors. It enables foreign keys on the connection used by the PRAGMA
// statement, then enttest creates the schema through the supplied SQL driver.
//
// The memory database uses a shared-cache name derived from t.Name, so
// simultaneous normalized-name collisions can share storage. Cleanup is
// registered for the SQL pool and Ent client, with the later client cleanup
// running first. SQLite foreign-key enforcement is per connection; this one
// statement does not configure a pool connection hook. PostgreSQL migration and
// transaction behavior belongs to the integration fixtures.
func NewClient(t *testing.T) *dbent.Client {
	t.Helper()
	name := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	db, err := sql.Open("sqlite", name)
	require.NoError(t, err)
	// Keep the SQL pool reachable for cleanup if schema fixture construction fails.
	// The Ent cleanup registered later runs first and also closes its supplied driver.
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	// SQLite applies this PRAGMA to its executing connection. Subsequent pooled
	// connections require their own setting when a test relies on foreign-key checks.
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return client
}
