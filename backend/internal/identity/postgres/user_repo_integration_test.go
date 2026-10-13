//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/apikey"
	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
	"github.com/TokenFlux/TokenRouter/ent/user"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/testutil/postgrescontainer"
)

// PostgreSQL account list queries combine cross-table search with
// current/deleted account selection and activity-based ordering. The fixture
// compares returned IDs and counts against Ent queries, then checks null
// placement, ID tie breaks, pagination, and every supplied five-filter
// intersection.

// TestUserListSearchAndActivityOrder seeds PostgreSQL accounts, keys, activity
// summaries, and attribute/group data. It compares search results and totals,
// activity null/tie ordering with one-row pages, and all five-filter
// intersections.
func TestUserListSearchAndActivityOrder(t *testing.T) {
	db := postgrescontainer.New(t)
	client := ent.NewClient(ent.Driver(sql.OpenDB(dialect.Postgres, db)))
	store := NewUserStore(client, db)
	ctx := context.Background()
	ids := make([]int64, 4)
	for i := range ids {
		err := db.QueryRow(`INSERT INTO users(email,password_hash,username,notes,role,status)
			VALUES($1,'hash',$2,$3,'user','active') RETURNING id`, fmt.Sprintf("user%d@test.local", i), fmt.Sprintf("测试%d", i), `literal %_\ Mixed`).Scan(&ids[i])
		require.NoError(t, err)
	}
	_, err := db.Exec(`INSERT INTO api_keys(user_id,key,name,deleted_at) VALUES
		($1,'search-Mixed-1','one',NULL),($1,'search-Mixed-2','two',NULL),($2,'deleted-Mixed','old',now())`, ids[0], ids[1])
	require.NoError(t, err)
	_, err = db.Exec("UPDATE users SET deleted_at=now() WHERE id=$1", ids[3])
	require.NoError(t, err)
	noSubscriptions := false
	for _, search := range []string{"Mixed", "测试", "%", "_", `\`, "user1", "deleted-Mixed", "absent"} {
		for _, deleted := range []bool{false, true} {
			queryCtx := ctx
			if deleted {
				queryCtx = mixins.SkipSoftDelete(ctx)
			}
			want, err := client.User.Query().Where(user.Or(user.EmailContainsFold(search), user.UsernameContainsFold(search), user.NotesContainsFold(search), user.HasAPIKeysWith(apikey.KeyContainsFold(search)))).Order(ent.Asc(user.FieldID)).IDs(queryCtx)
			require.NoError(t, err)
			got, total, err := store.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 100, SortBy: "id", SortOrder: "asc"}, identity.UserListFilters{Search: search, IncludeDeleted: deleted, IncludeSubscriptions: &noSubscriptions})
			require.NoError(t, err)
			var actual []int64
			for _, u := range got {
				actual = append(actual, u.ID)
			}
			require.EqualValues(t, len(want), total.Total)
			require.Equal(t, want, actual, search)
		}
	}
	// User IDs break activity-time ties; null placement follows the sort direction.
	stamp := time.Now().UTC().Truncate(time.Second)
	_, err = db.Exec("INSERT INTO usage_user_activity(user_id,last_used_at) VALUES($1,$3),($2,$3)", ids[0], ids[1], stamp)
	require.NoError(t, err)
	for _, tc := range []struct {
		order string  // requested activity sort direction
		want  []int64 // expected sorted account identifiers
	}{
		{"asc", []int64{ids[2], ids[0], ids[1]}},
		{"desc", []int64{ids[1], ids[0], ids[2]}},
	} {
		for i, id := range tc.want {
			got, total, err := store.ListWithFilters(ctx, pagination.PaginationParams{Page: i + 1, PageSize: 1, SortBy: "last_used_at", SortOrder: tc.order}, identity.UserListFilters{IncludeSubscriptions: &noSubscriptions})
			require.NoError(t, err)
			require.EqualValues(t, 3, total.Total)
			require.Len(t, got, 1)
			require.Equal(t, id, got[0].ID)
		}
	}
	// Search and all combinations of the five filters must count and page their intersection.
	var groupID, attributeID int64
	require.NoError(t, db.QueryRow("INSERT INTO groups(name) VALUES('activity-filter') RETURNING id").Scan(&groupID))
	require.NoError(t, db.QueryRow("INSERT INTO user_attribute_definitions(key,name,type) VALUES('activity-filter','筛选','text') RETURNING id").Scan(&attributeID))
	_, err = db.Exec("INSERT INTO user_allowed_groups(user_id,group_id) VALUES($1,$2)", ids[0], groupID)
	require.NoError(t, err)
	_, err = db.Exec("UPDATE api_keys SET group_id=$1 WHERE user_id=$2", groupID, ids[0])
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO user_attribute_values(user_id,attribute_id,value) VALUES($1,$2,'match')", ids[0], attributeID)
	require.NoError(t, err)
	_, err = db.Exec("UPDATE users SET role='admin' WHERE id=$1", ids[0])
	require.NoError(t, err)
	_, err = db.Exec("UPDATE users SET status='disabled' WHERE id=$1", ids[1])
	require.NoError(t, err)
	// Five independent filter switches produce thirty-two combinations. Compare
	// each complete intersection with both returned account IDs and the
	// pagination total.
	for mask := range 32 {
		filters := identity.UserListFilters{Search: "Mixed", IncludeSubscriptions: &noSubscriptions}
		if mask&1 != 0 {
			filters.Role = "admin"
		}
		if mask&2 != 0 {
			filters.Status = "disabled"
		}
		if mask&4 != 0 {
			filters.GroupName = "activity-filter"
		}
		if mask&8 != 0 {
			filters.APIKeyGroupID = groupID
		}
		if mask&16 != 0 {
			filters.Attributes = map[int64]string{attributeID: "match"}
		}
		var want, actual []int64
		for i, id := range ids[:3] {
			if mask&2 != 0 && i != 1 {
				continue
			}
			if mask&29 != 0 && i != 0 {
				continue
			}
			want = append(want, id)
		}
		got, total, err := store.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 100, SortBy: "id", SortOrder: "asc"}, filters)
		require.NoError(t, err)
		for _, row := range got {
			actual = append(actual, row.ID)
		}
		require.EqualValues(t, len(want), total.Total)
		require.Equal(t, want, actual, "筛选组合 %d", mask)
	}
}
