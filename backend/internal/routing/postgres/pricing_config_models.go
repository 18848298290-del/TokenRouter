package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lib/pq"

	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// dbExec is the SQL interface shared by *sql.DB and *sql.Tx.
type dbExec interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (r *PricingConfigStore) ListModelPricing(ctx context.Context, pricingConfigID int64) ([]routing.ModelPricingEntry, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, pricing_config_id, models, billing_mode, price_multiplier, fast_mode_multiplier, fast_multiplier, flex_multiplier, max_reasoning_effort_multiplier, input_price, output_price, cache_write_price, cache_write_1h_price, cache_read_price, image_input_price, image_output_price, per_request_price, time_pricing, created_at, updated_at
		 FROM pricing_config_model_pricing WHERE pricing_config_id = $1 ORDER BY id`, pricingConfigID,
	)
	if err != nil {
		return nil, fmt.Errorf("list model pricing: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result, pricingIDs, err := scanModelPricingRows(rows)
	if err != nil {
		return nil, err
	}

	if len(pricingIDs) > 0 {
		intervalMap, err := r.batchLoadIntervals(ctx, pricingIDs)
		if err != nil {
			return nil, err
		}
		for i := range result {
			result[i].Intervals = intervalMap[result[i].ID]
		}
	}

	return result, nil
}

func (r *PricingConfigStore) CreateModelPricing(ctx context.Context, pricing *routing.ModelPricingEntry) error {
	return createModelPricingExec(ctx, r.db, pricing)
}

func (r *PricingConfigStore) UpdateModelPricing(ctx context.Context, pricing *routing.ModelPricingEntry) error {
	modelsJSON, err := json.Marshal(pricing.Models)
	if err != nil {
		return fmt.Errorf("marshal models: %w", err)
	}
	billingMode := pricing.BillingMode
	if billingMode == "" {
		billingMode = routing.BillingModeToken
	}
	timePricingJSON, err := marshalTimePricingConfig(pricing.TimePricing)
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx,
		`UPDATE pricing_config_model_pricing
			 SET models = $1, billing_mode = $2, price_multiplier = $3, fast_mode_multiplier = $4, fast_multiplier = $5, flex_multiplier = $6, max_reasoning_effort_multiplier = $7, input_price = $8, output_price = $9, cache_write_price = $10, cache_write_1h_price = $11, cache_read_price = $12, image_input_price = $13, image_output_price = $14, per_request_price = $15, time_pricing = $16, updated_at = NOW()
			 WHERE id = $17`,
		modelsJSON, billingMode, pricing.PriceMultiplier, pricing.FastModeMultiplier, pricing.FastMultiplier, pricing.FlexMultiplier, pricing.MaxReasoningEffortMultiplier,
		pricing.InputPrice, pricing.OutputPrice, pricing.CacheWritePrice, pricing.CacheWrite1hPrice, pricing.CacheReadPrice,
		pricing.ImageInputPrice, pricing.ImageOutputPrice, pricing.PerRequestPrice, timePricingJSON, pricing.ID,
	)
	if err != nil {
		return fmt.Errorf("update model pricing: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("pricing entry not found: %d", pricing.ID)
	}
	return nil
}

func (r *PricingConfigStore) DeleteModelPricing(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM pricing_config_model_pricing WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete model pricing: %w", err)
	}
	return nil
}

func (r *PricingConfigStore) ReplaceModelPricing(ctx context.Context, pricingConfigID int64, pricingList []routing.ModelPricingEntry) error {
	return r.runInTx(ctx, func(tx *sql.Tx) error {
		return replaceModelPricingTx(ctx, tx, pricingConfigID, pricingList)
	})
}

// batchLoadModelPricing reads model prices and intervals for multiple configurations.
func (r *PricingConfigStore) batchLoadModelPricing(ctx context.Context, pricingConfigIDs []int64) (map[int64][]routing.ModelPricingEntry, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, pricing_config_id, models, billing_mode, price_multiplier, fast_mode_multiplier, fast_multiplier, flex_multiplier, max_reasoning_effort_multiplier, input_price, output_price, cache_write_price, cache_write_1h_price, cache_read_price, image_input_price, image_output_price, per_request_price, time_pricing, created_at, updated_at
		 FROM pricing_config_model_pricing WHERE pricing_config_id = ANY($1) ORDER BY pricing_config_id, id`,
		pq.Array(pricingConfigIDs),
	)
	if err != nil {
		return nil, fmt.Errorf("batch load model pricing: %w", err)
	}
	defer func() { _ = rows.Close() }()

	allPricing, allPricingIDs, err := scanModelPricingRows(rows)
	if err != nil {
		return nil, err
	}

	pricingMap := make(map[int64][]routing.ModelPricingEntry, len(pricingConfigIDs))
	for _, p := range allPricing {
		pricingMap[p.PricingConfigID] = append(pricingMap[p.PricingConfigID], p)
	}

	if len(allPricingIDs) > 0 {
		intervalMap, err := r.batchLoadIntervals(ctx, allPricingIDs)
		if err != nil {
			return nil, err
		}
		for chID := range pricingMap {
			for i := range pricingMap[chID] {
				pricingMap[chID][i].Intervals = intervalMap[pricingMap[chID][i].ID]
			}
		}
	}

	return pricingMap, nil
}

// batchLoadIntervals reads intervals for multiple price entries.
func (r *PricingConfigStore) batchLoadIntervals(ctx context.Context, pricingIDs []int64) (map[int64][]routing.PricingInterval, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, pricing_id, min_tokens, max_tokens, tier_label,
		        input_price, output_price, cache_write_price, cache_write_1h_price, cache_read_price,
		        input_multiplier, output_multiplier, cache_write_multiplier, cache_read_multiplier,
		        per_request_price, sort_order, created_at, updated_at
		 FROM pricing_config_pricing_intervals
		 WHERE pricing_id = ANY($1) ORDER BY pricing_id, sort_order, id`,
		pq.Array(pricingIDs),
	)
	if err != nil {
		return nil, fmt.Errorf("batch load intervals: %w", err)
	}
	defer func() { _ = rows.Close() }()

	intervalMap := make(map[int64][]routing.PricingInterval, len(pricingIDs))
	for rows.Next() {
		var iv routing.PricingInterval
		if err := rows.Scan(
			&iv.ID, &iv.PricingID, &iv.MinTokens, &iv.MaxTokens, &iv.TierLabel,
			&iv.InputPrice, &iv.OutputPrice, &iv.CacheWritePrice, &iv.CacheWrite1hPrice, &iv.CacheReadPrice,
			&iv.InputMultiplier, &iv.OutputMultiplier, &iv.CacheWriteMultiplier, &iv.CacheReadMultiplier,
			&iv.PerRequestPrice, &iv.SortOrder, &iv.CreatedAt, &iv.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan interval: %w", err)
		}
		intervalMap[iv.PricingID] = append(intervalMap[iv.PricingID], iv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate intervals: %w", err)
	}
	return intervalMap, nil
}

// scanModelPricingRows returns decoded price entries and their IDs.
func scanModelPricingRows(rows *sql.Rows) ([]routing.ModelPricingEntry, []int64, error) {
	var result []routing.ModelPricingEntry
	var pricingIDs []int64
	for rows.Next() {
		var p routing.ModelPricingEntry
		var modelsJSON []byte
		var timePricingJSON []byte
		if err := rows.Scan(
			&p.ID, &p.PricingConfigID, &modelsJSON, &p.BillingMode, &p.PriceMultiplier, &p.FastModeMultiplier, &p.FastMultiplier, &p.FlexMultiplier, &p.MaxReasoningEffortMultiplier,
			&p.InputPrice, &p.OutputPrice, &p.CacheWritePrice, &p.CacheWrite1hPrice, &p.CacheReadPrice,
			&p.ImageInputPrice, &p.ImageOutputPrice, &p.PerRequestPrice, &timePricingJSON, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, nil, fmt.Errorf("scan model pricing: %w", err)
		}
		if err := json.Unmarshal(modelsJSON, &p.Models); err != nil {
			p.Models = []string{}
		}
		timePricing, err := unmarshalTimePricingConfig(timePricingJSON)
		if err != nil {
			return nil, nil, err
		}
		p.TimePricing = timePricing
		if p.FastMultiplier == nil {
			p.FastMultiplier = p.FastModeMultiplier
		}
		pricingIDs = append(pricingIDs, p.ID)
		result = append(result, p)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate model pricing: %w", err)
	}
	return result, pricingIDs, nil
}

func setGroupIDsTx(ctx context.Context, exec dbExec, pricingConfigID int64, groupIDs []int64) error {
	if _, err := exec.ExecContext(ctx, `DELETE FROM pricing_config_groups WHERE pricing_config_id = $1`, pricingConfigID); err != nil {
		return fmt.Errorf("delete old group associations: %w", err)
	}
	if len(groupIDs) == 0 {
		return nil
	}
	_, err := exec.ExecContext(ctx,
		`INSERT INTO pricing_config_groups (pricing_config_id, group_id)
		 SELECT $1, unnest($2::bigint[])`,
		pricingConfigID, pq.Array(groupIDs),
	)
	if err != nil {
		return fmt.Errorf("insert group associations: %w", err)
	}
	return nil
}

func createModelPricingExec(ctx context.Context, exec dbExec, pricing *routing.ModelPricingEntry) error {
	modelsJSON, err := json.Marshal(pricing.Models)
	if err != nil {
		return fmt.Errorf("marshal models: %w", err)
	}
	billingMode := pricing.BillingMode
	if billingMode == "" {
		billingMode = routing.BillingModeToken
	}
	timePricingJSON, err := marshalTimePricingConfig(pricing.TimePricing)
	if err != nil {
		return err
	}
	err = exec.QueryRowContext(ctx,
		`INSERT INTO pricing_config_model_pricing (pricing_config_id, models, billing_mode, price_multiplier, fast_mode_multiplier, fast_multiplier, flex_multiplier, max_reasoning_effort_multiplier, input_price, output_price, cache_write_price, cache_write_1h_price, cache_read_price, image_input_price, image_output_price, per_request_price, time_pricing)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17) RETURNING id, created_at, updated_at`,
		pricing.PricingConfigID, modelsJSON, billingMode,
		pricing.PriceMultiplier, pricing.FastModeMultiplier, pricing.FastMultiplier, pricing.FlexMultiplier, pricing.MaxReasoningEffortMultiplier,
		pricing.InputPrice, pricing.OutputPrice, pricing.CacheWritePrice, pricing.CacheWrite1hPrice, pricing.CacheReadPrice,
		pricing.ImageInputPrice, pricing.ImageOutputPrice, pricing.PerRequestPrice, timePricingJSON,
	).Scan(&pricing.ID, &pricing.CreatedAt, &pricing.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert model pricing: %w", err)
	}

	for i := range pricing.Intervals {
		pricing.Intervals[i].PricingID = pricing.ID
		if err := createIntervalExec(ctx, exec, &pricing.Intervals[i]); err != nil {
			return err
		}
	}

	return nil
}

func marshalTimePricingConfig(config *routing.TimePricingConfig) (any, error) {
	if config == nil || len(config.Periods) == 0 {
		return nil, nil
	}
	data, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("marshal time pricing: %w", err)
	}
	return string(data), nil
}

func unmarshalTimePricingConfig(data []byte) (*routing.TimePricingConfig, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var config routing.TimePricingConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("unmarshal time pricing: %w", err)
	}
	return &config, nil
}

func createIntervalExec(ctx context.Context, exec dbExec, iv *routing.PricingInterval) error {
	return exec.QueryRowContext(ctx,
		`INSERT INTO pricing_config_pricing_intervals
		 (pricing_id, min_tokens, max_tokens, tier_label, input_price, output_price, cache_write_price, cache_write_1h_price, cache_read_price, input_multiplier, output_multiplier, cache_write_multiplier, cache_read_multiplier, per_request_price, sort_order)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) RETURNING id, created_at, updated_at`,
		iv.PricingID, iv.MinTokens, iv.MaxTokens, iv.TierLabel,
		iv.InputPrice, iv.OutputPrice, iv.CacheWritePrice, iv.CacheWrite1hPrice, iv.CacheReadPrice,
		iv.InputMultiplier, iv.OutputMultiplier, iv.CacheWriteMultiplier, iv.CacheReadMultiplier,
		iv.PerRequestPrice, iv.SortOrder,
	).Scan(&iv.ID, &iv.CreatedAt, &iv.UpdatedAt)
}

func replaceModelPricingTx(ctx context.Context, exec dbExec, pricingConfigID int64, pricingList []routing.ModelPricingEntry) error {
	if _, err := exec.ExecContext(ctx, `DELETE FROM pricing_config_model_pricing WHERE pricing_config_id = $1`, pricingConfigID); err != nil {
		return fmt.Errorf("delete old model pricing: %w", err)
	}
	for i := range pricingList {
		pricingList[i].PricingConfigID = pricingConfigID
		if err := createModelPricingExec(ctx, exec, &pricingList[i]); err != nil {
			return fmt.Errorf("insert model pricing: %w", err)
		}
	}
	return nil
}

// isUniqueViolation reports whether err wraps a PostgreSQL unique-constraint violation.
func isUniqueViolation(err error) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr != nil {
		return pqErr.Code == "23505"
	}
	return false
}

// escapeLike escapes literal backslashes, percent signs, and underscores for LIKE or ILIKE.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
