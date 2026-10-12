//go:build integration

package postgres

// This scenario covers activity writes in provider_states.go and list ordering in provider_queries.go.

import (
	"context"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/ent"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/testutil/postgrescontainer"
)

// TestProviderActivityListAndBatchUpdate checks batch activity updates and ascending and descending ordering.
func TestProviderActivityListAndBatchUpdate(t *testing.T) {
	db := postgrescontainer.New(t)
	client := ent.NewClient(ent.Driver(sql.OpenDB(dialect.Postgres, db)))
	ctx := context.Background()
	store := NewProviderStore(client, db, ProviderStoreOptions{})
	ids := make([]int64, 3)
	for i, name := range []string{"activity-one", "activity-two", "activity-empty"} {
		p, err := client.Provider.Create().SetName(name).SetPlatform("openai").SetType("apikey").SetCredentials(map[string]any{}).Save(ctx)
		require.NoError(t, err)
		ids[i] = p.ID
	}
	old := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	latest := old.Add(time.Minute)
	require.NoError(t, store.BatchUpdateLastUsed(ctx, map[int64]time.Time{ids[0]: old, ids[1]: latest}))
	for _, tc := range []struct {
		order string
		want  []int64
	}{
		{"asc", []int64{ids[0], ids[1], ids[2]}},
		{"desc", []int64{ids[2], ids[1], ids[0]}},
	} {
		rows, _, err := store.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 20, SortBy: "last_used_at", SortOrder: tc.order}, "", "", "", "", 0, "")
		require.NoError(t, err)
		require.Len(t, rows, 3)
		for i, id := range tc.want {
			require.Equal(t, id, rows[i].ID)
		}
	}
	row, err := client.Provider.Get(ctx, ids[1])
	require.NoError(t, err)
	require.True(t, latest.Equal(*row.LastUsedAt))
}
