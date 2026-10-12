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

// refreshExchange pauses token exchange so an administrator credential update can commit first.
type refreshExchange struct {
	started chan struct{}
	release chan struct{}
	calls   int
}

// TestRefreshCredentialsCASAndOuterRollback checks stale-version rejection and transaction isolation.
// Credential updates inside an outer transaction remain pending until its caller commits.
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
	// A separate connection still sees committed credentials while the outer transaction is pending.
	require.Equal(t, "administrator", readVersion().Credentials["refresh_token"])
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

func TestRefreshCredentialsOutboxFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := time.Now().UnixNano()
	row, err := client.Provider.Create().SetName(fmt.Sprintf("test-credential-outbox-%d", suffix)).SetPlatform(capability.PlatformGemini).SetType(capability.ProviderTypeOAuth).SetCredentials(map[string]any{"refresh_token": "first"}).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(row.ID).Exec(context.Background())) })
	functionName := fmt.Sprintf("test_credential_outbox_fail_%d", suffix)
	triggerName := fmt.Sprintf("test_credential_outbox_trigger_%d", suffix)
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.provider_id = %d THEN RAISE EXCEPTION 'test forced outbox failure'; END IF; RETURN NEW; END $$`, functionName, row.ID))
	require.NoError(t, err)
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

func (e *refreshExchange) CanRefresh(a *providercore.Record) bool {
	return a.Platform == capability.PlatformOpenAI && a.Type == capability.ProviderTypeOAuth
}
func (e *refreshExchange) NeedsRefresh(*providercore.Record, time.Duration) bool { return true }
func (e *refreshExchange) CacheKey(a *providercore.Record) string {
	return fmt.Sprintf("test-refresh:%d", a.ID)
}

func (e *refreshExchange) Refresh(ctx context.Context, _ *providercore.Record) (map[string]any, error) {
	e.calls++
	close(e.started)
	select {
	case <-e.release:
		return map[string]any{"refresh_token": "late-provider-result"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

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
	type result struct {
		value *providercore.OAuthRefreshResult
		err   error
	}
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
