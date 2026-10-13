//go:build integration

package provider_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Refresh credential writes must match the identity observed before exchange
// and keep scheduling events with the credential update. PostgreSQL cases check
// stale comparisons, caller-owned rollback, and a trigger that rejects outbox
// persistence.
//
// The public refresh case pauses an exchange through channels while an
// administrator commits new credentials. After release, the coordinator returns
// the current provider with Refreshed false and leaves the administrator token
// stored. The exchange fixture reports its first invocation before waiting,
// then returns either its fixed late credentials or the context error.
// This makes the competing update observable before completion.

// refreshExchange gates one token exchange so a committed administrator write
// can precede completion.
type refreshExchange struct {
	// started and release gate one exchange: Refresh closes started before
	// waiting, and the test closes release after committing its administrator
	// update. The fixture instance is used for one exchange call.
	started chan struct{} // exchange-entry signal channel
	release chan struct{} // exchange-completion gate channel
	// calls is incremented by Refresh and read after completion; the single-call
	// fixture provides no concurrent counter synchronization.
	calls int // observed exchange call count
}

// TestRefreshCredentialsCASAndOuterRollback applies stale and current
// credential versions on PostgreSQL, including a caller-owned transaction.
// Outside reads retain committed credentials until completion, rollback
// restores them, and disabled identity rejects the later write.
func TestRefreshCredentialsCASAndOuterRollback(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := time.Now().UnixNano()
	row, err := client.Provider.Create().SetName(fmt.Sprintf("test-credential-%d", suffix)).SetPlatform(capability.PlatformOpenAI).SetType(capability.ProviderTypeOAuth).SetCredentials(map[string]any{"refresh_token": "first"}).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(row.ID).Exec(context.Background())) })
	repo := newProviderStoreContract(client, integrationDB, nil)
	readVersion := func() providercore.CredentialVersion {
		current, e := repo.GetByID(ctx, row.ID)
		require.NoError(t, e)
		return providercore.CredentialVersion{ID: current.ID, Platform: current.Platform, Type: current.Type, Status: current.Status, ProxyID: current.ProxyID, Credentials: current.Credentials}
	}
	initial := readVersion()
	require.NoError(t, repo.UpdateCredentials(ctx, row.ID, map[string]any{"refresh_token": "administrator"}))
	applied, err := repo.UpdateOAuthCredentialsIfUnchanged(ctx, initial, map[string]any{"refresh_token": "stale"})
	require.NoError(t, err)
	require.False(t, applied)
	require.Equal(t, "administrator", readVersion().Credentials["refresh_token"])

	version := readVersion()
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	applied, err = repo.UpdateOAuthCredentialsIfUnchanged(txCtx, version, map[string]any{"refresh_token": "pending"})
	require.NoError(t, err)
	require.True(t, applied)
	pending, err := tx.Client().Provider.Get(txCtx, row.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", pending.Credentials["refresh_token"])
	// Read through the separate store connection while the transaction remains
	// open, making visibility of the pending credential update observable before rollback.
	require.Equal(t, "administrator", readVersion().Credentials["refresh_token"])
	// End the caller-owned transaction after its pending read; the next
	// committed-data read checks that the credential replacement was rolled back.
	require.NoError(t, tx.Rollback())
	require.Equal(t, "administrator", readVersion().Credentials["refresh_token"])

	applied, err = repo.UpdateOAuthCredentialsIfUnchanged(ctx, version, map[string]any{"refresh_token": "committed"})
	require.NoError(t, err)
	require.True(t, applied)
	require.Equal(t, "committed", readVersion().Credentials["refresh_token"])
	stale := readVersion()
	require.NoError(t, client.Provider.UpdateOneID(row.ID).SetStatus(billing.StatusDisabled).Exec(ctx))
	applied, err = repo.UpdateOAuthCredentialsIfUnchanged(ctx, stale, map[string]any{"refresh_token": "late"})
	require.NoError(t, err)
	require.False(t, applied)
	require.Equal(t, "committed", readVersion().Credentials["refresh_token"])
}

// TestRefreshCredentialsOutboxFailureRollsBack installs a provider-specific
// trigger that rejects scheduler event insertion or update. The conditional
// credential write returns an error and false, and a fresh read retains the
// original refresh token.
func TestRefreshCredentialsOutboxFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := time.Now().UnixNano()
	row, err := client.Provider.Create().SetName(fmt.Sprintf("test-credential-outbox-%d", suffix)).SetPlatform(capability.PlatformGemini).SetType(capability.ProviderTypeOAuth).SetCredentials(map[string]any{"refresh_token": "first"}).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(row.ID).Exec(context.Background())) })
	functionName := fmt.Sprintf("test_credential_outbox_fail_%d", suffix)
	triggerName := fmt.Sprintf("test_credential_outbox_trigger_%d", suffix)
	// Limit the failing outbox trigger to this provider and give its objects
	// unique names, so unrelated committed fixtures can continue enqueueing events.
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.provider_id = %d THEN RAISE EXCEPTION 'test forced outbox failure'; END IF; RETURN NEW; END $$`, functionName, row.ID))
	require.NoError(t, err)
	// Remove the trigger before its function when cleanup runs, releasing the
	// injected failure path before another test uses this shared outbox table.
	t.Cleanup(func() {
		_, e := integrationDB.ExecContext(context.Background(), fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON scheduler_outbox", triggerName))
		require.NoError(t, e)
		_, e = integrationDB.ExecContext(context.Background(), fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", functionName))
		require.NoError(t, e)
	})
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT OR UPDATE ON scheduler_outbox FOR EACH ROW EXECUTE FUNCTION %s()", triggerName, functionName))
	require.NoError(t, err)
	repo := newProviderStoreContract(client, integrationDB, nil)
	version := providercore.CredentialVersion{ID: row.ID, Platform: row.Platform, Type: row.Type, Status: row.Status, Credentials: row.Credentials}
	applied, err := repo.UpdateOAuthCredentialsIfUnchanged(ctx, version, map[string]any{"refresh_token": "uncommitted"})
	require.ErrorContains(t, err, "test forced outbox failure")
	require.False(t, applied)
	current, err := repo.GetByID(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, "first", current.Credentials["refresh_token"])
}

// CanRefresh reports whether the fixture record is an OpenAI OAuth provider.
func (e *refreshExchange) CanRefresh(a *providercore.Record) bool {
	return a.Platform == capability.PlatformOpenAI && a.Type == capability.ProviderTypeOAuth
}

// NeedsRefresh requests an exchange for every record admitted by the fixture.
func (e *refreshExchange) NeedsRefresh(*providercore.Record, time.Duration) bool { return true }

// CacheKey returns a provider-specific key for the fixture's refresh
// coordination.
func (e *refreshExchange) CacheKey(a *providercore.Record) string {
	return fmt.Sprintf("test-refresh:%d", a.ID)
}

// Refresh records one exchange and announces its start before waiting for
// release or cancellation. Each fixture instance accepts one call because
// closing started again would panic.
//
// Selecting release returns the fixture's fixed late token; selecting
// cancellation returns ctx.Err. When both channels are ready, select may take
// either branch. The test changes persisted credentials between start and
// release so the coordinator can compare that response with the current
// identity after exchange.
func (e *refreshExchange) Refresh(ctx context.Context, _ *providercore.Record) (map[string]any, error) {
	// Announce entry before waiting so the test can change PostgreSQL state while
	// the exchange result remains pending at this fixture's completion gate.
	e.calls++
	close(e.started)
	select {
	case <-e.release:
		return map[string]any{"refresh_token": "late-provider-result"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestRefreshPublicEntryPreservesConcurrentAdminCredentials suspends the
// exchange until an administrator commits a new refresh token. Completion
// returns the administrator record with Refreshed false and no new credentials,
// while storage retains the administrator token.
func TestRefreshPublicEntryPreservesConcurrentAdminCredentials(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := testEntClient(t)
	row, err := client.Provider.Create().SetName(fmt.Sprintf("test-refresh-public-%d", time.Now().UnixNano())).SetPlatform(capability.PlatformOpenAI).SetType(capability.ProviderTypeOAuth).SetCredentials(map[string]any{"refresh_token": "original"}).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(row.ID).Exec(context.Background())) })
	repo := newProviderStoreContract(client, integrationDB, nil)
	original, err := repo.GetByID(ctx, row.ID)
	require.NoError(t, err)
	executor := &refreshExchange{started: make(chan struct{}), release: make(chan struct{})}
	// result carries refresh coordination output to the waiting integration test.
	type result struct {
		value *providercore.OAuthRefreshResult // completed refresh coordination result
		err   error                            // returned refresh coordination error
	}
	// Buffer the refresh result so its goroutine can finish publishing after
	// the exchange gate is released, even before the test starts receiving.
	done := make(chan result, 1)
	go func() {
		v, e := providercore.NewOAuthRefreshAPI(repo, nil, providercore.RefreshOptions{Platform: providercore.ProviderRefreshPlatformPolicy()}).RefreshIfNeeded(ctx, original, executor, time.Minute)
		done <- result{v, e}
	}()
	select {
	case <-executor.started:
	case <-ctx.Done():
		t.Fatal("刷新未进入交换阶段")
	}
	require.NoError(t, repo.UpdateCredentials(ctx, row.ID, map[string]any{"refresh_token": "administrator"}))
	close(executor.release)
	var r result
	select {
	case r = <-done:
	case <-ctx.Done():
		t.Fatal("刷新未结束")
	}
	require.NoError(t, r.err)
	require.NotNil(t, r.value)
	require.False(t, r.value.Refreshed)
	require.Nil(t, r.value.NewCredentials)
	require.Equal(t, "administrator", r.value.Provider.Credentials["refresh_token"])
	persisted, err := repo.GetByID(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, "administrator", persisted.Credentials["refresh_token"])
	require.Equal(t, 1, executor.calls)
}
