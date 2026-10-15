package postgres

import (
	"context"
	"database/sql"
	"errors"
)

// risk_status.go provides account locking and status changes within
// moderation-owned SQL transactions.
// Risk decisions lock the account row and update disabled status through the
// caller-supplied SQL transaction. The participant reports whether a row
// existed or an update changed status, leaving transaction completion to
// moderation storage that owns the surrounding risk write.

// RiskStatusParticipant accesses account risk status through a caller-owned
// SQL transaction.
type RiskStatusParticipant struct {
	// tx belongs to the moderation writer. That caller commits or rolls back the
	// lock and status update with its other risk records.
	tx *sql.Tx
}

// NewRiskStatusParticipant retains the transaction supplied by moderation
// storage for account risk updates. The caller keeps that transaction open
// through row locking and mutation, then owns commit or rollback.
func NewRiskStatusParticipant(tx *sql.Tx) *RiskStatusParticipant {
	return &RiskStatusParticipant{tx: tx}
}

// LockUser locks the supplied account row and returns its stored status and
// presence. Missing rows return false with no error; the caller-owned
// transaction retains the row lock until completion.
func (p *RiskStatusParticipant) LockUser(ctx context.Context, id int64) (string, bool, error) {
	var status string
	err := p.tx.QueryRowContext(ctx, `SELECT status FROM users WHERE id = $1 FOR UPDATE`, id).Scan(&status)
	// Missing account rows report false presence with no storage error.
	// Risk-status callers can distinguish that absence from a failed lock/read
	// while retaining their surrounding transaction.
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return status, err == nil, err
}

// SetDisabled writes disabled status when the row currently has a different
// value. The boolean reports a successful positive affected count; execution
// errors propagate, while affected-count errors return false with nil error.
func (p *RiskStatusParticipant) SetDisabled(ctx context.Context, id int64) (bool, error) {
	result, err := p.tx.ExecContext(ctx, `UPDATE users SET status = $2, updated_at = NOW() WHERE id = $1 AND status <> $2`, id, "disabled")
	if err != nil {
		return false, err
	}
	// A failed affected-count read leaves the mutation outcome unavailable. This
	// participant reports false with nil error, so callers cannot distinguish
	// that case from an unchanged row.
	n, err := result.RowsAffected()
	return err == nil && n > 0, nil
}
