package postgres_test

import (
	"context"
	"errors"
	"testing"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	"github.com/TokenFlux/TokenRouter/internal/payment/postgres"
)

// rebateGrant returns a configured amount and records transaction participation.
type rebateGrant struct {
	amount float64 // configured granted amount
	calls  int     // observed grant invocations
	tx     *ent.Tx // observed transaction owner
}

// AccrueInviteRebateForOrder records the supplied transaction and returns the
// configured amount. The fixture performs no writes, allowing the test to
// isolate required audit persistence and commit outcomes.
func (g *rebateGrant) AccrueInviteRebateForOrder(ctx context.Context, _ int64, _ float64, _ *int64) (float64, error) {
	g.calls++
	g.tx = ent.TxFromContext(ctx)
	return g.amount, nil
}

// TestApplyOrderRebateAuditAndCommitOutcomes supplies skipped and applied grants
// with successful, rejected-audit, and rejected-commit outcomes. Each attempt
// grants once in its transaction and preserves the corresponding error and audit.
func TestApplyOrderRebateAuditAndCommitOutcomes(t *testing.T) {
	for _, amount := range []float64{0, 20} {
		for _, failure := range []string{"", "audit", "commit"} {
			action := "AFFILIATE_REBATE_APPLIED"
			prefix := "update affiliate rebate applied audit: "
			if amount == 0 {
				action = "AFFILIATE_REBATE_SKIPPED"
				prefix = "update affiliate rebate skipped audit: "
			}
			t.Run(action+"/"+failure, func(t *testing.T) {
				db, mock, err := sqlmock.New()
				require.NoError(t, err)
				client := ent.NewClient(ent.Driver(sql.OpenDB(dialect.Postgres, db)))
				t.Cleanup(func() { _ = client.Close() })
				grant := &rebateGrant{amount: amount}
				var owner *ent.Tx
				var failureDetails []string
				store := postgres.NewOrderStore(client, postgres.OrderStoreRuntime{
					Rebates: func(tx *ent.Tx) postgres.OrderRebates {
						owner = tx
						return grant
					},
					Audit: func(_ context.Context, id int64, gotAction, operator string, detail map[string]any) {
						require.EqualValues(t, 42, id)
						require.Equal(t, "AFFILIATE_REBATE_FAILED", gotAction)
						require.Equal(t, "system", operator)
						message, ok := detail["error"].(string)
						require.True(t, ok)
						failureDetails = append(failureDetails, message)
					},
				})
				mock.ExpectBegin()
				mock.ExpectQuery("INSERT INTO payment_audit_logs").
					WithArgs("42", sqlmock.AnyArg()).
					WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
				update := mock.ExpectExec(`UPDATE "payment_audit_logs" SET`).
					WithArgs(action, sqlmock.AnyArg(), "system", "42", "AFFILIATE_REBATE_APPLIED")
				cause := errors.New("planned persistence failure")
				// An audit failure rolls back before commit. Once commit is attempted,
				// database/sql finishes the transaction even when the driver reports failure.
				if failure == "audit" {
					update.WillReturnError(cause)
					mock.ExpectRollback()
				} else {
					update.WillReturnResult(sqlmock.NewResult(0, 1))
					commit := mock.ExpectCommit()
					if failure == "commit" {
						commit.WillReturnError(cause)
					}
				}
				err = store.ApplyOrderRebate(context.Background(), &payment.Order{ID: 42, UserID: 7}, 100)
				require.Equal(t, 1, grant.calls)
				require.Same(t, owner, grant.tx)
				if failure == "" {
					require.NoError(t, err)
					require.Empty(t, failureDetails)
				} else {
					require.ErrorIs(t, err, cause)
					if failure == "commit" {
						prefix = "commit affiliate rebate tx: "
						require.Equal(t, []string{prefix + cause.Error()}, failureDetails)
					} else {
						require.Equal(t, []string{cause.Error()}, failureDetails)
					}
					require.EqualError(t, err, prefix+cause.Error())
				}
				require.NoError(t, mock.ExpectationsWereMet())
			})
		}
	}
}
