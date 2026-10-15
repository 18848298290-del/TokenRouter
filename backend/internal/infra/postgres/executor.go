package postgres

import (
	"context"
	"database/sql"
)

// executor.go defines the SQL execution interface shared by database and
// transaction callers.
// Repository operations choose whether SQL runs on a shared database, a
// reserved connection, or a transaction. The small executor surface carries
// that choice through ExecContext and QueryContext; callers retain
// responsibility for transaction completion and successful result-row
// cleanup.

// Executor runs SQL on a caller-selected connection or transaction.
// The caller owns transaction creation and completion.
type Executor interface {
	// ExecContext executes a statement through the chosen database or transaction.
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	// QueryContext returns statement rows from the chosen database or transaction.
	// Callers close successful rows after iteration and check row errors before
	// treating the query as complete.
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}
