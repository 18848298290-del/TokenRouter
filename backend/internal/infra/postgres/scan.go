package postgres

import (
	"context"
	"database/sql"
	"errors"
)

type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// ScanSingleRow scans the first result row into dest and closes the result set.
// No results return sql.ErrNoRows; close errors are joined with any earlier error.
// Requiring QueryContext lets transaction adapters supply rows without exposing sql.Row.
func ScanSingleRow(ctx context.Context, q Queryer, query string, args []any, dest ...any) (err error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	if !rows.Next() {
		if err = rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	if err = rows.Scan(dest...); err != nil {
		return err
	}
	return rows.Err()
}
