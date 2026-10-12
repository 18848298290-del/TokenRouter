//go:build integration

package payment_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	stripe "github.com/stripe/stripe-go/v85"

	"github.com/TokenFlux/TokenRouter/ent/paymentauditlog"
	"github.com/TokenFlux/TokenRouter/internal/payment"
)

// TestPaymentRefundPostgresAtomicity checks transactional refund confirmation through the public workflow.
// Provider HTTP uses a test server; deductions, order claims, audit writes, and commits use production storage.
func TestPaymentRefundPostgresAtomicity(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		name := "audit_failure_rolls_back_then_retry"
		if concurrent {
			name = "concurrent_confirmation_deducts_once"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			client := testEntClient(t)
			var calls atomic.Int32
			ready := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/refunds/re_test" {
					http.Error(w, "unexpected refund request", http.StatusBadRequest)
					return
				}
				// Both callers read the pending state before competing for the PostgreSQL claim.
				if calls.Add(1) == 2 && concurrent {
					close(ready)
				}
				if concurrent {
					select {
					case <-ready:
					case <-r.Context().Done():
						return
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]string{
					"id": "re_test", "object": "refund", "status": "succeeded",
				})
			}))
			t.Cleanup(server.Close)
			originalBackend := stripe.GetBackend(stripe.APIBackend)
			stripe.SetBackend(stripe.APIBackend, stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{
				URL: stripe.String(server.URL), HTTPClient: server.Client(), MaxNetworkRetries: stripe.Int64(0),
			}))
			t.Cleanup(func() { stripe.SetBackend(stripe.APIBackend, originalBackend) })

			user, err := client.User.Create().SetEmail("test-refund@example.com").
				SetPasswordHash("test-hash").SetUsername("test-refund").SetBalance(100).Save(ctx)
			require.NoError(t, err)
			instance, err := client.PaymentProviderInstance.Create().SetProviderKey(payment.TypeStripe).
				SetName("test-refund").SetConfig(`{"secretKey":"test-only-key","currency":"USD"}`).
				SetSupportedTypes(payment.TypeStripe).SetRefundEnabled(true).Save(ctx)
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, client.PaymentProviderInstance.DeleteOneID(instance.ID).Exec(context.Background()))
			})
			order, err := client.PaymentOrder.Create().
				SetUserID(user.ID).SetUserEmail(user.Email).SetUserName(user.Username).
				SetAmount(50).SetPayAmount(50).SetRechargeCode("test-refund").
				SetOutTradeNo("test-refund").SetPaymentType(payment.TypeStripe).
				SetPaymentTradeNo("pi_test").SetOrderType(payment.OrderTypeBalance).
				SetStatus(payment.OrderStatusRefundPending).SetRefundAmount(50).
				SetExpiresAt(time.Now().Add(time.Hour)).SetPaidAt(time.Now()).
				SetClientIP("127.0.0.1").SetSrcHost("test.local").
				SetProviderInstanceID(strconv.FormatInt(instance.ID, 10)).Save(ctx)
			require.NoError(t, err)
			orderID := strconv.FormatInt(order.ID, 10)
			// Orders and audits need separate cleanup from the users cascade.
			t.Cleanup(func() {
				_, cleanupErr := client.PaymentAuditLog.Delete().Where(paymentauditlog.OrderIDEQ(orderID)).Exec(context.Background())
				require.NoError(t, cleanupErr)
				require.NoError(t, client.PaymentOrder.DeleteOneID(order.ID).Exec(context.Background()))
			})
			_, err = client.PaymentAuditLog.Create().SetOrderID(orderID).SetAction("REFUND_PENDING").
				SetOperator("admin").SetDetail(`{"refundID":"re_test","deductBalance":true,"balanceDeducted":50,"deductionRollbackOK":true}`).Save(ctx)
			require.NoError(t, err)
			svc := newPostgresRefundWorkflow(client)

			if concurrent {
				type outcome struct {
					result *payment.RefundResult
					err    error
				}
				results := make(chan outcome, 2)
				for range 2 {
					go func() {
						result, callErr := svc.QueryAndFinalizeRefund(ctx, order.ID)
						results <- outcome{result: result, err: callErr}
					}()
				}
				successes := 0
				for range 2 {
					got := <-results
					if got.err == nil {
						require.True(t, got.result.Success)
						require.Equal(t, 50.0, got.result.BalanceDeducted)
						successes++
					} else {
						require.ErrorContains(t, got.err, "order status changed")
					}
				}
				require.Equal(t, 1, successes)
			} else {
				// The trigger checks that the deduction is visible in this transaction, then fails the final audit write.
				functionName := fmt.Sprintf("test_refund_audit_failure_%d", order.ID)
				triggerName := functionName + "_trigger"
				t.Cleanup(func() {
					_, cleanupErr := integrationDB.ExecContext(context.Background(), fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON payment_audit_logs", triggerName))
					require.NoError(t, cleanupErr)
					_, cleanupErr = integrationDB.ExecContext(context.Background(), fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", functionName))
					require.NoError(t, cleanupErr)
				})
				_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(`
CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.order_id = '%s' AND NEW.action = 'REFUND_SUCCESS' THEN
		IF (SELECT balance FROM users WHERE id = %d) <> 50 THEN
			RAISE EXCEPTION 'deduction was not visible before audit';
		END IF;
		RAISE EXCEPTION 'forced refund audit failure';
	END IF;
	RETURN NEW;
END;
$$`, functionName, orderID, user.ID))
				require.NoError(t, err)
				_, err = integrationDB.ExecContext(ctx, fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON payment_audit_logs FOR EACH ROW EXECUTE FUNCTION %s()", triggerName, functionName))
				require.NoError(t, err)

				result, callErr := svc.QueryAndFinalizeRefund(ctx, order.ID)
				require.Nil(t, result)
				require.ErrorContains(t, callErr, "forced refund audit failure")
				assertRefundPostgresState(t, ctx, client, user.ID, order.ID, 100, payment.OrderStatusRefundPending, 0)

				_, err = integrationDB.ExecContext(ctx, fmt.Sprintf("DROP TRIGGER %s ON payment_audit_logs", triggerName))
				require.NoError(t, err)
				result, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
				require.NoError(t, err)
				require.True(t, result.Success)
				require.Equal(t, 50.0, result.BalanceDeducted)
			}

			assertRefundPostgresState(t, ctx, client, user.ID, order.ID, 50, payment.OrderStatusRefunded, 1)
			// Confirming the completed refund again must fail and leave the balance unchanged.
			_, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
			require.Error(t, err)
			assertRefundPostgresState(t, ctx, client, user.ID, order.ID, 50, payment.OrderStatusRefunded, 1)
		})
	}
}
