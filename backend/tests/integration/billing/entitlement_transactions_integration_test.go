//go:build integration

package billing_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/redeemcodeusage"
	"github.com/TokenFlux/TokenRouter/ent/usersubscription"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
)

// Entitlement grants are checked inside caller-owned PostgreSQL transactions,
// including users and plans whose creation remains uncommitted. Context-bound
// services and explicitly transaction-bound adapters read those fixture rows
// while the base client reports the new user absent.
//
// The outer-transaction cases roll back their grants or validity changes before
// inspecting persisted state again. Redemption cases inject an error after the
// real usage insert, then inspect balance, concurrency, subscription,
// code-count, and usage effects together. Authentication invalidation remains
// unobserved after that failure; retry through the working repository succeeds
// and produces one invalidation, so publication is checked after transaction
// completion.

// failedRedeemUsage injects a failure after its delegate has inserted
// redemption usage.
type failedRedeemUsage struct {
	// RedeemCodeRepository performs all storage calls and inserts the usage
	// row before CreateUsage returns its injected error to the
	// transaction-owning redemption service.
	billing.RedeemCodeRepository
}

// redeemAuthObservation records the total number of invalidation calls made by
// tested redemption services.
type redeemAuthObservation struct {
	count atomic.Int32 // observed user invalidation calls
}

// TestSubscriptionParticipantReadsUncommittedAndRollsBack grants a subscription
// and balance to an uncommitted user through two transaction-binding modes.
// External reads cannot see that user, and rollback removes the subscription.
func TestSubscriptionParticipantReadsUncommittedAndRollsBack(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			ctx := context.Background()
			client := committedEntitlementClient(t)
			tx, err := client.Tx(ctx)
			require.NoError(t, err)
			// Register rollback before uncommitted fixture writes
			// so a setup or grant failure releases the outer
			// transaction; the later explicit rollback is asserted.
			defer func() { _ = tx.Rollback() }()
			user, err := tx.Client().User.Create().SetEmail("test-participant@example.com").SetPasswordHash("hash").SetBalance(10).Save(ctx)
			require.NoError(t, err)
			plan, err := tx.Client().SubscriptionPlan.Create().SetPrice(10).SetName("test uncommitted").SetValidityDays(7).Save(ctx)
			require.NoError(t, err)
			callCtx := dbent.NewTxContext(ctx, tx)
			subs := billing.NewSubscriptionService(subscriptionContractEmptyGroups{}, billingpostgres.NewUserSubscriptionRepository(client), billingpostgres.NewSubscriptionMutations(client))
			if explicit {
				subs = billingpostgres.SubscriptionsInTx(tx, nil, billing.DateRuntime{Now: time.Now})
				callCtx = ctx // The adapter already holds the outer transaction.
			}
			result, err := subs.AssignSubscription(callCtx, &billing.AssignSubscriptionInput{UserID: user.ID, PlanID: plan.ID})
			require.NoError(t, err)
			require.NotZero(t, result.ID)
			require.NoError(t, billingpostgres.RedeemInTx(tx).ApplyBalance(ctx, user.ID, 5))
			inside, err := tx.Client().User.Get(ctx, user.ID)
			require.NoError(t, err)
			require.Equal(t, 15.0, inside.Balance)
			require.Equal(t, 5.0, inside.TotalRecharged)
			_, err = client.User.Get(ctx, user.ID)
			require.True(t, dbent.IsNotFound(err), "参与方法不得提前提交调用方用户")
			// End the caller transaction before querying its
			// assigned subscription from the shared client,
			// exposing any participant that committed its writes
			// early.
			require.NoError(t, tx.Rollback())
			_, err = client.UserSubscription.Get(ctx, result.ID)
			require.True(t, dbent.IsNotFound(err), "后续业务失败时权益与资金必须整体回滚")
		})
	}
}

// TestValidityChangeParticipatesInOuterTransaction changes subscription
// duration inside a caller transaction. External reads retain its previous
// expiry before rollback and continue to show that expiry afterward.
func TestValidityChangeParticipatesInOuterTransaction(t *testing.T) {
	ctx := context.Background()
	client := committedEntitlementClient(t)
	user, err := client.User.Create().SetEmail("test-validity@example.com").SetPasswordHash("hash").Save(ctx)
	require.NoError(t, err)
	plan, err := client.SubscriptionPlan.Create().SetPrice(10).SetName("test validity").SetValidityDays(2).Save(ctx)
	require.NoError(t, err)
	// Attempt independent plan cleanup before user truncation clears
	// dependent rows. Its discarded error can leave a plan behind when the
	// delete is rejected.
	t.Cleanup(func() { _ = client.SubscriptionPlan.DeleteOneID(plan.ID).Exec(context.Background()) })
	now := time.Now().UTC().Truncate(time.Second)
	expires := now.Add(48 * time.Hour)
	sub, err := client.UserSubscription.Create().SetUserID(user.ID).SetPlanID(plan.ID).SetStartsAt(now).SetExpiresAt(expires).SetStatus(billing.SubscriptionStatusActive).SetAssignedAt(now).Save(ctx)
	require.NoError(t, err)
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	// Keep rollback available on every exit so the duration mutation
	// remains within this caller transaction until its explicit rollback
	// assertion is reached.
	defer func() { _ = tx.Rollback() }()
	svc := billing.NewSubscriptionService(subscriptionContractEmptyGroups{}, billingpostgres.NewUserSubscriptionRepository(client), billingpostgres.NewSubscriptionMutations(client))
	_, err = svc.SetSubscriptionValidityDays(dbent.NewTxContext(ctx, tx), sub.ID, 30)
	require.NoError(t, err)
	outside, err := client.UserSubscription.Get(ctx, sub.ID)
	require.NoError(t, err)
	require.True(t, outside.ExpiresAt.Equal(expires), "外层提交前不能看到内部有效期写入")
	// Roll back the changed expiry before the second external read so the
	// comparison checks both precommit isolation and removal of this
	// mutation.
	require.NoError(t, tx.Rollback())
	outside, err = client.UserSubscription.Get(ctx, sub.ID)
	require.NoError(t, err)
	require.True(t, outside.ExpiresAt.Equal(expires))
}

// CreateUsage inserts a redemption usage row through the embedded repository,
// then returns an injected error. An insertion failure is returned directly
// before the post-write failure is introduced.
func (r failedRedeemUsage) CreateUsage(ctx context.Context, usage *billing.RedeemCodeUsage) error {
	if err := r.RedeemCodeRepository.CreateUsage(ctx, usage); err != nil {
		return err
	}
	return errors.New("test usage audit failed after actual insert")
}

// InvalidateAuthCacheByUserID atomically increments the observed user
// invalidation count. The counter starts at zero and treats every context and
// user ID alike, allowing tests to compare publication before failure and after
// successful redemption.
func (o *redeemAuthObservation) InvalidateAuthCacheByUserID(context.Context, int64) { o.count.Add(1) }

// TestRedeemEffectsWaitForCommit injects a post-insert usage failure for
// balance, concurrency, and subscription redemption. Grants and usage records
// roll back, invalidation waits, and a later successful redemption invalidates
// once.
func TestRedeemEffectsWaitForCommit(t *testing.T) {
	for _, kind := range []string{billing.RedeemTypeBalance, billing.RedeemTypeConcurrency, billing.RedeemTypeSubscription} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			client := committedEntitlementClient(t)
			user, err := client.User.Create().SetEmail("test-redeem@example.com").SetPasswordHash("hash").SetBalance(10).SetConcurrency(2).Save(ctx)
			require.NoError(t, err)
			plan, err := client.SubscriptionPlan.Create().SetPrice(10).SetName("test redeem").SetValidityDays(7).Save(ctx)
			require.NoError(t, err)
			// Attempt independent plan cleanup before user
			// truncation clears dependent rows. Its discarded error
			// can leave a plan behind when the delete is rejected.
			t.Cleanup(func() { _ = client.SubscriptionPlan.DeleteOneID(plan.ID).Exec(context.Background()) })
			repo := billingpostgres.NewRedeemCodeRepository(client)
			code := &billing.RedeemCode{Code: "TEST-" + kind, Type: kind, Value: 5, Status: billing.StatusUnused, MaxUses: 1, PlanID: &plan.ID}
			require.NoError(t, repo.Create(ctx, code))
			auth := &redeemAuthObservation{}
			subs := billing.NewSubscriptionService(subscriptionContractEmptyGroups{}, billingpostgres.NewUserSubscriptionRepository(client), billingpostgres.NewSubscriptionMutations(client))
			makeService := func(store billing.RedeemCodeRepository) *billing.RedeemService {
				return billing.NewRedeemService(store, billingUsersForContract{identitypostgres.NewUserStore(client, integrationDB)}, subs, nil, nil,
					billingpostgres.NewRedeemMutations(client, billingpostgres.RedeemWriters{Balances: billingpostgres.NewBalanceStore(client), Concurrency: identitypostgres.NewConcurrencyStore(client)}), auth, nil, billing.RedeemRuntime{Now: time.Now})
			}
			// Fail after the real usage insert, then inspect each
			// grant and audit counter before retrying through the
			// successful delegate to check rollback ordering.
			_, err = makeService(failedRedeemUsage{repo}).Redeem(ctx, user.ID, code.Code)
			require.ErrorContains(t, err, "test usage audit failed")
			require.Zero(t, auth.count.Load(), "写失败不得发布认证失效")
			unchanged, err := client.User.Get(ctx, user.ID)
			require.NoError(t, err)
			require.Equal(t, 10.0, unchanged.Balance)
			require.Zero(t, unchanged.TotalRecharged)
			require.Equal(t, 2, unchanged.Concurrency)
			count, err := client.UserSubscription.Query().Where(usersubscription.UserIDEQ(user.ID)).Count(ctx)
			require.NoError(t, err)
			require.Zero(t, count)
			count, err = client.RedeemCodeUsage.Query().Where(redeemcodeusage.RedeemCodeIDEQ(code.ID)).Count(ctx)
			require.NoError(t, err)
			require.Zero(t, count)
			fresh, err := repo.GetByID(ctx, code.ID)
			require.NoError(t, err)
			require.Zero(t, fresh.UsedCount)
			_, err = makeService(repo).Redeem(ctx, user.ID, code.Code)
			require.NoError(t, err)
			require.Equal(t, int32(1), auth.count.Load())
			_, err = makeService(repo).Redeem(ctx, user.ID, code.Code)
			require.Error(t, err)
			require.Equal(t, int32(1), auth.count.Load())
		})
	}
}
