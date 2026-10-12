package postgres

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/lib/pq"
)

// Storage callers need to distinguish duplicate writes, deadlocks, and absent
// rows after errors have been wrapped. PostgreSQL SQLSTATE supplies typed
// decisions where available; selected message compatibility checks support
// duplicate and no-row errors exposed through other adapters.

// IsUniqueConstraintViolation reports whether a wrapped pq error has SQLSTATE
// 23505 or another error contains a recognized duplicate message. A pq
// error's code decides directly; nil input returns false and text matching is
// case-insensitive.
func IsUniqueConstraintViolation(err error) bool {
	if err == nil {
		return false
	}

	// A pq error carries SQLSTATE even when its diagnostic text suggests another
	// category. Let that code decide before considering message compatibility
	// for other error types.
	// See https://www.postgresql.org/docs/current/errcodes-appendix.html
	if pgErr, ok := errors.AsType[*pq.Error](err); ok {
		return pgErr.Code == "23505"
	}

	// Adapters can expose duplicate diagnostics without a pq error. Recognize
	// these common database messages after the typed SQLSTATE path has been unavailable.
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicate entry")
}

// IsDeadlock reports whether a wrapped PostgreSQL error has deadlock SQLSTATE
// 40P01.
func IsDeadlock(err error) bool {
	return SQLState(err) == "40P01"
}

// SQLState returns the code from a non-nil wrapped pq error. Nil input, other
// error types, and a wrapped nil pq pointer return empty text for callers
// lacking typed classification.
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

// IsNoRows reports whether err wraps sql.ErrNoRows or contains the exact
// no-rows message. Nil input returns false; the compatibility substring
// comparison retains the error text's casing.
func IsNoRows(err error) bool {
	if err == nil {
		return false
	}
	// A wrapped sentinel remains recognizable by identity. The exact lowercase
	// diagnostic also recognizes absence when an adapter exposes text without that sentinel.
	return errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "no rows in result set")
}
