package postgres

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

// provider_usage.go increments provider quotas and clears quota and cooldown
// state.
// Provider quota increments update total usage and each enabled finite
// daily/weekly/monthly period together in JSONB. Stored expiry expressions decide
// whether a period receives just this increment or adds it to its previous
// usage, and fixed resets also advance their next configured timestamp.
//
// The returned total usage and limit reconstruct whether this increment
// crossed the total ceiling, allowing the outer store to request a scheduler
// change event. A missing non-deleted provider row yields false with no error
// on increment; reset uses its affected count to return ErrProviderNotFound.
// The supplied executor owns transaction completion and any publication that
// follows these statement results.

// ProviderUsageParticipant writes provider quota observations through a
// caller-selected SQL/Ent executor.
type ProviderUsageParticipant struct {
	// exec is supplied by the outer use case. Statements use it directly, and any
	// transaction completion or post-commit event ordering remains with that
	// caller.
	exec postgresinfra.Executor // caller-selected provider usage executor
}

// ProviderUsageInTx returns a participant using the supplied executor for each
// statement. The caller supplies a usable connection and owns any transaction,
// commit, rollback and later publication; this constructor opens no
// transaction.
func ProviderUsageInTx(exec postgresinfra.Executor) ProviderUsageParticipant {
	return ProviderUsageParticipant{exec: exec}
}

// Increment adds amount to total usage and positive daily/weekly/monthly quota
// periods, returning a total-ceiling crossing flag. The caller supplies finite
// stored quota values and amount; absent non-deleted rows return false with no
// error after an empty successful result.
//
// Expired enabled periods reset to amount before their new start is recorded,
// with fixed schedules also receiving a next reset timestamp. Crossing
// compares returned usage against a positive total limit and reconstructs the
// prior total by subtracting amount. It identifies this statement's crossing,
// while the caller owns transaction completion and scheduler notification.
func (p ProviderUsageParticipant) Increment(ctx context.Context, id int64, amount float64) (bool, error) {
	// Update all enabled quota periods with total usage in one statement, then
	// use its returned total to identify the event for this increment.
	rows, err := p.exec.QueryContext(ctx,
		providerQuotaIncrementSQL+`
		RETURNING
			COALESCE((extra->>'quota_used')::numeric, 0),
			COALESCE((extra->>'quota_limit')::numeric, 0)`,
		amount, id)
	if err != nil {
		return false, err
	}
	// Close the rows after inspection and discard its cleanup error; query, scan
	// and iteration failures determine the reported increment outcome on this path.
	defer func() { _ = rows.Close() }()

	var newUsed, limit float64
	if rows.Next() {
		if err := rows.Scan(&newUsed, &limit); err != nil {
			return false, err
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}

	// A total-limit event requires a positive ceiling reached from below;
	// subtract this increment from returned usage to reconstruct its prior total.
	return limit > 0 && newUsed >= limit && (newUsed-amount) < limit, nil
}

// ResetAndClearRateLimitCooldown zeros provider quota amounts, removes period
// start/reset metadata and clears rate-limit timestamps. Other extra keys and
// scheduling restrictions retain their stored values; missing non-deleted
// providers return ErrProviderNotFound, and the caller owns the executor's
// transaction lifecycle.
func (p ProviderUsageParticipant) ResetAndClearRateLimitCooldown(ctx context.Context, id int64) error {
	result, err := p.exec.ExecContext(ctx,
		`UPDATE providers SET extra = (
			COALESCE(extra, '{}'::jsonb)
			|| '{"quota_used": 0, "quota_daily_used": 0, "quota_weekly_used": 0, "quota_monthly_used": 0}'::jsonb
		) - 'quota_daily_start' - 'quota_weekly_start' - 'quota_daily_reset_at' - 'quota_weekly_reset_at' - 'quota_monthly_start' - 'quota_monthly_reset_at',
		rate_limited_at = NULL, rate_limit_reset_at = NULL, updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`,
		id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return billing.ErrProviderNotFound
	}
	return nil
}
