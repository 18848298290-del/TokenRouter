package postgres

import (
	"context"
	"database/sql"
	"errors"
)

// Transaction adapters can supply QueryContext rows without constructing
// sql.Row. Single-row scanning consumes the first result and then closes the
// obtained rows, combining cleanup failures with scan or iteration errors;
// additional rows do not change the selected value.

// Queryer supplies result rows from a caller-selected SQL connection or transaction.
type Queryer interface {
	// QueryContext returns rows for the selected SQL statement and arguments.
	// Successful rows remain caller-owned until closed; query errors are returned
	// before ScanSingleRow registers any result cleanup.
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// ScanSingleRow queries q, scans the first row into dest, and closes
// successfully obtained rows. Query errors return before cleanup is
// registered; an empty successful result returns sql.ErrNoRows unless
// iteration failed. After row acquisition, Close errors join the returned
// scan or row error, including an otherwise successful result.
func ScanSingleRow(ctx context.Context, q Queryer, query string, args []any, dest ...any) (err error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	// Rows now belong to this scan. Join their cleanup failure with the
	// chosen row result so a successful scan cannot hide a later Close error.
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
