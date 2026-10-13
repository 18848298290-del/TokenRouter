//go:build integration

package app

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Configuration edits must not overwrite quota consumption accumulated after an
// execution target was read. The persistent scenario checks copied credentials,
// a declared name-only edit, and an outer Ent transaction whose uncommitted
// name is visible through its participant and disappears after rollback.

// NewExecutionProviderStoreForTest uses production store assembly for
// persistent provider and funding-state scenarios.
var NewExecutionProviderStoreForTest = provideExecutionProviderStore

// TestExecutionStoreUsesNativeStateAndOuterTransaction edits a stale execution
// target after a quota increment. It checks credential isolation and name-only
// persistence, then confirms an outer transaction can read its change and roll
// it back while retaining prior quota state.
func TestExecutionStoreUsesNativeStateAndOuterTransaction(t *testing.T) {
	f := newDatabaseFixture(t)
	ctx := t.Context()
	data := NewProviderStoreForTest(f.client, f.db, nil)
	funds := billingpostgres.NewProviderUsageStore(f.db, billingpostgres.ProviderUsageOptions{})
	store := NewExecutionProviderStoreForTest(data, funds)
	row, err := f.client.Provider.Create().SetName("execution-store").SetPlatform(provider.PlatformOpenAI).SetType(provider.ProviderTypeAPIKey).SetCredentials(map[string]any{"api_key": "fixture-key"}).SetExtra(map[string]any{"quota_limit": 100.0, "quota_used": 0.0}).Save(ctx)
	require.NoError(t, err)
	value, err := store.GetByID(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, "fixture-key", value.View().GetCredential("api_key"))
	value.Record.Credentials["api_key"] = "request-private"
	another, err := store.GetByID(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, "fixture-key", another.View().GetCredential("api_key"))
	require.NoError(t, store.IncrementQuotaUsed(ctx, row.ID, 2.5))
	writer, ok := store.(interface {
		// UpdateConfiguration participates in the supplied Ent transaction while
		// merging only declared configuration fields, retaining current credentials and funding counters.
		UpdateConfiguration(context.Context, *gatewayadapter.ExecutionProvider, provider.ConfigurationChange) error
	})
	require.True(t, ok, "保留执行边界原配置参与能力")
	value.Record.Name = "renamed"
	require.NoError(t, writer.UpdateConfiguration(ctx, value, provider.ConfigurationChange{Fields: provider.ConfigName}))
	persisted, err := data.GetByID(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, "renamed", persisted.Name)
	require.Equal(t, 2.5, persisted.GetQuotaUsed(), "配置不得覆盖读快照后的原子消费")
	require.Equal(t, "fixture-key", persisted.GetCredential("api_key"))
	tx, err := f.client.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		// The test rolls back explicitly below. Cleanup also handles assertion
		// exits, and an already completed transaction is accepted by this final rollback.
		err := tx.Rollback()
		if err != nil && !errors.Is(err, sql.ErrTxDone) {
			require.NoError(t, err)
		}
	})
	txctx := dbent.NewTxContext(ctx, tx)
	value.Record.Name = "uncommitted"
	require.NoError(t, writer.UpdateConfiguration(txctx, value, provider.ConfigurationChange{Fields: provider.ConfigName}))
	// Read through the transaction client to observe its uncommitted name.
	// The later ordinary store read happens after rollback and must retain the prior committed name.
	within, err := tx.Client().Provider.Get(txctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, "uncommitted", within.Name)
	require.NoError(t, tx.Rollback())
	after, err := store.GetByID(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, "renamed", after.Record.Name)
	require.Equal(t, 2.5, after.View().GetQuotaUsed())
}
