package postgres

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// funding.go writes creative reservation snapshots and allowance markers through
// the SQL transaction supplied by billing. The funds owner commits or rolls back
// these run metadata writes together with its ledger changes, keeping their
// transaction lifetime under billing ownership.

// FundingParticipant writes task funding metadata in the caller-owned transaction.
// The caller commits or rolls back the transaction.
type FundingParticipant struct {
	// tx is borrowed from billing and remains usable through participant writes.
	// The billing owner commits or rolls back the transaction after those writes.
	tx *sql.Tx // borrowed billing SQL transaction
	id string  // funding task run identifier
}

// NewFundingParticipant binds a run identifier to the supplied SQL
// transaction. Billing owns the transaction lifetime and commits or rolls back
// all participant writes together with its funds changes.
func NewFundingParticipant(tx *sql.Tx, id string) *FundingParticipant {
	return &FundingParticipant{tx: tx, id: id}
}

// SaveReservation writes allocation JSON and USD funding amounts for the bound
// run in the borrowed transaction. It returns billing.ErrTaskNotFound when no
// row matches; the billing caller owns commit or rollback.
func (p *FundingParticipant) SaveReservation(ctx context.Context, balance float64, allocations []billing.BillingAllocation, hold, estimated float64) error {
	encoded, err := json.Marshal(allocations)
	if err != nil {
		return err
	}
	result, err := p.tx.ExecContext(ctx, `UPDATE creative_runs
 SET balance_hold_amount = $2, subscription_hold_allocations = $3::jsonb,
 hold_amount = $4, estimated_cost = $5, updated_at = NOW()
 WHERE run_id = $1`, p.id, balance, string(encoded), hold, estimated)
	return fundingAffected(result, err)
}

// SetAllowanceReserved writes the run allowance marker in the borrowed funding
// transaction. An absent run returns billing.ErrTaskNotFound, and successful
// writes become durable only when the billing caller commits.
func (p *FundingParticipant) SetAllowanceReserved(ctx context.Context, reserved bool) error {
	result, err := p.tx.ExecContext(ctx, `UPDATE creative_runs SET allowance_reserved = $2, updated_at = NOW() WHERE run_id = $1`, p.id, reserved)
	return fundingAffected(result, err)
}

// fundingAffected returns an execution error or validates the SQL affected-row
// count. Zero affected rows mean the creative task was missing; every positive
// count succeeds without committing the transaction.
func fundingAffected(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return billing.ErrTaskNotFound
	}
	return nil
}
