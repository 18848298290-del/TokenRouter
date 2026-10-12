package postgres

import (
	"context"
	"database/sql"
)

// Executor runs SQL on a caller-selected connection or transaction.
// The caller owns transaction creation and completion.
type Executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}
