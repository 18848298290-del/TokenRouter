package postgres

import (
	"database/sql"
	"fmt"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/enttest"
)

// Account storage unit tests use SQLite to exercise Ent rows and SQL
// expectations to inspect redemption updates. Fixture cleanup closes the Ent
// client before its underlying pool; neither fixture establishes PostgreSQL
// transaction or cross-process locking behavior.

// newUserEntRepo creates a SQLite account store with a database name derived
// from the test. It enables foreign keys and registers Ent-client and pool
// cleanup; callers share the returned pool through both adapters.
func newUserEntRepo(t *testing.T) (*UserStore, *dbent.Client) {
	t.Helper()

	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared&_fk=1", t.Name()))
	require.NoError(t, err)
	// Retain pool cleanup even when later fixture construction fails. Its close
	// error is discarded; the later Ent cleanup runs first under testing cleanup
	// order.
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(10)

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	// Ent cleanup is registered after pool cleanup, so it runs first. This keeps
	// driver teardown ahead of the final underlying pool close for each fixture.
	t.Cleanup(func() { _ = client.Close() })

	return NewUserStoreWithSQL(client, db), client
}

// newRedeemAdjustmentRepoMock creates an account store backed by
// PostgreSQL-shaped SQL expectations. Cleanup closes the Ent client before its
// pool, and callers verify the returned mock expectations after their storage
// operation.
func newRedeemAdjustmentRepoMock(t *testing.T) (*UserStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Retain pool cleanup even when later fixture construction fails. Its close
	// error is discarded; the later Ent cleanup runs first under testing cleanup
	// order.
	t.Cleanup(func() { _ = db.Close() })
	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
	// Ent cleanup is registered after pool cleanup, so it runs first. This keeps
	// driver teardown ahead of the final underlying pool close for each fixture.
	t.Cleanup(func() { _ = client.Close() })
	return NewUserStoreWithSQL(client, db), mock
}
