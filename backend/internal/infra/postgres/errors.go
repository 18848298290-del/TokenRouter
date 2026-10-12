package postgres

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/lib/pq"
)

func IsUniqueConstraintViolation(err error) bool {
	if err == nil {
		return false
	}

	// Use the PostgreSQL SQLSTATE when available: 23505 is unique_violation.
	// See https://www.postgresql.org/docs/current/errcodes-appendix.html
	if pgErr, ok := errors.AsType[*pq.Error](err); ok {
		return pgErr.Code == "23505"
	}

	// For other error types, recognize common PostgreSQL and MySQL duplicate messages.
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicate entry")
}

func IsDeadlock(err error) bool {
	return SQLState(err) == "40P01"
}

func SQLState(err error) string {
	if err == nil {
		return ""
	}
	var pgErr *pq.Error
	if !errors.As(err, &pgErr) || pgErr == nil {
		return ""
	}
	return string(pgErr.Code)
}

// IsNoRows recognizes sql.ErrNoRows and errors containing "no rows in result set".
func IsNoRows(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "no rows in result set")
}
