//go:build integration

package identity_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
)

// Two pending-session consumers can each read an unconsumed PostgreSQL row
// before their conditional updates compete. An Ent mutation hook parks both
// attempts after those reads and releases them together, allowing the test to
// count one committed consumer and one consumed-session error.
//
// The hook announces each mutation arrival through a buffered channel and
// observes cancellation while sending or waiting for release. A sync.Once makes
// release safe from both normal progress and test cleanup. Each worker owns an
// Ent transaction, commits after successful consumption, and rolls back on
// exit; buffered results are joined before their channel closes. The separate
// Ent wrapper isolates hooks while borrowing the cloned database pool.

// TestPendingTransactionConsumesOnce parks two PostgreSQL consumers at the
// pending-session mutation hook after their initial reads. Releasing both
// attempts yields one successful commit and one PendingAuthSessionConsumed
// result.
func TestPendingTransactionConsumesOnce(t *testing.T) {
	integrationDB, _ := identityDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Isolate these mutation hooks in a separate Ent wrapper while retaining the
	// fixture-owned SQL pool, whose cloned-database cleanup closes the underlying
	// connection lifetime.
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, integrationDB)))
	session, err := client.PendingAuthSession.Create().SetSessionToken(uuid.NewString()).SetIntent("login").SetProviderType("oidc").SetProviderKey("test-test").SetProviderSubject(uuid.NewString()).SetBrowserSessionKey("test-browser").SetExpiresAt(time.Now().Add(time.Minute)).Save(ctx)
	require.NoError(t, err)
	// The later unblock cleanup opens release before this deletion, but the
	// installed mutation hook still sends an arrival. Deletion needs available
	// arrival-buffer capacity.
	t.Cleanup(func() {
		require.NoError(t, client.PendingAuthSession.DeleteOneID(session.ID).Exec(context.Background()))
	})
	// Buffer both post-read mutation signals so the test can wait for both
	// consumers to reach their writes before releasing the shared update gate.
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	// Let normal progress and cleanup both release parked consumers safely; one
	// close opens their shared gate, and repeated cleanup leaves that gate open.
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	client.PendingAuthSession.Use(func(next dbent.Mutator) dbent.Mutator {
		return dbent.MutateFunc(func(ctx context.Context, m dbent.Mutation) (dbent.Value, error) {
			// Signal arrival before executing the conditional update so both consumers
			// can finish their initial reads and still compete for one unconsumed row.
			select {
			case arrived <- struct{}{}:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			select {
			case <-release:
				return next.Mutate(ctx, m)
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
	})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			tx, e := client.Tx(ctx)
			if e != nil {
				results <- e
				return
			}
			// Keep rollback registered until worker exit so failed consumption or commit
			// cannot leave an open transaction; an already completed rollback error is
			// discarded.
			defer func() { _ = tx.Rollback() }()
			e = identitypostgres.ConsumePendingOAuthBrowserSessionTx(dbent.NewTxContext(ctx, tx), tx, session)
			// Commit successful consumption before sending its result so the competing
			// transaction can observe the consumed state after its conditional write
			// loses.
			if e == nil {
				e = tx.Commit()
			}
			results <- e
		})
	}
	for range 2 {
		select {
		case <-arrived:
		case <-ctx.Done():
			t.Fatal("两个事务未到达消费写入屏障", ctx.Err())
		}
	}
	// Release updates after both mutation signals, then join each worker's send
	// before closing results so outcome counting sees complete, committed
	// transaction attempts.
	unblock()
	workers.Wait()
	close(results)
	success, consumed := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, identity.ErrPendingAuthSessionConsumed) {
			consumed++
		} else {
			t.Errorf("非预期事务错误: %v", e)
		}
	}
	require.Equal(t, 1, success, "同一 pending session 只能由一个事务完成")
	require.Equal(t, 1, consumed, "另一个事务必须报告已消费")
}
