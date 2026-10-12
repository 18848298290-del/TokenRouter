//go:build integration

package billing_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/redeemcodeusage"
	"github.com/TokenFlux/TokenRouter/ent/usersubscription"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	paymentpostgres "github.com/TokenFlux/TokenRouter/internal/payment/postgres"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// createEligibilityOrder creates a payment fixture; paid_at marks confirmed payment.
func createEligibilityOrder(t *testing.T, client *ent.Client, user *ent.User, status string, paid bool, amount float64) *ent.PaymentOrder {
	t.Helper()
	builder := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName("test").
		SetAmount(amount).
		SetPayAmount(amount).
		SetRechargeCode(fmt.Sprintf("order-%d-%s", user.ID, status)).
		SetPaymentType("alipay").
		SetPaymentTradeNo("").
		SetStatus(status).
		SetClientIP("127.0.0.1").
		SetSrcHost("localhost").
		SetExpiresAt(time.Now().Add(time.Hour))
	if paid {
		builder.SetPaidAt(time.Now())
	}
	order, err := builder.Save(context.Background())
	require.NoError(t, err)
	// Payment orders retain user snapshots and need their own cleanup.
	t.Cleanup(func() { require.NoError(t, client.PaymentOrder.DeleteOneID(order.ID).Exec(context.Background())) })
	return order
}

// TestRedeemRequiresPayment checks rejected grants, retries after payment, duplicate redemption, and each entitlement type.
func TestRedeemRequiresPayment(t *testing.T) {
	for _, kind := range []string{billing.RedeemTypeBalance, billing.RedeemTypeConcurrency, billing.RedeemTypeSubscription} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			client := committedEntitlementClient(t)
			user, err := client.User.Create().
				SetEmail("test-paid-redeem@example.com").
				SetPasswordHash("hash").
				SetBalance(10).
				SetConcurrency(2).
				SetTotalRecharged(100).
				Save(ctx)
			require.NoError(t, err)
			other, err := client.User.Create().SetEmail("test-other-paid@example.com").SetPasswordHash("hash").Save(ctx)
			require.NoError(t, err)
			createEligibilityOrder(t, client, other, payment.OrderStatusCompleted, true, 10)
			order := createEligibilityOrder(t, client, user, payment.OrderStatusPending, false, 0.01)
			plan, err := client.SubscriptionPlan.Create().SetName("test payment eligibility").SetPrice(1).SetValidityDays(7).Save(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { _ = client.SubscriptionPlan.DeleteOneID(plan.ID).Exec(ctx) })
			repo := billingpostgres.NewRedeemCodeRepository(client)
			admin := billing.NewRedeemAdmin(repo, billingpostgres.NewRedeemAdministrationMutations(client), time.Now)
			codes, err := admin.GenerateRedeemCodes(ctx, &billing.GenerateRedeemCodesInput{Count: 1, Type: kind, Value: 5, PlanID: &plan.ID, RequiresPayment: true})
			require.NoError(t, err)
			code := codes[0]
			stored, err := repo.GetByID(ctx, code.ID)
			require.NoError(t, err)
			require.True(t, stored.RequiresPayment)
			subs := billing.NewSubscriptionService(subscriptionContractEmptyGroups{}, billingpostgres.NewUserSubscriptionRepository(client), billingpostgres.NewSubscriptionMutations(client))
			makeService := func(check func(context.Context, int64) (bool, error)) *billing.RedeemService {
				return billing.NewRedeemService(repo, billingUsersForContract{identitypostgres.NewUserStore(client, integrationDB)}, subs, nil, nil,
					billingpostgres.NewRedeemMutations(client, billingpostgres.RedeemWriters{Balances: billingpostgres.NewBalanceStore(client), Concurrency: identitypostgres.NewConcurrencyStore(client)}), nil, nil, billing.RedeemRuntime{HasPaidOrder: check})
			}
			service := makeService(paymentpostgres.NewOrderStore(client).HasPaidOrder)
			_, err = service.Redeem(ctx, user.ID, code.Code)
			require.ErrorIs(t, err, billing.ErrRedeemPaymentRequired)
			_, err = makeService(nil).Redeem(ctx, user.ID, code.Code)
			require.ErrorContains(t, err, "payment history lookup is unavailable")
			lookupErr := errors.New("payment lookup failed")
			_, err = makeService(func(context.Context, int64) (bool, error) { return false, lookupErr }).Redeem(ctx, user.ID, code.Code)
			require.ErrorIs(t, err, lookupErr)
			unchanged, err := client.User.Get(ctx, user.ID)
			require.NoError(t, err)
			require.Equal(t, 10.0, unchanged.Balance)
			require.Equal(t, 2, unchanged.Concurrency)
			require.Equal(t, 100.0, unchanged.TotalRecharged)
			count, err := client.UserSubscription.Query().Where(usersubscription.UserIDEQ(user.ID)).Count(ctx)
			require.NoError(t, err)
			require.Zero(t, count)
			count, err = client.RedeemCodeUsage.Query().Where(redeemcodeusage.RedeemCodeIDEQ(code.ID)).Count(ctx)
			require.NoError(t, err)
			require.Zero(t, count)
			stored, err = repo.GetByID(ctx, code.ID)
			require.NoError(t, err)
			require.Zero(t, stored.UsedCount)
			require.NoError(t, client.PaymentOrder.UpdateOneID(order.ID).SetStatus(payment.OrderStatusPaid).SetPaidAt(time.Now()).Exec(ctx))
			_, err = service.Redeem(ctx, user.ID, code.Code)
			require.NoError(t, err)
			stored, err = repo.GetByID(ctx, code.ID)
			require.NoError(t, err)
			require.Equal(t, 1, stored.UsedCount)
			require.True(t, stored.RequiresPayment)
			credited, err := client.User.Get(ctx, user.ID)
			require.NoError(t, err)
			switch kind {
			case billing.RedeemTypeBalance:
				require.Equal(t, 15.0, credited.Balance)
			case billing.RedeemTypeConcurrency:
				require.Equal(t, 7, credited.Concurrency)
			case billing.RedeemTypeSubscription:
				count, err := client.UserSubscription.Query().Where(usersubscription.UserIDEQ(user.ID)).Count(ctx)
				require.NoError(t, err)
				require.Equal(t, 1, count)
			}
			_, err = service.Redeem(ctx, user.ID, code.Code)
			require.Error(t, err)
		})
	}
}

// TestPaymentHistoryEligibility requires a positive confirmed payment regardless of later fulfillment or refund status.
func TestPaymentHistoryEligibility(t *testing.T) {
	for _, tc := range []struct {
		status string
		paid   bool
		amount float64
		want   bool
	}{
		{payment.OrderStatusPending, false, 1, false},
		{payment.OrderStatusCancelled, false, 1, false},
		{payment.OrderStatusExpired, false, 1, false},
		{payment.OrderStatusCompleted, false, 1, false},
		{payment.OrderStatusPaid, true, 0, false},
		{payment.OrderStatusPaid, true, 0.01, true},
		{payment.OrderStatusFailed, true, 1, true},
		{payment.OrderStatusCompleted, true, 1, true},
		{payment.OrderStatusRefunded, true, 1, true},
	} {
		t.Run(fmt.Sprintf("%s-%t-%g", tc.status, tc.paid, tc.amount), func(t *testing.T) {
			client := committedEntitlementClient(t)
			user, err := client.User.Create().SetEmail("test-payment-history@example.com").SetPasswordHash("hash").Save(context.Background())
			require.NoError(t, err)
			createEligibilityOrder(t, client, user, tc.status, tc.paid, tc.amount)
			got, err := paymentpostgres.NewOrderStore(client).HasPaidOrder(context.Background(), user.ID)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestRedeemPaymentAdministration checks the default, omitted updates, disabling, invitation restrictions, and migration replay.
func TestRedeemPaymentAdministration(t *testing.T) {
	ctx := context.Background()
	client := committedEntitlementClient(t)
	repo := billingpostgres.NewRedeemCodeRepository(client)
	admin := billing.NewRedeemAdmin(repo, billingpostgres.NewRedeemAdministrationMutations(client), time.Now)
	codes, err := admin.GenerateRedeemCodes(ctx, &billing.GenerateRedeemCodesInput{Count: 1, Type: billing.RedeemTypeBalance, Value: 1})
	require.NoError(t, err)
	id := codes[0].ID
	require.False(t, codes[0].RequiresPayment)
	for _, enabled := range []bool{true, false} {
		code, err := admin.UpdateRedeemCode(ctx, id, &billing.UpdateRedeemCodeInput{RequiresPayment: &enabled})
		require.NoError(t, err)
		require.Equal(t, enabled, code.RequiresPayment)
		code, err = admin.UpdateRedeemCode(ctx, id, &billing.UpdateRedeemCodeInput{})
		require.NoError(t, err)
		require.Equal(t, enabled, code.RequiresPayment)
	}
	_, err = admin.GenerateRedeemCodes(ctx, &billing.GenerateRedeemCodesInput{Count: 1, Type: billing.RedeemTypeInvitation, RequiresPayment: true})
	require.ErrorIs(t, err, billing.ErrRedeemPaymentRequirementUnsupported)
	migration, err := migrations.FS.ReadFile("287_redeem_code_requires_payment.sql")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	code, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	require.False(t, code.RequiresPayment)
}
