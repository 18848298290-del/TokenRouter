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

// adminResetGroupProjection forwards group reads to the injected repository.
type adminResetGroupProjection struct {
	apikey.GroupRepository
	reader routing.GroupRepository
}

// TestAdminKeyResetAndInvalidGroupAreAtomic checks that a failed request rolls back usage resets.
func TestAdminKeyResetAndInvalidGroupAreAtomic(t *testing.T) {
	ctx := context.Background()
	integrationDB, client := identityDatabase(t)
	users := postgres.NewUserStore(client, integrationDB)
	keys := newKeyStoreFixture(client, integrationDB)
	user := mustCreateUser(t, client, &identity.User{})
	key := mustCreateApiKey(t, client, &apikey.APIKey{UserID: user.ID, Key: "test-reset-" + uuid.NewString()})
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

// TestAdminKeyCombinedWriteRollsBackOnDatabaseFailure checks reset rollback after a database write fails.
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
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf("CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test reject key configuration'; END; $$", name))
	require.NoError(t, err)
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

func (p adminResetGroupProjection) GetByID(ctx context.Context, id int64) (*routing.Group, error) {
	value, err := p.reader.GetByID(ctx, id)
	return apikey.GroupFromRouting(value), err
}
