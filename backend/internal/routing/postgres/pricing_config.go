package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/lib/pq"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

type PricingConfigStore struct {
	db *sql.DB
}

// NewPricingConfigStore creates a SQL-backed pricing configuration store.
func NewPricingConfigStore(db *sql.DB) *PricingConfigStore {
	return &PricingConfigStore{db: db}
}

// runInTx commits fn on success and rolls back if fn fails.
func (r *PricingConfigStore) runInTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *PricingConfigStore) Create(ctx context.Context, pricingConfig *routing.PricingConfig) error {
	return r.runInTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx,
			`INSERT INTO pricing_configs (name, description, status, billing_model_source,
          peak_rate_enabled, peak_start, peak_end, peak_rate_multiplier,
          long_context_pricing_enabled, free_openai_fast,
          batch_image_discount_multiplier, batch_image_hold_multiplier,
          web_search_price_per_call, search_price_per_1k,
          audio_realtime_price_per_min, audio_tts_price_per_million_chars, audio_stt_price_per_hour)
         VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
			 RETURNING id,
          created_at, updated_at`,
			pricingConfig.Name, pricingConfig.Description, pricingConfig.Status, pricingConfig.BillingModelSource,
			pricingConfig.PeakRateEnabled, pricingConfig.PeakStart, pricingConfig.PeakEnd, pricingConfig.PeakRateMultiplier,
			pricingConfig.LongContextPricingEnabled, pricingConfig.FreeOpenAIFast,
			pricingConfig.BatchImageDiscountMultiplier, pricingConfig.BatchImageHoldMultiplier,
			pricingConfig.WebSearchPricePerCall, pricingConfig.SearchPricePer1k,
			pricingConfig.AudioRealtimePricePerMin, pricingConfig.AudioTTSPricePerMillionChars, pricingConfig.AudioSTTPricePerHour,
		).Scan(&pricingConfig.ID, &pricingConfig.CreatedAt, &pricingConfig.UpdatedAt)
		if err != nil {
			if isUniqueViolation(err) {
				return routing.ErrPricingConfigExists
			}
			return fmt.Errorf("insert price configuration: %w", err)
		}

		if len(pricingConfig.GroupIDs) > 0 {
			if err := setGroupIDsTx(ctx, tx, pricingConfig.ID, pricingConfig.GroupIDs); err != nil {
				return err
			}
		}

		if len(pricingConfig.ModelPricing) > 0 {
			if err := replaceModelPricingTx(ctx, tx, pricingConfig.ID, pricingConfig.ModelPricing); err != nil {
				return err
			}
		}

		if len(pricingConfig.ProviderStatsPricingRules) > 0 {
			if err := replaceProviderStatsPricingRulesTx(ctx, tx, pricingConfig.ID, pricingConfig.ProviderStatsPricingRules); err != nil {
				return err
			}
		}

		return nil
	})
}

func (r *PricingConfigStore) GetByID(ctx context.Context, id int64) (*routing.PricingConfig, error) {
	ch := &routing.PricingConfig{}

	err := r.db.QueryRowContext(ctx,
		`SELECT id, name, description, status, billing_model_source,
          peak_rate_enabled, peak_start, peak_end, peak_rate_multiplier,
          long_context_pricing_enabled, free_openai_fast,
          batch_image_discount_multiplier, batch_image_hold_multiplier,
          web_search_price_per_call, search_price_per_1k,
          audio_realtime_price_per_min, audio_tts_price_per_million_chars, audio_stt_price_per_hour,
          created_at, updated_at
		 FROM pricing_configs WHERE id = $1`, id,
	).Scan(&ch.ID, &ch.Name, &ch.Description, &ch.Status, &ch.BillingModelSource,
		&ch.PeakRateEnabled, &ch.PeakStart, &ch.PeakEnd, &ch.PeakRateMultiplier,
		&ch.LongContextPricingEnabled, &ch.FreeOpenAIFast,
		&ch.BatchImageDiscountMultiplier, &ch.BatchImageHoldMultiplier,
		&ch.WebSearchPricePerCall, &ch.SearchPricePer1k,
		&ch.AudioRealtimePricePerMin, &ch.AudioTTSPricePerMillionChars, &ch.AudioSTTPricePerHour,
		&ch.CreatedAt, &ch.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, routing.ErrPricingConfigNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get price configuration: %w", err)
	}

	groupIDs, err := r.GetGroupIDs(ctx, id)
	if err != nil {
		return nil, err
	}
	ch.GroupIDs = groupIDs

	pricing, err := r.ListModelPricing(ctx, id)
	if err != nil {
		return nil, err
	}
	ch.ModelPricing = pricing

	statsPricingRules, err := r.loadProviderStatsPricingRules(ctx, id)
	if err != nil {
		return nil, err
	}
	ch.ProviderStatsPricingRules = statsPricingRules

	return ch, nil
}

func (r *PricingConfigStore) Update(ctx context.Context, pricingConfig *routing.PricingConfig) error {
	return r.runInTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`UPDATE pricing_configs SET name = $1, description = $2, status = $3, billing_model_source = $4,
          peak_rate_enabled = $5, peak_start = $6, peak_end = $7, peak_rate_multiplier = $8,
          long_context_pricing_enabled = $9, free_openai_fast = $10,
          batch_image_discount_multiplier = $11, batch_image_hold_multiplier = $12,
          web_search_price_per_call = $13, search_price_per_1k = $14,
          audio_realtime_price_per_min = $15, audio_tts_price_per_million_chars = $16, audio_stt_price_per_hour = $17, updated_at = NOW()
			 WHERE id = $18`,
			pricingConfig.Name, pricingConfig.Description, pricingConfig.Status, pricingConfig.BillingModelSource,
			pricingConfig.PeakRateEnabled, pricingConfig.PeakStart, pricingConfig.PeakEnd, pricingConfig.PeakRateMultiplier,
			pricingConfig.LongContextPricingEnabled, pricingConfig.FreeOpenAIFast,
			pricingConfig.BatchImageDiscountMultiplier, pricingConfig.BatchImageHoldMultiplier,
			pricingConfig.WebSearchPricePerCall, pricingConfig.SearchPricePer1k,
			pricingConfig.AudioRealtimePricePerMin, pricingConfig.AudioTTSPricePerMillionChars, pricingConfig.AudioSTTPricePerHour, pricingConfig.ID,
		)
		if err != nil {
			if isUniqueViolation(err) {
				return routing.ErrPricingConfigExists
			}
			return fmt.Errorf("update price configuration: %w", err)
		}
		rows, _ := result.RowsAffected()
		if rows == 0 {
			return routing.ErrPricingConfigNotFound
		}

		// Nil leaves associations unchanged; an empty slice clears them.
		if pricingConfig.GroupIDs != nil {
			if err := setGroupIDsTx(ctx, tx, pricingConfig.ID, pricingConfig.GroupIDs); err != nil {
				return err
			}
		}

		if pricingConfig.ModelPricing != nil {
			if err := replaceModelPricingTx(ctx, tx, pricingConfig.ID, pricingConfig.ModelPricing); err != nil {
				return err
			}
		}

		if pricingConfig.ProviderStatsPricingRules != nil {
			if err := replaceProviderStatsPricingRulesTx(ctx, tx, pricingConfig.ID, pricingConfig.ProviderStatsPricingRules); err != nil {
				return err
			}
		}

		return nil
	})
}

func (r *PricingConfigStore) Delete(ctx context.Context, id int64) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM pricing_configs WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete price configuration: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return routing.ErrPricingConfigNotFound
	}
	return nil
}

func (r *PricingConfigStore) List(ctx context.Context, params pagination.PaginationParams, status, search string) ([]routing.PricingConfig, *pagination.PaginationResult, error) {
	where := []string{"1=1"}
	args := []any{}
	argIdx := 1

	if status != "" {
		where = append(where, fmt.Sprintf("c.status = $%d", argIdx))
		args = append(args, status)
		argIdx++
	}
	if search != "" {
		where = append(where, fmt.Sprintf("(c.name ILIKE $%d OR c.description ILIKE $%d)", argIdx, argIdx))
		args = append(args, "%"+escapeLike(search)+"%")
		argIdx++
	}

	whereClause := strings.Join(where, " AND ")

	var total int64
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM pricing_configs c WHERE %s", whereClause)
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, nil, fmt.Errorf("count price configurations: %w", err)
	}

	pageSize := params.Limit() // Clamped to [1, 100].
	page := max(params.Page, 1)
	offset := (page - 1) * pageSize

	dataQuery := fmt.Sprintf(
		`SELECT c.id, c.name, c.description, c.status, c.billing_model_source,
          c.peak_rate_enabled, c.peak_start, c.peak_end, c.peak_rate_multiplier,
          c.long_context_pricing_enabled, c.free_openai_fast,
          c.batch_image_discount_multiplier, c.batch_image_hold_multiplier,
          c.web_search_price_per_call, c.search_price_per_1k,
          c.audio_realtime_price_per_min, c.audio_tts_price_per_million_chars, c.audio_stt_price_per_hour,
          c.created_at, c.updated_at
		 FROM pricing_configs c WHERE %s ORDER BY %s LIMIT $%d OFFSET $%d`,
		whereClause, pricingConfigListOrderBy(params), argIdx, argIdx+1,
	)
	args = append(args, pageSize, offset)

	rows, err := r.db.QueryContext(ctx, dataQuery, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("query price configurations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var pricingConfigs []routing.PricingConfig
	var pricingConfigIDs []int64
	for rows.Next() {
		var ch routing.PricingConfig

		if err := rows.Scan(&ch.ID, &ch.Name, &ch.Description, &ch.Status, &ch.BillingModelSource,
			&ch.PeakRateEnabled, &ch.PeakStart, &ch.PeakEnd, &ch.PeakRateMultiplier,
			&ch.LongContextPricingEnabled, &ch.FreeOpenAIFast,
			&ch.BatchImageDiscountMultiplier, &ch.BatchImageHoldMultiplier,
			&ch.WebSearchPricePerCall, &ch.SearchPricePer1k,
			&ch.AudioRealtimePricePerMin, &ch.AudioTTSPricePerMillionChars, &ch.AudioSTTPricePerHour,
			&ch.CreatedAt, &ch.UpdatedAt); err != nil {
			return nil, nil, fmt.Errorf("scan price configuration: %w", err)
		}

		pricingConfigs = append(pricingConfigs, ch)
		pricingConfigIDs = append(pricingConfigIDs, ch.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate price configurations: %w", err)
	}

	// Batch-load associations and prices to avoid one query per configuration.
	if len(pricingConfigIDs) > 0 {
		groupMap, err := r.batchLoadGroupIDs(ctx, pricingConfigIDs)
		if err != nil {
			return nil, nil, err
		}
		pricingMap, err := r.batchLoadModelPricing(ctx, pricingConfigIDs)
		if err != nil {
			return nil, nil, err
		}
		statsRulesMap, err := r.batchLoadProviderStatsPricingRules(ctx, pricingConfigIDs)
		if err != nil {
			return nil, nil, err
		}
		for i := range pricingConfigs {
			pricingConfigs[i].GroupIDs = groupMap[pricingConfigs[i].ID]
			pricingConfigs[i].ModelPricing = pricingMap[pricingConfigs[i].ID]
			pricingConfigs[i].ProviderStatsPricingRules = statsRulesMap[pricingConfigs[i].ID]
		}
	}

	pages := 0
	if total > 0 {
		pages = int((total + int64(pageSize) - 1) / int64(pageSize))
	}

	paginationResult := &pagination.PaginationResult{
		Total:    total,
		Page:     page,
		PageSize: pageSize,
		Pages:    pages,
	}

	return pricingConfigs, paginationResult, nil
}

func pricingConfigListOrderBy(params pagination.PaginationParams) string {
	sortBy := strings.ToLower(strings.TrimSpace(params.SortBy))
	sortOrder := strings.ToUpper(params.NormalizedSortOrder(pagination.SortOrderAsc))

	var column string
	switch sortBy {
	case "":
		column = "c.id"
		sortOrder = "ASC"
	case "id":
		column = "c.id"
	case "name":
		column = "c.name"
	case "status":
		column = "c.status"
	case "created_at":
		column = "c.created_at"
	default:
		column = "c.id"
		sortOrder = "ASC"
	}

	return fmt.Sprintf("%s %s, c.id %s", column, sortOrder, sortOrder)
}

func (r *PricingConfigStore) ListAll(ctx context.Context) ([]routing.PricingConfig, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, description, status, billing_model_source,
          peak_rate_enabled, peak_start, peak_end, peak_rate_multiplier,
          long_context_pricing_enabled, free_openai_fast,
          batch_image_discount_multiplier, batch_image_hold_multiplier,
          web_search_price_per_call, search_price_per_1k,
          audio_realtime_price_per_min, audio_tts_price_per_million_chars, audio_stt_price_per_hour,
          created_at, updated_at FROM pricing_configs ORDER BY id`,
	)
	if err != nil {
		return nil, fmt.Errorf("query all price configurations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var pricingConfigs []routing.PricingConfig
	var pricingConfigIDs []int64
	for rows.Next() {
		var ch routing.PricingConfig

		if err := rows.Scan(&ch.ID, &ch.Name, &ch.Description, &ch.Status, &ch.BillingModelSource,
			&ch.PeakRateEnabled, &ch.PeakStart, &ch.PeakEnd, &ch.PeakRateMultiplier,
			&ch.LongContextPricingEnabled, &ch.FreeOpenAIFast,
			&ch.BatchImageDiscountMultiplier, &ch.BatchImageHoldMultiplier,
			&ch.WebSearchPricePerCall, &ch.SearchPricePer1k,
			&ch.AudioRealtimePricePerMin, &ch.AudioTTSPricePerMillionChars, &ch.AudioSTTPricePerHour,
			&ch.CreatedAt, &ch.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan price configuration: %w", err)
		}

		pricingConfigs = append(pricingConfigs, ch)
		pricingConfigIDs = append(pricingConfigIDs, ch.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate price configurations: %w", err)
	}

	if len(pricingConfigIDs) == 0 {
		return pricingConfigs, nil
	}

	groupMap, err := r.batchLoadGroupIDs(ctx, pricingConfigIDs)
	if err != nil {
		return nil, err
	}

	pricingMap, err := r.batchLoadModelPricing(ctx, pricingConfigIDs)
	if err != nil {
		return nil, err
	}

	statsRulesMap, err := r.batchLoadProviderStatsPricingRules(ctx, pricingConfigIDs)
	if err != nil {
		return nil, err
	}

	for i := range pricingConfigs {
		pricingConfigs[i].GroupIDs = groupMap[pricingConfigs[i].ID]
		pricingConfigs[i].ModelPricing = pricingMap[pricingConfigs[i].ID]
		pricingConfigs[i].ProviderStatsPricingRules = statsRulesMap[pricingConfigs[i].ID]
	}

	return pricingConfigs, nil
}

// batchLoadGroupIDs reads group IDs for multiple pricing configurations.
func (r *PricingConfigStore) batchLoadGroupIDs(ctx context.Context, pricingConfigIDs []int64) (map[int64][]int64, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT pricing_config_id, group_id FROM pricing_config_groups
		 WHERE pricing_config_id = ANY($1) ORDER BY pricing_config_id, group_id`,
		pq.Array(pricingConfigIDs),
	)
	if err != nil {
		return nil, fmt.Errorf("batch load group ids: %w", err)
	}
	defer func() { _ = rows.Close() }()

	groupMap := make(map[int64][]int64, len(pricingConfigIDs))
	for rows.Next() {
		var pricingConfigID, groupID int64
		if err := rows.Scan(&pricingConfigID, &groupID); err != nil {
			return nil, fmt.Errorf("scan group id: %w", err)
		}
		groupMap[pricingConfigID] = append(groupMap[pricingConfigID], groupID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate group ids: %w", err)
	}
	return groupMap, nil
}

func (r *PricingConfigStore) ExistsByName(ctx context.Context, name string) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM pricing_configs WHERE name = $1)`, name,
	).Scan(&exists)
	return exists, err
}

func (r *PricingConfigStore) ExistsByNameExcluding(ctx context.Context, name string, excludeID int64) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM pricing_configs WHERE name = $1 AND id != $2)`, name, excludeID,
	).Scan(&exists)
	return exists, err
}

func (r *PricingConfigStore) GetGroupIDs(ctx context.Context, pricingConfigID int64) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT group_id FROM pricing_config_groups WHERE pricing_config_id = $1 ORDER BY group_id`, pricingConfigID,
	)
	if err != nil {
		return nil, fmt.Errorf("get group ids: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan group id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate group ids: %w", err)
	}
	return ids, nil
}

func (r *PricingConfigStore) SetGroupIDs(ctx context.Context, pricingConfigID int64, groupIDs []int64) error {
	return setGroupIDsTx(ctx, r.db, pricingConfigID, groupIDs)
}

func (r *PricingConfigStore) GetPricingConfigIDByGroupID(ctx context.Context, groupID int64) (int64, error) {
	var pricingConfigID int64
	err := r.db.QueryRowContext(ctx,
		`SELECT pricing_config_id FROM pricing_config_groups WHERE group_id = $1`, groupID,
	).Scan(&pricingConfigID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return pricingConfigID, err
}

func (r *PricingConfigStore) GetGroupsInOtherPricingConfigs(ctx context.Context, pricingConfigID int64, groupIDs []int64) ([]int64, error) {
	if len(groupIDs) == 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT group_id FROM pricing_config_groups WHERE group_id = ANY($1) AND pricing_config_id != $2`,
		pq.Array(groupIDs), pricingConfigID,
	)
	if err != nil {
		return nil, fmt.Errorf("get groups in other price configurations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var conflicting []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan conflicting group id: %w", err)
		}
		conflicting = append(conflicting, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conflicting group ids: %w", err)
	}
	return conflicting, nil
}
