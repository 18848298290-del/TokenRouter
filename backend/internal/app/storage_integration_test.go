//go:build integration

package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	"github.com/TokenFlux/TokenRouter/internal/idempotency"
	idempotencypostgres "github.com/TokenFlux/TokenRouter/internal/idempotency/postgres"
	"github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	settingspostgres "github.com/TokenFlux/TokenRouter/internal/settings/postgres"
	"github.com/TokenFlux/TokenRouter/internal/site"
	sitepostgres "github.com/TokenFlux/TokenRouter/internal/site/postgres"
)

// Persistent storage scenarios exercise migration replay and checksums
// alongside settings, idempotency, and announcement transactions. Concurrent
// callers contend on production database coordination, while injected SQL
// failure and explicit rollback make atomic state effects visible through later
// ordinary reads.
//
// Migration checks include a nontransactional concurrent index and cancellation
// while its advisory lock is held. Settings batch failure retains the old
// value, repeated idempotency claims produce one side effect before
// replay/conflict checks, and announcement read state rolls back with creation
// while a later repeated read retains its first timestamp.

// TestMigrationsLockReplayAndRollback applies the same migrations concurrently
// on isolated PostgreSQL. It checks one recorded copy of each migration,
// checksum rejection, rollback of a failing transactional migration, and
// deadline cancellation while the advisory lock is held.
func TestMigrationsLockReplayAndRollback(t *testing.T) {
	fixture := newDatabaseFixture(t)
	ctx := context.Background()
	migrations := fstest.MapFS{
		"900_test_contract.sql":            {Data: []byte("CREATE TABLE test_migration_contract(id bigint PRIMARY KEY, value text);")},
		"901_test_contract_index_notx.sql": {Data: []byte("CREATE INDEX CONCURRENTLY IF NOT EXISTS test_migration_index ON test_migration_contract(value);")},
	}
	// Apply one migration set from two callers before examining results. The
	// buffered channel lets both finish while database coordination serializes their persistent changes.
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { results <- postgres.ApplyMigrations(ctx, fixture.db, migrations) })
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	var count int
	require.NoError(t, fixture.db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE filename LIKE '90%_test_%'`).Scan(&count))
	require.Equal(t, 2, count)
	changed := fstest.MapFS{"900_test_contract.sql": {Data: []byte("SELECT 1;")}}
	require.ErrorContains(t, postgres.ApplyMigrations(ctx, fixture.db, changed), "checksum")
	failed := fstest.MapFS{"902_test_rollback.sql": {Data: []byte("CREATE TABLE test_should_rollback(id bigint); SELECT 1/0;")}}
	require.Error(t, postgres.ApplyMigrations(ctx, fixture.db, failed))
	var rolledBack bool
	require.NoError(t, fixture.db.QueryRow(`SELECT to_regclass('test_should_rollback') IS NULL`).Scan(&rolledBack))
	require.True(t, rolledBack)
	conn, err := fixture.db.Conn(ctx)
	require.NoError(t, err)
	// The later registered unlock runs before this connection release. Discard
	// the final close error after lock behavior assertions while preserving the connection throughout that held-lock scenario.
	defer func() { _ = conn.Close() }()
	// Hold the migration advisory identity on a dedicated connection so the
	// next ApplyMigrations waits and its short context demonstrates lock acquisition cancellation.
	_, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock(694208311321144027)`)
	require.NoError(t, err)
	defer func() {
		_, err := conn.ExecContext(ctx, `SELECT pg_advisory_unlock(694208311321144027)`)
		require.NoError(t, err)
	}()
	lockCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	require.True(t, errors.Is(postgres.ApplyMigrations(lockCtx, fixture.db, migrations), context.DeadlineExceeded))
}

// TestStorageContracts exercises transactional settings batches, concurrent
// idempotency claims, and announcement persistence. It checks rollback on
// injected failure, one side effect with replay/conflict cleanup, and
// announcement/read atomicity plus retained first-read time and expiry
// archival.
func TestStorageContracts(t *testing.T) {
	fixture := newDatabaseFixture(t)
	ctx := context.Background()
	t.Run("settings-batch-atomicity", func(t *testing.T) {
		store := settings.New(settingspostgres.NewSettingRepository(fixture.client))
		require.Same(t, store, settings.New(store), "接口投影必须保留同一设置状态")
		require.NoError(t, store.Set(ctx, "test_existing", "before"))
		// Reject one key inside the batch write so the test can verify an
		// existing key and the attempted new key share the same persistence rollback.
		_, err := fixture.db.ExecContext(ctx, `CREATE FUNCTION test_reject_setting() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.key = 'test_reject' THEN RAISE EXCEPTION 'test injected failure'; END IF; RETURN NEW; END $$;
CREATE TRIGGER test_setting_failure BEFORE INSERT OR UPDATE ON settings FOR EACH ROW EXECUTE FUNCTION test_reject_setting();`)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, e := fixture.db.ExecContext(ctx, `DROP TRIGGER test_setting_failure ON settings; DROP FUNCTION test_reject_setting();`)
			require.NoError(t, e)
		})
		err = store.SetMultiple(ctx, map[string]string{"test_existing": "after", "test_reject": "value"})
		require.Error(t, err)
		value, err := store.GetValue(ctx, "test_existing")
		require.NoError(t, err)
		require.Equal(t, "before", value)
		_, err = store.Get(ctx, "test_reject")
		require.ErrorIs(t, err, settings.ErrSettingNotFound)
	})
	t.Run("idempotency-concurrent-claim-replay-cleanup", func(t *testing.T) {
		repo := idempotencypostgres.NewIdempotencyRepository(fixture.db)
		cfg := idempotency.DefaultIdempotencyConfig()
		cfg.ObserveOnly = false
		coordinator := idempotency.NewIdempotencyCoordinator(repo, cfg)
		opts := idempotency.IdempotencyExecuteOptions{Scope: "test.test", ActorScope: "user:1", Method: "POST", Route: "/contract", IdempotencyKey: "same-key", Payload: map[string]string{"value": "same"}, RequireKey: true}
		// Run concurrent claims for one fingerprint and count only the winning
		// callback. Joining every caller before replay assertions makes duplicate side effects observable in the final count.
		var effects atomic.Int32
		var wg sync.WaitGroup
		results := make(chan error, 24)
		for range 24 {
			wg.Go(func() {
				_, err := coordinator.Execute(ctx, opts, func(context.Context) (any, error) {
					effects.Add(1)
					// Keep the winning callback in progress briefly so other claims
					// can observe its active lease before the result becomes available for replay.
					time.Sleep(25 * time.Millisecond)
					return map[string]string{"result": "ok"}, nil
				})
				results <- err
			})
		}
		wg.Wait()
		close(results)
		for err := range results {
			require.True(t, err == nil || errors.Is(err, idempotency.ErrIdempotencyInProgress), "unexpected result: %v", err)
		}
		require.EqualValues(t, 1, effects.Load())
		replay, err := coordinator.Execute(ctx, opts, func(context.Context) (any, error) { t.Error("重放再次执行副作用"); return nil, nil })
		require.NoError(t, err)
		require.True(t, replay.Replayed)
		opts.Payload = map[string]string{"value": "different"}
		_, err = coordinator.Execute(ctx, opts, func(context.Context) (any, error) { t.Error("冲突执行副作用"); return nil, nil })
		require.ErrorIs(t, err, idempotency.ErrIdempotencyKeyConflict)
		// Advance cleanup time past the fixture entry lifetime so expiry removal
		// tests the same claimed record after successful replay and payload-conflict handling have completed.
		deleted, err := repo.DeleteExpired(ctx, time.Now().Add(48*time.Hour), 500)
		require.NoError(t, err)
		require.EqualValues(t, 1, deleted)
	})
	t.Run("announcement-transactions-and-first-read", func(t *testing.T) {
		user, err := fixture.client.User.Create().SetEmail("test@example.test").SetPasswordHash("test-only").Save(ctx)
		require.NoError(t, err)
		repo := sitepostgres.NewAnnouncementRepository(fixture.client)
		reads := sitepostgres.NewAnnouncementReadRepository(fixture.client)
		tx, err := fixture.client.Tx(ctx)
		require.NoError(t, err)
		txCtx := dbent.NewTxContext(ctx, tx)
		rolledBack := &site.Announcement{Title: "rollback", Content: "body", Status: site.AnnouncementStatusActive, NotifyMode: site.AnnouncementNotifyModeSilent}
		require.NoError(t, repo.Create(txCtx, rolledBack))
		require.NoError(t, reads.MarkRead(txCtx, rolledBack.ID, user.ID, time.Now()))
		// Roll back creation and its read marker through the shared Ent context.
		// Later ordinary repository reads must observe neither part of that uncommitted announcement transaction.
		require.NoError(t, tx.Rollback())
		_, err = repo.GetByID(ctx, rolledBack.ID)
		require.ErrorIs(t, err, site.ErrAnnouncementNotFound)
		count, err := reads.CountByAnnouncementID(ctx, rolledBack.ID)
		require.NoError(t, err)
		require.Zero(t, count)
		announcement := &site.Announcement{Title: "visible", Content: "body", Status: site.AnnouncementStatusActive, NotifyMode: site.AnnouncementNotifyModePopup}
		require.NoError(t, repo.Create(ctx, announcement))
		// PostgreSQL read timestamps retain microseconds, so match that precision before comparison.
		first := time.Now().UTC().Truncate(time.Microsecond)
		require.NoError(t, reads.MarkRead(ctx, announcement.ID, user.ID, first))
		require.NoError(t, reads.MarkRead(ctx, announcement.ID, user.ID, first.Add(time.Hour)))
		readMap, err := reads.GetReadMapByUser(ctx, user.ID, []int64{announcement.ID})
		require.NoError(t, err)
		require.True(t, first.Equal(readMap[announcement.ID]))
		past := time.Now().Add(-time.Hour)
		announcement.EndsAt = &past
		require.NoError(t, repo.Update(ctx, announcement))
		archived, err := repo.ArchiveExpired(ctx, time.Now())
		require.NoError(t, err)
		require.EqualValues(t, 1, archived)
		stored, err := repo.GetByID(ctx, announcement.ID)
		require.NoError(t, err)
		require.Equal(t, site.AnnouncementStatusArchived, stored.Status)
	})
}
