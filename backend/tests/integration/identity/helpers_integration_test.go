//go:build integration

package identity_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keypostgres "github.com/TokenFlux/TokenRouter/internal/apikey/postgres"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	routingpostgres "github.com/TokenFlux/TokenRouter/internal/routing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	schedulerpostgres "github.com/TokenFlux/TokenRouter/internal/scheduler/postgres"
	usagepostgres "github.com/TokenFlux/TokenRouter/internal/usage/postgres"
)

// Identity repository fixtures use separate databases cloned from a migrated
// PostgreSQL template, allowing committed operations and independent
// connections inside one case. The user repository suite reuses one clone
// across its cases and clears user-related rows before and after each case.
// Group and plan fixtures can remain in that suite database, so user cleanup
// does not make every table empty.
//
// Direct user construction saves the owning row before inserting allowed-group
// memberships with its generated ID. The caller's supplied Ent client
// determines whether those writes share a transaction. Group deletion fixtures
// bind provider links, user access, and scheduler event writes to the deletion
// executor, while key fixtures bind usage totals to their storage executor.
// Test cleanup closes the Ent wrapper before the cloned pool is dropped by
// databaseSuite.

// UserRepoSuite retains one PostgreSQL clone while replacing user storage state
// for each case.
type UserRepoSuite struct {
	// db and client share the suite's cloned database until suite cleanup closes
	// the Ent wrapper and pool. SetupTest replaces ctx and repo before each case,
	// while sequential cases reuse db and client after clearing user-related rows.
	db          *sql.DB             // borrowed suite connection pool
	suite.Suite                     // assertion and suite lifecycle support
	ctx         context.Context     // case execution context
	client      *dbent.Client       // borrowed suite Ent client
	repo        *postgres.UserStore // case user storage fixture
}

// identityDatabase returns SQL and Ent access to an independent migrated clone
// from databaseSuite. Test cleanup closes its Ent client before the suite's
// pool-close and forced-database-drop cleanup runs.
func identityDatabase(t *testing.T) (*sql.DB, *dbent.Client) {
	t.Helper()
	db := databaseSuite.New(t)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	// Close the Ent wrapper before databaseSuite closes and drops its cloned pool;
	// this callback discards the wrapper's error while the suite checks pool
	// cleanup.
	t.Cleanup(func() { _ = client.Close() })
	return db, client
}

// newGroupStoreFixture retains group storage dependencies and supplies
// provider-link, user-access, and scheduler-event participants for group
// deletion. Each participant uses the deletion executor, and the caller owns
// the supplied client and executor lifetimes.
func newGroupStoreFixture(client *dbent.Client, db postgresinfra.Executor) *routingpostgres.GroupStore {
	return routingpostgres.NewGroupStore(client, db, routingpostgres.GroupStoreOptions{
		Providers: func(exec postgresinfra.Executor) routingpostgres.GroupLinkParticipant {
			return providerpostgres.GroupLinksInTx(exec)
		},
		Users: func(exec postgresinfra.Executor) routingpostgres.GroupAccessParticipant {
			return postgres.GroupAccessDeletionInTx(exec)
		},
		Enqueue: func(ctx context.Context, exec postgresinfra.Executor, id *int64) error {
			return schedulerpostgres.EnqueueSchedulerChange(ctx, exec, scheduler.SchedulerOutboxEventGroupChanged, nil, id, nil)
		},
	})
}

// newKeyStoreFixture retains client and exec for key storage and batched
// usage-total reads. Callers own these dependencies and supply matching
// transaction scope when the usage reader must observe uncommitted key
// fixtures.
func newKeyStoreFixture(client *dbent.Client, exec postgresinfra.Executor) *keypostgres.KeyStore {
	return keypostgres.NewKeyStoreWithSQL(client, exec, func(ctx context.Context, ids []int64) (map[int64]float64, error) {
		return usagepostgres.ReadAPIKeyUsageTotals(ctx, exec, nil, ids)
	})
}

// mustCreateUser saves selected user fields and fills generated identity and
// timestamps on the supplied object. Empty email, password, role, and status
// receive fixture values, and zero concurrency becomes five.
//
// Allowed-group memberships follow user insertion so they can reference its
// stored ID. The supplied client determines their common transaction, and the
// helper uses a background context. A later membership failure fails the test
// after earlier writes have occurred, leaving rollback or committed-row cleanup
// to the caller.
func mustCreateUser(t *testing.T, client *dbent.Client, u *identity.User) *identity.User {
	t.Helper()
	ctx := context.Background()

	if u.Email == "" {
		u.Email = "user-" + time.Now().Format(time.RFC3339Nano) + "@example.com"
	}
	if u.PasswordHash == "" {
		u.PasswordHash = "test-password-hash"
	}
	if u.Role == "" {
		u.Role = identity.RoleUser
	}
	if u.Status == "" {
		u.Status = billing.StatusActive
	}
	if u.Concurrency == 0 {
		u.Concurrency = 5
	}

	create := client.User.Create().
		SetEmail(u.Email).
		SetPasswordHash(u.PasswordHash).
		SetRole(u.Role).
		SetStatus(u.Status).
		SetBalance(u.Balance).
		SetConcurrency(u.Concurrency).
		SetUsername(u.Username).
		SetNotes(u.Notes)
	if !u.CreatedAt.IsZero() {
		create.SetCreatedAt(u.CreatedAt)
	}
	if !u.UpdatedAt.IsZero() {
		create.SetUpdatedAt(u.UpdatedAt)
	}

	created, err := create.Save(ctx)
	require.NoError(t, err, "create user")

	u.ID = created.ID
	u.CreatedAt = created.CreatedAt
	u.UpdatedAt = created.UpdatedAt

	// Insert memberships after the user ID is available; these dependent writes
	// share the supplied client's scope, whose owner handles rollback after a
	// later failure.
	if len(u.AllowedGroups) > 0 {
		for _, groupID := range u.AllowedGroups {
			_, err := client.UserAllowedGroup.Create().
				SetUserID(u.ID).
				SetGroupID(groupID).
				Save(ctx)
			require.NoError(t, err, "create user_allowed_groups row")
		}
	}

	return u
}

// mustCreateApiKey saves selected key ownership, group, team, quota,
// spending-window, expiry, and timestamp fields. Empty status, credential, and
// name receive fixture defaults; storage fills k's ID and timestamps, and the
// caller owns cleanup.
func mustCreateApiKey(t *testing.T, client *dbent.Client, k *apikey.APIKey) *apikey.APIKey {
	t.Helper()
	ctx := context.Background()

	if k.Status == "" {
		k.Status = billing.StatusActive
	}
	if k.Key == "" {
		k.Key = "sk-" + time.Now().Format("150405.000000")
	}
	if k.Name == "" {
		k.Name = "default"
	}

	create := client.APIKey.Create().
		SetUserID(k.UserID).
		SetKey(k.Key).
		SetName(k.Name).
		SetStatus(k.Status)
	if k.Quota != 0 {
		create.SetQuota(k.Quota)
	}
	if k.QuotaUsed != 0 {
		create.SetQuotaUsed(k.QuotaUsed)
	}
	if k.RateLimit5h != 0 {
		create.SetRateLimit5h(k.RateLimit5h)
	}
	if k.RateLimit1d != 0 {
		create.SetRateLimit1d(k.RateLimit1d)
	}
	if k.RateLimit7d != 0 {
		create.SetRateLimit7d(k.RateLimit7d)
	}
	if k.Usage5h != 0 {
		create.SetUsage5h(k.Usage5h)
	}
	if k.Usage1d != 0 {
		create.SetUsage1d(k.Usage1d)
	}
	if k.Usage7d != 0 {
		create.SetUsage7d(k.Usage7d)
	}
	if k.Window5hStart != nil {
		create.SetWindow5hStart(*k.Window5hStart)
	}
	if k.Window1dStart != nil {
		create.SetWindow1dStart(*k.Window1dStart)
	}
	if k.Window7dStart != nil {
		create.SetWindow7dStart(*k.Window7dStart)
	}
	if k.ExpiresAt != nil {
		create.SetExpiresAt(*k.ExpiresAt)
	}
	if k.GroupID != nil {
		create.SetGroupID(*k.GroupID)
	}
	if k.TeamID != nil {
		create.SetTeamID(*k.TeamID)
	}
	if !k.CreatedAt.IsZero() {
		create.SetCreatedAt(k.CreatedAt)
	}
	if !k.UpdatedAt.IsZero() {
		create.SetUpdatedAt(k.UpdatedAt)
	}

	created, err := create.Save(ctx)
	require.NoError(t, err, "create api key")

	k.ID = created.ID
	k.CreatedAt = created.CreatedAt
	k.UpdatedAt = created.UpdatedAt
	return k
}

// mustCreateGroup saves selected routing group fields and fills generated
// identity and timestamps on g. Empty status becomes active; explicit
// description and timestamps are used when present, and save failures fail the
// test.
func mustCreateGroup(t *testing.T, client *dbent.Client, g *routing.Group) *routing.Group {
	t.Helper()
	ctx := context.Background()

	if g.Status == "" {
		g.Status = billing.StatusActive
	}

	create := client.Group.Create().
		SetName(g.Name).
		SetStatus(g.Status).
		SetRateMultiplier(g.RateMultiplier).
		SetIsExclusive(g.IsExclusive).
		SetForceOpenaiFast(g.ForceOpenAIFast)
	if g.Description != "" {
		create.SetDescription(g.Description)
	}
	if !g.CreatedAt.IsZero() {
		create.SetCreatedAt(g.CreatedAt)
	}
	if !g.UpdatedAt.IsZero() {
		create.SetUpdatedAt(g.UpdatedAt)
	}

	created, err := create.Save(ctx)
	require.NoError(t, err, "create group")

	g.ID = created.ID
	g.CreatedAt = created.CreatedAt
	g.UpdatedAt = created.UpdatedAt
	return g
}

// SetupSuite obtains one migrated clone for all user repository suite cases and
// retains its pool and Ent client. Suite cleanup owns connection closure and
// clone deletion after the cases finish.
func (s *UserRepoSuite) SetupSuite() {
	s.db, s.client = identityDatabase(s.T())
}

// SetupTest constructs the user store and clears user-related fixture rows
// before each suite case. It registers a cascading users-table truncation after
// the case, whose errors fail the test.
//
// Before-case deletion visits identity channels before identities, then
// subscriptions, allowed-group grants, and users. Those individual deletion
// errors are discarded and can leave partial cleanup. The suite's pool and Ent
// client remain shared across these sequential cases; this setup does not clear
// routing groups or subscription plans.
func (s *UserRepoSuite) SetupTest() {
	s.ctx = context.Background()
	// Truncate users and cascading dependencies after case work ends, resetting
	// IDs before the next suite case; this cleanup reports its database error
	// through assertions.
	s.T().Cleanup(func() {
		_, err := s.db.ExecContext(context.Background(), "TRUNCATE users RESTART IDENTITY CASCADE")
		s.Require().NoError(err)
	})
	s.repo = postgres.NewUserStoreWithSQL(s.client, s.db)

	// Delete dependent identity and entitlement rows before their user owners so
	// this shared suite can reuse fixture emails. Individual cleanup errors are
	// discarded.
	_, _ = s.db.ExecContext(s.ctx, "DELETE FROM auth_identity_channels")
	_, _ = s.db.ExecContext(s.ctx, "DELETE FROM auth_identities")
	_, _ = s.db.ExecContext(s.ctx, "DELETE FROM user_subscriptions")
	_, _ = s.db.ExecContext(s.ctx, "DELETE FROM user_allowed_groups")
	_, _ = s.db.ExecContext(s.ctx, "DELETE FROM users")
}

// mustCreateUser applies suite email, password, role, status, and concurrency
// defaults before invoking UserStore.Create. It mutates the supplied object and
// fails the current case if repository creation fails.
func (s *UserRepoSuite) mustCreateUser(u *identity.User) *identity.User {
	s.T().Helper()

	if u.Email == "" {
		u.Email = "user-" + time.Now().Format(time.RFC3339Nano) + "@example.com"
	}
	if u.PasswordHash == "" {
		u.PasswordHash = "test-password-hash"
	}
	if u.Role == "" {
		u.Role = identity.RoleUser
	}
	if u.Status == "" {
		u.Status = billing.StatusActive
	}
	if u.Concurrency == 0 {
		u.Concurrency = 5
	}

	s.Require().NoError(s.repo.Create(s.ctx, u), "create user")
	return u
}

// mustCreateGroup inserts an active group in the shared suite database and
// returns its routing view. Creation errors fail the current case, and user
// cleanup leaves this group table intact.
func (s *UserRepoSuite) mustCreateGroup(name string) *routing.Group {
	s.T().Helper()

	g, err := s.client.Group.Create().
		SetName(name).
		SetStatus(billing.StatusActive).
		Save(s.ctx)
	s.Require().NoError(err, "create group")
	return routingpostgres.GroupFromEnt(g)
}
