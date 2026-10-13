//go:build integration

package team_test

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
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	dbprovider "github.com/TokenFlux/TokenRouter/ent/provider"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keypostgres "github.com/TokenFlux/TokenRouter/internal/apikey/postgres"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	schedulerpostgres "github.com/TokenFlux/TokenRouter/internal/scheduler/postgres"
	"github.com/TokenFlux/TokenRouter/internal/team"
	teampostgres "github.com/TokenFlux/TokenRouter/internal/team/postgres"
	"github.com/TokenFlux/TokenRouter/internal/testutil/postgrescontainer"
	"github.com/TokenFlux/TokenRouter/internal/usage"
	usagepostgres "github.com/TokenFlux/TokenRouter/internal/usage/postgres"
)

// Team storage separates the paying owner from the acting member and records
// membership history across admission, billing, transfer, and removal.
// PostgreSQL cases inspect member capacity, owner deletion guards,
// owner-disabled key flags, default member limits, and historical usage
// alongside replayed settlement and a changed owner.
//
// The capacity case releases two invitation acceptances through one start
// channel and joins both results before counting stored members. Billing
// commands retain their payer ID when ownership changes, so the later
// admitted-cost scenario checks the old payer balance independently of current
// membership roles. User fixture creation writes allowed-group references after
// the stored owner ID, and each standalone database fixture closes its SQL pool
// and dedicated container at test cleanup.

// TestTeamInvitationConcurrentAcceptanceEnforcesMemberLimit releases two
// acceptances against one available member slot in PostgreSQL. One succeeds,
// one reports the member-limit error, and the stored active member count is
// one.
func TestTeamInvitationConcurrentAcceptanceEnforcesMemberLimit(t *testing.T) {
	integrationEntClient, integrationDB := teamDatabase(t)
	ctx := context.Background()
	repo := teampostgres.NewTeamRepository(integrationDB, keypostgres.NewTeamKeys(integrationDB), billingpostgres.NewMemberUsageStore(integrationDB, nil))
	owner := mustCreateUser(t, integrationEntClient, &identity.User{Email: uniqueTeamTestEmail("owner"), Balance: 10})
	first := mustCreateUser(t, integrationEntClient, &identity.User{Email: uniqueTeamTestEmail("first")})
	second := mustCreateUser(t, integrationEntClient, &identity.User{Email: uniqueTeamTestEmail("second")})
	teamCtx, err := repo.Create(ctx, "并发邀请团队", owner.ID, 1)
	require.NoError(t, err)

	firstToken := uuid.NewString()
	secondToken := uuid.NewString()
	_, err = repo.CreateInvitation(ctx, teamCtx.Team.ID, owner.ID, first.Email, firstToken, time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = repo.CreateInvitation(ctx, teamCtx.Team.ID, owner.ID, second.Email, secondToken, time.Now().Add(time.Hour))
	require.NoError(t, err)

	// Hold both acceptances at one start gate and buffer their results so the
	// member-capacity assertion reads completed PostgreSQL attempts after the
	// workers have joined.
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	accept := func(token string, user *identity.User) {
		defer wg.Done()
		<-start
		_, acceptErr := repo.ResolveInvitation(ctx, token, user.ID, user.Email, "accepted", time.Now())
		results <- acceptErr
	}
	wg.Add(2)
	go accept(firstToken, first)
	go accept(secondToken, second)
	// Release competing acceptances before waiting, then close results after all
	// sends finish so counting observes one complete outcome per invitation
	// without a producer race.
	close(start)
	wg.Wait()
	close(results)

	var accepted, limited int
	for result := range results {
		switch {
		case result == nil:
			accepted++
		case errors.Is(result, team.ErrTeamMemberLimitReached):
			limited++
		default:
			require.NoError(t, result)
		}
	}
	require.Equal(t, 1, accepted)
	require.Equal(t, 1, limited)

	var memberCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM team_memberships WHERE team_id = $1 AND left_at IS NULL AND role = 'member'`, teamCtx.Team.ID).Scan(&memberCount))
	require.Equal(t, 1, memberCount)
}

// TestTeamInvitationMemberLimitZeroRejectsMembers creates a team with member
// capacity zero and invites an ordinary user. Acceptance returns
// TeamMemberLimitReached for that zero-capacity team despite its existing
// owner.
func TestTeamInvitationMemberLimitZeroRejectsMembers(t *testing.T) {
	integrationEntClient, integrationDB := teamDatabase(t)
	ctx := context.Background()
	repo := teampostgres.NewTeamRepository(integrationDB, keypostgres.NewTeamKeys(integrationDB), billingpostgres.NewMemberUsageStore(integrationDB, nil))
	owner := mustCreateUser(t, integrationEntClient, &identity.User{Email: uniqueTeamTestEmail("zero-owner")})
	member := mustCreateUser(t, integrationEntClient, &identity.User{Email: uniqueTeamTestEmail("zero-member")})
	teamCtx, err := repo.Create(ctx, "零成员容量团队", owner.ID, 0)
	require.NoError(t, err)
	token := uuid.NewString()
	_, err = repo.CreateInvitation(ctx, teamCtx.Team.ID, owner.ID, member.Email, token, time.Now().Add(time.Hour))
	require.NoError(t, err)

	_, err = repo.ResolveInvitation(ctx, token, member.ID, member.Email, "accepted", time.Now())
	require.ErrorIs(t, err, team.ErrTeamMemberLimitReached)
}

// TestTeamMembershipAndBillingAreIdempotent rejects joining a second team and
// replays one admitted billing command. Member windows record one charge, and a
// later ownership transfer still charges the saved preceding payer.
func TestTeamMembershipAndBillingAreIdempotent(t *testing.T) {
	integrationEntClient, integrationDB := teamDatabase(t)
	ctx := context.Background()
	teamRepo := teampostgres.NewTeamRepository(integrationDB, keypostgres.NewTeamKeys(integrationDB), billingpostgres.NewMemberUsageStore(integrationDB, nil))
	owner := mustCreateUser(t, integrationEntClient, &identity.User{Email: uniqueTeamTestEmail("billing-owner"), Balance: 10})
	member := mustCreateUser(t, integrationEntClient, &identity.User{Email: uniqueTeamTestEmail("billing-member")})
	teamCtx, err := teamRepo.Create(ctx, "计费团队", owner.ID, 10)
	require.NoError(t, err)
	token := uuid.NewString()
	_, err = teamRepo.CreateInvitation(ctx, teamCtx.Team.ID, owner.ID, member.Email, token, time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = teamRepo.ResolveInvitation(ctx, token, member.ID, member.Email, "accepted", time.Now())
	require.NoError(t, err)
	// A two-month-old anchor makes all member calendar windows stale for this
	// charge.
	oldWindow := time.Now().AddDate(0, -2, 0)
	_, err = integrationDB.ExecContext(ctx, `
		UPDATE team_memberships SET daily_usage_usd = 9, weekly_usage_usd = 9, monthly_usage_usd = 9,
			daily_window_start = $3, weekly_window_start = $3, monthly_window_start = $3
		WHERE team_id = $1 AND user_id = $2`, teamCtx.Team.ID, member.ID, oldWindow)
	require.NoError(t, err)

	otherOwner := mustCreateUser(t, integrationEntClient, &identity.User{Email: uniqueTeamTestEmail("other-owner")})
	otherTeam, err := teamRepo.Create(ctx, "其他团队", otherOwner.ID, 10)
	require.NoError(t, err)
	otherToken := uuid.NewString()
	_, err = teamRepo.CreateInvitation(ctx, otherTeam.Team.ID, otherOwner.ID, member.Email, otherToken, time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = teamRepo.ResolveInvitation(ctx, otherToken, member.ID, member.Email, "accepted", time.Now())
	require.ErrorIs(t, err, team.ErrTeamAlreadyJoined)

	teamID := teamCtx.Team.ID
	apiKey := mustCreateApiKey(t, integrationEntClient, &apikey.APIKey{UserID: member.ID, TeamID: &teamID, Key: "sk-team-" + uuid.NewString(), Name: "team"})
	provider := mustCreateProvider(t, integrationEntClient, &providercore.Record{Name: "team-provider-" + uuid.NewString(), Type: capability.ProviderTypeAPIKey})
	billingRepo := billingpostgres.NewSettlementStore(integrationDB, timezone.NewCalendar(time.Local), schedulerpostgres.EnqueueProviderQuotaChangedInTx, billingpostgres.TaskProjectionFactories{})
	command := &billing.UsageBillingCommand{
		RequestID:         uuid.NewString(),
		APIKeyID:          apiKey.ID,
		UserID:            owner.ID,
		ActorUserID:       member.ID,
		TeamID:            &teamID,
		ProviderID:        provider.ID,
		ProviderType:      capability.ProviderTypeAPIKey,
		BillableAmountUSD: 1.25,
	}
	firstResult, err := billingRepo.Apply(ctx, command)
	require.NoError(t, err)
	require.True(t, firstResult.Applied)
	secondResult, err := billingRepo.Apply(ctx, command)
	require.NoError(t, err)
	require.False(t, secondResult.Applied)

	var daily, weekly, monthly float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT daily_usage_usd, weekly_usage_usd, monthly_usage_usd
		FROM team_memberships WHERE team_id = $1 AND user_id = $2 AND left_at IS NULL`, teamID, member.ID).Scan(&daily, &weekly, &monthly))
	// Compare member costs and both payer balances within a micro-dollar
	// difference.
	require.InDelta(t, 1.25, daily, 0.000001)
	require.InDelta(t, 1.25, weekly, 0.000001)
	require.InDelta(t, 1.25, monthly, 0.000001)

	// Transfer ownership before settling another command that retains its admitted
	// payer ID, exposing an implementation that rewrites funding to the current
	// team owner.
	transferToken := uuid.NewString()
	_, err = teamRepo.CreateOwnershipTransfer(ctx, teamID, owner.ID, member.ID, transferToken, time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = teamRepo.ResolveOwnershipTransfer(ctx, transferToken, member.ID, "accepted", time.Now())
	require.NoError(t, err)
	// Reuse the admitted payer and actor while choosing a new request and charge;
	// clear the inherited fingerprint so this separate settlement describes its
	// own changed cost.
	secondCommand := *command
	secondCommand.RequestID = uuid.NewString()
	secondCommand.BillableAmountUSD = 0.75
	secondCommand.RequestFingerprint = ""
	transferResult, err := billingRepo.Apply(ctx, &secondCommand)
	require.NoError(t, err)
	require.True(t, transferResult.Applied)

	var oldOwnerBalance, newOwnerBalance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id = $1`, owner.ID).Scan(&oldOwnerBalance))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id = $1`, member.ID).Scan(&newOwnerBalance))
	require.InDelta(t, 8, oldOwnerBalance, 0.000001)
	require.InDelta(t, 0, newOwnerBalance, 0.000001)

	var oldRole, newRole string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT role FROM team_memberships WHERE team_id = $1 AND user_id = $2`, teamID, owner.ID).Scan(&oldRole))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT role FROM team_memberships WHERE team_id = $1 AND user_id = $2`, teamID, member.ID).Scan(&newRole))
	require.Equal(t, team.TeamRoleMember, oldRole)
	require.Equal(t, team.TeamRoleOwner, newRole)
}

// TestActiveTeamOwnerCannotBeDeleted attempts owner deletion through UserStore
// and direct PostgreSQL mutation. Both reject while the team remains active,
// and direct soft deletion succeeds after team dissolution.
func TestActiveTeamOwnerCannotBeDeleted(t *testing.T) {
	integrationEntClient, integrationDB := teamDatabase(t)
	ctx := context.Background()
	repo := teampostgres.NewTeamRepository(integrationDB, keypostgres.NewTeamKeys(integrationDB), billingpostgres.NewMemberUsageStore(integrationDB, nil))
	userRepo := postgres.NewUserStore(integrationEntClient, integrationDB)
	owner := mustCreateUser(t, integrationEntClient, &identity.User{Email: uniqueTeamTestEmail("protected-owner")})
	teamCtx, err := repo.Create(ctx, "删除保护团队", owner.ID, 10)
	require.NoError(t, err)

	err = userRepo.Delete(ctx, owner.ID)
	require.ErrorIs(t, err, team.ErrTeamOwnerTransferRequired)

	// Bypass UserStore with a direct soft-delete mutation to check the database
	// owner guard independently, then repeat that mutation after dissolution
	// removes the active ownership.
	_, err = integrationDB.ExecContext(ctx, `UPDATE users SET deleted_at = NOW() WHERE id = $1`, owner.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "TEAM_OWNER_TRANSFER_REQUIRED")

	require.NoError(t, repo.Dissolve(ctx, teamCtx.Team.ID, time.Now()))
	// Retry the same raw mutation after dissolving the active team so the database
	// guard can distinguish retained ownership from ownership that no longer
	// blocks deletion.
	_, err = integrationDB.ExecContext(ctx, `UPDATE users SET deleted_at = NOW() WHERE id = $1`, owner.ID)
	require.NoError(t, err)
}

// TestSoftDeletedTeamMemberIsRemovedAndKeysDisabled directly marks an ordinary
// member user deleted in PostgreSQL. Its membership receives a nonzero leave
// time and the associated team key becomes disabled.
func TestSoftDeletedTeamMemberIsRemovedAndKeysDisabled(t *testing.T) {
	integrationEntClient, integrationDB := teamDatabase(t)
	ctx := context.Background()
	repo := teampostgres.NewTeamRepository(integrationDB, keypostgres.NewTeamKeys(integrationDB), billingpostgres.NewMemberUsageStore(integrationDB, nil))
	owner := mustCreateUser(t, integrationEntClient, &identity.User{Email: uniqueTeamTestEmail("delete-owner")})
	member := mustCreateUser(t, integrationEntClient, &identity.User{Email: uniqueTeamTestEmail("delete-member")})
	teamCtx, err := repo.Create(ctx, "成员删除清理团队", owner.ID, 5)
	require.NoError(t, err)
	token := uuid.NewString()
	_, err = repo.CreateInvitation(ctx, teamCtx.Team.ID, owner.ID, member.Email, token, time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = repo.ResolveInvitation(ctx, token, member.ID, member.Email, "accepted", time.Now())
	require.NoError(t, err)
	teamID := teamCtx.Team.ID
	apiKey := mustCreateApiKey(t, integrationEntClient, &apikey.APIKey{
		UserID: member.ID,
		TeamID: &teamID,
		Key:    "sk-team-delete-" + uuid.NewString(),
		Name:   "delete-member-key",
	})

	_, err = integrationDB.ExecContext(ctx, `UPDATE users SET deleted_at = NOW() WHERE id = $1`, member.ID)
	require.NoError(t, err)

	var leftAt time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT left_at FROM team_memberships WHERE team_id = $1 AND user_id = $2`, teamID, member.ID).Scan(&leftAt))
	require.False(t, leftAt.IsZero())
	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM api_keys WHERE id = $1`, apiKey.ID).Scan(&status))
	require.Equal(t, apikey.StatusAPIKeyDisabled, status)
}

// TestTeamOwnerKeyLockRequiresExplicitOwnerEnable disables a team key through
// the owner operation and then directly sets active status. The independent
// owner-disabled flag remains until the enable operation clears it.
func TestTeamOwnerKeyLockRequiresExplicitOwnerEnable(t *testing.T) {
	integrationEntClient, integrationDB := teamDatabase(t)
	ctx := context.Background()
	repo := teampostgres.NewTeamRepository(integrationDB, keypostgres.NewTeamKeys(integrationDB), billingpostgres.NewMemberUsageStore(integrationDB, nil))
	owner := mustCreateUser(t, integrationEntClient, &identity.User{Email: uniqueTeamTestEmail("key-owner")})
	member := mustCreateUser(t, integrationEntClient, &identity.User{Email: uniqueTeamTestEmail("key-member")})
	teamCtx, err := repo.Create(ctx, "团队密钥锁定测试", owner.ID, 5)
	require.NoError(t, err)
	token := uuid.NewString()
	_, err = repo.CreateInvitation(ctx, teamCtx.Team.ID, owner.ID, member.Email, token, time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = repo.ResolveInvitation(ctx, token, member.ID, member.Email, "accepted", time.Now())
	require.NoError(t, err)
	teamID := teamCtx.Team.ID
	apiKey := mustCreateApiKey(t, integrationEntClient, &apikey.APIKey{
		UserID: member.ID,
		TeamID: &teamID,
		Key:    "sk-team-owner-lock-" + uuid.NewString(),
		Name:   "owner-lock",
	})

	_, err = repo.DisableTeamKey(ctx, teamID, apiKey.ID, nil)
	require.NoError(t, err)
	var status string
	var ownerDisabled bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status, team_owner_disabled FROM api_keys WHERE id = $1`, apiKey.ID).Scan(&status, &ownerDisabled))
	require.Equal(t, apikey.StatusAPIKeyDisabled, status)
	require.True(t, ownerDisabled)

	// Set status directly before the owner-enable operation so the following read
	// can detect whether status updates mistakenly clear the separate
	// owner-disabled flag.
	_, err = integrationDB.ExecContext(ctx, `UPDATE api_keys SET status = 'active' WHERE id = $1`, apiKey.ID)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status, team_owner_disabled FROM api_keys WHERE id = $1`, apiKey.ID).Scan(&status, &ownerDisabled))
	require.Equal(t, apikey.StatusAPIKeyActive, status)
	require.True(t, ownerDisabled)

	_, err = repo.EnableTeamKey(ctx, teamID, apiKey.ID, nil)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status, team_owner_disabled FROM api_keys WHERE id = $1`, apiKey.ID).Scan(&status, &ownerDisabled))
	require.Equal(t, apikey.StatusAPIKeyActive, status)
	require.False(t, ownerDisabled)
}

// TestTeamInvitationCopiesCurrentDefaultMemberLimits sets defaults before
// invitation acceptance and reads all three joined member limits. A later
// defaults change leaves the exercised member's daily limit at its joined
// value.
func TestTeamInvitationCopiesCurrentDefaultMemberLimits(t *testing.T) {
	integrationEntClient, integrationDB := teamDatabase(t)
	ctx := context.Background()
	repo := teampostgres.NewTeamRepository(integrationDB, keypostgres.NewTeamKeys(integrationDB), billingpostgres.NewMemberUsageStore(integrationDB, nil))
	owner := mustCreateUser(t, integrationEntClient, &identity.User{Email: uniqueTeamTestEmail("default-limit-owner")})
	member := mustCreateUser(t, integrationEntClient, &identity.User{Email: uniqueTeamTestEmail("default-limit-member")})
	teamCtx, err := repo.Create(ctx, "默认限额团队", owner.ID, 10)
	require.NoError(t, err)
	require.NoError(t, repo.SetDefaultMemberLimits(ctx, teamCtx.Team.ID, 1.5, 8, 30))

	token := uuid.NewString()
	_, err = repo.CreateInvitation(ctx, teamCtx.Team.ID, owner.ID, member.Email, token, time.Now().Add(time.Hour))
	require.NoError(t, err)
	memberCtx, err := repo.ResolveInvitation(ctx, token, member.ID, member.Email, "accepted", time.Now())
	require.NoError(t, err)
	// A micro-dollar tolerance compares the copied member limits and retained
	// daily value.
	require.InDelta(t, 1.5, memberCtx.Membership.DailyLimitUSD, 0.000001)
	require.InDelta(t, 8, memberCtx.Membership.WeeklyLimitUSD, 0.000001)
	require.InDelta(t, 30, memberCtx.Membership.MonthlyLimitUSD, 0.000001)

	// Change defaults after this member joins, then reload its context so the
	// daily-limit assertion checks the stored joined value rather than the new
	// team default.
	require.NoError(t, repo.SetDefaultMemberLimits(ctx, teamCtx.Team.ID, 2, 10, 40))
	memberCtx, err = repo.GetContextByUserID(ctx, member.ID)
	require.NoError(t, err)
	// The same numeric tolerance checks the retained joined daily allowance.
	require.InDelta(t, 1.5, memberCtx.Membership.DailyLimitUSD, 0.000001)
}

// TestTeamMemberUsageSeriesKeepsDepartedMemberHistory persists owner and member
// usage before member removal. The history series retains both costs, totals
// agree with summary, and the departed member is labeled left.
func TestTeamMemberUsageSeriesKeepsDepartedMemberHistory(t *testing.T) {
	integrationEntClient, integrationDB := teamDatabase(t)
	ctx := context.Background()
	teamRepo := teampostgres.NewTeamRepository(integrationDB, keypostgres.NewTeamKeys(integrationDB), billingpostgres.NewMemberUsageStore(integrationDB, nil))
	usageRepo := usagepostgres.NewUsageLogRepositoryWithSQL(integrationEntClient, integrationDB, timezone.NewCalendar(time.Local))
	owner := mustCreateUser(t, integrationEntClient, &identity.User{Email: uniqueTeamTestEmail("usage-owner")})
	member := mustCreateUser(t, integrationEntClient, &identity.User{Email: uniqueTeamTestEmail("usage-member")})
	teamCtx, err := teamRepo.Create(ctx, "历史成员用量团队", owner.ID, 5)
	require.NoError(t, err)
	token := uuid.NewString()
	_, err = teamRepo.CreateInvitation(ctx, teamCtx.Team.ID, owner.ID, member.Email, token, time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = teamRepo.ResolveInvitation(ctx, token, member.ID, member.Email, "accepted", time.Now())
	require.NoError(t, err)
	teamID := teamCtx.Team.ID
	ownerKey := mustCreateApiKey(t, integrationEntClient, &apikey.APIKey{UserID: owner.ID, TeamID: &teamID, Key: "sk-team-usage-owner-" + uuid.NewString()})
	memberKey := mustCreateApiKey(t, integrationEntClient, &apikey.APIKey{UserID: member.ID, TeamID: &teamID, Key: "sk-team-usage-member-" + uuid.NewString()})
	provider := mustCreateProvider(t, integrationEntClient, &providercore.Record{Name: "team-usage-" + uuid.NewString(), Type: capability.ProviderTypeAPIKey})
	createdAt := time.Now().UTC()
	for _, item := range []struct {
		userID   int64   // acting user fixture ID
		apiKeyID int64   // corresponding team key fixture ID
		cost     float64 // USD usage cost
	}{
		{userID: owner.ID, apiKeyID: ownerKey.ID, cost: 1.2},
		{userID: member.ID, apiKeyID: memberKey.ID, cost: 0.8},
	} {
		_, err = usageRepo.Create(ctx, &usage.UsageLog{
			UserID:        item.userID,
			BillingUserID: owner.ID,
			TeamID:        &teamID,
			APIKeyID:      item.apiKeyID,
			ProviderID:    provider.ID,
			RequestID:     uuid.NewString(),
			Model:         "team-usage-test",
			InputTokens:   10,
			OutputTokens:  5,
			TotalCost:     item.cost,
			ActualCost:    item.cost,
			CreatedAt:     createdAt,
		})
		require.NoError(t, err)
	}
	require.NoError(t, teamRepo.RemoveMember(ctx, teamID, member.ID, time.Now()))

	query := team.TeamUsageQuery{From: createdAt.Add(-time.Hour), To: createdAt.Add(time.Hour)}
	total, err := teamRepo.GetUsageSummary(ctx, teamID, query)
	require.NoError(t, err)
	series, err := teamRepo.ListMemberUsageSeries(ctx, teamID, query)
	require.NoError(t, err)

	var seriesTotal float64
	statuses := make(map[int64]string, len(series))
	for _, item := range series {
		seriesTotal += item.Summary.ActualCost
		statuses[item.ActorUserID] = item.Status
	}
	// A micro-dollar tolerance compares summed member costs with the overall team
	// summary.
	require.InDelta(t, total.ActualCost, seriesTotal, 0.000001)
	require.InDelta(t, 2, seriesTotal, 0.000001)
	require.Equal(t, "active", statuses[owner.ID])
	require.Equal(t, "left", statuses[member.ID])
}

// uniqueTeamTestEmail returns a team-prefixed email containing the supplied
// fixture label and a fresh UUID.
func uniqueTeamTestEmail(prefix string) string {
	return fmt.Sprintf("team-%s-%s@example.com", prefix, uuid.NewString())
}

// teamDatabase returns Ent and SQL access to a dedicated migrated PostgreSQL
// container. postgrescontainer.New owns pool and container cleanup; the Ent
// wrapper borrows that pool and has no separate registered Close callback.
func teamDatabase(t *testing.T) (*dbent.Client, *sql.DB) {
	t.Helper()
	db := postgrescontainer.New(t)
	return dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db))), db
}

// mustCreateProvider saves selected provider fields and fills generated ID and
// timestamps on a. Empty platform, type, and status select Anthropic, OAuth,
// and active; zero concurrency and priority become three and fifty, false
// schedulability becomes true, and nil maps become empty.
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
		a.Status = providercore.StatusActive
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

// mustCreateUser saves selected user fields and fills generated ID and
// timestamps on the supplied object. Empty email, password, role, and status
// receive fixture values, and zero concurrency becomes five.
//
// Allowed-group rows are written after creation so each row can reference the
// stored user ID. The supplied client determines their transaction scope, and
// this helper uses a background context. A later grant failure fails the test
// after earlier writes may have occurred, leaving their rollback or
// committed-fixture cleanup to the caller.
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

	// Insert group grants after the owner ID is stored so references have a valid
	// user, leaving the caller's supplied client to determine rollback after a
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

// mustCreateApiKey saves selected ownership, group, team, quota,
// spending-window, expiry, and timestamp fields and fills generated identity on
// k. Empty status, credential, and name receive defaults; the caller supplies
// storage scope and cleanup.
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
