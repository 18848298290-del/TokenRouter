package postgres

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// Image-job funding fields are written inside the billing caller's SQL
// transaction. Reservation amounts and subscription allocations are persisted
// together, while the allowance flag is another participant update whose commit
// or rollback remains with that transaction's owner.

// FundingParticipant writes batch-job funding metadata in a caller-owned
// billing transaction.
type FundingParticipant struct {
	// tx is borrowed from the billing settlement store for coordinated writes.
	// FundingParticipant neither commits nor rolls it back; its owner determines
	// the outcome for all funding participants.
	tx *sql.Tx // caller-owned billing SQL transaction
	id string  // bound batch job identifier
}

// NewFundingParticipant binds funding writes to the supplied transaction and
// batch ID. The billing caller supplies a usable transaction, then commits or
// rolls back all task and funding updates together.
func NewFundingParticipant(tx *sql.Tx, id string) *FundingParticipant {
	return &FundingParticipant{tx: tx, id: id}
}

// SaveReservation serializes subscription allocations and stores the job's
// balance hold, total hold, and estimate in its bound transaction. Amounts are
// supplied by billing; a missing job returns ErrTaskNotFound.
func (p *FundingParticipant) SaveReservation(ctx context.Context, balance float64, allocations []billing.BillingAllocation, hold, estimated float64) error {
	encoded, err := json.Marshal(allocations)
	if err != nil {
		return err
	}
	result, err := p.tx.ExecContext(ctx, `UPDATE batch_image_jobs
 SET balance_hold_amount = $2, subscription_hold_allocations = $3::jsonb,
 hold_amount = $4, estimated_cost = $5, updated_at = NOW()
 WHERE batch_id = $1`, p.id, balance, string(encoded), hold, estimated)
	return fundingAffected(result, err)
}

// SetAllowanceReserved updates the job's allowance reservation flag inside the
// bound transaction. A missing row returns ErrTaskNotFound; the caller decides
// whether this flag and other funding changes commit together.
func (p *FundingParticipant) SetAllowanceReserved(ctx context.Context, reserved bool) error {
	result, err := p.tx.ExecContext(ctx, `UPDATE batch_image_jobs SET allowance_reserved = $2, updated_at = NOW() WHERE batch_id = $1`, p.id, reserved)
	return fundingAffected(result, err)
}

// fundingAffected returns an execution or affected-row error before accepting
// a funding update. A successful update affecting no rows becomes ErrTaskNotFound,
// allowing billing to reject funding metadata for a missing task.
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
