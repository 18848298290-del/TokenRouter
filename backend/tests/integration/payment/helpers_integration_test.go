//go:build integration

package payment_test

import (
	"context"
	"strconv"
	"testing"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/paymentauditlog"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	paymentpostgres "github.com/TokenFlux/TokenRouter/internal/payment/postgres"
	paymentadapter "github.com/TokenFlux/TokenRouter/internal/payment/provider"
)

type refundBalanceParticipant struct {
	payment.RefundRights
	balances *billingpostgres.BalanceStore
}

// assertRefundPostgresState reads balance, order status, and success audits outside the refund transaction.
func assertRefundPostgresState(t *testing.T, ctx context.Context, client *dbent.Client, userID, orderID int64, balance float64, status string, audits int) {
	t.Helper()
	user, err := client.User.Get(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, balance, user.Balance)
	order, err := client.PaymentOrder.Get(ctx, orderID)
	require.NoError(t, err)
	require.Equal(t, status, order.Status)
	count, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(orderID, 10)), paymentauditlog.ActionEQ("REFUND_SUCCESS")).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, audits, count)
}

func testEntClient(t *testing.T) *dbent.Client {
	t.Helper()
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), "TRUNCATE users RESTART IDENTITY CASCADE")
		require.NoError(t, err)
	})
	return integrationEntClient
}

func (p refundBalanceParticipant) DeductBalance(ctx context.Context, id int64, amount float64) (float64, error) {
	return p.balances.DeductRefundBalance(ctx, id, amount)
}

func (p refundBalanceParticipant) CompensateBalance(ctx context.Context, id int64, amount float64) error {
	return p.balances.CompensateRefundBalance(ctx, id, amount)
}

// newPostgresRefundWorkflow binds balance deductions and refund writes to the same Ent transaction.
func newPostgresRefundWorkflow(client *dbent.Client) *payment.RefundWorkflow {
	instances := paymentpostgres.NewInstanceStore(client)
	bindings := payment.NewProviderBindings(instances, payment.NewRegistry(), payment.NewDefaultLoadBalancer(instances, nil), payment.BindingRuntime{Factory: paymentadapter.CreateProvider, RegistryFactory: paymentadapter.CreateProvider}, false)
	store := paymentpostgres.NewRefundStore(client, func(tx *dbent.Tx) payment.RefundRights {
		return refundBalanceParticipant{balances: billingpostgres.BalanceInTx(tx)}
	})
	return payment.NewRefundWorkflow(store, payment.RefundRuntime{
		Instance: bindings.GetRefundOrderProviderInstance,
		Provider: bindings.GetRefundProvider,
		User: func(ctx context.Context, id int64) (*payment.RefundUser, error) {
			user, err := client.User.Get(ctx, id)
			if err != nil {
				return nil, err
			}
			return &payment.RefundUser{Balance: user.Balance}, nil
		},
	})
}
