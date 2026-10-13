package integration

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/authidentity"
	"github.com/TokenFlux/TokenRouter/ent/enttest"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
)

// Local email credentials can exist before their auth identity is backfilled.
// SQLite scenarios separate successful-login recording from credential checks,
// and verify that this legacy backfill leaves existing balance, concurrency and
// subscription defaults untouched even when first-binding grants are enabled.

// authIdentityDefaultSubAssignerStub records subscription inputs supplied by
// authentication default-grant checks.
type authIdentityDefaultSubAssignerStub struct {
	// calls owns copied input structs in invocation order; nested quota and
	// order pointers still reference the caller's supplied values for test
	// inspection.
	calls []*billing.AssignSubscriptionInput // saved grant inputs
}

// flakyAuthIdentityDefaultSubAssignerStub records calls and can fail a
// configured number of assignments.
type flakyAuthIdentityDefaultSubAssignerStub struct {
	// failuresRemaining counts calls still scheduled to fail; calls records
	// every invocation before that decision, allowing tests to distinguish
	// an untouched assigner from a failed attempt. Input pointer fields
	// remain shared.
	failuresRemaining int                                // remaining injected assignment failures
	calls             []*billing.AssignSubscriptionInput // saved grant inputs
}

// authIdentitySettingRepoStub supplies configured scalar and bulk reads to
// authentication fixtures.
type authIdentitySettingRepoStub struct {
	// values retains the scenario's supplied settings map; callers keep it
	// stable while runtime readers select scalar values or build bulk
	// results.
	values map[string]string // configured authentication setting values
}

// AssignOrExtendSubscription records a shallow copy of nonnil input and returns
// its user and plan identifiers. Quota and order pointers remain shared with
// the supplied input, so tests keep those values stable.
func (s *authIdentityDefaultSubAssignerStub) AssignOrExtendSubscription(
	_ context.Context,
	input *billing.AssignSubscriptionInput,
) (*billing.UserSubscription, bool, error) {
	cloned := *input
	s.calls = append(s.calls, &cloned)
	return &billing.UserSubscription{UserID: input.UserID, PlanID: input.PlanID}, true, nil
}

// AssignOrExtendSubscription records nonnil input before consuming one
// configured failure, then succeeds after the failure count reaches zero. The
// shallow recorded input retains its quota and order pointers.
func (s *flakyAuthIdentityDefaultSubAssignerStub) AssignOrExtendSubscription(
	_ context.Context,
	input *billing.AssignSubscriptionInput,
) (*billing.UserSubscription, bool, error) {
	cloned := *input
	s.calls = append(s.calls, &cloned)
	// Record the attempted subscription first so a configured temporary
	// failure remains visible; the legacy backfill tests require the entire
	// call list to stay empty.
	if s.failuresRemaining > 0 {
		s.failuresRemaining--
		return nil, false, errors.New("temporary assign failure")
	}
	return &billing.UserSubscription{UserID: input.UserID, PlanID: input.PlanID}, true, nil
}

// Get panics for record-oriented setting reads in this fixture. Runtime
// readers use GetValue or GetMultiple to inspect the supplied map; requesting
// a Setting object exposes unsupported test wiring.
func (s *authIdentitySettingRepoStub) Get(context.Context, string) (*settingscore.Setting, error) {
	panic("unexpected Get call")
}

// GetValue reads the supplied settings map and returns ErrSettingNotFound for
// an absent key. Present empty strings succeed, retaining the distinction used
// by the runtime settings readers.
func (s *authIdentitySettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", settingscore.ErrSettingNotFound
}

// Set panics when a caller attempts an individual setting write. The
// scenario supplies values before assembly; its authentication readers inspect
// that map through GetValue and GetMultiple during service operations.
func (s *authIdentitySettingRepoStub) Set(context.Context, string, string) error {
	panic("unexpected Set call")
}

// GetMultiple returns a new map containing the requested keys found in the
// fixture. Missing keys are omitted, while present empty values remain
// available to each settings reader.
func (s *authIdentitySettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
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
func (s *authIdentitySettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	panic("unexpected SetMultiple call")
}

// GetAll panics when a caller requests every setting in this fixture.
// Readers select values by key through GetValue or GetMultiple; bulk enumeration
// exposes a dependency outside those supported operations.
func (s *authIdentitySettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

// Delete panics for setting removal requests against this read fixture.
// Scenarios keep the supplied map stable during authentication, so a removal
// call exposes a write dependency this fixture does not implement.
func (s *authIdentitySettingRepoStub) Delete(context.Context, string) error {
	panic("unexpected Delete call")
}

// newAuthServiceWithEnt constructs authentication and PostgreSQL adapters on a
// named shared-memory SQLite database. Call it from sequential scenarios;
// cleanup closes Ent before the SQL pool, and the subscription participant is
// supplied by each test.
func newAuthServiceWithEnt(
	t *testing.T,
	settings map[string]string,
	defaultSubAssigner identitycore.DefaultSubscriptionAssigner,
) (*identitycore.AuthService, identitycore.UserRepository, *dbent.Client) {
	t.Helper()

	db, err := sql.Open("sqlite", "file:auth_service_identity_sync?mode=memory&cache=shared")
	require.NoError(t, err)
	// Register pool cleanup before Ent cleanup so later registrations run
	// first; cleanup errors are discarded while the scenario assertions
	// determine the test result.
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	// Ent's SQLite fixture supplies schema entities, while first-bind grant
	// claims use this manually created table and its user/provider/reason
	// uniqueness constraint.
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
	// Close the Ent client before the earlier SQL-pool cleanup, keeping the
	// shared-memory fixture alive until all test-owned adapter work has
	// finished.
	t.Cleanup(func() { _ = client.Close() })

	repo := identitypostgres.NewUserStore(client, db)
	options := &identitycore.AuthOptions{
		JWT: identitycore.SessionOptions{
			Secret:     "test-auth-identity-secret",
			ExpireHour: 1,
		},
	}
	options.Default.UserBalance = 3.5
	options.Default.UserConcurrency = 2
	settingsStore := &authIdentitySettingRepoStub{
		values: settings,
	}

	svc := newIdentityAuthForTest(client, repo, options, settingsStore, nil, nil, defaultSubAssigner)
	return svc, repo, client
}

// TestAuthServiceRegisterDualWritesEmailIdentity registers a local email
// account with registration enabled. It requires a token, email signup source,
// login/activity timestamps and one verified identity owned by the returned
// user.
func TestAuthServiceRegisterDualWritesEmailIdentity(t *testing.T) {
	svc, _, client := newAuthServiceWithEnt(t, map[string]string{
		identitycore.SettingKeyRegistrationEnabled: "true",
	}, nil)
	ctx := context.Background()

	token, user, err := svc.RegisterWithVerification(ctx, "user@example.com", "password", "", "", "", "")
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.NotNil(t, user)

	storedUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, "email", storedUser.SignupSource)
	require.NotNil(t, storedUser.LastLoginAt)
	require.NotNil(t, storedUser.LastActiveAt)

	identity, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("email"),
			authidentity.ProviderKeyEQ("email"),
			authidentity.ProviderSubjectEQ("user@example.com"),
		).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, user.ID, identity.UserID)
	require.NotNil(t, identity.VerifiedAt)
}

// TestAuthServiceLoginDefersLastLoginTouchUntilRecordSuccessfulLogin
// authenticates a legacy account with seeded activity times and no email
// identity. Login leaves those observations untouched, then
// RecordSuccessfulLogin creates an identity owned by that account.
func TestAuthServiceLoginDefersLastLoginTouchUntilRecordSuccessfulLogin(t *testing.T) {
	svc, _, client := newAuthServiceWithEnt(t, map[string]string{
		identitycore.SettingKeyRegistrationEnabled: "true",
	}, nil)
	ctx := context.Background()

	passwordHash, err := svc.HashPassword("password")
	require.NoError(t, err)
	user, err := client.User.Create().
		SetEmail("login@example.com").
		SetPasswordHash(passwordHash).
		SetRole(identitycore.RoleUser).
		SetStatus(identitycore.StatusActive).
		SetBalance(1).
		SetConcurrency(1).
		Save(ctx)
	require.NoError(t, err)

	// Seed second-aligned old timestamps so equality checks observe
	// unintended login writes directly and avoid relying on subsecond
	// timestamp precision in SQLite.
	old := time.Now().Add(-2 * time.Hour).UTC().Round(time.Second)
	_, err = client.User.UpdateOneID(user.ID).
		SetLastLoginAt(old).
		SetLastActiveAt(old).
		Save(ctx)
	require.NoError(t, err)

	token, gotUser, err := svc.Login(ctx, user.Email, "password")
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.NotNil(t, gotUser)

	storedUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.NotNil(t, storedUser.LastLoginAt)
	require.NotNil(t, storedUser.LastActiveAt)
	require.True(t, storedUser.LastLoginAt.Equal(old))
	require.True(t, storedUser.LastActiveAt.Equal(old))

	identityCount, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("email"),
			authidentity.ProviderKeyEQ("email"),
			authidentity.ProviderSubjectEQ("login@example.com"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, identityCount)

	svc.RecordSuccessfulLogin(ctx, user.ID)

	identity, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("email"),
			authidentity.ProviderKeyEQ("email"),
			authidentity.ProviderSubjectEQ("login@example.com"),
		).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, user.ID, identity.UserID)
}

// TestAuthServiceRecordSuccessfulLoginBackfillsEmailIdentity stores a
// password-bearing account through the user repository before recording login
// success. The recording call must create an email identity associated with
// that account.
func TestAuthServiceRecordSuccessfulLoginBackfillsEmailIdentity(t *testing.T) {
	svc, repo, client := newAuthServiceWithEnt(t, map[string]string{
		identitycore.SettingKeyRegistrationEnabled: "true",
	}, nil)
	ctx := context.Background()

	user := &identitycore.User{
		Email:       "record@example.com",
		Role:        identitycore.RoleUser,
		Status:      identitycore.StatusActive,
		Balance:     1,
		Concurrency: 1,
	}
	require.NoError(t, user.SetPassword("password"))
	require.NoError(t, repo.Create(ctx, user))

	svc.RecordSuccessfulLogin(ctx, user.ID)

	identity, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("email"),
			authidentity.ProviderKeyEQ("email"),
			authidentity.ProviderSubjectEQ("record@example.com"),
		).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, user.ID, identity.UserID)
}

// TestAuthServiceLogin_DoesNotApplyEmailFirstBindDefaultsWhenBackfillingLegacyEmailIdentity
// logs in an account while email-source grants are enabled. Backfill creates
// one identity; successive logins keep balance and concurrency unchanged, with
// no subscription call or first-bind grant record.
func TestAuthServiceLogin_DoesNotApplyEmailFirstBindDefaultsWhenBackfillingLegacyEmailIdentity(t *testing.T) {
	assigner := &authIdentityDefaultSubAssignerStub{}
	svc, _, client := newAuthServiceWithEnt(t, map[string]string{
		identitycore.SettingKeyRegistrationEnabled:                    "true",
		identitycore.SettingKeyAuthSourceDefaultEmailBalance:          "8.5",
		identitycore.SettingKeyAuthSourceDefaultEmailConcurrency:      "4",
		identitycore.SettingKeyAuthSourceDefaultEmailSubscriptions:    `[{"plan_id":11}]`,
		identitycore.SettingKeyAuthSourceDefaultEmailGrantOnFirstBind: "true",
	}, assigner)
	ctx := context.Background()

	passwordHash, err := svc.HashPassword("password")
	require.NoError(t, err)
	user, err := client.User.Create().
		SetEmail("legacy@example.com").
		SetUsername("legacy-user").
		SetPasswordHash(passwordHash).
		SetBalance(1.5).
		SetConcurrency(2).
		SetRole(identitycore.RoleUser).
		SetStatus(identitycore.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	token, gotUser, err := svc.Login(ctx, user.Email, "password")
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.NotNil(t, gotUser)
	svc.RecordSuccessfulLogin(ctx, user.ID)

	storedUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 1.5, storedUser.Balance)
	require.Equal(t, 2, storedUser.Concurrency)
	require.Empty(t, assigner.calls)

	identityCount, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("email"),
			authidentity.ProviderKeyEQ("email"),
			authidentity.ProviderSubjectEQ("legacy@example.com"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, identityCount)
	require.Equal(t, 0, countProviderGrantRecords(t, client, user.ID, "email", "first_bind"))

	token, gotUser, err = svc.Login(ctx, user.Email, "password")
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.NotNil(t, gotUser)

	storedUser, err = client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 1.5, storedUser.Balance)
	require.Equal(t, 2, storedUser.Concurrency)
	require.Empty(t, assigner.calls)
	require.Equal(t, 0, countProviderGrantRecords(t, client, user.ID, "email", "first_bind"))
}

// TestAuthServiceLogin_DoesNotApplyMergedEmailFirstBindDefaultsWhenBackfillingLegacyEmailIdentity
// enables a global subscription default and an empty email-source list.
// Successful-login backfill retains account balance and concurrency and
// produces neither a subscription assignment nor a first-bind marker.
func TestAuthServiceLogin_DoesNotApplyMergedEmailFirstBindDefaultsWhenBackfillingLegacyEmailIdentity(t *testing.T) {
	assigner := &authIdentityDefaultSubAssignerStub{}
	svc, _, client := newAuthServiceWithEnt(t, map[string]string{
		identitycore.SettingKeyRegistrationEnabled:                    "true",
		identitycore.SettingKeyDefaultSubscriptions:                   `[{"plan_id":21}]`,
		identitycore.SettingKeyAuthSourceDefaultEmailBalance:          "8.5",
		identitycore.SettingKeyAuthSourceDefaultEmailConcurrency:      "5",
		identitycore.SettingKeyAuthSourceDefaultEmailSubscriptions:    `[]`,
		identitycore.SettingKeyAuthSourceDefaultEmailGrantOnFirstBind: "true",
	}, assigner)
	ctx := context.Background()

	passwordHash, err := svc.HashPassword("password")
	require.NoError(t, err)
	user, err := client.User.Create().
		SetEmail("merged-first-bind@example.com").
		SetUsername("merged-user").
		SetPasswordHash(passwordHash).
		SetBalance(1.5).
		SetConcurrency(2).
		SetRole(identitycore.RoleUser).
		SetStatus(identitycore.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	token, gotUser, err := svc.Login(ctx, user.Email, "password")
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.NotNil(t, gotUser)
	svc.RecordSuccessfulLogin(ctx, user.ID)

	storedUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 1.5, storedUser.Balance)
	require.Equal(t, 2, storedUser.Concurrency)
	require.Empty(t, assigner.calls)
	require.Equal(t, 0, countProviderGrantRecords(t, client, user.ID, "email", "first_bind"))
}

// TestAuthServiceLogin_DoesNotApplyEmailFirstBindDefaultsWhenIdentityAlreadyExists
// seeds an email identity before authenticating its user. Recording success
// leaves existing account limits unchanged and makes no subscription call or
// first-bind grant record.
func TestAuthServiceLogin_DoesNotApplyEmailFirstBindDefaultsWhenIdentityAlreadyExists(t *testing.T) {
	assigner := &authIdentityDefaultSubAssignerStub{}
	svc, _, client := newAuthServiceWithEnt(t, map[string]string{
		identitycore.SettingKeyRegistrationEnabled:                    "true",
		identitycore.SettingKeyAuthSourceDefaultEmailBalance:          "8.5",
		identitycore.SettingKeyAuthSourceDefaultEmailConcurrency:      "4",
		identitycore.SettingKeyAuthSourceDefaultEmailSubscriptions:    `[{"plan_id":11}]`,
		identitycore.SettingKeyAuthSourceDefaultEmailGrantOnFirstBind: "true",
	}, assigner)
	ctx := context.Background()

	passwordHash, err := svc.HashPassword("password")
	require.NoError(t, err)
	user, err := client.User.Create().
		SetEmail("bound@example.com").
		SetUsername("bound-user").
		SetPasswordHash(passwordHash).
		SetBalance(2).
		SetConcurrency(3).
		SetRole(identitycore.RoleUser).
		SetStatus(identitycore.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.AuthIdentity.Create().
		SetUserID(user.ID).
		SetProviderType("email").
		SetProviderKey("email").
		SetProviderSubject("bound@example.com").
		SetVerifiedAt(time.Now().UTC()).
		SetMetadata(map[string]any{"source": "preexisting"}).
		Save(ctx)
	require.NoError(t, err)

	token, gotUser, err := svc.Login(ctx, user.Email, "password")
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.NotNil(t, gotUser)
	svc.RecordSuccessfulLogin(ctx, user.ID)

	storedUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 2.0, storedUser.Balance)
	require.Equal(t, 3, storedUser.Concurrency)
	require.Empty(t, assigner.calls)
	require.Equal(t, 0, countProviderGrantRecords(t, client, user.ID, "email", "first_bind"))
}

// TestAuthServiceLogin_DoesNotRetryEmailFirstBindDefaultsForBackfilledEmailIdentity
// supplies an assigner that would fail its first call. Two legacy-account
// logins leave that assigner uncalled, preserve balance and concurrency, and
// create no first-bind grant record.
func TestAuthServiceLogin_DoesNotRetryEmailFirstBindDefaultsForBackfilledEmailIdentity(t *testing.T) {
	assigner := &flakyAuthIdentityDefaultSubAssignerStub{failuresRemaining: 1}
	svc, _, client := newAuthServiceWithEnt(t, map[string]string{
		identitycore.SettingKeyRegistrationEnabled:                    "true",
		identitycore.SettingKeyAuthSourceDefaultEmailBalance:          "8.5",
		identitycore.SettingKeyAuthSourceDefaultEmailConcurrency:      "4",
		identitycore.SettingKeyAuthSourceDefaultEmailSubscriptions:    `[{"plan_id":11}]`,
		identitycore.SettingKeyAuthSourceDefaultEmailGrantOnFirstBind: "true",
	}, assigner)
	ctx := context.Background()

	passwordHash, err := svc.HashPassword("password")
	require.NoError(t, err)
	user, err := client.User.Create().
		SetEmail("retry-first-bind@example.com").
		SetUsername("retry-user").
		SetPasswordHash(passwordHash).
		SetBalance(1.5).
		SetConcurrency(2).
		SetRole(identitycore.RoleUser).
		SetStatus(identitycore.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	token, gotUser, err := svc.Login(ctx, user.Email, "password")
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.NotNil(t, gotUser)
	svc.RecordSuccessfulLogin(ctx, user.ID)

	storedUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 1.5, storedUser.Balance)
	require.Equal(t, 2, storedUser.Concurrency)
	require.Empty(t, assigner.calls)
	require.Equal(t, 0, countProviderGrantRecords(t, client, user.ID, "email", "first_bind"))

	token, gotUser, err = svc.Login(ctx, user.Email, "password")
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.NotNil(t, gotUser)
	svc.RecordSuccessfulLogin(ctx, user.ID)

	storedUser, err = client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 1.5, storedUser.Balance)
	require.Equal(t, 2, storedUser.Concurrency)
	require.Empty(t, assigner.calls)
	require.Equal(t, 0, countProviderGrantRecords(t, client, user.ID, "email", "first_bind"))
}
