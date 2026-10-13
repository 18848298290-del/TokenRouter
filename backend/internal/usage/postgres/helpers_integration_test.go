//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	dbprovider "github.com/TokenFlux/TokenRouter/ent/provider"
	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Integration fixtures use the migrated shared database while each
// UsageLogRepoSuite test selects an Ent transaction for rollback isolation.
// Tests that exercise committed pool-backed batches instead register truncation
// cleanup, keeping those records out of later retained-usage assertions.
//
// Fixture constructors fill practical defaults and mutate their supplied domain
// values with persistent identity and timestamps. Provider defaults force
// schedulability, and key/user constructors synthesize text when missing, so
// tests that need other states use explicit post-creation updates or different
// setup. Stores receive the transaction's SQL executor and Calendar, while
// uniquely identified usage rows prevent accidental fixture deduplication.

// UsageLogRepoSuite runs repository scenarios against one rollback-isolated Ent
// transaction.
type UsageLogRepoSuite struct {
	suite.Suite // assertions and suite lifecycle
	// ctx, tx, client, and repo are reset by SetupTest for each method. client and
	// repo use tx's connection, whose registered cleanup rolls back fixture writes.
	ctx    context.Context // current fixture query context
	tx     *dbent.Tx       // current rollback-owned transaction
	client *dbent.Client   // transaction scoped Ent client
	repo   *Store          // transaction scoped usage repository
}

// mustCreateUser inserts a fixture user and writes its identity/timestamps back
// into u. Missing email, password hash, role, and status use fixture defaults,
// while zero concurrency becomes five.
//
// The provided Ent client determines transaction ownership. After the user is
// saved, requested allowed-group links are inserted in sequence; any persistence
// error fails the test immediately, and the surrounding fixture transaction or
// registered cleanup owns rollback/removal. The helper returns the same supplied
// pointer, so later assertions see its generated ID and timestamps.
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

	// The user ID must exist before membership links are written. The caller's
	// transaction and cleanup own recovery if a later allowed-group insert fails.
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

// mustCreateGroup inserts a fixture group and mutates g with its persistent
// identity/timestamps. Empty status becomes active, supplied nonzero timestamps
// are used, and the selected Ent client owns any surrounding transaction.
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

// mustCreateProvider inserts a fixture provider and writes identity/timestamps
// into a. Missing platform/type/status use fixture values, zero
// concurrency/priority choose defaults, and false Schedulable is forced true;
// nullable settings are copied when supplied.
func mustCreateProvider(t *testing.T, client *dbent.Client, a *providercore.Record) *providercore.Record {
	t.Helper()
	ctx := context.Background()

	if a.Platform == "" {
		a.Platform = capability.PlatformAnthropic
	}
	if a.Type == "" {
		a.Type = capability.ProviderTypeOAuth
	}
	if a.Status == "" {
		a.Status = billing.StatusActive
	}
	if a.Concurrency == 0 {
		a.Concurrency = 3
	}
	if a.Priority == 0 {
		a.Priority = 50
	}
	if !a.Schedulable {
		a.Schedulable = true
	}
	if a.Credentials == nil {
		a.Credentials = map[string]any{}
	}
	if a.Extra == nil {
		a.Extra = map[string]any{}
	}

	create := client.Provider.Create().
		SetName(a.Name).
		SetPlatform(a.Platform).
		SetType(a.Type).
		SetCredentials(a.Credentials).
		SetExtra(a.Extra).
		SetConcurrency(a.Concurrency).
		SetPriority(a.Priority).
		SetStatus(a.Status).
		SetSchedulable(a.Schedulable).
		SetErrorMessage(a.ErrorMessage)

	if a.ProxyID != nil {
		create.SetProxyID(*a.ProxyID)
	}
	if a.LastUsedAt != nil {
		create.SetLastUsedAt(*a.LastUsedAt)
	}
	if a.RateLimitedAt != nil {
		create.SetRateLimitedAt(*a.RateLimitedAt)
	}
	if a.RateLimitResetAt != nil {
		create.SetRateLimitResetAt(*a.RateLimitResetAt)
	}
	if a.OverloadUntil != nil {
		create.SetOverloadUntil(*a.OverloadUntil)
	}
	if a.SessionWindowStart != nil {
		create.SetSessionWindowStart(*a.SessionWindowStart)
	}
	if a.SessionWindowEnd != nil {
		create.SetSessionWindowEnd(*a.SessionWindowEnd)
	}
	if a.SessionWindowStatus != "" {
		create.SetSessionWindowStatus(a.SessionWindowStatus)
	}
	if !a.CreatedAt.IsZero() {
		create.SetCreatedAt(a.CreatedAt)
	}
	if !a.UpdatedAt.IsZero() {
		create.SetUpdatedAt(a.UpdatedAt)
	}
	if a.ParentProviderID != nil {
		create.SetParentProviderID(*a.ParentProviderID)
	}
	if a.QuotaDimension != "" {
		create.SetQuotaDimension(dbprovider.QuotaDimension(a.QuotaDimension))
	}

	created, err := create.Save(ctx)
	require.NoError(t, err, "create provider")

	a.ID = created.ID
	a.CreatedAt = created.CreatedAt
	a.UpdatedAt = created.UpdatedAt
	return a
}

// mustCreateApiKey inserts a fixture key and mutates its identity/timestamps.
// Missing status, key text, or name use fixture defaults; optional ownership,
// limit, and window fields are persisted when their represented values are
// supplied.
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

// testEntClient returns the shared Ent client for tests whose writes commit. It
// registers a cascading user-table truncation with identity restart after the
// test, so callers serialize use of this shared committed-fixture database.
func testEntClient(t *testing.T) *dbent.Client {
	t.Helper()
	t.Cleanup(func() {
		// Committed batch fixtures cannot be rolled back by testEntTx. Cascade from
		// users to remove dependent rows and restart identities before later
		// scenarios.
		_, err := integrationDB.ExecContext(context.Background(), "TRUNCATE users RESTART IDENTITY CASCADE")
		require.NoError(t, err, "清理集成测试提交的数据")
	})
	return integrationEntClient
}

// testEntTx starts a shared-database Ent transaction and registers rollback
// cleanup. The caller chooses its transaction client/executor for test writes;
// cleanup ignores rollback errors, including a transaction already completed by
// the test.
func testEntTx(t *testing.T) *dbent.Tx {
	t.Helper()

	tx, err := integrationEntClient.Tx(context.Background())
	require.NoError(t, err, "begin ent tx")
	t.Cleanup(func() {
		// Roll back fixture work on every test exit; an already completed transaction
		// may return an error, which this cleanup deliberately ignores.
		_ = tx.Rollback()
	})
	return tx
}

// SetupTest creates a rollback-isolated Ent transaction for one suite method.
// The suite stores its context, transaction client, and usage repository using
// the same executor, with a Calendar based on current time.Local.
func (s *UsageLogRepoSuite) SetupTest() {
	s.ctx = context.Background()
	tx := testEntTx(s.T())
	s.tx = tx
	s.client = tx.Client()
	s.repo = NewUsageLogRepositoryWithSQL(s.client, tx, timezone.NewCalendar(time.Local))
}

// createUsageLog creates one charged fixture row with a unique request identity.
// The supplied actor/key/provider and timestamp are persisted through the suite
// store; failures stop the test, and the returned row carries its database
// identity.
func (s *UsageLogRepoSuite) createUsageLog(user *identity.User, apiKey *apikey.APIKey, provider *providercore.Record, inputTokens, outputTokens int, cost float64, createdAt time.Time) *usage.UsageLog {
	log := &usage.UsageLog{
		UserID:       user.ID,
		APIKeyID:     apiKey.ID,
		ProviderID:   provider.ID,
		RequestID:    uuid.New().String(), // Separate requests cannot collapse under the request/key uniqueness rule.
		Model:        "claude-3",
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalCost:    cost,
		ActualCost:   cost,
		CreatedAt:    createdAt,
	}
	_, err := s.repo.Create(s.ctx, log)
	s.Require().NoError(err)
	return log
}
