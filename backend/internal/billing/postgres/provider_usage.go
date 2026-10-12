package postgres

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

// ProviderUsageParticipant writes provider usage through the supplied connection.
// Cross-module operations pass the caller's Ent or SQL transaction connection.
type ProviderUsageParticipant struct{ exec postgresinfra.Executor }

func ProviderUsageInTx(exec postgresinfra.Executor) ProviderUsageParticipant {
	return ProviderUsageParticipant{exec: exec}
}

// Increment atomically adds total usage and enabled daily/weekly usage, returning whether the total limit was crossed.
// Expired daily and weekly windows reset before adding the amount.
// Both rolling and fixed reset schedules are supported.
func (p ProviderUsageParticipant) Increment(ctx context.Context, id int64, amount float64) (bool, error) {
	rows, err := p.exec.QueryContext(ctx,
		`UPDATE providers SET extra = (
			COALESCE(extra, '{}'::jsonb)
			-- 总额度：始终递增
			|| jsonb_build_object('quota_used', COALESCE((extra->>'quota_used')::numeric, 0) + $1)
			-- 日额度：仅在 quota_daily_limit > 0 时处理
			|| CASE WHEN COALESCE((extra->>'quota_daily_limit')::numeric, 0) > 0 THEN
				jsonb_build_object(
					'quota_daily_used',
					CASE WHEN `+DailyExpiredExpr+`
					THEN $1
					ELSE COALESCE((extra->>'quota_daily_used')::numeric, 0) + $1 END,
					'quota_daily_start',
					CASE WHEN `+DailyExpiredExpr+`
					THEN `+NowUTC+`
					ELSE COALESCE(extra->>'quota_daily_start', `+NowUTC+`) END
				)
				-- 固定模式重置时更新下次重置时间
				|| CASE WHEN `+DailyExpiredExpr+` AND `+NextDailyResetAtExpr+` IS NOT NULL
				   THEN jsonb_build_object('quota_daily_reset_at', `+NextDailyResetAtExpr+`)
				   ELSE '{}'::jsonb END
			ELSE '{}'::jsonb END
			-- 周额度：仅在 quota_weekly_limit > 0 时处理
			|| CASE WHEN COALESCE((extra->>'quota_weekly_limit')::numeric, 0) > 0 THEN
				jsonb_build_object(
					'quota_weekly_used',
					CASE WHEN `+WeeklyExpiredExpr+`
					THEN $1
					ELSE COALESCE((extra->>'quota_weekly_used')::numeric, 0) + $1 END,
					'quota_weekly_start',
					CASE WHEN `+WeeklyExpiredExpr+`
					THEN `+NowUTC+`
					ELSE COALESCE(extra->>'quota_weekly_start', `+NowUTC+`) END
				)
				-- 固定模式重置时更新下次重置时间
				|| CASE WHEN `+WeeklyExpiredExpr+` AND `+NextWeeklyResetAtExpr+` IS NOT NULL
				   THEN jsonb_build_object('quota_weekly_reset_at', `+NextWeeklyResetAtExpr+`)
				   ELSE '{}'::jsonb END
			ELSE '{}'::jsonb END
		), updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
		RETURNING
			COALESCE((extra->>'quota_used')::numeric, 0),
			COALESCE((extra->>'quota_limit')::numeric, 0)`,
		amount, id)
	if err != nil {
		return false, err
	}
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

	return limit > 0 && newUsed >= limit && (newUsed-amount) < limit, nil
}

// ResetAndClearRateLimitCooldown resets usage and quota cooldown fields while retaining other scheduling restrictions.
func (p ProviderUsageParticipant) ResetAndClearRateLimitCooldown(ctx context.Context, id int64) error {
	result, err := p.exec.ExecContext(ctx,
		`UPDATE providers SET extra = (
			COALESCE(extra, '{}'::jsonb)
			|| '{"quota_used": 0, "quota_daily_used": 0, "quota_weekly_used": 0}'::jsonb
		) - 'quota_daily_start' - 'quota_weekly_start' - 'quota_daily_reset_at' - 'quota_weekly_reset_at',
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
