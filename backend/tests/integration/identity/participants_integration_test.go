//go:build integration

package identity_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/redeemcodeusage"
	keypostgres "github.com/TokenFlux/TokenRouter/internal/apikey/postgres"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	teampostgres "github.com/TokenFlux/TokenRouter/internal/team/postgres"
)

// Team membership and credential writes need the same PostgreSQL transaction so
// failed participant work cannot leave a departed member with independently
// disabled keys. Separate entitlement cases check caller-owned concurrency
// adjustment, registration invitation consumption and restoration, and initial
// funds that do not increase cumulative recharge.

// failingMemberKeys injects a post-write error into transaction-bound member
// key disabling.
type failingMemberKeys struct {
	// TeamKeys performs the actual key updates with the supplied transaction
	// before this override returns an injected error to its team repository
	// caller.
	*keypostgres.TeamKeys
	// err follows successful delegated disabling. Nil makes the wrapper report
	// success, so rollback cases configure an error before requesting member
	// removal.
	err error
}

// DisableMemberInTx delegates member-key disabling with the supplied SQL
// transaction before returning its configured error. Delegate failures return
// directly, and the team repository owns rollback after an injected post-write
// failure.
func (p failingMemberKeys) DisableMemberInTx(ctx context.Context, tx *sql.Tx, teamID, userID int64, now time.Time) error {
	if err := p.TeamKeys.DisableMemberInTx(ctx, tx, teamID, userID, now); err != nil {
		return err
	}
	return p.err
}

// TestMemberRemovalRollsBackKeyParticipant fails after disabling a team
// member's key inside removal. Both membership and key state remain active,
// then a working removal succeeds and disables the key.
func TestMemberRemovalRollsBackKeyParticipant(t *testing.T) {
	integrationDB, integrationEntClient := identityDatabase(t)
	ctx := context.Background()
	owner := mustCreateUser(t, integrationEntClient, &identity.User{Email: "test-owner-" + uuid.NewString() + "@example.com"})
	member := mustCreateUser(t, integrationEntClient, &identity.User{Email: "test-member-" + uuid.NewString() + "@example.com"})
	repo := teampostgres.NewTeamRepository(integrationDB, keypostgres.NewTeamKeys(integrationDB), billingpostgres.NewMemberUsageStore(integrationDB, nil))
	current, err := repo.Create(ctx, "事务参与验证", owner.ID, 10)
	require.NoError(t, err)
	token := uuid.NewString()
	_, err = repo.CreateInvitation(ctx, current.Team.ID, owner.ID, member.Email, token, time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = repo.ResolveInvitation(ctx, token, member.ID, member.Email, "accepted", time.Now())
	require.NoError(t, err)
	key, err := integrationEntClient.APIKey.Create().SetUserID(member.ID).SetTeamID(current.Team.ID).
		SetKey("test-" + uuid.NewString()).SetName("参与者验证").SetStatus("active").Save(ctx)
	require.NoError(t, err)
	failure := errors.New("test key participant failed after write")
	broken := teampostgres.NewTeamRepository(integrationDB,
		failingMemberKeys{TeamKeys: keypostgres.NewTeamKeys(integrationDB), err: failure},
		billingpostgres.NewMemberUsageStore(integrationDB, nil))
	// Fail after the key participant writes so outside membership and key reads
	// can detect a component that committed independently of the enclosing removal
	// transaction.
	require.ErrorIs(t, broken.RemoveMember(ctx, current.Team.ID, member.ID, time.Now()), failure)
	var leftAt sql.NullTime
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"SELECT left_at FROM team_memberships WHERE team_id = $1 AND user_id = $2", current.Team.ID, member.ID).Scan(&leftAt))
	require.False(t, leftAt.Valid, "Key 参与者失败必须回滚成员离开状态")
	stored, err := integrationEntClient.APIKey.Get(ctx, key.ID)
	require.NoError(t, err)
	require.Equal(t, "active", stored.Status, "Key 禁用不得在另一个连接独立提交")
	require.NoError(t, repo.RemoveMember(ctx, current.Team.ID, member.ID, time.Now()))
	stored, err = integrationEntClient.APIKey.Get(ctx, key.ID)
	require.NoError(t, err)
	require.Equal(t, "disabled", stored.Status)
}

// TestRedeemConcurrencyUsesOuterEntTransaction adjusts concurrency on an
// uncommitted user through a billing participant. Positive and clamped negative
// changes remain inside the outer transaction, leaving no visible user after
// rollback.
func TestRedeemConcurrencyUsesOuterEntTransaction(t *testing.T) {
	ctx := context.Background()
	_, client := identityDatabase(t)
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	// Keep uncommitted fixture and participant writes under caller rollback on any
	// early return; the later explicit rollback is asserted before checking
	// external absence.
	defer func() { _ = tx.Rollback() }()
	user, err := tx.Client().User.Create().SetEmail("test-concurrency-" + uuid.NewString() + "@example.com").
		SetPasswordHash("hash").SetConcurrency(3).SetBalance(10).Save(ctx)
	require.NoError(t, err)
	participant := billingpostgres.RedeemInTx(tx, identitypostgres.NewConcurrencyStore(tx.Client()))
	require.NoError(t, participant.ApplyConcurrency(ctx, user.ID, 7))
	inside, err := tx.Client().User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 10, inside.Concurrency)
	require.Equal(t, 10.0, inside.Balance)
	require.NoError(t, participant.ApplyConcurrency(ctx, user.ID, -12))
	inside, err = tx.Client().User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Zero(t, inside.Concurrency, "负数兑换保持下限为零")
	_, err = client.User.Get(ctx, user.ID)
	require.True(t, dbent.IsNotFound(err), "参与者不得提交尚未提交的用户")
	require.NoError(t, tx.Rollback())
	_, err = client.User.Get(ctx, user.ID)
	require.True(t, dbent.IsNotFound(err))
}

// TestInitialFundsDoNotCountAsRecharge creates an identity user with initial
// balance through UserStore. The stored balance equals the supplied amount
// while cumulative recharge remains zero for that registration-style funding.
func TestInitialFundsDoNotCountAsRecharge(t *testing.T) {
	ctx := context.Background()
	integrationDB, client := identityDatabase(t)
	user := &identity.User{
		Email: "test-initial-" + uuid.NewString() + "@example.com", PasswordHash: "hash",
		Role: identity.RoleUser, Status: billing.StatusActive, Balance: 12.5, Concurrency: 2,
	}
	require.NoError(t, identitypostgres.NewUserStore(client, integrationDB).Create(ctx, user))
	stored, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 12.5, stored.Balance)
	require.Zero(t, stored.TotalRecharged, "注册初始资金不能套用累计充值语义")
}

// TestRegistrationInvitationUsesOuterTransaction consumes and restores an
// invitation inside an outer Ent transaction. It creates no ordinary usage
// rows, rejects reuse, and rollback removes both uncommitted invitation and
// user.
func TestRegistrationInvitationUsesOuterTransaction(t *testing.T) {
	ctx := context.Background()
	_, client := identityDatabase(t)
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	// Keep uncommitted fixture and participant writes under caller rollback on any
	// early return; the later explicit rollback is asserted before checking
	// external absence.
	defer func() { _ = tx.Rollback() }()
	user, err := tx.Client().User.Create().SetEmail("test-invite-" + uuid.NewString() + "@example.invalid").SetPasswordHash("hash").Save(ctx)
	require.NoError(t, err)
	// Twenty-four UUID bytes plus the prefix fit the invitation fixture's code
	// limit.
	invitation, err := tx.Client().RedeemCode.Create().SetCode("test-" + uuid.NewString()[:24]).SetType("invitation").SetStatus("unused").SetValue(0).SetMaxUses(1).Save(ctx)
	require.NoError(t, err)
	// Bind invitation operations to the outer transaction so consumed state and
	// snapshot restoration can be observed internally before rollback removes the
	// fixture entirely.
	participant := billingpostgres.RegistrationInvitationsInTx(tx)
	require.NoError(t, participant.Consume(ctx, invitation.ID, user.ID))
	usages, err := tx.Client().RedeemCodeUsage.Query().Where(redeemcodeusage.RedeemCodeIDEQ(invitation.ID)).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, usages, "注册邀请码继续只更新原使用状态，不新增普通兑换 usage")
	stored, err := participant.Load(ctx, invitation.Code)
	require.NoError(t, err)
	require.Equal(t, "used", stored.Status)
	require.Equal(t, 1, stored.UsedCount)
	require.Equal(t, user.ID, *stored.UsedBy)
	require.ErrorIs(t, participant.Consume(ctx, invitation.ID, user.ID), billing.ErrRedeemCodeUsed)
	_, err = client.RedeemCode.Get(ctx, invitation.ID)
	require.True(t, dbent.IsNotFound(err), "参与方法不得提前提交调用方的创建")
	stored.Status = "unused"
	stored.UsedCount = 0
	stored.UsedBy = nil
	stored.UsedAt = nil
	require.NoError(t, participant.RestoreSnapshot(ctx, stored))
	restored, err := tx.Client().RedeemCode.Get(ctx, invitation.ID)
	require.NoError(t, err)
	require.Equal(t, "unused", restored.Status)
	require.Nil(t, restored.UsedBy)
	require.NoError(t, tx.Rollback())
	_, err = client.RedeemCode.Get(ctx, invitation.ID)
	require.True(t, dbent.IsNotFound(err))
	_, err = client.User.Get(ctx, user.ID)
	require.True(t, dbent.IsNotFound(err))
}
