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

// PostgreSQL refund scenarios bind balance rights and order/audit writes to the
// same Ent transaction while using a local provider transport. State assertions
// read through the base client after workflow calls, comparing the payer
// balance, order status, and retained success-audit count.

// refundBalanceParticipant binds supported refund balance rights to the
// caller's Ent transaction.
type refundBalanceParticipant struct {
	// RefundRights supplies promoted methods beyond deduction and
	// compensation. It remains nil in these balance fixtures, so
	// unsupported rights would panic if invoked.
	payment.RefundRights
	// balances is the BalanceInTx store selected by the refund transaction
	// factory. Both overrides use that same store, and transaction
	// completion belongs to refund storage.
	balances *billingpostgres.BalanceStore
}

// assertRefundPostgresState reads the user, order, and refund-success audit
// count through the supplied client and context. It fails the case on read
// errors or mismatched expected values; callers use the base client after
// transaction completion.
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

// testEntClient returns the package-shared migrated PostgreSQL Ent client and
// registers cascading users-table truncation. Callers finish their workers
// before cleanup and serialize fixture lifetimes because that truncation
// affects all shared users.
func testEntClient(t *testing.T) *dbent.Client {
	t.Helper()
	// Truncate shared users and cascading dependencies after this committed
	// case, resetting IDs before later cases; independent orders and audits
	// register their own cleanup.
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), "TRUNCATE users RESTART IDENTITY CASCADE")
		require.NoError(t, err)
	})
	return integrationEntClient
}

// DeductBalance delegates the requested refund deduction to the
// transaction-bound balance store and returns its actual deducted amount. The
// caller's refund transaction owns completion, and missing or invalid storage
// results propagate unchanged.
func (p refundBalanceParticipant) DeductBalance(ctx context.Context, id int64, amount float64) (float64, error) {
	return p.balances.DeductRefundBalance(ctx, id, amount)
}

// CompensateBalance delegates restoration of the supplied refund amount to the
// transaction-bound balance store. The caller owns transaction completion, and
// this wrapper returns the balance store's failure unchanged.
func (p refundBalanceParticipant) CompensateBalance(ctx context.Context, id int64, amount float64) error {
	return p.balances.CompensateRefundBalance(ctx, id, amount)
}

// newPostgresRefundWorkflow constructs provider bindings and refund storage
// using the supplied shared Ent client. Each refund transaction gets balance
// rights from BalanceInTx; other promoted rights remain unsupported in this
// fixture.
//
// Provider instance and provider selection delegate to the configured payment
// adapters, while the runtime user callback reads current balance through the
// base client. The caller owns client and provider-transport cleanup, including
// the local Stripe backend override used by these scenarios.
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
