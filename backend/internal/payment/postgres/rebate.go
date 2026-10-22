package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"entgo.io/ent/dialect"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/paymentauditlog"
	"github.com/TokenFlux/TokenRouter/internal/payment"
)

// rebate.go coordinates order rebate claims, accrual, and completion audit
// updates within one database transaction.
// An order rebate needs one persistent replay claim because webhook, query,
// and recovery calls can repeat entitlement work. The audit uniqueness key and
// completed applied/skipped observations guard a provisional claim that
// participates in the same transaction as promotion quota changes.
//
// The transaction records skipped handling for nonpositive grants or applied
// detail for a positive grant, then commits both participation and final audit
// state together. Early errors retain rollback cleanup, while ordinary failure
// observations use the separate callback and cannot replace that transaction
// result. Claim SQL chooses dialect-specific placeholders and clock expression
// but keeps values bound. Detail encoding errors are discarded by helpers, so
// transaction callers provide encodable diagnostic values.

// ApplyOrderRebate owns the transaction that claims an order audit action,
// invokes promotion participation, and persists the applied/skipped result.
// Supply a nonnil order and usable runtime rebate factory; the grant receives
// the same Ent transaction context.
//
// The claim query checks completed actions and ignores its uniqueness
// conflict. When it returns no inserted ID, this invocation exits before
// granting. A nonpositive grant changes the claim to skipped, while a positive
// amount records applied detail. Required record or participation failure
// returns before commit with rollback cleanup; ordinary failure audits use the
// separate errorless callback. Successful commit publishes grant and final
// claim result together before fulfillment completion continues.
func (s *OrderStore) ApplyOrderRebate(ctx context.Context, o *payment.Order, basePoints float64) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		s.runtime.Audit(ctx, o.ID, "AFFILIATE_REBATE_FAILED", "system", map[string]any{
			"error": fmt.Sprintf("begin affiliate rebate tx: %v", err),
		})
		return fmt.Errorf("begin affiliate rebate tx: %w", err)
	}
	// Keep the claim, promotion writes, and final audit under this owned
	// transaction. Early returns attempt rollback, while ordinary failure
	// observation is recorded through its separate callback.
	defer func() { _ = tx.Rollback() }()

	// Carry the same Ent transaction into claim and promotion participation so a
	// failed grant or final audit cannot commit only the provisional order claim.
	txCtx := dbent.NewTxContext(ctx, tx)
	claimed, err := s.TryClaimAffiliateRebateAudit(txCtx, tx.Client(), o.ID, basePoints)
	if err != nil {
		s.runtime.Audit(ctx, o.ID, "AFFILIATE_REBATE_FAILED", "system", map[string]any{
			"error": err.Error(),
		})
		return fmt.Errorf("claim affiliate rebate audit: %w", err)
	}
	if !claimed {
		return nil
	}

	sourceOrderID := o.ID
	rebateAmount, err := s.runtime.Rebates(tx).AccrueInviteRebateForOrder(txCtx, o.UserID, basePoints, &sourceOrderID)
	if err != nil {
		s.runtime.Audit(ctx, o.ID, "AFFILIATE_REBATE_FAILED", "system", map[string]any{
			"error": err.Error(),
		})
		return fmt.Errorf("accrue affiliate rebate: %w", err)
	}

	if rebateAmount <= 0 {
		if err := s.UpdateClaimedAffiliateRebateAudit(txCtx, tx.Client(), o.ID, "AFFILIATE_REBATE_SKIPPED", map[string]any{
			"baseAmount": basePoints,
			"reason":     "no inviter bound or rebate amount <= 0",
		}); err != nil {
			s.runtime.Audit(ctx, o.ID, "AFFILIATE_REBATE_FAILED", "system", map[string]any{
				"error": err.Error(),
			})
			return fmt.Errorf("update affiliate rebate skipped audit: %w", err)
		}
	} else if err := s.UpdateClaimedAffiliateRebateAudit(txCtx, tx.Client(), o.ID, "AFFILIATE_REBATE_APPLIED", map[string]any{
		"baseAmount":   basePoints,
		"rebateAmount": rebateAmount,
	}); err != nil {
		s.runtime.Audit(ctx, o.ID, "AFFILIATE_REBATE_FAILED", "system", map[string]any{
			"error": err.Error(),
		})
		return fmt.Errorf("update affiliate rebate applied audit: %w", err)
	}

	// Commit the grant outcome and its final applied or skipped audit together.
	// A later fulfillment retry can then recognize this order's completed
	// rebate handling.
	if err := tx.Commit(); err != nil {
		s.runtime.Audit(ctx, o.ID, "AFFILIATE_REBATE_FAILED", "system", map[string]any{
			"error": fmt.Sprintf("commit affiliate rebate tx: %v", err),
		})
		return fmt.Errorf("commit affiliate rebate tx: %w", err)
	}
	return nil
}

// TryClaimAffiliateRebateAudit executes the order/action claim query through
// the supplied client and reports whether it returned an inserted ID. Nil
// client is an error, and row iteration or scan errors return before the claim
// is accepted.
//
// The query checks completed applied/skipped actions and uses conflict-ignore
// on the audit uniqueness key. ApplyOrderRebate calls it inside the owned
// grant transaction, so the provisional applied action is committed only with
// final rebate handling. JSON marshal errors are discarded for the diagnostic
// detail; callers supply a usable base amount for that encoding.
func (s *OrderStore) TryClaimAffiliateRebateAudit(ctx context.Context, client *dbent.Client, orderID int64, basePoints float64) (bool, error) {
	if client == nil {
		return false, errors.New("nil payment client")
	}
	oid := strconv.FormatInt(orderID, 10)
	// Claim detail is diagnostic while the inserted action supplies replay
	// identity. Encoding errors are discarded here, so callers keep the numeric
	// base encodable.
	detail, _ := json.Marshal(map[string]any{
		"baseAmount": basePoints,
		"status":     "reserved",
	})
	query, args := BuildAffiliateRebateAuditClaimQuery(client, oid, string(detail))
	rows, err := client.QueryContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	// Finish the claim result rows after reading their inserted ID or empty
	// result. This cleanup does not commit the enclosing transaction, and close
	// errors are discarded.
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return false, err
		}
		return false, nil
	}
	var claimID int64
	if err := rows.Scan(&claimID); err != nil {
		return false, err
	}
	return true, nil
}

// BuildAffiliateRebateAuditClaimQuery returns the claim SQL and arguments for
// the supplied client dialect. PostgreSQL uses numbered text-cast placeholders
// and NOW; other dialects use positional placeholders with a repeated order
// argument.
//
// Both forms avoid an existing applied/skipped record and ignore conflicts on
// order/action before returning the new ID. The inserted action begins as
// applied with reserved detail, and UpdateClaimedAffiliateRebateAudit sets its
// final action/detail inside the caller's transaction. Query construction
// performs no persistence and relies on the existing audit uniqueness rule for
// its conflict handling.
func BuildAffiliateRebateAuditClaimQuery(client *dbent.Client, orderID, detail string) (string, []any) {
	nowExpr := PaymentAuditCurrentTimestampExpr(client)
	if PaymentAuditDialect(client) == dialect.Postgres {
		return fmt.Sprintf(`
INSERT INTO payment_audit_logs (order_id, action, detail, operator, created_at)
SELECT $1::text, 'AFFILIATE_REBATE_APPLIED', $2::text, 'system', %s
WHERE NOT EXISTS (
	SELECT 1
	FROM payment_audit_logs
	WHERE order_id = $1::text
	  AND action IN ('AFFILIATE_REBATE_APPLIED', 'AFFILIATE_REBATE_SKIPPED')
)
ON CONFLICT (order_id, action) DO NOTHING
RETURNING id`, nowExpr), []any{orderID, detail}
	}
	return fmt.Sprintf(`
INSERT INTO payment_audit_logs (order_id, action, detail, operator, created_at)
SELECT ?, 'AFFILIATE_REBATE_APPLIED', ?, 'system', %s
WHERE NOT EXISTS (
	SELECT 1
	FROM payment_audit_logs
	WHERE order_id = ?
	  AND action IN ('AFFILIATE_REBATE_APPLIED', 'AFFILIATE_REBATE_SKIPPED')
)
ON CONFLICT (order_id, action) DO NOTHING
RETURNING id`, nowExpr), []any{orderID, detail, orderID}
}

// PaymentAuditCurrentTimestampExpr returns NOW for PostgreSQL and
// CURRENT_TIMESTAMP for other or unavailable client dialects. The claim query
// embeds that trusted expression while keeping order and detail values as
// bound arguments.
func PaymentAuditCurrentTimestampExpr(client *dbent.Client) string {
	if PaymentAuditDialect(client) == dialect.Postgres {
		return "NOW()"
	}
	return "CURRENT_TIMESTAMP"
}

// PaymentAuditDialect returns the supplied client driver's dialect name, or
// empty text when client/driver is absent. Query builders use that empty
// result to select their non-PostgreSQL placeholder and timestamp form.
func PaymentAuditDialect(client *dbent.Client) string {
	if client == nil || client.Driver() == nil {
		return ""
	}
	return client.Driver().Dialect()
}

// UpdateClaimedAffiliateRebateAudit updates the provisional applied action to
// the supplied action and encoded detail for one text-keyed order. Nil client
// and zero matched records return errors; callers use the transaction that
// claimed the record. JSON marshal errors are discarded, so callers provide
// encodable detail values.
func (s *OrderStore) UpdateClaimedAffiliateRebateAudit(ctx context.Context, client *dbent.Client, orderID int64, action string, detail map[string]any) error {
	if client == nil {
		return errors.New("nil payment client")
	}
	oid := strconv.FormatInt(orderID, 10)
	// Final diagnostic detail encoding can fail independently of its action key.
	// Errors are discarded, so the transaction caller supplies encodable values
	// before updating that record.
	detailJSON, _ := json.Marshal(detail)
	updated, err := client.PaymentAuditLog.Update().
		Where(
			paymentauditlog.OrderIDEQ(oid),
			paymentauditlog.ActionEQ("AFFILIATE_REBATE_APPLIED"),
		).
		SetAction(action).
		SetDetail(string(detailJSON)).
		SetOperator("system").
		Save(ctx)
	if err != nil {
		return err
	}
	if updated == 0 {
		return errors.New("affiliate rebate claim log not found")
	}
	return nil
}
