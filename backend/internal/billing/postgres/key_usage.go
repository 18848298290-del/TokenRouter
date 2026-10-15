package postgres

import (
	"context"
	"database/sql"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

// key_usage.go persists API-key quota consumption and spending-window usage.
// API Key quotas and five-hour, one-day and seven-day spending windows need
// updates based on stored usage. Quota state updates can mark a positive
// ceiling exhausted as usage reaches it, while window updates replace
// expired-period usage with the current cost and retain unexpired totals.
//
// PostgreSQL transaction time drives these window writes. A missing start
// initializes its timestamp while adding to existing usage, and one/seven-day
// starts use the connection's midnight through date_trunc. Separate reset
// statements clear only present expired windows. Ent and SQL connections are
// supplied independently, so these methods use their bound connections and do
// not select a transaction from context themselves.

const (
	// apiKeyRateLimitUsageSetSQL adds the supplied cost using stored start times
	// and PostgreSQL transaction time. Present expired starts reset usage, while
	// missing starts initialize timestamps and retain previous usage.
	apiKeyRateLimitUsageSetSQL = `			usage_5h = CASE WHEN window_5h_start IS NOT NULL AND window_5h_start + INTERVAL '5 hours' <= NOW() THEN $1 ELSE usage_5h + $1 END,
			usage_1d = CASE WHEN window_1d_start IS NOT NULL AND window_1d_start + INTERVAL '24 hours' <= NOW() THEN $1 ELSE usage_1d + $1 END,
			usage_7d = CASE WHEN window_7d_start IS NOT NULL AND window_7d_start + INTERVAL '7 days' <= NOW() THEN $1 ELSE usage_7d + $1 END,
			window_5h_start = CASE WHEN window_5h_start IS NULL OR window_5h_start + INTERVAL '5 hours' <= NOW() THEN NOW() ELSE window_5h_start END,
			window_1d_start = CASE WHEN window_1d_start IS NULL OR window_1d_start + INTERVAL '24 hours' <= NOW() THEN date_trunc('day', NOW()) ELSE window_1d_start END,
			window_7d_start = CASE WHEN window_7d_start IS NULL OR window_7d_start + INTERVAL '7 days' <= NOW() THEN date_trunc('day', NOW()) ELSE window_7d_start END,
			updated_at = NOW()`
)

// KeyUsageStore writes API Key quota and spending observations through bound
// Ent/SQL connections.
type KeyUsageStore struct {
	// client and sql are independently supplied connections retained by this
	// store. Each method uses its designated connection directly, so the caller
	// arranges shared transaction scope when needed.
	client *dbent.Client // bound Ent quota-update client
	sql    KeyUsageSQL   // bound SQL usage executor
}

// KeyUsageSQL provides execution and row-query operations for API Key usage
// persistence.
type KeyUsageSQL interface {
	// ExecContext executes a key usage statement and returns its affected-row
	// result. The owning method decides whether an empty match is an error and
	// owns connection lifecycle.
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	// QueryContext returns key usage rows for the owning method to decode and
	// close. Query, scan and iteration errors are interpreted by that method with
	// its domain absence rule.
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// NewKeyUsageStore binds caller-owned Ent and SQL connections for usage
// operations. Ent quota increments use client and other methods use db; the
// caller supplies compatible connections and owns any transaction, since this
// store does not select one from ctx.
func NewKeyUsageStore(client *dbent.Client, db KeyUsageSQL) *KeyUsageStore {
	return &KeyUsageStore{client: client, sql: db}
}

// IncrementQuotaUsed adds amount to a non-deleted key's quota usage and
// returns the stored result. Ent not-found becomes ErrAPIKeyNotFound; this
// entry does not mark quota_exhausted, and callers validate the supplied
// amount.
func (r *KeyUsageStore) IncrementQuotaUsed(ctx context.Context, id int64, amount float64) (float64, error) {
	updated, err := r.client.APIKey.UpdateOneID(id).
		Where(apikey.DeletedAtIsNil()).
		AddQuotaUsed(amount).
		Save(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return 0, billing.ErrAPIKeyNotFound
		}
		return 0, err
	}
	return updated.QuotaUsed, nil
}

// IncrementQuotaUsedAndGetState adds amount and returns usage, limit,
// credential and status for a non-deleted key. Missing rows return
// ErrAPIKeyNotFound; the caller supplies a finite amount and owns the SQL
// connection.
//
// The same update marks quota_exhausted when a positive ceiling is reached or
// exceeded, retaining other status values below that condition. Returning the
// state from the update lets callers use the observation that applied this
// increment, including under concurrent usage writes.
func (r *KeyUsageStore) IncrementQuotaUsedAndGetState(ctx context.Context, id int64, amount float64) (*billing.APIKeyQuotaUsageState, error) {
	query := `
		UPDATE api_keys
		SET
			quota_used = quota_used + $1,
			status = CASE
				WHEN quota > 0 AND quota_used + $1 >= quota THEN $2
				ELSE status
			END,
			updated_at = NOW()
		WHERE id = $3 AND deleted_at IS NULL
		RETURNING quota_used, quota, key, status
	`

	state := &billing.APIKeyQuotaUsageState{}
	if err := postgresinfra.ScanSingleRow(ctx, r.sql, query, []any{amount, "quota_exhausted", id}, &state.QuotaUsed, &state.Quota, &state.Key, &state.Status); err != nil {
		if err == sql.ErrNoRows {
			return nil, billing.ErrAPIKeyNotFound
		}
		return nil, err
	}
	return state, nil
}

// IncrementRateLimitUsage adds cost to each non-deleted key spending window
// through the bound SQL connection. The caller supplies a finite cost and owns
// any transaction. A missing row can return success because affected counts
// are not inspected.
//
// Present expired starts reset usage to this cost, while a missing start
// initializes its timestamp and retains old usage plus cost. Five-hour
// replacement starts use transaction NOW; one/seven-day starts use its
// connection-local midnight. The shared SQL updates each amount/start pair
// together, retaining unexpired periods from the stored row.
func (r *KeyUsageStore) IncrementRateLimitUsage(ctx context.Context, id int64, cost float64) error {
	_, err := r.sql.ExecContext(ctx, `
		UPDATE api_keys SET
`+apiKeyRateLimitUsageSetSQL+`
		WHERE id = $2 AND deleted_at IS NULL`,
		cost, id)
	return err
}

// ResetRateLimitWindows clears usage for present expired starts through the
// bound SQL connection. The caller owns any transaction, and a missing
// non-deleted row returns success because affected counts are not inspected.
//
// Missing starts and unexpired windows retain their data. A five-hour
// replacement starts at transaction NOW, while one/seven-day replacements use
// connection-local midnight through date_trunc. All three amount/start pairs
// are updated in one statement, so the reset decisions share PostgreSQL
// transaction time instead of separate application clock observations.
func (r *KeyUsageStore) ResetRateLimitWindows(ctx context.Context, id int64) error {
	_, err := r.sql.ExecContext(ctx, `
		UPDATE api_keys SET
			usage_5h = CASE WHEN window_5h_start IS NOT NULL AND window_5h_start + INTERVAL '5 hours' <= NOW() THEN 0 ELSE usage_5h END,
			window_5h_start = CASE WHEN window_5h_start IS NOT NULL AND window_5h_start + INTERVAL '5 hours' <= NOW() THEN NOW() ELSE window_5h_start END,
			usage_1d = CASE WHEN window_1d_start IS NOT NULL AND window_1d_start + INTERVAL '24 hours' <= NOW() THEN 0 ELSE usage_1d END,
			window_1d_start = CASE WHEN window_1d_start IS NOT NULL AND window_1d_start + INTERVAL '24 hours' <= NOW() THEN date_trunc('day', NOW()) ELSE window_1d_start END,
			usage_7d = CASE WHEN window_7d_start IS NOT NULL AND window_7d_start + INTERVAL '7 days' <= NOW() THEN 0 ELSE usage_7d END,
			window_7d_start = CASE WHEN window_7d_start IS NOT NULL AND window_7d_start + INTERVAL '7 days' <= NOW() THEN date_trunc('day', NOW()) ELSE window_7d_start END,
			updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`,
		id)
	return err
}

// GetRateLimitData reads usage and optional starts for a non-deleted key using
// the bound SQL connection. Query/scan errors propagate, and a successful row
// returns its iteration error. A false rows.Next returns ErrAPIKeyNotFound
// before rows.Err is inspected; close errors replace only an absent earlier
// error.
func (r *KeyUsageStore) GetRateLimitData(ctx context.Context, id int64) (result *billing.APIKeyRateLimitData, err error) {
	rows, err := r.sql.QueryContext(ctx, `
		SELECT usage_5h, usage_1d, usage_7d, window_5h_start, window_1d_start, window_7d_start
		FROM api_keys
		WHERE id = $1 AND deleted_at IS NULL`,
		id)
	if err != nil {
		return nil, err
	}
	// Close decoded rows while retaining any earlier lookup error; a close
	// failure becomes the return error only when no earlier failure exists.
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	if !rows.Next() {
		return nil, billing.ErrAPIKeyNotFound
	}
	data := &billing.APIKeyRateLimitData{}
	if err := rows.Scan(&data.Usage5h, &data.Usage1d, &data.Usage7d, &data.Window5hStart, &data.Window1dStart, &data.Window7dStart); err != nil {
		return nil, err
	}
	return data, rows.Err()
}

// ApplyKeyUsageReset stages requested quota and spending resets on a non-nil
// caller builder. Unselected sections remain untouched; selected windows set
// amounts and either set or clear each optional start. The caller owns
// validation, persistence and surrounding transaction ordering.
func ApplyKeyUsageReset(builder *dbent.APIKeyUpdate, input billing.KeyUsageReset) {
	if input.ResetQuota {
		builder.SetQuotaUsed(input.QuotaUsed)
	}

	if input.ResetWindows {
		builder.
			SetUsage5h(input.Windows.Usage5h).
			SetUsage1d(input.Windows.Usage1d).
			SetUsage7d(input.Windows.Usage7d)

		// A selected reset replaces the complete supplied window state; nil starts
		// therefore clear saved timestamps instead of retaining a previous period's
		// start.
		if input.Windows.Window5hStart != nil {
			builder.SetWindow5hStart(*input.Windows.Window5hStart)
		} else {
			builder.ClearWindow5hStart()
		}
		if input.Windows.Window1dStart != nil {
			builder.SetWindow1dStart(*input.Windows.Window1dStart)
		} else {
			builder.ClearWindow1dStart()
		}
		if input.Windows.Window7dStart != nil {
			builder.SetWindow7dStart(*input.Windows.Window7dStart)
		} else {
			builder.ClearWindow7dStart()
		}
	}
}
