package postgres

import (
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// newSQLMock opens a database fixture with regular-expression query matching
// and registers its close with test cleanup. SQL expectations belong to the
// caller, while initialization failures stop the test.
func newSQLMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, m, e := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, e)
	// Close the mock handle after caller assertions finish; a cleanup close
	// failure is suppressed and does not replace expectation results.
	t.Cleanup(func() { _ = db.Close() })
	return db, m
}
