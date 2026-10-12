package postgres

import (
	"context"
	"database/sql"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

const (
	// apiKeyRateLimitUsageSetSQL adds the cost in $1 using the PostgreSQL transaction's NOW() for window times.
	apiKeyRateLimitUsageSetSQL = `			usage_5h = CASE WHEN window_5h_start IS NOT NULL AND window_5h_start + INTERVAL '5 hours' <= NOW() THEN $1 ELSE usage_5h + $1 END,
			usage_1d = CASE WHEN window_1d_start IS NOT NULL AND window_1d_start + INTERVAL '24 hours' <= NOW() THEN $1 ELSE usage_1d + $1 END,
			usage_7d = CASE WHEN window_7d_start IS NOT NULL AND window_7d_start + INTERVAL '7 days' <= NOW() THEN $1 ELSE usage_7d + $1 END,
			window_5h_start = CASE WHEN window_5h_start IS NULL OR window_5h_start + INTERVAL '5 hours' <= NOW() THEN NOW() ELSE window_5h_start END,
			window_1d_start = CASE WHEN window_1d_start IS NULL OR window_1d_start + INTERVAL '24 hours' <= NOW() THEN date_trunc('day', NOW()) ELSE window_1d_start END,
			window_7d_start = CASE WHEN window_7d_start IS NULL OR window_7d_start + INTERVAL '7 days' <= NOW() THEN date_trunc('day', NOW()) ELSE window_7d_start END,
			updated_at = NOW()`
)

// KeyUsageStore writes API Key quota usage and spending windows.
type KeyUsageStore struct {
	client *dbent.Client
	sql    KeyUsageSQL
}
type KeyUsageSQL interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func NewKeyUsageStore(client *dbent.Client, db KeyUsageSQL) *KeyUsageStore {
	return &KeyUsageStore{client: client, sql: db}
}

// IncrementQuotaUsed atomically increments quota_used through Ent and returns the new value.
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

// IncrementQuotaUsedAndGetState atomically increments quota_used, conditionally marks the key
// as quota_exhausted, and returns the latest quota state in one round trip.
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

// IncrementRateLimitUsage atomically adds costs, resetting expired windows
// and initializing missing window starts with transaction time.
func (r *KeyUsageStore) IncrementRateLimitUsage(ctx context.Context, id int64, cost float64) error {
	_, err := r.sql.ExecContext(ctx, `
		UPDATE api_keys SET
`+apiKeyRateLimitUsageSetSQL+`
		WHERE id = $2 AND deleted_at IS NULL`,
		cost, id)
	return err
}

// ResetRateLimitWindows resets expired rate limit windows atomically.
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

// GetRateLimitData returns the current rate limit usage and window start times for an API key.
func (r *KeyUsageStore) GetRateLimitData(ctx context.Context, id int64) (result *billing.APIKeyRateLimitData, err error) {
	rows, err := r.sql.QueryContext(ctx, `
		SELECT usage_5h, usage_1d, usage_7d, window_5h_start, window_1d_start, window_7d_start
		FROM api_keys
		WHERE id = $1 AND deleted_at IS NULL`,
		id)
	if err != nil {
		return nil, err
	}
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

// ApplyKeyUsageReset adds quota and window resets to the caller's update builder.
func ApplyKeyUsageReset(builder *dbent.APIKeyUpdate, input billing.KeyUsageReset) {
	if input.ResetQuota {
		builder.SetQuotaUsed(input.QuotaUsed)
	}

	if input.ResetWindows {
		builder.
			SetUsage5h(input.Windows.Usage5h).
			SetUsage1d(input.Windows.Usage1d).
			SetUsage7d(input.Windows.Usage7d)

		// Nil window starts clear their stored values.
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
