//go:build integration

package identity_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	keycore "github.com/TokenFlux/TokenRouter/internal/apikey"
	keypostgres "github.com/TokenFlux/TokenRouter/internal/apikey/postgres"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Administrator user deletion, group replacement, and key grants write identity
// and key records through transaction participants. PostgreSQL fixtures return
// failures after the participant's actual write, then inspect owner state, key
// configuration, and invalidation counts before retrying through a working
// participant.

// adminFailure injects a configured error after delegated administrator key
// writes.
type adminFailure struct {
	// AdminKeyParticipant performs the actual writes in its caller-selected scope;
	// the overrides retain its results before introducing a post-write failure.
	identity.AdminKeyParticipant
	// failure follows a successful delegate write. Nil leaves the delegate result
	// successful, so rollback scenarios configure an error before invoking the
	// participant.
	failure error
}

// keyUpdateFailure delegates key persistence before injecting a configured
// update failure.
type keyUpdateFailure struct {
	// APIKeyRepository supplies the key write using the administrator's
	// transaction context, letting a later injected failure exercise rollback of
	// the actual update.
	keycore.APIKeyRepository
	// failure is returned after successful Update; nil makes that override report
	// success and therefore does not request rollback through the caller's error
	// path.
	failure error
}

// DeleteWithAudit calls the embedded participant before returning the
// configured post-write failure. Delegate errors return directly, and the
// caller's administrator transaction determines whether the completed key write
// is rolled back.
func (p adminFailure) DeleteWithAudit(ctx context.Context, id int64) error {
	if err := p.AdminKeyParticipant.DeleteWithAudit(ctx, id); err != nil {
		return err
	}
	return p.failure
}

// UpdateGroupIDByUserAndGroup performs the delegated key-group migration and
// retains its affected count even on error. After delegate success it returns
// that count with the configured failure for outer rollback testing.
func (p adminFailure) UpdateGroupIDByUserAndGroup(ctx context.Context, id, oldID, newID int64) (int64, error) {
	n, err := p.AdminKeyParticipant.UpdateGroupIDByUserAndGroup(ctx, id, oldID, newID)
	if err != nil {
		return n, err
	}
	return n, p.failure
}

// Update delegates the selected key-field write before returning the configured
// post-write failure. Delegate errors return directly, while the caller's
// transaction owns rollback of any successful write preceding the injected
// error.
func (p keyUpdateFailure) Update(ctx context.Context, key *keycore.APIKey, fields keycore.APIKeyUpdateFields) error {
	if err := p.APIKeyRepository.Update(ctx, key, fields); err != nil {
		return err
	}
	return p.failure
}

// TestAdminDeleteRollsBackKeyParticipant fails after a key tombstone write
// during administrator user deletion. The user, credential, and event count
// remain intact, and retry through a working participant deletes the user.
func TestAdminDeleteRollsBackKeyParticipant(t *testing.T) {
	ctx := context.Background()
	integrationDB, client := identityDatabase(t)
	users := identitypostgres.NewUserStore(client, integrationDB)
	keys := keypostgres.NewKeyStore(client, integrationDB, nil)
	user := mustCreateUser(t, client, &identity.User{})
	key := mustCreateApiKey(t, client, &keycore.APIKey{UserID: user.ID, Key: "test-delete-" + uuid.NewString()})
	failure := errors.New("test after tombstone write")
	mutations := &identitypostgres.AdminMutations{Client: client, Users: users, Keys: keys, KeysInTx: func(tx *dbent.Tx) identity.AdminKeyParticipant {
		return adminFailure{keys.LifecycleInTx(tx), failure}
	}}
	before := outboxCount(t, integrationDB, ctx, key.Key)
	// Fail after the participant's tombstone mutation, then query through the
	// outside client to detect a key or owner write that escaped outer rollback.
	err := mutations.DeleteUserAndKeys(ctx, user.ID, []identity.AdminKeySummary{{ID: key.ID, Key: key.Key}})
	require.ErrorIs(t, err, failure)
	_, err = users.GetByID(ctx, user.ID)
	require.NoError(t, err)
	stored, err := client.APIKey.Get(ctx, key.ID)
	require.NoError(t, err)
	require.Equal(t, key.Key, stored.Key)
	require.Equal(t, before, outboxCount(t, integrationDB, ctx, key.Key), "回滚不能留下成功失效事件")
	mutations.KeysInTx = func(tx *dbent.Tx) identity.AdminKeyParticipant { return keys.LifecycleInTx(tx) }
	require.NoError(t, mutations.DeleteUserAndKeys(ctx, user.ID, []identity.AdminKeySummary{{ID: key.ID, Key: key.Key}}))
	_, err = users.GetByID(ctx, user.ID)
	require.ErrorIs(t, err, identity.ErrUserNotFound)
}

// TestGroupReplacementRollsBackKeyParticipant fails after migrating a user's
// key to another exclusive group. Key binding, user grants, and event count
// roll back, then successful retry replaces the user's grant.
func TestGroupReplacementRollsBackKeyParticipant(t *testing.T) {
	ctx := context.Background()
	integrationDB, client := identityDatabase(t)
	users := identitypostgres.NewUserStore(client, integrationDB)
	keys := keypostgres.NewKeyStore(client, integrationDB, nil)
	old := mustCreateGroup(t, client, &routing.Group{Name: "test-old-" + uuid.NewString(), IsExclusive: true})
	next := mustCreateGroup(t, client, &routing.Group{Name: "test-new-" + uuid.NewString(), IsExclusive: true})
	user := mustCreateUser(t, client, &identity.User{AllowedGroups: []int64{old.ID}})
	key := mustCreateApiKey(t, client, &keycore.APIKey{UserID: user.ID, GroupID: &old.ID, Key: "test-replace-" + uuid.NewString()})
	failure := errors.New("test after group migration")
	mutations := &identitypostgres.AdminMutations{Client: client, Users: users, Keys: keys, KeysInTx: func(tx *dbent.Tx) identity.AdminKeyParticipant {
		return adminFailure{keys.LifecycleInTx(tx), failure}
	}}
	before := outboxCount(t, integrationDB, ctx, key.Key)
	// Run replacement with a post-migration failure before retrying the working
	// participant, exposing any independently committed key, grant, or
	// invalidation event.
	_, err := mutations.ReplaceUserGroup(ctx, user.ID, old.ID, next.ID)
	require.ErrorIs(t, err, failure)
	stored, err := client.APIKey.Get(ctx, key.ID)
	require.NoError(t, err)
	require.Equal(t, old.ID, *stored.GroupID)
	reloaded, err := users.GetByID(ctx, user.ID)
	require.NoError(t, err)
	require.Contains(t, reloaded.AllowedGroups, old.ID)
	require.NotContains(t, reloaded.AllowedGroups, next.ID)
	require.Equal(t, before, outboxCount(t, integrationDB, ctx, key.Key))
	mutations.KeysInTx = func(tx *dbent.Tx) identity.AdminKeyParticipant { return keys.LifecycleInTx(tx) }
	n, err := mutations.ReplaceUserGroup(ctx, user.ID, old.ID, next.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	reloaded, err = users.GetByID(ctx, user.ID)
	require.NoError(t, err)
	require.NotContains(t, reloaded.AllowedGroups, old.ID)
	require.Contains(t, reloaded.AllowedGroups, next.ID)
}

// TestAdminKeyGrantRollsBackIdentityParticipant fails after a key-group write
// following an identity grant. The grant, key binding, and event count roll
// back, while retry through the working repository grants access.
func TestAdminKeyGrantRollsBackIdentityParticipant(t *testing.T) {
	ctx := context.Background()
	integrationDB, client := identityDatabase(t)
	users := identitypostgres.NewUserStore(client, integrationDB)
	keys := keypostgres.NewKeyStore(client, integrationDB, nil)
	group := mustCreateGroup(t, client, &routing.Group{Name: "test-grant-" + uuid.NewString(), IsExclusive: true})
	user := mustCreateUser(t, client, &identity.User{})
	old := mustCreateApiKey(t, client, &keycore.APIKey{UserID: user.ID, Key: "test-grant-" + uuid.NewString()})
	key, err := keys.GetByID(ctx, old.ID)
	require.NoError(t, err)
	key.GroupID = &group.ID
	failure := errors.New("test after key group update")
	mutations := &keypostgres.AdminGroupMutations{Client: client, Users: users, Keys: keyUpdateFailure{keys, failure}, UsersInTx: func(tx *dbent.Tx) keypostgres.GroupAccessWriter { return identitypostgres.GroupAccessInTx(tx) }}
	before := outboxCount(t, integrationDB, ctx, old.Key)
	// Inject failure after key persistence so the earlier identity grant must
	// disappear together with the selected binding and its trigger-generated
	// invalidation event.
	require.ErrorIs(t, mutations.GrantGroupAndUpdateFields(ctx, key, keycore.APIKeyUpdateFields{GroupID: true}, group.ID), failure)
	reloaded, err := users.GetByID(ctx, user.ID)
	require.NoError(t, err)
	require.NotContains(t, reloaded.AllowedGroups, group.ID)
	stored, err := client.APIKey.Get(ctx, old.ID)
	require.NoError(t, err)
	require.Nil(t, stored.GroupID)
	require.Equal(t, before, outboxCount(t, integrationDB, ctx, old.Key))
	mutations.Keys = keys
	require.NoError(t, mutations.GrantGroupAndUpdateFields(ctx, key, keycore.APIKeyUpdateFields{GroupID: true}, group.ID))
	reloaded, err = users.GetByID(ctx, user.ID)
	require.NoError(t, err)
	require.Contains(t, reloaded.AllowedGroups, group.ID)
}

// outboxCount returns authentication invalidation rows matching the hash of the
// supplied credential. Query errors fail the current test before its
// transaction-effect assertions inspect the returned count.
func outboxCount(t *testing.T, integrationDB *sql.DB, ctx context.Context, key string) int {
	t.Helper()
	var n int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_cache_invalidation_outbox WHERE cache_key=$1", keycore.AuthCacheKey(key)).Scan(&n))
	return n
}
