package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"time"
)

// Periodic maintenance needs a database-wide admission decision when Redis
// locking is unavailable. PostgreSQL session advisory locks require holding
// one reserved SQL connection through the work; a successful caller receives
// cleanup that attempts unlock before returning that connection to its pool.

// HashAdvisoryLockID derives a stable signed FNV-1a identifier from key.
func HashAdvisoryLockID(key string) int64 {
	h := fnv.New64a()
	// FNV Write consumes every supplied byte and returns a nil error, so
	// discarding its result still hashes the complete caller-supplied maintenance identity.
	_, _ = h.Write([]byte(key))
	return int64(h.Sum64())
}

// TryAcquireDBAdvisoryLock attempts a session advisory lock, treating
// database errors as failure to acquire. A successful caller invokes release
// once after its work; callers needing diagnostics use
// TryAcquireDBAdvisoryLockWithError.
func TryAcquireDBAdvisoryLock(ctx context.Context, db *sql.DB, lockID int64) (func(), bool) {
	// This convenience API exposes admission as a boolean. Discard acquisition
	// diagnostics here; maintenance callers needing the failure reason use the
	// error-returning variant.
	release, acquired, _ := TryAcquireDBAdvisoryLockWithError(ctx, db, lockID)
	return release, acquired
}

// TryAcquireDBAdvisoryLockWithError reserves a SQL connection and attempts
// lockID on that session. Nil db or a busy lock returns no acquisition; nil
// ctx selects a background context. Acquisition errors return diagnostics.
//
// On success, call release once after the protected work. Cleanup uses an
// independent two-second budget, discards unlock errors, and returns the
// connection to its pool; a failed unlock can leave the session lock held.
func TryAcquireDBAdvisoryLockWithError(ctx context.Context, db *sql.DB, lockID int64) (func(), bool, error) {
	if db == nil {
		return nil, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Keep the reserved session until release: PostgreSQL session locks follow
	// that connection, so returning it before protected work ends permits reuse.
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("open advisory-lock connection: %w", err)
	}

	acquired := false
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", lockID).Scan(&acquired); err != nil {
		_ = conn.Close()
		return nil, false, fmt.Errorf("query advisory lock: %w", err)
	}
	if !acquired {
		_ = conn.Close()
		return nil, false, nil
	}

	release := func() {
		// Work cancellation must not skip the unlock attempt. Its separate budget
		// bounds cleanup before returning the reserved session; failures are discarded.
		unlockCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(unlockCtx, "SELECT pg_advisory_unlock($1)", lockID)
		_ = conn.Close()
	}
	return release, true, nil
}
