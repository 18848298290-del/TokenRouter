package postgres

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/batchimage"
	batchpg "github.com/TokenFlux/TokenRouter/internal/batchimage/postgres"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

const (
	reserveBatchImageHoldSQL = `(?s)UPDATE users\s+SET balance = balance - \$1,\s+frozen_balance = COALESCE\(frozen_balance, 0\) \+ \$1,\s+updated_at = NOW\(\)\s+WHERE id = \$2 AND deleted_at IS NULL AND balance >= \$1\s+RETURNING balance, frozen_balance`
	captureBatchImageHoldSQL = `(?s)UPDATE users\s+SET balance = balance\s+\+ CASE WHEN \$1 > \$2 THEN \$1 - \$2 ELSE 0 END\s+- CASE WHEN \$2 > \$1 THEN \$2 - \$1 ELSE 0 END,\s+frozen_balance = COALESCE\(frozen_balance, 0\) - \$1,\s+updated_at = NOW\(\)\s+WHERE id = \$3 AND deleted_at IS NULL AND COALESCE\(frozen_balance, 0\) >= \$1\s+RETURNING balance, frozen_balance`
	releaseBatchImageHoldSQL = `(?s)UPDATE users\s+SET balance = balance \+ \$1,\s+frozen_balance = COALESCE\(frozen_balance, 0\) - \$1,\s+updated_at = NOW\(\)\s+WHERE id = \$2 AND deleted_at IS NULL AND COALESCE\(frozen_balance, 0\) >= \$1\s+RETURNING balance, frozen_balance`
	userExistsForBillingSQL  = `(?s)SELECT 1\s+FROM users\s+WHERE id = \$1 AND deleted_at IS NULL`

	usageBillingClaimSQL         = `(?s)INSERT INTO usage_billing_dedup.*ON CONFLICT.*RETURNING id`
	usageBillingArchiveSQL       = `(?s)SELECT request_fingerprint.*FROM usage_billing_dedup_archive`
	usageBillingUserLockSQL      = `(?s)SELECT id\s+FROM users\s+WHERE id = \$1 AND deleted_at IS NULL\s+FOR NO KEY UPDATE`
	usageBillingBalanceDeductSQL = `(?s)WITH locked_user AS.*FOR NO KEY UPDATE.*UPDATE users.*RETURNING users.balance`
	usageBillingAPIKeyQuotaSQL   = `(?s)UPDATE api_keys.*quota_used = quota_used \+ \$1.*RETURNING quota > 0`
)

func TestReserveUsageBillingBatchImageBalance_MovesAvailableToFrozen(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	mock.ExpectQuery(reserveBatchImageHoldSQL).
		WithArgs(2.5, int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"balance", "frozen_balance"}).AddRow(7.5, 2.5))
	mock.ExpectCommit()

	result, err := reserveUsageBillingTaskBalance(ctx, tx, &billing.TaskFundsCommand{UserID: 42, HoldAmount: 2.5})
	require.NoError(t, err)
	require.NotNil(t, result.NewBalance)
	require.NotNil(t, result.FrozenBalance)
	require.InDelta(t, 7.5, *result.NewBalance, 0.000001)
	require.InDelta(t, 2.5, *result.FrozenBalance, 0.000001)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReserveUsageBillingBatchImageBalance_InsufficientBalance(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	mock.ExpectQuery(reserveBatchImageHoldSQL).
		WithArgs(10.0, int64(42)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(userExistsForBillingSQL).
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"?column?"}).AddRow(1))
	mock.ExpectRollback()

	_, err = reserveUsageBillingTaskBalance(ctx, tx, &billing.TaskFundsCommand{UserID: 42, HoldAmount: 10})
	require.ErrorIs(t, err, billing.ErrTaskInsufficientBalance)
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReserveUsageBillingBatchImageBilling_UsesBalanceRateAfterPartialSubscription(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	now := time.Now().UTC()
	windowStart := now.Add(-time.Hour)
	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	mock.ExpectQuery(`(?s)SELECT\s+id,\s+plan_id,.*FROM user_subscriptions.*FOR UPDATE`).
		WithArgs(int64(42), billing.SubscriptionStatusActive, billing.SubscriptionStatusPending, nil, int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "plan_id", "starts_at", "expires_at",
			"daily_window_start", "weekly_window_start", "monthly_window_start",
			"daily_limit_usd", "weekly_limit_usd", "monthly_limit_usd",
			"daily_usage_usd", "weekly_usage_usd", "monthly_usage_usd", "group_rates",
		}).AddRow(
			int64(11), int64(22), now.Add(-24*time.Hour), now.Add(30*24*time.Hour),
			windowStart, windowStart, windowStart,
			1.0, 1.0, 1.0,
			0.8, 0.8, 0.8, `{"7":0.5}`,
		))
	mock.ExpectExec(`(?s)UPDATE user_subscriptions\s+SET.*WHERE id = \$7`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), 1.0, 1.0, 1.0, int64(11)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(reserveBatchImageHoldSQL).
		WithArgs(sqlmock.AnyArg(), int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"balance", "frozen_balance"}).AddRow(3.8, 1.2))
	mock.ExpectExec(`(?s)UPDATE batch_image_jobs\s+SET balance_hold_amount = \$2,.*estimated_cost = \$5`).
		WithArgs("imgbatch_partial_rate", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	groupID := int64(7)
	result, err := reserveUsageBillingTaskBilling(ctx, tx, batchpg.NewFundingParticipant(tx, "imgbatch_partial_rate"), &billing.TaskFundsCommand{
		UserID:                          42,
		GroupID:                         &groupID,
		Task:                            batchimage.FundingReference("imgbatch_partial_rate"),
		HoldAmount:                      0.5,
		PricingSnapshotVersion:          2,
		BaseAmountUSD:                   1,
		SubscriptionRateMultiplier:      1,
		SubscriptionRateMultiplierScale: 1,
		BalanceRateMultiplier:           2,
		SettlementRateScale:             0.5,
	})
	require.NoError(t, err)
	require.InDelta(t, 0.2, result.SubscriptionAmountUSD, 0.000001)
	require.InDelta(t, 1.2, result.BalanceAmountUSD, 0.000001)
	require.InDelta(t, 1.4, result.HoldAmountUSD, 0.000001)
	require.InDelta(t, 0.4, result.EstimatedAmountUSD, 0.000001)
	require.Len(t, result.BillingAllocations, 2)
	require.InDelta(t, 0.4, result.BillingAllocations[0].BaseAmountUSD, 0.000001)
	require.InDelta(t, 0.5, result.BillingAllocations[0].RateMultiplier, 0.000001)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReserveUsageBillingBatchImageBilling_StrictSubscriptionRejectsPartialHold(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	now := time.Now().UTC()
	windowStart := now.Add(-time.Hour)
	preferredID := int64(11)
	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	mock.ExpectQuery(`(?s)SELECT\s+id,\s+plan_id,.*FROM user_subscriptions.*AND NOT EXISTS\s*\(\s*SELECT 1\s+FROM subscription_plan_groups.*FOR UPDATE`).
		WithArgs(int64(42), billing.SubscriptionStatusActive, billing.SubscriptionStatusPending, preferredID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "plan_id", "starts_at", "expires_at",
			"daily_window_start", "weekly_window_start", "monthly_window_start",
			"daily_limit_usd", "weekly_limit_usd", "monthly_limit_usd",
			"daily_usage_usd", "weekly_usage_usd", "monthly_usage_usd", "group_rates",
		}).AddRow(
			preferredID, int64(22), now.Add(-24*time.Hour), now.Add(30*24*time.Hour),
			windowStart, windowStart, windowStart,
			1.0, 1.0, 1.0,
			0.8, 0.8, 0.8, `{}`,
		))
	// Tasks reserve quota before upstream submission and reject insufficient quota.
	mock.ExpectExec(`(?s)UPDATE user_subscriptions\s+SET.*WHERE id = \$7`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), 1.0, 1.0, 1.0, preferredID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()

	_, err = reserveUsageBillingTaskBilling(ctx, tx, batchpg.NewFundingParticipant(tx, "imgbatch_strict_subscription"), &billing.TaskFundsCommand{
		UserID:                  42,
		Task:                    batchimage.FundingReference("imgbatch_strict_subscription"),
		HoldAmount:              0.5,
		APIKeyBillingMode:       billing.APIKeyBillingModeSubscription,
		PreferredSubscriptionID: &preferredID,
	})

	require.ErrorIs(t, err, billing.ErrPreferredSubscriptionInsufficient)
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyUsageBillingEffects_StrictSubscriptionChargesOverflowToBalance(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	now := time.Now().UTC()
	windowStart := now.Add(-time.Hour)
	preferredID := int64(11)
	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	mock.ExpectQuery(`(?s)SELECT\s+id,\s+plan_id,.*FROM user_subscriptions.*AND NOT EXISTS\s*\(\s*SELECT 1\s+FROM subscription_plan_groups.*FOR UPDATE`).
		WithArgs(int64(42), billing.SubscriptionStatusActive, billing.SubscriptionStatusPending, preferredID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "plan_id", "starts_at", "expires_at",
			"daily_window_start", "weekly_window_start", "monthly_window_start",
			"daily_limit_usd", "weekly_limit_usd", "monthly_limit_usd",
			"daily_usage_usd", "weekly_usage_usd", "monthly_usage_usd", "group_rates",
		}).AddRow(
			preferredID, int64(22), now.Add(-24*time.Hour), now.Add(30*24*time.Hour),
			windowStart, windowStart, windowStart,
			1.0, 1.0, 1.0,
			0.8, 0.8, 0.8, `{}`,
		))
	// The pinned subscription stops at its quota; remaining base usage becomes balance debt at the balance multiplier.
	mock.ExpectExec(`(?s)UPDATE user_subscriptions\s+SET.*WHERE id = \$7`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), 1.0, 1.0, 1.0, preferredID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)WITH locked_user AS \(.*SELECT updated.balance, \$1::numeric AS deducted_amount`).
		WithArgs(1.2, int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"balance", "deducted_amount"}).AddRow(-1.2, 1.2))
	mock.ExpectCommit()

	result := &billing.UsageBillingApplyResult{}
	err = (&SettlementStore{}).applyUsageBillingEffects(ctx, tx, &billing.UsageBillingCommand{
		UserID:                          42,
		BillableAmountUSD:               0.5,
		BaseAmountUSD:                   1,
		SubscriptionRateMultiplier:      0.5,
		SubscriptionRateMultiplierScale: 1,
		BalanceRateMultiplier:           2,
		APIKeyBillingMode:               billing.APIKeyBillingModeSubscription,
		PreferredSubscriptionID:         &preferredID,
	}, result)

	require.NoError(t, err)
	require.InDelta(t, 0.2, result.SubscriptionAmountUSD, 0.000001)
	require.InDelta(t, 1.2, result.BalanceAmountUSD, 0.000001)
	require.NotNil(t, result.NewBalance)
	require.InDelta(t, -1.2, *result.NewBalance, 0.000001)
	require.Len(t, result.BillingAllocations, 2)
	require.InDelta(t, 0.2, result.BillingAllocations[0].AmountUSD, 0.000001)
	require.InDelta(t, 1.2, result.BillingAllocations[1].AmountUSD, 0.000001)
	require.NotNil(t, result.EffectiveRateMultiplier)
	require.InDelta(t, 1.4, *result.EffectiveRateMultiplier, 0.000001)
	require.NotNil(t, result.BillingAllocations[0].SubscriptionID)
	require.Equal(t, preferredID, *result.BillingAllocations[0].SubscriptionID)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyUsageBillingEffects_StrictSubscriptionWithoutGroupFiltersRestrictedPlans(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	preferredID := int64(11)
	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	// Without a final group, the pinned subscription query requires an unrestricted plan.
	mock.ExpectQuery(`(?s)SELECT\s+id,\s+plan_id,.*FROM user_subscriptions.*AND NOT EXISTS\s*\(\s*SELECT 1\s+FROM subscription_plan_groups.*FOR UPDATE`).
		WithArgs(int64(42), billing.SubscriptionStatusActive, billing.SubscriptionStatusPending, preferredID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "plan_id", "starts_at", "expires_at",
			"daily_window_start", "weekly_window_start", "monthly_window_start",
			"daily_limit_usd", "weekly_limit_usd", "monthly_limit_usd",
			"daily_usage_usd", "weekly_usage_usd", "monthly_usage_usd", "group_rates",
		}))
	mock.ExpectRollback()

	err = (&SettlementStore{}).applyUsageBillingEffects(ctx, tx, &billing.UsageBillingCommand{
		UserID:                  42,
		BillableAmountUSD:       1,
		APIKeyBillingMode:       billing.APIKeyBillingModeSubscription,
		PreferredSubscriptionID: &preferredID,
	}, &billing.UsageBillingApplyResult{})

	require.ErrorIs(t, err, billing.ErrPreferredSubscriptionInsufficient)
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCaptureUsageBillingBatchImageBalance_ReleasesRemainder(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	mock.ExpectQuery(captureBatchImageHoldSQL).
		WithArgs(1.0, 0.25, int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"balance", "frozen_balance"}).AddRow(9.75, 0.0))
	mock.ExpectCommit()

	result, err := captureUsageBillingTaskBalance(ctx, tx, &billing.TaskFundsCommand{UserID: 42, HoldAmount: 1, ActualAmount: 0.25})
	require.NoError(t, err)
	require.InDelta(t, 9.75, *result.NewBalance, 0.000001)
	require.InDelta(t, 0.0, *result.FrozenBalance, 0.000001)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCaptureUsageBillingBatchImageBalance_RejectsActualCostOverHold(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	mock.ExpectRollback()

	_, err = captureUsageBillingTaskBalance(ctx, tx, &billing.TaskFundsCommand{UserID: 42, HoldAmount: 0.5, ActualAmount: 1})
	require.ErrorIs(t, err, billing.ErrTaskSettlementCostExceedsHold)
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReleaseUsageBillingBatchImageBalance_ReturnsFrozenToAvailable(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	mock.ExpectQuery(`SELECT 1\s+FROM usage_billing_dedup\s+WHERE request_id = \$1 AND api_key_id = \$2`).
		WithArgs(("batch_image_hold:" + "imgbatch_release"), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"?column?"}).AddRow(1))
	mock.ExpectQuery(releaseBatchImageHoldSQL).
		WithArgs(1.0, int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"balance", "frozen_balance"}).AddRow(10.0, 0.0))
	mock.ExpectCommit()

	result, err := releaseUsageBillingTaskBilling(ctx, tx, &billing.TaskFundsCommand{UserID: 42, APIKeyID: 7, Task: batchimage.FundingReference("imgbatch_release"), HoldAmount: 1})
	require.NoError(t, err)
	require.InDelta(t, 10.0, *result.NewBalance, 0.000001)
	require.InDelta(t, 0.0, *result.FrozenBalance, 0.000001)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReleaseUsageBillingBatchImageBalance_SkipsWhenHoldNeverReserved(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	// No hold claim in either deduplication table means the task never committed a reservation.
	// Skip release so another task's frozen funds cannot become available balance.
	mock.ExpectQuery(`SELECT 1\s+FROM usage_billing_dedup\s+WHERE request_id = \$1 AND api_key_id = \$2`).
		WithArgs(("batch_image_hold:" + "imgbatch_phantom"), int64(7)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT 1\s+FROM usage_billing_dedup_archive\s+WHERE request_id = \$1 AND api_key_id = \$2`).
		WithArgs(("batch_image_hold:" + "imgbatch_phantom"), int64(7)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectCommit()

	result, err := releaseUsageBillingTaskBilling(ctx, tx, &billing.TaskFundsCommand{UserID: 42, APIKeyID: 7, Task: batchimage.FundingReference("imgbatch_phantom"), HoldAmount: 1})
	require.NoError(t, err)
	require.Nil(t, result.NewBalance)
	require.Nil(t, result.FrozenBalance)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestNormalizeUsageBillingWindow_ExpiryTailKeepsMonthlyUsage(t *testing.T) {
	now := time.Date(2026, 5, 30, 0, 5, 0, 0, time.UTC)
	startsAt := time.Date(2026, 4, 30, 8, 0, 0, 0, time.UTC)
	expiresAt := time.Date(2026, 5, 30, 8, 0, 0, 0, time.UTC)
	windowStart := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)

	start, used := billing.NormalizeSettlementWindow(usageBillingNullableTimePtr(sql.NullTime{Time: windowStart, Valid: true}), usageBillingNullableFloat64Ptr(sql.NullFloat64{Float64: 100, Valid: true}), 90, timezone.NewCalendar(now.Location()).StartOfDay(now), 30*24*time.Hour, now, startsAt, expiresAt, false)

	require.NotNil(t, start)
	require.Equal(t, windowStart, *start)
	require.Equal(t, 90.0, used, "到期尾段不足完整月窗口时不应清零 monthly usage")
}

func TestNormalizeUsageBillingWindow_ExpiryTailMissingMonthlyWindowStaysNil(t *testing.T) {
	now := time.Date(2026, 5, 30, 0, 5, 0, 0, time.UTC)
	startsAt := time.Date(2026, 4, 30, 8, 0, 0, 0, time.UTC)
	expiresAt := time.Date(2026, 5, 30, 8, 0, 0, 0, time.UTC)

	start, used := billing.NormalizeSettlementWindow(usageBillingNullableTimePtr(sql.NullTime{}), usageBillingNullableFloat64Ptr(sql.NullFloat64{Float64: 100, Valid: true}), 90, timezone.NewCalendar(now.Location()).StartOfDay(now), 30*24*time.Hour, now, startsAt, expiresAt, false)

	require.Nil(t, start, "到期尾段不足完整月窗口时不应补写 monthly window_start")
	require.Equal(t, 90.0, used)
}

func TestNormalizeUsageBillingWindow_DailyCardDoesNotResetAfterMidnight(t *testing.T) {
	now := time.Date(2026, 5, 31, 0, 5, 0, 0, time.UTC)
	startsAt := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	expiresAt := startsAt.Add(24 * time.Hour)
	windowStart := time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC)

	start, used := billing.NormalizeSettlementWindow(usageBillingNullableTimePtr(sql.NullTime{Time: windowStart, Valid: true}), usageBillingNullableFloat64Ptr(sql.NullFloat64{Float64: 10, Valid: true}), 10, timezone.NewCalendar(now.Location()).StartOfDay(now), 24*time.Hour, now, startsAt, expiresAt, true)

	require.NotNil(t, start)
	require.Equal(t, windowStart, *start)
	require.Equal(t, 10.0, used, "1 日卡跨 0 点后不应刷新第二份 daily quota")
}

func TestNormalizeUsageBillingWindow_MultiDayDailyStillResetsWhenFullWindowFits(t *testing.T) {
	now := time.Date(2026, 5, 29, 0, 5, 0, 0, time.UTC)
	startsAt := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	expiresAt := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	windowStart := time.Date(2026, 5, 28, 0, 0, 0, 0, time.UTC)
	resetStart := timezone.NewCalendar(now.Location()).StartOfDay(now)

	start, used := billing.NormalizeSettlementWindow(usageBillingNullableTimePtr(sql.NullTime{Time: windowStart, Valid: true}), usageBillingNullableFloat64Ptr(sql.NullFloat64{Float64: 10, Valid: true}), 10, resetStart, 24*time.Hour, now, startsAt, expiresAt, false)

	require.NotNil(t, start)
	require.Equal(t, resetStart, *start)
	require.Equal(t, 0.0, used, "多日订阅仍应在可覆盖完整日窗口时重置 daily usage")
}

func TestNormalizeUsageBillingWindow_DailyTailResetsWithFiniteOuterLimit(t *testing.T) {
	now := time.Date(2026, 5, 30, 0, 5, 0, 0, time.UTC)
	startsAt := time.Date(2026, 4, 30, 8, 0, 0, 0, time.UTC)
	expiresAt := time.Date(2026, 5, 30, 8, 0, 0, 0, time.UTC)
	windowStart := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	resetStart := timezone.NewCalendar(now.Location()).StartOfDay(now)

	start, used := billing.NormalizeSettlementWindow(usageBillingNullableTimePtr(sql.NullTime{Time: windowStart, Valid: true}), usageBillingNullableFloat64Ptr(sql.NullFloat64{Float64: 10, Valid: true}), 10, resetStart, 24*time.Hour, now, startsAt, expiresAt, true)

	require.NotNil(t, start)
	require.Equal(t, resetStart, *start)
	require.Equal(t, 0.0, used, "有限周或月额度存在时，订阅尾段仍应刷新日额度")
}

func TestNormalizeUsageBillingWindow_WeeklyTailResetsWithFiniteMonthlyLimit(t *testing.T) {
	now := time.Date(2026, 5, 30, 0, 5, 0, 0, time.UTC)
	startsAt := time.Date(2026, 4, 30, 8, 0, 0, 0, time.UTC)
	expiresAt := time.Date(2026, 5, 30, 8, 0, 0, 0, time.UTC)
	windowStart := time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC)
	resetStart := timezone.NewCalendar(now.Location()).StartOfDay(now)

	start, used := billing.NormalizeSettlementWindow(usageBillingNullableTimePtr(sql.NullTime{Time: windowStart, Valid: true}), usageBillingNullableFloat64Ptr(sql.NullFloat64{Float64: 50, Valid: true}), 50, resetStart, 7*24*time.Hour, now, startsAt, expiresAt, true)

	require.NotNil(t, start)
	require.Equal(t, resetStart, *start)
	require.Equal(t, 0.0, used, "有限月额度存在时，订阅尾段仍应刷新周额度")
}

func TestHasFiniteUsageBillingLimit_RequiresPositiveConfiguredLimit(t *testing.T) {
	tests := []struct {
		name  string
		limit sql.NullFloat64
		want  bool
	}{
		{name: "未配置", limit: sql.NullFloat64{}, want: false},
		{name: "零表示无限", limit: sql.NullFloat64{Float64: 0, Valid: true}, want: false},
		{name: "负数表示无限", limit: sql.NullFloat64{Float64: -1, Valid: true}, want: false},
		{name: "正数有限额度", limit: sql.NullFloat64{Float64: 100, Valid: true}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, billing.PositiveSubscriptionLimit(usageBillingNullableFloat64Ptr(tt.limit)))
		})
	}
}

func TestUsageBillingRepositoryApply_DeadlockRestartsWholeTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() {
		_ = db.Close()
	}()

	cmd := &billing.UsageBillingCommand{
		RequestID:          "req-deadlock-retry",
		RequestFingerprint: "fingerprint",
		APIKeyID:           7,
		UserID:             42,
	}
	for attempt := 1; attempt <= 3; attempt++ {
		mock.ExpectBegin()
		mock.ExpectQuery(usageBillingClaimSQL).
			WithArgs(cmd.RequestID, cmd.APIKeyID, cmd.RequestFingerprint).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(attempt)))
		mock.ExpectQuery(usageBillingArchiveSQL).
			WithArgs(cmd.RequestID, cmd.APIKeyID).
			WillReturnError(sql.ErrNoRows)
		userLock := mock.ExpectQuery(usageBillingUserLockSQL).WithArgs(cmd.UserID)
		if attempt < 3 {
			userLock.WillReturnError(&pq.Error{Code: "40P01"})
			mock.ExpectRollback()
			continue
		}
		userLock.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(cmd.UserID))
		mock.ExpectCommit()
	}

	repo := &SettlementStore{db: db}
	result, err := repo.Apply(context.Background(), cmd)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Applied)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLockUsageBillingUser_ReturnsUserNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() {
		_ = db.Close()
	}()

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	mock.ExpectQuery(usageBillingUserLockSQL).
		WithArgs(int64(42)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	err = lockUsageBillingUser(context.Background(), tx, 42)
	require.ErrorIs(t, err, billing.ErrUserNotFound)
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDeductUsageBillingBalance_KeepsForeignKeyCompatibleUserLock(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() {
		_ = db.Close()
	}()

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	mock.ExpectQuery(usageBillingBalanceDeductSQL).
		WithArgs(1.25, int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"balance", "deducted_amount"}).AddRow(8.75, 1.25))
	mock.ExpectRollback()

	newBalance, deductedAmount, err := deductUsageBillingBalance(context.Background(), tx, 42, 1.25)
	require.NoError(t, err)
	require.InDelta(t, 8.75, newBalance, 0.000001)
	require.InDelta(t, 1.25, deductedAmount, 0.000001)
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageBillingMonetaryEffectsQuantizeBeforeSQL(t *testing.T) {
	const rawAmount = 0.000078125
	wantAmount := billing.QuantizeUsageBillingAmount(rawAmount)

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() {
		_ = db.Close()
	}()

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	mock.ExpectQuery(usageBillingBalanceDeductSQL).
		WithArgs(wantAmount, int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"balance", "deducted_amount"}).AddRow(9999.99992187, wantAmount))
	mock.ExpectQuery(usageBillingAPIKeyQuotaSQL).
		WithArgs(wantAmount, int64(7), "active", "quota_exhausted").
		WillReturnRows(sqlmock.NewRows([]string{"exhausted"}).AddRow(false))
	mock.ExpectRollback()

	_, deductedAmount, err := deductUsageBillingBalance(context.Background(), tx, 42, rawAmount)
	require.NoError(t, err)
	_, err = incrementUsageBillingAPIKeyQuota(context.Background(), tx, 7, rawAmount)
	require.NoError(t, err)
	require.Equal(t, wantAmount, deductedAmount)
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestTaskProjectionRequiresRegisteredScope checks that unknown task scopes fail before beginning a transaction.
func TestTaskProjectionRequiresRegisteredScope(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	store := NewSettlementStore(db, timezone.NewCalendar(time.Local), nil)
	_, err = store.Reserve(context.Background(), &billing.TaskFundsCommand{Task: billing.TaskReference{Scope: "unknown", ID: "opaque", ReserveRequestID: "hold:opaque"}, RequestID: "hold:opaque"})
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestBatchImageHoldClaimRequestID checks that billing uses the reservation ID supplied by the task.
func TestBatchImageHoldClaimRequestID(t *testing.T) {
	require.Empty(t, taskHoldClaimRequestID(nil))
	for _, id := range []string{"batch_image_hold:imgbatch_x", "creative_hold:crun_x"} {
		require.Equal(t, id, taskHoldClaimRequestID(&billing.TaskFundsCommand{Task: billing.TaskReference{ID: "opaque", ReserveRequestID: id}}))
	}
}
