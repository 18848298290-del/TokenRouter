package postgres

import (
	"context"
	"database/sql"

	infra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// Ops jobs can attempt a PostgreSQL advisory lock when their chosen Redis
// policy requires a database alternative. The shared lock helper reserves a
// pooled connection for a successful session lock and returns cleanup that
// attempts unlocking before returning that connection.

// Advisory borrows the SQL pool for named Ops job lock attempts.
type Advisory struct {
	// db is the application-owned SQL pool. Successful lock attempts reserve one
	// pooled connection until the caller invokes the returned release function.
	db *sql.DB
}

// NewAdvisory wraps a caller-owned database pool for Ops lock attempts. A nil
// pool returns a nil interface, allowing jobs to distinguish an unavailable
// locking dependency from a configured adapter.
func NewAdvisory(db *sql.DB) ops.AdvisoryLocker {
	if db == nil {
		return nil
	}
	return &Advisory{db}
}

// Acquire hashes key and attempts a PostgreSQL session advisory lock on a
// reserved connection. Call the returned release once after successful
// acquisition; contention and acquisition errors both return false through this
// wrapper.
func (a *Advisory) Acquire(ctx context.Context, key string) (func(), bool) {
	return infra.TryAcquireDBAdvisoryLock(ctx, a.db, infra.HashAdvisoryLockID(key))
}
