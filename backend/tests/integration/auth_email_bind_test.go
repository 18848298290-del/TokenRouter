package integration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/authidentity"
	"github.com/TokenFlux/TokenRouter/ent/enttest"
	dbuser "github.com/TokenFlux/TokenRouter/ent/user"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
)

// Binding a first nonreserved mailbox can change credentials and apply provider
// grants together, while replacing an existing mailbox requires its current
// password and the email-change switch. SQLite scenarios exercise the identity
// service and PostgreSQL adapter for alias ownership, suffix policy and
// rollback when subscription assignment fails. The separate token scenario
// supplies no Ent client and observes credential-based renewal rejection
// through memory repositories.
//
// Refresh credentials and their user/family indexes are coordinated under one
// fixture mutex, and account ID/email lookups share a second indexed store.
// Copies isolate outer structs while retaining nested pointer and collection
// fields. These fixtures discard TTL behavior and provide several no-op
// interface completions, so the assertions describe selected binding and
// revocation cases rather than the production Redis failure or expiration
// model.

// emailBindDefaultSubAssignerStub records subscription requests made during
// successful first-mailbox binding.
type emailBindDefaultSubAssignerStub struct {
	// calls owns shallow input copies in invocation order; quota and order
	// pointers still reference the original request values retained by the
	// scenario.
	calls []*billing.AssignSubscriptionInput // saved grant inputs
}

// flakyEmailBindDefaultSubAssignerStub records subscription inputs and returns
// its configured assignment error.
type flakyEmailBindDefaultSubAssignerStub struct {
	// err is returned after calls receives a shallow copy of the requested
	// assignment. The rollback test retains this observed call even when
	// its database writes fail.
	err   error                              // injected grant error
	calls []*billing.AssignSubscriptionInput // saved grant inputs
}

// emailBindSettingRepoStub supplies fixed scalar and selected-key settings
// reads for binding scenarios.
type emailBindSettingRepoStub struct {
	// values retains the scenario's settings map and is read by runtime
	// settings; callers keep the supplied map stable during service
	// operations.
	values map[string]string // configured mailbox binding settings
}

// emailBindCacheStub supplies one shared challenge and records
// verification-code destinations in binding tests.
type emailBindCacheStub struct {
	// data and err select the fixed challenge read, while setEmails records
	// requested storage destinations. SetVerificationCode ignores
	// replacement data and TTL, and deletion leaves the fixed pointer
	// available.
	data *identity.VerificationCodeData // shared fixed challenge data
	// err takes precedence over data whenever nonnil, making every
	// challenge read return that injected failure regardless of the
	// requested mailbox.
	err       error    // injected challenge read error
	setEmails []string // observed challenge storage destinations
}

// emailBindRefreshTokenCacheStub coordinates stored refresh credentials and
// reverse indexes for the revocation scenario.
type emailBindRefreshTokenCacheStub struct {
	// mu guards tokens, userSets and families across fixture reads and
	// writes. Credential deletion and consumption update the selected
	// indexes within that same critical section; TTL is not applied to any
	// map.
	mu       sync.Mutex                            // token state guard
	tokens   map[string]*identity.RefreshTokenData // saved refresh data
	userSets map[int64]map[string]struct{}         // account token digests
	families map[string]map[string]struct{}        // family token digests
}

// emailBindUserRepoStub stores one account through shared ID and email indexes
// for token renewal.
type emailBindUserRepoStub struct {
	// mu guards usersByID and usersByEmail, which point to the same stored
	// account. Indexed replacement removes the old email before publishing
	// the copied account to both lookups.
	mu           sync.Mutex                // account lookup guard
	usersByID    map[int64]*identity.User  // account ID lookup
	usersByEmail map[string]*identity.User // stored account by exact email
}

// AssignOrExtendSubscription records a shallow nonnil input copy and returns a
// successful user/plan result with a false flag. Quota and order pointers
// remain shared, so tests keep the referenced input values stable.
func (s *emailBindDefaultSubAssignerStub) AssignOrExtendSubscription(
	_ context.Context,
	input *billing.AssignSubscriptionInput,
) (*billing.UserSubscription, bool, error) {
	cloned := *input
	s.calls = append(s.calls, &cloned)
	return &billing.UserSubscription{UserID: input.UserID, PlanID: input.PlanID}, false, nil
}

// AssignOrExtendSubscription records a shallow nonnil input copy before
// returning the configured error and nil subscription. The rollback scenario
// supplies a nonnil error; recorded attempts survive the database transaction
// rollback.
func (s *flakyEmailBindDefaultSubAssignerStub) AssignOrExtendSubscription(
	_ context.Context,
	input *billing.AssignSubscriptionInput,
) (*billing.UserSubscription, bool, error) {
	cloned := *input
	s.calls = append(s.calls, &cloned)
	return nil, false, s.err
}

// newAuthServiceForEmailBind constructs the SQLite-backed binding fixture with
// the supplied settings, challenge cache and subscription assigner.
// Refresh-token caching is omitted here; use the companion constructor when the
// scenario needs that dependency.
func newAuthServiceForEmailBind(
	t *testing.T,
	settings map[string]string,
	emailCache identity.EmailCache,
	defaultSubAssigner identity.DefaultSubscriptionAssigner,
) (*identity.AuthService, identity.UserRepository, *dbent.Client) {
	return newAuthServiceForEmailBindWithRefreshCache(t, settings, emailCache, defaultSubAssigner, nil)
}

// newAuthServiceForEmailBindWithRefreshCache creates a timestamp-named
// shared-memory SQLite fixture and binds identity to its PostgreSQL adapter.
// Test cleanup closes Ent before the SQL pool; supplied caches and assigner
// remain shared with the caller.
func newAuthServiceForEmailBindWithRefreshCache(
	t *testing.T,
	settings map[string]string,
	emailCache identity.EmailCache,
	defaultSubAssigner identity.DefaultSubscriptionAssigner,
	refreshTokenCache identity.RefreshTokenCache,
) (*identity.AuthService, identity.UserRepository, *dbent.Client) {
	t.Helper()

	dbName := fmt.Sprintf("file:auth_service_email_bind_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := sql.Open("sqlite", dbName)
	require.NoError(t, err)
	// Register SQL-pool cleanup first so the later Ent cleanup runs before
	// it; both close errors are discarded after the binding scenario
	// assertions.
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	// Create the grant-claim table outside Ent schema setup so
	// first-binding defaults can record their user/provider/reason
	// uniqueness marker in the SQLite fixture.
	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS user_provider_default_grants (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER NOT NULL,
	provider_type TEXT NOT NULL,
	grant_reason TEXT NOT NULL DEFAULT 'first_bind',
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE(user_id, provider_type, grant_reason)
)`)
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	// Close Ent before the earlier pool cleanup releases the shared-memory
	// database, keeping the fixture available throughout service calls and
	// final stored-state assertions.
	t.Cleanup(func() { _ = client.Close() })

	repo := identitypostgres.NewUserStore(client, db)
	options := &identity.AuthOptions{
		JWT: identity.SessionOptions{
			Secret:     "test-bind-email-secret",
			ExpireHour: 1,
		},
	}
	options.Default.UserBalance = 3.5
	options.Default.UserConcurrency = 2

	settingRepo := &emailBindSettingRepoStub{values: settings}
	svc := newIdentityAuthForTest(client, repo, options, settingRepo, emailCache, refreshTokenCache, defaultSubAssigner)
	return svc, repo, client
}

// TestAuthServiceBindEmailIdentity_UpdatesEmailAndAppliesFirstBindDefaults
// binds a normalized mailbox to an account with a synthetic email. It requires
// new password credentials, additive balance and concurrency grants, one owned
// identity, one subscription call and one grant marker.
func TestAuthServiceBindEmailIdentity_UpdatesEmailAndAppliesFirstBindDefaults(t *testing.T) {
	assigner := &emailBindDefaultSubAssignerStub{}
	cache := &emailBindCacheStub{
		data: &identity.VerificationCodeData{
			Code:      "123456",
			CreatedAt: time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
		},
	}
	svc, _, client := newAuthServiceForEmailBind(t, map[string]string{
		identity.SettingKeyAuthSourceDefaultEmailBalance:          "8.5",
		identity.SettingKeyAuthSourceDefaultEmailConcurrency:      "4",
		identity.SettingKeyAuthSourceDefaultEmailSubscriptions:    `[{"plan_id":11}]`,
		identity.SettingKeyAuthSourceDefaultEmailGrantOnFirstBind: "true",
	}, cache, assigner)

	ctx := context.Background()
	user, err := client.User.Create().
		SetEmail("legacy-user" + identity.LinuxDoConnectSyntheticEmailDomain).
		SetUsername("legacy-user").
		SetPasswordHash("old-hash").
		SetBalance(2.5).
		SetConcurrency(1).
		SetRole(identity.RoleUser).
		SetStatus(identity.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	updatedUser, err := svc.BindEmailIdentity(ctx, user.ID, "  NewEmail@Example.com  ", "123456", "new-password")
	require.NoError(t, err)
	require.NotNil(t, updatedUser)
	require.Equal(t, "newemail@example.com", updatedUser.Email)

	storedUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, "newemail@example.com", storedUser.Email)
	require.Equal(t, 11.0, storedUser.Balance)
	require.Equal(t, 5, storedUser.Concurrency)
	require.True(t, svc.CheckPassword("new-password", storedUser.PasswordHash))

	identityCount, err := client.AuthIdentity.Query().
		Where(
			authidentity.UserIDEQ(user.ID),
			authidentity.ProviderTypeEQ("email"),
			authidentity.ProviderKeyEQ("email"),
			authidentity.ProviderSubjectEQ("newemail@example.com"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, identityCount)

	require.Len(t, assigner.calls, 1)
	require.Equal(t, user.ID, assigner.calls[0].UserID)
	require.Equal(t, int64(11), assigner.calls[0].PlanID)
	require.Equal(t, 1, countProviderGrantRecords(t, client, user.ID, "email", "first_bind"))
}

// TestAuthServiceBindEmailIdentity_RejectsExistingEmailOnAnotherUser targets a
// mailbox already stored on a different account. Binding returns ErrEmailExists
// and nil user, while the source email and absent first-bind marker remain
// unchanged.
func TestAuthServiceBindEmailIdentity_RejectsExistingEmailOnAnotherUser(t *testing.T) {
	cache := &emailBindCacheStub{
		data: &identity.VerificationCodeData{
			Code:      "123456",
			CreatedAt: time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
		},
	}
	svc, _, client := newAuthServiceForEmailBind(t, nil, cache, nil)

	ctx := context.Background()
	sourceUser, err := client.User.Create().
		SetEmail("source-user" + identity.OIDCConnectSyntheticEmailDomain).
		SetUsername("source-user").
		SetPasswordHash("old-hash").
		SetBalance(1).
		SetConcurrency(1).
		SetRole(identity.RoleUser).
		SetStatus(identity.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.User.Create().
		SetEmail("taken@example.com").
		SetUsername("taken-user").
		SetPasswordHash("hash").
		SetBalance(1).
		SetConcurrency(1).
		SetRole(identity.RoleUser).
		SetStatus(identity.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	updatedUser, err := svc.BindEmailIdentity(ctx, sourceUser.ID, "taken@example.com", "123456", "new-password")
	require.ErrorIs(t, err, identity.ErrEmailExists)
	require.Nil(t, updatedUser)

	storedUser, err := client.User.Get(ctx, sourceUser.ID)
	require.NoError(t, err)
	require.Equal(t, "source-user"+identity.OIDCConnectSyntheticEmailDomain, storedUser.Email)
	require.Equal(t, 0, countProviderGrantRecords(t, client, sourceUser.ID, "email", "first_bind"))
}

// TestAuthServiceBindEmailIdentity_RejectsBoundEmailChangeWhenDisabled starts
// with a real mailbox and an unset change switch. Sending and binding both
// return ErrEmailChangeDisabled, with no code destination recorded and the
// original email retained.
func TestAuthServiceBindEmailIdentity_RejectsBoundEmailChangeWhenDisabled(t *testing.T) {
	cache := &emailBindCacheStub{
		data: &identity.VerificationCodeData{
			Code:      "123456",
			CreatedAt: time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
		},
	}
	svc, _, client := newAuthServiceForEmailBind(t, nil, cache, nil)

	ctx := context.Background()
	hashedPassword, err := svc.HashPassword("current-password")
	require.NoError(t, err)
	user := createEmailBindTestUser(t, client, "current@example.com", "bound-user", hashedPassword)

	err = svc.SendEmailIdentityBindCode(ctx, user.ID, "new@example.com")
	require.ErrorIs(t, err, identity.ErrEmailChangeDisabled)
	require.Empty(t, cache.setEmails)

	updatedUser, err := svc.BindEmailIdentity(ctx, user.ID, "new@example.com", "123456", "current-password")
	require.ErrorIs(t, err, identity.ErrEmailChangeDisabled)
	require.Nil(t, updatedUser)

	storedUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, "current@example.com", storedUser.Email)
}

// TestAuthServiceBindEmailIdentity_RejectsAliasOfExistingEmailOnAnotherUser
// targets a Gmail dot/plus alias owned by another account. Sending and binding
// reject the conflict, leaving the source email and password hash unchanged.
func TestAuthServiceBindEmailIdentity_RejectsAliasOfExistingEmailOnAnotherUser(t *testing.T) {
	cache := &emailBindCacheStub{
		data: &identity.VerificationCodeData{
			Code:      "123456",
			CreatedAt: time.Now().UTC().Add(-10 * time.Minute),
			ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
		},
	}
	svc, _, client := newAuthServiceForEmailBind(t, nil, cache, nil)

	ctx := context.Background()
	sourceUser := createEmailBindTestUser(
		t,
		client,
		"source-user"+identity.OIDCConnectSyntheticEmailDomain,
		"source-user",
		"old-hash",
	)
	createEmailBindTestUser(t, client, "zck.ioio123@gmail.com", "inbox-owner", "hash")

	err := svc.SendEmailIdentityBindCode(ctx, sourceUser.ID, "zckioio123+new@gmail.com")
	require.ErrorIs(t, err, identity.ErrEmailExists)
	require.Empty(t, cache.setEmails)

	updatedUser, err := svc.BindEmailIdentity(
		ctx,
		sourceUser.ID,
		"zckioio123+new@gmail.com",
		"123456",
		"new-password",
	)
	require.ErrorIs(t, err, identity.ErrEmailExists)
	require.Nil(t, updatedUser)

	storedUser, err := client.User.Get(ctx, sourceUser.ID)
	require.NoError(t, err)
	require.Equal(t, "source-user"+identity.OIDCConnectSyntheticEmailDomain, storedUser.Email)
	require.Equal(t, "old-hash", storedUser.PasswordHash)
}

// TestAuthServiceBindEmailIdentity_AllowsOnlyOneConcurrentAliasVariant starts
// two SQLite binding calls for different plus aliases of one inbox. Exactly one
// succeeds, one reports ErrEmailExists, and only one target email is stored.
func TestAuthServiceBindEmailIdentity_AllowsOnlyOneConcurrentAliasVariant(t *testing.T) {
	cache := &emailBindCacheStub{
		data: &identity.VerificationCodeData{
			Code:      "123456",
			CreatedAt: time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
		},
	}
	svc, _, client := newAuthServiceForEmailBind(t, nil, cache, nil)

	ctx := context.Background()
	unique := fmt.Sprintf("%d", time.Now().UnixNano())
	first := createEmailBindTestUser(
		t,
		client,
		"first-"+unique+identity.OIDCConnectSyntheticEmailDomain,
		"first-"+unique,
		"old-hash",
	)
	second := createEmailBindTestUser(
		t,
		client,
		"second-"+unique+identity.OIDCConnectSyntheticEmailDomain,
		"second-"+unique,
		"old-hash",
	)

	// Release both binding calls through one start channel, then collect
	// two buffered outcomes before inspecting storage so neither worker is
	// left awaiting a result receiver.
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, err := svc.BindEmailIdentity(ctx, first.ID, "inbox-"+unique+"+one@gmail.com", "123456", "new-password")
		results <- err
	}()
	go func() {
		<-start
		_, err := svc.BindEmailIdentity(ctx, second.ID, "inbox-"+unique+"+two@gmail.com", "123456", "new-password")
		results <- err
	}()
	// Opening the shared start gate lets both aliases compete for the same
	// inbox; the following receive loop joins both attempts before
	// asserting the winning stored mailbox.
	close(start)

	var successes, conflicts int
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, identity.ErrEmailExists):
			conflicts++
		default:
			t.Fatalf("unexpected bind error: %v", err)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)

	boundCount, err := client.User.Query().
		Where(dbuser.EmailIn(
			"inbox-"+unique+"+one@gmail.com",
			"inbox-"+unique+"+two@gmail.com",
		)).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, boundCount)
}

// TestAuthServiceBindEmailIdentity_RejectsNewAliasWhenAnotherUserSharesCurrentUserInbox
// seeds two legacy addresses sharing one Gmail inbox. Even with email change
// enabled, the current owner cannot bind another alias while the other account
// occupies that inbox.
func TestAuthServiceBindEmailIdentity_RejectsNewAliasWhenAnotherUserSharesCurrentUserInbox(t *testing.T) {
	cache := &emailBindCacheStub{
		data: &identity.VerificationCodeData{
			Code:      "123456",
			CreatedAt: time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
		},
	}
	svc, _, client := newAuthServiceForEmailBind(t, map[string]string{
		identity.SettingKeyUserEmailChangeEnabled: "true",
	}, cache, nil)

	ctx := context.Background()
	hashedPassword, err := svc.HashPassword("current-password")
	require.NoError(t, err)
	currentUser := createEmailBindTestUser(t, client, "inbox+own@gmail.com", "current", hashedPassword)
	createEmailBindTestUser(t, client, "inbox+legacy@gmail.com", "legacy", "hash")

	updatedUser, err := svc.BindEmailIdentity(
		ctx,
		currentUser.ID,
		"inbox+new@gmail.com",
		"123456",
		"current-password",
	)
	require.ErrorIs(t, err, identity.ErrEmailExists)
	require.Nil(t, updatedUser)

	storedUser, err := client.User.Get(ctx, currentUser.ID)
	require.NoError(t, err)
	require.Equal(t, "inbox+own@gmail.com", storedUser.Email)
}

// TestAuthServiceBindEmailIdentity_RollsBackWhenFirstBindDefaultsFail injects a
// subscription assignment error during first-mailbox binding. Credentials,
// balance, concurrency, identity and grant marker stay unchanged, while the
// assigner records the attempted external fixture call.
func TestAuthServiceBindEmailIdentity_RollsBackWhenFirstBindDefaultsFail(t *testing.T) {
	assigner := &flakyEmailBindDefaultSubAssignerStub{err: errors.New("temporary assign failure")}
	cache := &emailBindCacheStub{
		data: &identity.VerificationCodeData{
			Code:      "123456",
			CreatedAt: time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
		},
	}
	svc, _, client := newAuthServiceForEmailBind(t, map[string]string{
		identity.SettingKeyAuthSourceDefaultEmailBalance:          "8.5",
		identity.SettingKeyAuthSourceDefaultEmailConcurrency:      "4",
		identity.SettingKeyAuthSourceDefaultEmailSubscriptions:    `[{"plan_id":11}]`,
		identity.SettingKeyAuthSourceDefaultEmailGrantOnFirstBind: "true",
	}, cache, assigner)

	ctx := context.Background()
	originalEmail := "legacy-rollback" + identity.LinuxDoConnectSyntheticEmailDomain
	user, err := client.User.Create().
		SetEmail(originalEmail).
		SetUsername("legacy-rollback").
		SetPasswordHash("old-hash").
		SetBalance(2.5).
		SetConcurrency(1).
		SetRole(identity.RoleUser).
		SetStatus(identity.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	// Inject the subscription failure after credentials and first-bind
	// writes have begun, then reload each persisted component to observe
	// the adapter transaction's rollback in this SQLite scenario.
	updatedUser, err := svc.BindEmailIdentity(ctx, user.ID, "rollback@example.com", "123456", "new-password")
	require.ErrorContains(t, err, "apply email first bind defaults")
	require.ErrorContains(t, err, "temporary assign failure")
	require.Nil(t, updatedUser)

	storedUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, originalEmail, storedUser.Email)
	require.Equal(t, "old-hash", storedUser.PasswordHash)
	require.Equal(t, 2.5, storedUser.Balance)
	require.Equal(t, 1, storedUser.Concurrency)

	identityCount, err := client.AuthIdentity.Query().
		Where(
			authidentity.UserIDEQ(user.ID),
			authidentity.ProviderTypeEQ("email"),
			authidentity.ProviderKeyEQ("email"),
			authidentity.ProviderSubjectEQ("rollback@example.com"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, identityCount)

	require.Len(t, assigner.calls, 1)
	require.Equal(t, 0, countProviderGrantRecords(t, client, user.ID, "email", "first_bind"))
}

// TestAuthServiceBindEmailIdentity_RejectsReservedEmail submits a synthetic
// LinuxDo address as the desired destination mailbox. The service must return
// ErrEmailReserved and no updated user before that address can be bound.
func TestAuthServiceBindEmailIdentity_RejectsReservedEmail(t *testing.T) {
	cache := &emailBindCacheStub{
		data: &identity.VerificationCodeData{
			Code:      "123456",
			CreatedAt: time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
		},
	}
	svc, _, client := newAuthServiceForEmailBind(t, nil, cache, nil)

	ctx := context.Background()
	user, err := client.User.Create().
		SetEmail("source-user@example.com").
		SetUsername("source-user").
		SetPasswordHash("old-hash").
		SetBalance(1).
		SetConcurrency(1).
		SetRole(identity.RoleUser).
		SetStatus(identity.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	updatedUser, err := svc.BindEmailIdentity(ctx, user.ID, "reserved"+identity.LinuxDoConnectSyntheticEmailDomain, "123456", "new-password")
	require.ErrorIs(t, err, identity.ErrEmailReserved)
	require.Nil(t, updatedUser)
}

// TestAuthServiceBindEmailIdentity_ReplacesBoundEmailAndSkipsFirstBindDefaults
// enables replacement and verifies the current password on a bound account. The
// new identity replaces the old subject, while balance, concurrency,
// subscription calls and grant-marker count remain unchanged.
func TestAuthServiceBindEmailIdentity_ReplacesBoundEmailAndSkipsFirstBindDefaults(t *testing.T) {
	assigner := &emailBindDefaultSubAssignerStub{}
	cache := &emailBindCacheStub{
		data: &identity.VerificationCodeData{
			Code:      "123456",
			CreatedAt: time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
		},
	}
	svc, _, client := newAuthServiceForEmailBind(t, map[string]string{
		identity.SettingKeyAuthSourceDefaultEmailBalance:          "8.5",
		identity.SettingKeyAuthSourceDefaultEmailConcurrency:      "4",
		identity.SettingKeyAuthSourceDefaultEmailSubscriptions:    `[{"plan_id":11}]`,
		identity.SettingKeyAuthSourceDefaultEmailGrantOnFirstBind: "true",
		identity.SettingKeyUserEmailChangeEnabled:                 "true",
	}, cache, assigner)

	ctx := context.Background()
	hashedPassword, err := svc.HashPassword("current-password")
	require.NoError(t, err)

	user, err := client.User.Create().
		SetEmail("current@example.com").
		SetUsername("bound-user").
		SetPasswordHash(hashedPassword).
		SetBalance(7.5).
		SetConcurrency(3).
		SetRole(identity.RoleUser).
		SetStatus(identity.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	require.NoError(t, client.AuthIdentity.Create().
		SetUserID(user.ID).
		SetProviderType("email").
		SetProviderKey("email").
		SetProviderSubject("current@example.com").
		SetVerifiedAt(time.Now().UTC()).
		SetMetadata(map[string]any{"source": "test"}).
		Exec(ctx))

	updatedUser, err := svc.BindEmailIdentity(ctx, user.ID, "new@example.com", "123456", "current-password")
	require.NoError(t, err)
	require.NotNil(t, updatedUser)
	require.Equal(t, "new@example.com", updatedUser.Email)

	storedUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, "new@example.com", storedUser.Email)
	require.Equal(t, 7.5, storedUser.Balance)
	require.Equal(t, 3, storedUser.Concurrency)
	require.True(t, svc.CheckPassword("current-password", storedUser.PasswordHash))

	newIdentityCount, err := client.AuthIdentity.Query().
		Where(
			authidentity.UserIDEQ(user.ID),
			authidentity.ProviderTypeEQ("email"),
			authidentity.ProviderKeyEQ("email"),
			authidentity.ProviderSubjectEQ("new@example.com"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, newIdentityCount)

	oldIdentityCount, err := client.AuthIdentity.Query().
		Where(
			authidentity.UserIDEQ(user.ID),
			authidentity.ProviderTypeEQ("email"),
			authidentity.ProviderKeyEQ("email"),
			authidentity.ProviderSubjectEQ("current@example.com"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, oldIdentityCount)

	require.Empty(t, assigner.calls)
	require.Equal(t, 0, countProviderGrantRecords(t, client, user.ID, "email", "first_bind"))
}

// TestAuthServiceBindEmailIdentity_RejectsWrongCurrentPasswordForBoundEmail
// enables replacement but submits the wrong existing password. It requires
// ErrPasswordIncorrect, retained credentials and the old identity, with no
// identity for the requested new mailbox.
func TestAuthServiceBindEmailIdentity_RejectsWrongCurrentPasswordForBoundEmail(t *testing.T) {
	cache := &emailBindCacheStub{
		data: &identity.VerificationCodeData{
			Code:      "123456",
			CreatedAt: time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
		},
	}
	svc, _, client := newAuthServiceForEmailBind(t, map[string]string{
		identity.SettingKeyUserEmailChangeEnabled: "true",
	}, cache, nil)

	ctx := context.Background()
	hashedPassword, err := svc.HashPassword("current-password")
	require.NoError(t, err)

	user, err := client.User.Create().
		SetEmail("current@example.com").
		SetUsername("bound-user").
		SetPasswordHash(hashedPassword).
		SetBalance(1).
		SetConcurrency(1).
		SetRole(identity.RoleUser).
		SetStatus(identity.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	require.NoError(t, client.AuthIdentity.Create().
		SetUserID(user.ID).
		SetProviderType("email").
		SetProviderKey("email").
		SetProviderSubject("current@example.com").
		SetVerifiedAt(time.Now().UTC()).
		SetMetadata(map[string]any{"source": "test"}).
		Exec(ctx))

	updatedUser, err := svc.BindEmailIdentity(ctx, user.ID, "new@example.com", "123456", "wrong-password")
	require.ErrorIs(t, err, identity.ErrPasswordIncorrect)
	require.Nil(t, updatedUser)

	storedUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, "current@example.com", storedUser.Email)
	require.True(t, svc.CheckPassword("current-password", storedUser.PasswordHash))

	oldIdentityCount, err := client.AuthIdentity.Query().
		Where(
			authidentity.UserIDEQ(user.ID),
			authidentity.ProviderTypeEQ("email"),
			authidentity.ProviderKeyEQ("email"),
			authidentity.ProviderSubjectEQ("current@example.com"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, oldIdentityCount)

	newIdentityCount, err := client.AuthIdentity.Query().
		Where(
			authidentity.UserIDEQ(user.ID),
			authidentity.ProviderTypeEQ("email"),
			authidentity.ProviderKeyEQ("email"),
			authidentity.ProviderSubjectEQ("new@example.com"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, newIdentityCount)
}

// TestAuthServiceBindEmailIdentity_RevokesExistingAccessAndRefreshTokens signs
// a pair from a partial account snapshot before binding new stored credentials.
// It checks those credentials and rejects both renewal paths afterward;
// pre-bind token usability is not asserted.
func TestAuthServiceBindEmailIdentity_RevokesExistingAccessAndRefreshTokens(t *testing.T) {
	ctx := context.Background()
	cache := &emailBindCacheStub{
		data: &identity.VerificationCodeData{
			Code:      "123456",
			CreatedAt: time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
		},
	}
	refreshTokenCache := newEmailBindRefreshTokenCacheStub()
	userRepo := newEmailBindUserRepoStub(&identity.User{
		ID:           41,
		Email:        "legacy-user" + identity.OIDCConnectSyntheticEmailDomain,
		Username:     "legacy-user",
		PasswordHash: "old-hash",
		Role:         identity.RoleUser,
		Status:       identity.StatusActive,
		TokenVersion: 4,
	})
	options := &identity.AuthOptions{
		JWT: identity.SessionOptions{
			Secret:                   "test-bind-email-secret",
			ExpireHour:               1,
			AccessTokenExpireMinutes: 60,
			RefreshTokenExpireDays:   7,
		},
	}
	svc := newIdentityAuthForTest(nil, userRepo, options, nil, cache, refreshTokenCache, nil)

	// The signing snapshot omits the seeded account's old password hash.
	// ResolvedTokenVersion includes that hash, so the later rejection
	// checks do not establish that this pair was usable before binding.
	oldTokenPair, err := svc.GenerateTokenPair(ctx, &identity.User{
		ID:           41,
		Email:        "legacy-user" + identity.OIDCConnectSyntheticEmailDomain,
		Role:         identity.RoleUser,
		Status:       identity.StatusActive,
		TokenVersion: 4,
	}, "")
	require.NoError(t, err)

	updatedUser, err := svc.BindEmailIdentity(ctx, 41, "new@example.com", "123456", "new-password")
	require.NoError(t, err)
	require.NotNil(t, updatedUser)

	storedUser, err := userRepo.GetByID(ctx, 41)
	require.NoError(t, err)
	require.Equal(t, "new@example.com", storedUser.Email)
	require.True(t, svc.CheckPassword("new-password", storedUser.PasswordHash))

	_, err = svc.RefreshToken(ctx, oldTokenPair.AccessToken)
	require.ErrorIs(t, err, identity.ErrTokenRevoked)

	_, err = svc.RefreshTokenPair(ctx, oldTokenPair.RefreshToken)
	require.True(t, errors.Is(err, identity.ErrTokenRevoked) || errors.Is(err, identity.ErrRefreshTokenInvalid))
}

// TestAuthServiceEmailIdentityBinding_RejectsEmailOutsideRegistrationSuffixWhitelist
// configures a QQ-only suffix policy and submits a Gmail mailbox. Sending and
// binding fail with ErrEmailSuffixNotAllowed, record no code destination, and
// retain the source email.
func TestAuthServiceEmailIdentityBinding_RejectsEmailOutsideRegistrationSuffixWhitelist(t *testing.T) {
	ctx := context.Background()
	cache := &emailBindCacheStub{
		data: &identity.VerificationCodeData{
			Code:      "123456",
			CreatedAt: time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
		},
	}
	svc, _, client := newAuthServiceForEmailBind(t, map[string]string{
		identity.SettingKeyRegistrationEmailSuffixWhitelist: `["@qq.com"]`,
	}, cache, nil)

	user := createEmailBindTestUser(t, client, "legacy-user"+identity.OIDCConnectSyntheticEmailDomain, "legacy-user", "old-hash")

	err := svc.SendEmailIdentityBindCode(ctx, user.ID, "intruder@gmail.com")
	require.ErrorIs(t, err, identity.ErrEmailSuffixNotAllowed)
	require.Empty(t, cache.setEmails)

	updatedUser, err := svc.BindEmailIdentity(ctx, user.ID, "intruder@gmail.com", "123456", "new-password")
	require.ErrorIs(t, err, identity.ErrEmailSuffixNotAllowed)
	require.Nil(t, updatedUser)

	storedUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, "legacy-user"+identity.OIDCConnectSyntheticEmailDomain, storedUser.Email)
}

// TestAuthServiceBindEmailIdentity_AllowsEmailInsideRegistrationSuffixWhitelist
// binds a mixed-case QQ mailbox while the suffix policy admits QQ. The returned
// and stored emails must both contain the normalized lowercase address.
func TestAuthServiceBindEmailIdentity_AllowsEmailInsideRegistrationSuffixWhitelist(t *testing.T) {
	ctx := context.Background()
	cache := &emailBindCacheStub{
		data: &identity.VerificationCodeData{
			Code:      "123456",
			CreatedAt: time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
		},
	}
	svc, _, client := newAuthServiceForEmailBind(t, map[string]string{
		identity.SettingKeyRegistrationEmailSuffixWhitelist: `["@qq.com"]`,
	}, cache, nil)

	user := createEmailBindTestUser(t, client, "legacy-qq"+identity.LinuxDoConnectSyntheticEmailDomain, "legacy-qq", "old-hash")

	updatedUser, err := svc.BindEmailIdentity(ctx, user.ID, " Member@QQ.com ", "123456", "new-password")
	require.NoError(t, err)
	require.NotNil(t, updatedUser)
	require.Equal(t, "member@qq.com", updatedUser.Email)

	storedUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, "member@qq.com", storedUser.Email)
}

// TestAuthServiceBindEmailIdentity_RegistrationSuffixWhitelistWildcard admits a
// matching edu.cn subdomain and rejects a Gmail address through separate SQLite
// fixtures. The denied case returns the suffix error and keeps the source
// mailbox unchanged.
func TestAuthServiceBindEmailIdentity_RegistrationSuffixWhitelistWildcard(t *testing.T) {
	ctx := context.Background()

	t.Run("allows wildcard suffix", func(t *testing.T) {
		cache := &emailBindCacheStub{
			data: &identity.VerificationCodeData{
				Code:      "123456",
				CreatedAt: time.Now().UTC(),
				ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
			},
		}
		svc, _, client := newAuthServiceForEmailBind(t, map[string]string{
			identity.SettingKeyRegistrationEmailSuffixWhitelist: `["*.edu.cn"]`,
		}, cache, nil)
		user := createEmailBindTestUser(t, client, "legacy-student"+identity.OIDCConnectSyntheticEmailDomain, "legacy-student", "old-hash")

		updatedUser, err := svc.BindEmailIdentity(ctx, user.ID, "student@cs.edu.cn", "123456", "new-password")
		require.NoError(t, err)
		require.NotNil(t, updatedUser)
		require.Equal(t, "student@cs.edu.cn", updatedUser.Email)
	})

	t.Run("rejects outside wildcard suffix", func(t *testing.T) {
		cache := &emailBindCacheStub{
			data: &identity.VerificationCodeData{
				Code:      "123456",
				CreatedAt: time.Now().UTC(),
				ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
			},
		}
		svc, _, client := newAuthServiceForEmailBind(t, map[string]string{
			identity.SettingKeyRegistrationEmailSuffixWhitelist: `["*.edu.cn"]`,
		}, cache, nil)
		user := createEmailBindTestUser(t, client, "legacy-wildcard"+identity.OIDCConnectSyntheticEmailDomain, "legacy-wildcard", "old-hash")

		updatedUser, err := svc.BindEmailIdentity(ctx, user.ID, "foo@gmail.com", "123456", "new-password")
		require.ErrorIs(t, err, identity.ErrEmailSuffixNotAllowed)
		require.Nil(t, updatedUser)

		storedUser, err := client.User.Get(ctx, user.ID)
		require.NoError(t, err)
		require.Equal(t, "legacy-wildcard"+identity.OIDCConnectSyntheticEmailDomain, storedUser.Email)
	})
}

// TestAuthServiceBindEmailIdentity_AllowsAnyEmailWhenRegistrationSuffixWhitelistEmpty
// supplies an empty suffix list before binding a Gmail address. Binding
// succeeds and returns that mailbox, protecting unrestricted destination policy
// for this configuration.
func TestAuthServiceBindEmailIdentity_AllowsAnyEmailWhenRegistrationSuffixWhitelistEmpty(t *testing.T) {
	ctx := context.Background()
	cache := &emailBindCacheStub{
		data: &identity.VerificationCodeData{
			Code:      "123456",
			CreatedAt: time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
		},
	}
	svc, _, client := newAuthServiceForEmailBind(t, map[string]string{
		identity.SettingKeyRegistrationEmailSuffixWhitelist: "[]",
	}, cache, nil)

	user := createEmailBindTestUser(t, client, "legacy-empty"+identity.LinuxDoConnectSyntheticEmailDomain, "legacy-empty", "old-hash")

	updatedUser, err := svc.BindEmailIdentity(ctx, user.ID, "anyone@gmail.com", "123456", "new-password")
	require.NoError(t, err)
	require.NotNil(t, updatedUser)
	require.Equal(t, "anyone@gmail.com", updatedUser.Email)
}

// createEmailBindTestUser inserts an active regular user with the supplied
// stored password hash and mailbox. Balance and concurrency start at one;
// insert failure stops the owning fixture test.
func createEmailBindTestUser(t *testing.T, client *dbent.Client, email, username, passwordHash string) *dbent.User {
	t.Helper()

	user, err := client.User.Create().
		SetEmail(email).
		SetUsername(username).
		SetPasswordHash(passwordHash).
		SetBalance(1).
		SetConcurrency(1).
		SetRole(identity.RoleUser).
		SetStatus(identity.StatusActive).
		Save(context.Background())
	require.NoError(t, err)
	return user
}

// Get panics for record-oriented setting reads in this fixture. Runtime
// readers use GetValue or GetMultiple to inspect the supplied map; requesting
// a Setting object exposes unsupported test wiring.
func (s *emailBindSettingRepoStub) Get(context.Context, string) (*settingscore.Setting, error) {
	panic("unexpected Get call")
}

// GetValue returns a configured value or ErrSettingNotFound when the key is
// absent. Present empty strings remain successful observations for the binding
// runtime settings readers to interpret.
func (s *emailBindSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", settingscore.ErrSettingNotFound
}

// Set panics when a caller attempts an individual setting write. The
// scenario supplies values before assembly; its authentication readers inspect
// that map through GetValue and GetMultiple during service operations.
func (s *emailBindSettingRepoStub) Set(context.Context, string, string) error {
	panic("unexpected Set call")
}

// GetMultiple builds a new result map from requested keys present in the
// fixture. Missing keys are omitted, keeping absence distinguishable from an
// existing empty setting value.
func (s *emailBindSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if v, ok := s.values[key]; ok {
			out[key] = v
		}
	}
	return out, nil
}

// SetMultiple panics for bulk updates to the scenario's setting map. The
// map is configured before authentication construction and remains readable
// through GetValue and GetMultiple while the service operates.
func (s *emailBindSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	panic("unexpected SetMultiple call")
}

// GetAll panics when a caller requests every setting in this fixture.
// Readers select values by key through GetValue or GetMultiple; bulk enumeration
// exposes a dependency outside those supported operations.
func (s *emailBindSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

// Delete panics for setting removal requests against this read fixture.
// Scenarios keep the supplied map stable during authentication, so a removal
// call exposes a write dependency this fixture does not implement.
func (s *emailBindSettingRepoStub) Delete(context.Context, string) error {
	panic("unexpected Delete call")
}

// GetVerificationCode returns the fixed shared challenge or the configured read
// error for every mailbox. The fixture does not key or expire that pointer, so
// tests supply a stable challenge.
func (s *emailBindCacheStub) GetVerificationCode(context.Context, string) (*identity.VerificationCodeData, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.data, nil
}

// SetVerificationCode records the destination mailbox and reports success while
// ignoring challenge and TTL arguments. Sending-policy assertions inspect that
// list; this fixture does not replace its configured challenge pointer.
func (s *emailBindCacheStub) SetVerificationCode(_ context.Context, email string, _ *identity.VerificationCodeData, _ time.Duration) error {
	s.setEmails = append(s.setEmails, email)
	return nil
}

// DeleteVerificationCode reports success while retaining the fixed challenge
// pointer for every mailbox. Later verification can read that same fixture
// value, so this method does not model challenge consumption.
func (s *emailBindCacheStub) DeleteVerificationCode(context.Context, string) error {
	return nil
}

// GetNotifyVerifyCode returns no notification-email challenge for the binding
// cache fixture.
func (s *emailBindCacheStub) GetNotifyVerifyCode(context.Context, string) (*identity.VerificationCodeData, error) {
	return nil, nil
}

// SetNotifyVerifyCode reports success without storing notification-email
// challenge data in this fixture.
func (s *emailBindCacheStub) SetNotifyVerifyCode(context.Context, string, *identity.VerificationCodeData, time.Duration) error {
	return nil
}

// DeleteNotifyVerifyCode reports success without changing the fixed binding
// challenge fixture.
func (s *emailBindCacheStub) DeleteNotifyVerifyCode(context.Context, string) error {
	return nil
}

// GetPasswordResetToken returns no password-reset credential from the binding
// cache fixture.
func (s *emailBindCacheStub) GetPasswordResetToken(context.Context, string) (*identity.PasswordResetTokenData, error) {
	return nil, nil
}

// SetPasswordResetToken reports success without storing reset credentials in
// this fixture.
func (s *emailBindCacheStub) SetPasswordResetToken(context.Context, string, *identity.PasswordResetTokenData, time.Duration) error {
	return nil
}

// DeletePasswordResetToken reports success without changing reset state in the
// binding fixture.
func (s *emailBindCacheStub) DeletePasswordResetToken(context.Context, string) error {
	return nil
}

// IsPasswordResetEmailInCooldown reports no password-reset cooldown for any
// fixture mailbox.
func (s *emailBindCacheStub) IsPasswordResetEmailInCooldown(context.Context, string) bool {
	return false
}

// SetPasswordResetEmailCooldown reports success without storing a cooldown in
// this fixture.
func (s *emailBindCacheStub) SetPasswordResetEmailCooldown(context.Context, string, time.Duration) error {
	return nil
}

// GetNotifyCodeUserRate reports zero observed notification-code requests for
// any fixture user.
func (s *emailBindCacheStub) GetNotifyCodeUserRate(context.Context, int64) (int64, error) {
	return 0, nil
}

// IncrNotifyCodeUserRate reports zero without maintaining a notification-code
// counter in this fixture.
func (s *emailBindCacheStub) IncrNotifyCodeUserRate(context.Context, int64, time.Duration) (int64, error) {
	return 0, nil
}

// newEmailBindRefreshTokenCacheStub allocates credential and reverse-index maps
// for the binding revocation scenario.
func newEmailBindRefreshTokenCacheStub() *emailBindRefreshTokenCacheStub {
	return &emailBindRefreshTokenCacheStub{
		tokens:   make(map[string]*identity.RefreshTokenData),
		userSets: make(map[int64]map[string]struct{}),
		families: make(map[string]map[string]struct{}),
	}
}

// StoreRefreshToken saves a shallow copy of nonnil metadata under the supplied
// digest while holding the fixture mutex. TTL is ignored and reverse indexes
// are added separately by the session service.
func (s *emailBindRefreshTokenCacheStub) StoreRefreshToken(_ context.Context, tokenHash string, data *identity.RefreshTokenData, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cloned := *data
	s.tokens[tokenHash] = &cloned
	return nil
}

// GetRefreshToken returns a fresh outer metadata value or
// ErrRefreshTokenNotFound for an absent digest. It uses the fixture mutex for
// lookup and copying; cache TTL expiration is not modeled.
func (s *emailBindRefreshTokenCacheStub) GetRefreshToken(_ context.Context, tokenHash string) (*identity.RefreshTokenData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.tokens[tokenHash]
	if !ok {
		return nil, identity.ErrRefreshTokenNotFound
	}
	cloned := *data
	return &cloned, nil
}

// DeleteRefreshToken removes one credential and its membership from every user
// and family set under the fixture mutex. An absent record also succeeds, with
// empty index sets retained for subsequent observations.
//
// The record removal and index cleanup finish before another fixture operation
// can inspect those maps. This fixture eagerly removes memberships as part of
// deletion, so its behavior cannot establish the production Redis adapter's
// treatment of stale index entries or partial cleanup failures.
func (s *emailBindRefreshTokenCacheStub) DeleteRefreshToken(_ context.Context, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// An absent record can still have index memberships. Scan both reverse
	// indexes so deletion by digest also removes those stale fixture
	// candidates.
	delete(s.tokens, tokenHash)
	for _, tokenSet := range s.userSets {
		delete(tokenSet, tokenHash)
	}
	for _, tokenSet := range s.families {
		delete(tokenSet, tokenHash)
	}
	return nil
}

// DeleteUserRefreshTokens removes records named by the selected user index,
// then removes their memberships from family sets and deletes that user index.
// It holds the fixture mutex across this sequence so another fixture operation
// cannot observe partial work.
//
// Records outside the selected membership remain available, and other user sets
// are retained as supplied. The configured revocation scenario uses this
// indexed deletion to remove the issued pair; cache failures and concurrent
// additions in a Redis command sequence are not reproduced.
func (s *emailBindRefreshTokenCacheStub) DeleteUserRefreshTokens(_ context.Context, userID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A credential can be stored before its reverse indexes are added.
	// Reading userSets under this mutex freezes the membership candidates
	// observed by this deletion.
	for tokenHash := range s.userSets[userID] {
		delete(s.tokens, tokenHash)
		for _, tokenSet := range s.families {
			delete(tokenSet, tokenHash)
		}
	}
	delete(s.userSets, userID)
	return nil
}

// DeleteTokenFamily removes credentials named by the selected family, their
// memberships in user sets, and the selected family index. The fixture mutex
// covers the complete sequence, keeping readers from observing its intermediate
// index changes.
//
// Credentials outside that family membership remain available, and other family
// sets are retained as supplied. This synchronous fixture deletion supports the
// selected session scenario without modeling expiry, Redis read/delete failures
// or additions between separate network commands.
func (s *emailBindRefreshTokenCacheStub) DeleteTokenFamily(_ context.Context, familyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Family indexes and credential records are written separately. This
	// locked membership read determines the cleanup candidates even when a
	// corresponding record has already been removed.
	for tokenHash := range s.families[familyID] {
		delete(s.tokens, tokenHash)
		for _, tokenSet := range s.userSets {
			delete(tokenSet, tokenHash)
		}
	}
	delete(s.families, familyID)
	return nil
}

// AddToUserTokenSet creates the selected user index when absent and records the
// supplied digest under the mutex. It ignores TTL and permits membership before
// a corresponding credential exists.
func (s *emailBindRefreshTokenCacheStub) AddToUserTokenSet(_ context.Context, userID int64, tokenHash string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.userSets[userID] == nil {
		s.userSets[userID] = make(map[string]struct{})
	}
	s.userSets[userID][tokenHash] = struct{}{}
	return nil
}

// AddToFamilyTokenSet creates the selected family index when absent and adds
// the digest under the mutex. TTL is ignored, and membership is accepted
// independently of credential record presence.
func (s *emailBindRefreshTokenCacheStub) AddToFamilyTokenSet(_ context.Context, familyID string, tokenHash string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.families[familyID] == nil {
		s.families[familyID] = make(map[string]struct{})
	}
	s.families[familyID][tokenHash] = struct{}{}
	return nil
}

// GetUserTokenHashes copies indexed digests into a new slice under the fixture
// mutex. An absent index yields an empty result, and map iteration leaves the
// returned order unspecified.
func (s *emailBindRefreshTokenCacheStub) GetUserTokenHashes(_ context.Context, userID int64) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tokenSet := s.userSets[userID]
	out := make([]string, 0, len(tokenSet))
	for tokenHash := range tokenSet {
		out = append(out, tokenHash)
	}
	return out, nil
}

// GetFamilyTokenHashes copies the selected family's indexed digests into a new
// slice under the fixture mutex. Missing sets yield empty results, while digest
// order follows unspecified map iteration.
func (s *emailBindRefreshTokenCacheStub) GetFamilyTokenHashes(_ context.Context, familyID string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tokenSet := s.families[familyID]
	out := make([]string, 0, len(tokenSet))
	for tokenHash := range tokenSet {
		out = append(out, tokenHash)
	}
	return out, nil
}

// IsTokenInFamily checks digest membership in the selected family under the
// fixture mutex. Missing sets return false; membership alone does not confirm a
// corresponding credential record remains stored.
func (s *emailBindRefreshTokenCacheStub) IsTokenInFamily(_ context.Context, familyID string, tokenHash string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.families[familyID][tokenHash]
	return ok, nil
}

// newEmailBindUserRepoStub copies the supplied nonnil user and installs that
// same stored copy in both indexes. Nested pointers and collections remain
// shared with the input account for this fixture.
func newEmailBindUserRepoStub(user *identity.User) *emailBindUserRepoStub {
	cloned := cloneEmailBindUser(user)
	return &emailBindUserRepoStub{
		usersByID: map[int64]*identity.User{
			cloned.ID: cloned,
		},
		usersByEmail: map[string]*identity.User{
			cloned.Email: cloned,
		},
	}
}

// Create reports success without adding an account to the preseeded user
// fixture.
func (s *emailBindUserRepoStub) Create(context.Context, *identity.User) error { return nil }

// CreateWithNormalizedEmailGuard delegates to the fixture's no-op Create and
// ignores the normalized address. It provides interface completion for this
// preseeded account rather than registration uniqueness enforcement.
func (s *emailBindUserRepoStub) CreateWithNormalizedEmailGuard(ctx context.Context, user *identity.User, _ string) error {
	return s.Create(ctx, user)
}

// GetByID looks up the account under its ID and returns a shallow user copy.
// Missing IDs return ErrUserNotFound; nested pointers and collections remain
// shared with the stored value.
func (s *emailBindUserRepoStub) GetByID(_ context.Context, id int64) (*identity.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.usersByID[id]
	if !ok {
		return nil, identity.ErrUserNotFound
	}
	return cloneEmailBindUser(user), nil
}

// GetByEmail uses exact stored email keys and returns a shallow user copy.
// Missing addresses return ErrUserNotFound; the fixture does not normalize this
// lookup or copy nested account values.
func (s *emailBindUserRepoStub) GetByEmail(_ context.Context, email string) (*identity.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.usersByEmail[email]
	if !ok {
		return nil, identity.ErrUserNotFound
	}
	return cloneEmailBindUser(user), nil
}

// GetFirstAdmin panics when a caller asks this fixture to select an
// administrator. The preseeded account remains available through GetByID and
// GetByEmail; administrator selection is an unsupported dependency.
func (s *emailBindUserRepoStub) GetFirstAdmin(context.Context) (*identity.User, error) {
	panic("unexpected GetFirstAdmin call")
}

// Update replaces the stored user and email index using a shallow copy of the
// supplied nonnil account. A missing ID returns ErrUserNotFound, while the
// supplied field mask is ignored by this fixture.
//
// The previous email is removed before both indexes receive the same
// replacement pointer, all under the fixture mutex. GetByID and GetByEmail
// therefore observe the completed indexed replacement when they acquire that
// mutex; nested data still shares the supplied account's pointers and
// collections.
func (s *emailBindUserRepoStub) Update(_ context.Context, user *identity.User, _ identity.UserUpdateFields) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.usersByID[user.ID]
	if !ok {
		return identity.ErrUserNotFound
	}
	// After credentials change, the former address would otherwise still
	// resolve to the outdated stored account. Remove that exact lookup
	// before publishing the replacement account copy.
	delete(s.usersByEmail, existing.Email)
	cloned := cloneEmailBindUser(user)
	s.usersByID[user.ID] = cloned
	s.usersByEmail[cloned.Email] = cloned
	return nil
}

// UpdateWithNormalizedEmailGuard delegates indexed replacement to Update and
// ignores the normalized-email argument. It retains Update's full shallow
// replacement behavior, so this fixture supplies no additional
// normalized-address ownership lock.
func (s *emailBindUserRepoStub) UpdateWithNormalizedEmailGuard(ctx context.Context, user *identity.User, _ string, fields identity.UserUpdateFields) error {
	return s.Update(ctx, user, fields)
}

// Delete reports success while retaining the seeded account and both fixture
// indexes.
func (s *emailBindUserRepoStub) Delete(context.Context, int64) error { return nil }

// GetUserAvatar returns no loaded avatar for the binding fixture's seeded
// account.
func (s *emailBindUserRepoStub) GetUserAvatar(context.Context, int64) (*identity.UserAvatar, error) {
	return nil, nil
}

// UpsertUserAvatar panics for avatar-write calls against the seeded
// account fixture. Credential binding uses the ID and email indexes; this
// operation supplies no stored metadata or saved avatar result.
func (s *emailBindUserRepoStub) UpsertUserAvatar(context.Context, int64, identity.UpsertUserAvatarInput) (*identity.UserAvatar, error) {
	panic("unexpected UpsertUserAvatar call")
}

// DeleteUserAvatar panics for avatar-removal calls against the seeded
// account fixture. Credential binding uses the ID and email indexes; this
// operation supplies no account update or success result.
func (s *emailBindUserRepoStub) DeleteUserAvatar(context.Context, int64) error {
	panic("unexpected DeleteUserAvatar call")
}

// List panics for paginated user enumeration against this fixture.
// GetByID and GetByEmail provide its supported account reads; listing calls
// expose an unsupported collection-read dependency before any result is returned.
func (s *emailBindUserRepoStub) List(context.Context, pagination.PaginationParams) ([]identity.User, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}

// ListWithFilters panics for filtered user enumeration against this
// fixture. GetByID and GetByEmail provide targeted account reads; this operation
// supplies no listing result or pagination metadata.
func (s *emailBindUserRepoStub) ListWithFilters(context.Context, pagination.PaginationParams, identity.UserListFilters) ([]identity.User, *pagination.PaginationResult, error) {
	panic("unexpected ListWithFilters call")
}

// GetLatestUsedAtByUserIDs returns an empty request-use timestamp map for this
// account fixture.
func (s *emailBindUserRepoStub) GetLatestUsedAtByUserIDs(context.Context, []int64) (map[int64]*time.Time, error) {
	return map[int64]*time.Time{}, nil
}

// GetLatestUsedAtByUserID returns no observed request-use timestamp for this
// fixture.
func (s *emailBindUserRepoStub) GetLatestUsedAtByUserID(context.Context, int64) (*time.Time, error) {
	return nil, nil
}

// UpdateUserLastActiveAt reports success while leaving the seeded fixture
// account unchanged.
func (s *emailBindUserRepoStub) UpdateUserLastActiveAt(context.Context, int64, time.Time) error {
	return nil
}

// AddBalance applies an unrestricted balance delta to the stored shared account
// under the mutex. Missing IDs return ErrUserNotFound; both indexes retain that
// same object and observe the changed balance.
func (s *emailBindUserRepoStub) AddBalance(_ context.Context, id int64, amount float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.usersByID[id]
	if !ok {
		return identity.ErrUserNotFound
	}
	user.Balance += amount
	return nil
}

// UpdateBalance delegates its amount to AddBalance as an additive delta. It
// does not assign an absolute target balance, and missing accounts retain
// AddBalance's error in this fixture.
func (s *emailBindUserRepoStub) UpdateBalance(ctx context.Context, id int64, amount float64) error {
	return s.AddBalance(ctx, id, amount)
}

// DeductBalance applies the negative amount through AddBalance and returns the
// supplied amount on success. The fixture makes no sufficiency check, so this
// operation can leave a negative balance.
func (s *emailBindUserRepoStub) DeductBalance(ctx context.Context, id int64, amount float64) (float64, error) {
	if err := s.AddBalance(ctx, id, -amount); err != nil {
		return 0, err
	}
	return amount, nil
}

// UpdateConcurrency reports success while leaving the account's fixture
// concurrency value unchanged.
func (s *emailBindUserRepoStub) UpdateConcurrency(context.Context, int64, int) error { return nil }

// BatchSetConcurrency reports zero changed users and leaves fixture concurrency
// values untouched.
func (s *emailBindUserRepoStub) BatchSetConcurrency(context.Context, []int64, int) (int, error) {
	return 0, nil
}

// BatchAddConcurrency reports zero changed users and leaves fixture concurrency
// values untouched.
func (s *emailBindUserRepoStub) BatchAddConcurrency(context.Context, []int64, int) (int, error) {
	return 0, nil
}

// ExistsByEmail checks exact email-index membership while holding the fixture
// mutex. It applies no trimming or normalization, matching the stored key
// representation used by GetByEmail in this fixture.
func (s *emailBindUserRepoStub) ExistsByEmail(_ context.Context, email string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.usersByEmail[email]
	return ok, nil
}

// ExistsByNormalizedEmail compares each stored address after registration
// normalization with the supplied prepared value. The argument itself is not
// normalized here, and the fixture mutex protects the index scan.
func (s *emailBindUserRepoStub) ExistsByNormalizedEmail(_ context.Context, normalizedEmail string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for email := range s.usersByEmail {
		if identity.NormalizeRegistrationEmailAddress(email) == normalizedEmail {
			return true, nil
		}
	}
	return false, nil
}

// AdjustBalance panics for adjustments requiring a BalanceChange result.
// AddBalance supplies the fixture's delta mutation, while this method exposes
// an unsupported accounting call before the stored account can change.
func (s *emailBindUserRepoStub) AdjustBalance(ctx context.Context, id int64, delta float64) (identity.BalanceChange, error) {
	panic("unexpected AdjustBalance call")
}

// SetBalance panics for absolute balance writes requiring a BalanceChange
// result. AddBalance and UpdateBalance both apply deltas in this fixture, so
// callers cannot use them as absolute replacement operations.
func (s *emailBindUserRepoStub) SetBalance(ctx context.Context, id int64, value float64) (identity.BalanceChange, error) {
	panic("unexpected SetBalance call")
}

// LockRegistrationEmail reports success without acquiring a
// registration-address lock in this fixture. The preseeded token scenario uses
// memory credential updates, so it cannot establish storage-side uniqueness
// enforcement.
func (s *emailBindUserRepoStub) LockRegistrationEmail(context.Context, string) error {
	return nil
}

// BatchUpdateLimits reports zero changed users while retaining the seeded
// fixture account limits.
func (s *emailBindUserRepoStub) BatchUpdateLimits(context.Context, []int64, *int, *int) (int, error) {
	return 0, nil
}

// RemoveGroupFromAllowedGroups reports zero changes while retaining fixture
// account group grants.
func (s *emailBindUserRepoStub) RemoveGroupFromAllowedGroups(context.Context, int64) (int64, error) {
	return 0, nil
}

// AddGroupToAllowedGroups reports success without changing the seeded account's
// fixture group grants.
func (s *emailBindUserRepoStub) AddGroupToAllowedGroups(context.Context, int64, int64) error {
	return nil
}

// RemoveGroupFromUserAllowedGroups reports success while retaining the seeded
// account's fixture group grants.
func (s *emailBindUserRepoStub) RemoveGroupFromUserAllowedGroups(context.Context, int64, int64) error {
	return nil
}

// ListUserAuthIdentities returns no identity records for the database-free
// account fixture.
func (s *emailBindUserRepoStub) ListUserAuthIdentities(context.Context, int64) ([]identity.UserAuthIdentityRecord, error) {
	return nil, nil
}

// UnbindUserAuthProvider reports success without changing identities in the
// account fixture.
func (s *emailBindUserRepoStub) UnbindUserAuthProvider(context.Context, int64, string) error {
	return nil
}

// UpdateTotpSecret reports success while retaining all seeded fixture
// authenticator fields.
func (s *emailBindUserRepoStub) UpdateTotpSecret(context.Context, int64, *string) error { return nil }

// EnableTotp reports success while retaining the fixture account's seeded
// authenticator state.
func (s *emailBindUserRepoStub) EnableTotp(context.Context, int64) error { return nil }

// DisableTotp reports success while retaining the fixture account's seeded
// authenticator state.
func (s *emailBindUserRepoStub) DisableTotp(context.Context, int64) error { return nil }

// GetByIDIncludeDeleted delegates to GetByID and returns its shallow fixture
// user copy. Deleted-state filtering is not added here, so any seeded account
// is read through the same index.
func (s *emailBindUserRepoStub) GetByIDIncludeDeleted(ctx context.Context, id int64) (*identity.User, error) {
	return s.GetByID(ctx, id)
}

// cloneEmailBindUser returns a fresh outer user value, or nil for nil input.
// Pointers, slices and maps such as AllowedGroups and GroupRates remain shared
// with the supplied user.
func cloneEmailBindUser(user *identity.User) *identity.User {
	if user == nil {
		return nil
	}
	cloned := *user
	return &cloned
}

// ConsumeRefreshToken reports whether this call removed a stored digest exactly
// once under the fixture mutex. A missing record returns false before index
// work, keeping that result distinct from a successful credential claim.
//
// The winning call deletes the record and every user/family membership before
// releasing the mutex. A later consumer then observes absence and cannot also
// win. Context and cache failures are not simulated; the fixture provides this
// synchronous claim behavior for the issued token-pair scenario.
func (s *emailBindRefreshTokenCacheStub) ConsumeRefreshToken(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tokens[key]; !ok {
		return false, nil
	}
	// Once the digest is claimed, scan every reverse set because fixture
	// membership can be added independently of the saved credential's user
	// and family fields.
	delete(s.tokens, key)
	for _, set := range s.userSets {
		delete(set, key)
	}
	for _, set := range s.families {
		delete(set, key)
	}
	return true, nil
}
