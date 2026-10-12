//go:build integration

package migrations_test

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"net/url"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"

	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// integrationDSN and integrationDB let migration tests create transactions and historical databases.
var (
	integrationDSN string
	integrationDB  *sql.DB
)

// testTx creates a transaction and registers rollback at test cleanup.
func testTx(t *testing.T) *sql.Tx {
	t.Helper()
	tx, err := integrationDB.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

// historicalTx prepares an isolated database using migrations whose filenames sort before before.
func historicalTx(t *testing.T, before string) *sql.Tx {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("migration_history_%d", time.Now().UnixNano())
	_, err := integrationDB.ExecContext(ctx, "CREATE DATABASE "+name)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
		require.NoError(t, err)
	})
	dsn, err := url.Parse(integrationDSN)
	require.NoError(t, err)
	dsn.Path = "/" + name
	db, err := sql.Open("postgres", dsn.String())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	history := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.FS, ".")
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.Name() >= before {
			continue
		}
		data, err := migrations.FS.ReadFile(entry.Name())
		require.NoError(t, err)
		history[entry.Name()] = &fstest.MapFile{Data: data}
	}
	require.NoError(t, postgresinfra.ApplyMigrations(ctx, db, history))
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}
