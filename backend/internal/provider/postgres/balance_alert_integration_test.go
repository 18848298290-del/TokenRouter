//go:build integration

package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/ent"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/testutil/postgrescontainer"
)

// TestBalanceAlertClaims exercises the migrated runtime table using concurrent instances.
// One worker consumes each deadline, stale outcomes cannot overwrite newer rounds,
// and provider timestamps remain unchanged by reminder scheduling and completion.
func TestBalanceAlertClaims(t *testing.T) {
	db := postgrescontainer.New(t)
	ctx := context.Background()
	client := ent.NewClient(ent.Driver(sql.OpenDB(dialect.Postgres, db)))
	c := provider.BalanceAlertConfig{Enabled: true, Threshold: 1000, IntervalMinutes: 60, RecipientEmail: "alerts@example.com"}
	record, err := client.Provider.Create().SetName("wallet").SetPlatform("deepseek").SetType("apikey").SetCredentials(map[string]any{}).SetExtra(map[string]any{provider.UpstreamBalanceAlertExtraKey: c}).Save(ctx)
	require.NoError(t, err)
	store := NewProviderStore(client, db, ProviderStoreOptions{})
	p, err := store.GetByID(ctx, record.ID)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	claims := make(chan *provider.BalanceAlertState, 16)
	failures := make(chan error, 16)
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			state, err := store.ClaimBalanceAlert(ctx, p, "identity", c, now)
			claims <- state
			failures <- err
		})
	}
	group.Wait()
	close(claims)
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	var first *provider.BalanceAlertState
	for state := range claims {
		if state != nil {
			require.Nil(t, first)
			first = state
		}
	}
	require.NotNil(t, first)
	first.ErrorCode = "first"
	require.NoError(t, store.FinishBalanceAlert(ctx, first))
	next, err := store.ClaimBalanceAlert(ctx, p, "identity", c, now.Add(time.Hour))
	require.NoError(t, err)
	require.NotNil(t, next)
	require.Equal(t, first.Round+1, next.Round)
	require.NoError(t, store.FinishBalanceAlert(ctx, first))
	state, err := store.GetBalanceAlertState(ctx, p.ID)
	require.NoError(t, err)
	require.Equal(t, next.Round, state.Round)
	require.Empty(t, state.ErrorCode)
	current, err := store.GetByID(ctx, p.ID)
	require.NoError(t, err)
	require.Equal(t, p.UpdatedAt, current.UpdatedAt)
	require.Equal(t, p.Extra, current.Extra)
	p.UpdatedAt = p.UpdatedAt.Add(time.Second)
	stale, err := store.ClaimBalanceAlert(ctx, p, "changed", c, now.Add(2*time.Hour))
	require.NoError(t, err)
	require.Nil(t, stale)
}
