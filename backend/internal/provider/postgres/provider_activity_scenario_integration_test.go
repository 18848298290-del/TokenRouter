//go:build integration

package postgres

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

// Provider activity writes affect administrator list order as well as the
// stored last-used timestamp. This PostgreSQL scenario covers
// provider_states.go and provider_queries.go, checking two observed times
// alongside a provider with no activity and confirming the database's
// ascending and descending null placement.

// TestProviderActivityListAndBatchUpdate stores two activity times and leaves
// a third provider unobserved. Ascending order places the null last,
// descending places it first, and a direct row read confirms the latest
// supplied timestamp persisted.
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
	// Whole-second fixtures avoid comparing database precision with subsecond clock
	// observations while keeping distinct activity times for the ordered results.
	old := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	latest := old.Add(time.Minute)
	require.NoError(t, store.BatchUpdateLastUsed(ctx, map[int64]time.Time{ids[0]: old, ids[1]: latest}))
	for _, tc := range []struct {
		// order selects PostgreSQL ordering for last_used_at; want names the full
		// ID sequence, including the null row, expected from that selected direction.
		order string  // database activity sort direction
		want  []int64 // expected ordered provider identifiers
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
