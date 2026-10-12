package postgres

import (
	"context"
	"database/sql"

	infra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// Advisory uses the SQL pool for advisory locks; callers choose the fallback policy.
type Advisory struct{ db *sql.DB }

func NewAdvisory(db *sql.DB) ops.AdvisoryLocker {
	if db == nil {
		return nil
	}
	return &Advisory{db}
}

func (a *Advisory) Acquire(ctx context.Context, key string) (func(), bool) {
	return infra.TryAcquireDBAdvisoryLock(ctx, a.db, infra.HashAdvisoryLockID(key))
}
