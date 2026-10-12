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

// TestPendingTransactionConsumesOnce races two PostgreSQL transactions after both read an unconsumed session.
func TestPendingTransactionConsumesOnce(t *testing.T) {
	integrationDB, _ := identityDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Use a separate Ent wrapper to isolate hooks; the database fixture owns the underlying pool.
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, integrationDB)))
	session, err := client.PendingAuthSession.Create().SetSessionToken(uuid.NewString()).SetIntent("login").SetProviderType("oidc").SetProviderKey("test-test").SetProviderSubject(uuid.NewString()).SetBrowserSessionKey("test-browser").SetExpiresAt(time.Now().Add(time.Minute)).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, client.PendingAuthSession.DeleteOneID(session.ID).Exec(context.Background()))
	})
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	client.PendingAuthSession.Use(func(next dbent.Mutator) dbent.Mutator {
		return dbent.MutateFunc(func(ctx context.Context, m dbent.Mutation) (dbent.Value, error) {
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
			defer func() { _ = tx.Rollback() }()
			e = identitypostgres.ConsumePendingOAuthBrowserSessionTx(dbent.NewTxContext(ctx, tx), tx, session)
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
