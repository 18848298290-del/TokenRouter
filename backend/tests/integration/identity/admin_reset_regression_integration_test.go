//go:build integration

package identity_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	keypostgres "github.com/TokenFlux/TokenRouter/internal/apikey/postgres"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	groupdto "github.com/TokenFlux/TokenRouter/internal/routing/httpapi/dto"
)

// An administrator request can combine group assignment with spending-window
// reset. PostgreSQL-backed HTTP cases reject an invalid group before its resets
// persist and inject a group-write trigger failure to check that a combined
// update leaves its earlier counters and binding unchanged until successful
// retry.

// adminResetGroupProjection overrides the group read used by administrator
// binding validation.
type adminResetGroupProjection struct {
	// GroupRepository supplies promoted methods other than GetByID. These fixtures
	// leave it nil and exercise the overridden read; calling promoted methods
	// would fail.
	apikey.GroupRepository
	// reader is required for GetByID and remains borrowed from the case's group
	// store, whose database fixture owns its connection lifetime and transaction
	// scope.
	reader routing.GroupRepository
}

// TestAdminKeyResetAndInvalidGroupAreAtomic submits an invalid group with a
// spending-reset request. HTTP returns BadRequest, all three stored counters
// remain unchanged, and the five-hour start remains within one millisecond.
func TestAdminKeyResetAndInvalidGroupAreAtomic(t *testing.T) {
	ctx := context.Background()
	integrationDB, client := identityDatabase(t)
	users := postgres.NewUserStore(client, integrationDB)
	keys := newKeyStoreFixture(client, integrationDB)
	user := mustCreateUser(t, client, &identity.User{})
	key := mustCreateApiKey(t, client, &apikey.APIKey{UserID: user.ID, Key: "test-reset-" + uuid.NewString()})
	// Whole-second input and a millisecond tolerance compare the retained window
	// timestamp.
	now := time.Now().UTC().Truncate(time.Second)
	_, err := integrationDB.ExecContext(ctx, "UPDATE api_keys SET usage_5h=12,usage_1d=13,usage_7d=14,window_5h_start=$2,window_1d_start=$2,window_7d_start=$2 WHERE id=$1", key.ID, now)
	require.NoError(t, err)
	admin := &apikey.Admin{Keys: keys, Users: users, Mutations: &keypostgres.AdminGroupMutations{Client: client, Keys: keys, Users: users, UsersInTx: func(tx *dbent.Tx) keypostgres.GroupAccessWriter { return postgres.GroupAccessInTx(tx) }}}
	h := keyhttp.NewAdminAPIKeyHandler(admin, func(g *routing.Group) *groupdto.Group { return groupdto.GroupFromRouting(apikey.RoutingGroup(g)) })
	router := gin.New()
	router.PUT("/admin/api-keys/:id", h.UpdateGroup)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/admin/api-keys/%d", key.ID), bytes.NewBufferString(`{"group_id":-1,"reset_rate_limit_usage":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	stored, err := client.APIKey.Get(ctx, key.ID)
	require.NoError(t, err)
	require.Equal(t, 12.0, stored.Usage5h, "非法分组失败不能先提交限额重置")
	require.Equal(t, 13.0, stored.Usage1d)
	require.Equal(t, 14.0, stored.Usage7d)
	require.NotNil(t, stored.Window5hStart)
	require.WithinDuration(t, now, *stored.Window5hStart, time.Millisecond)
}

// TestAdminKeyCombinedWriteRollsBackOnDatabaseFailure rejects group changes
// through a PostgreSQL trigger during a combined reset request. Failure retains
// counters and binding; removing the trigger allows the group and reset writes
// to succeed.
func TestAdminKeyCombinedWriteRollsBackOnDatabaseFailure(t *testing.T) {
	ctx := context.Background()
	integrationDB, client := identityDatabase(t)
	users := postgres.NewUserStore(client, integrationDB)
	keys := newKeyStoreFixture(client, integrationDB)
	user := mustCreateUser(t, client, &identity.User{})
	group := mustCreateGroup(t, client, &routing.Group{Name: "test-atomic-group-" + uuid.NewString()})
	key := mustCreateApiKey(t, client, &apikey.APIKey{UserID: user.ID, Key: "test-atomic-update-" + uuid.NewString()})
	_, err := integrationDB.ExecContext(ctx, "UPDATE api_keys SET usage_5h=12,usage_1d=13,usage_7d=14,window_5h_start=NOW(),window_1d_start=NOW(),window_7d_start=NOW() WHERE id=$1", key.ID)
	require.NoError(t, err)
	name := fmt.Sprintf("test_reject_key_%d", key.ID)
	// Reject the selected group mutation after reset processing can begin so
	// outside reads reveal whether the combined write retains a partial reset on
	// failure.
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf("CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test reject key configuration'; END; $$", name))
	require.NoError(t, err)
	// Attempt trigger removal before removing its function on every test exit; IF
	// EXISTS tolerates the successful path's earlier removal, and cleanup errors
	// are discarded.
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DROP TRIGGER IF EXISTS "+name+" ON api_keys")
		_, _ = integrationDB.ExecContext(ctx, "DROP FUNCTION IF EXISTS "+name+"()")
	})
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf("CREATE TRIGGER %s BEFORE UPDATE OF group_id ON api_keys FOR EACH ROW WHEN (OLD.id=%d AND NEW.group_id IS DISTINCT FROM OLD.group_id) EXECUTE FUNCTION %s()", name, key.ID, name))
	require.NoError(t, err)
	groups := newGroupStoreFixture(client, integrationDB)
	admin := &apikey.Admin{Keys: keys, Users: users, Groups: adminResetGroupProjection{reader: groups}, Mutations: &keypostgres.AdminGroupMutations{Client: client, Keys: keys, Users: users, UsersInTx: func(tx *dbent.Tx) keypostgres.GroupAccessWriter { return postgres.GroupAccessInTx(tx) }}}
	h := keyhttp.NewAdminAPIKeyHandler(admin, func(g *routing.Group) *groupdto.Group { return groupdto.GroupFromRouting(apikey.RoutingGroup(g)) })
	router := gin.New()
	router.PUT("/admin/api-keys/:id", h.UpdateGroup)
	send := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/admin/api-keys/%d", key.ID), bytes.NewBufferString(fmt.Sprintf(`{"group_id":%d,"reset_rate_limit_usage":true}`, group.ID)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	require.Equal(t, http.StatusInternalServerError, send().Code)
	stored, err := client.APIKey.Get(ctx, key.ID)
	require.NoError(t, err)
	require.Equal(t, 12.0, stored.Usage5h)
	require.Equal(t, 13.0, stored.Usage1d)
	require.Equal(t, 14.0, stored.Usage7d)
	require.Nil(t, stored.GroupID)
	require.NotNil(t, stored.Window5hStart)
	// Remove the injected database rejection before replaying the same combined
	// request so success can check both new group assignment and cleared spending
	// windows.
	_, err = integrationDB.ExecContext(ctx, "DROP TRIGGER "+name+" ON api_keys")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, send().Code)
	stored, err = client.APIKey.Get(ctx, key.ID)
	require.NoError(t, err)
	require.Zero(t, stored.Usage5h)
	require.Zero(t, stored.Usage1d)
	require.Zero(t, stored.Usage7d)
	require.Nil(t, stored.Window5hStart)
	require.Nil(t, stored.Window1dStart)
	require.Nil(t, stored.Window7dStart)
	require.Equal(t, group.ID, *stored.GroupID)
}

// GetByID delegates to the injected routing reader and clones its result for
// key administration. The reader error returns unchanged, and nil input to
// GroupFromRouting remains a nil group.
func (p adminResetGroupProjection) GetByID(ctx context.Context, id int64) (*routing.Group, error) {
	value, err := p.reader.GetByID(ctx, id)
	return apikey.GroupFromRouting(value), err
}
