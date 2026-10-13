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

// A shared pricing configuration owns group associations, customer model
// prices, and provider-cost rules. Aggregate creation and update put these
// records in one transaction so a child failure rolls back the accompanying
// configuration write. Update treats a nil collection as retained state and an
// empty supplied collection as a complete clear.
//
// Configuration reads attach groups and both price trees through separate
// queries, including batch queries for lists. Their assembled result is not a
// transaction snapshot across those reads. Standalone association and
// model-price methods use the connection pool directly; their multi-statement
// recovery differs from aggregate saves. The application owns the pool, while
// service callers manage validation and cache invalidation after mutations.

// PricingConfigStore persists shared pricing configurations and their group and
// price records.
type PricingConfigStore struct {
	// db is the application's borrowed pool for reads and standalone writes.
	// Aggregate mutations create their own transactions, and pool shutdown remains
	// with the application.
	db *sql.DB
}

// NewPricingConfigStore retains the application pool for pricing reads and
// writes. Construction performs no query or migration; the caller keeps the
// pool open and closes it after store users finish.
func NewPricingConfigStore(db *sql.DB) *PricingConfigStore {
	return &PricingConfigStore{db: db}
}

// runInTx runs a nonnil callback in a newly opened transaction and commits
// after it succeeds. The callback uses tx for every write that needs this
// commit scope. A callback error is returned directly, while a begin failure is
// wrapped.
//
// Deferred rollback handles callback failures before the commit attempt,
// undoing earlier writes in that transaction. The helper owns completion, so
// the callback leaves commit and rollback to it. A commit error is returned to
// the caller, whose in-memory callback effects remain; callers re-read stored
// state when the commit outcome is uncertain.
func (r *PricingConfigStore) runInTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	// Roll back a callback error after earlier writes. Once Commit attempts
	// completion, the transaction may already be closed, so deferred cleanup
	// cannot guarantee recovery from a commit error.
	defer func() { _ = tx.Rollback() }()

	if err := fn(tx); err != nil {
		return err
	}
	// Commit after the callback accepts every dependent write, making its parent
	// records and associations visible through one transaction completion.
	return tx.Commit()
}

// Create persists a nonnil configuration, supplied group associations, and both
// price trees in one new transaction. Configuration insertion supplies its ID
// and timestamps before dependent writes. A unique configuration name returns
// ErrPricingConfigExists.
//
// Association and child-tree writes run on the same transaction as the
// configuration row, so a dependent-write failure returns through rollback.
// Empty child lists add no records. Generated configuration and nested price
// IDs can remain assigned in the input after failure, so callers reload
// committed state before treating those values as persisted.
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

		// Store group links and both price trees after obtaining the configuration
		// ID, using this transaction so later child failure also rolls back the
		// parent.
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

// GetByID reads a configuration and attaches its groups, model prices, and
// provider-cost rules. A missing parent returns ErrPricingConfigNotFound, and
// child-read failures discard the configuration; these separate queries do not
// share a transaction snapshot.
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

// Update writes a nonnil configuration's scalar settings and selectively
// replaces its child collections in one transaction. Nil GroupIDs,
// ModelPricing, or ProviderStatsPricingRules retain their stored collections;
// supplied empty slices clear them. Missing parent rows and conflicting names
// return their routing errors.
//
// The scalar write precedes replacement, allowing any association or
// nested-price failure to roll back the whole update. Replacement creates new
// child identities and assigns them into supplied slices before commit. The
// input's UpdatedAt is not refreshed by the scalar UPDATE, and a failed
// transaction leaves those in-memory child assignments available to the caller.
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
		// Use the affected count to detect a missing configuration. Any accompanying
		// RowsAffected error is discarded, so the following check uses its reported
		// count even when obtaining that count failed.
		rows, _ := result.RowsAffected()
		if rows == 0 {
			return routing.ErrPricingConfigNotFound
		}

		// Apply replacements only for supplied collections; deleting and rebuilding
		// their child rows stays inside the scalar-setting transaction for rollback
		// on conflicts.
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

// Delete removes a configuration, cascading its stored group links and price
// children through foreign keys. A successful statement affecting zero rows
// returns ErrPricingConfigNotFound; callers invalidate pricing and
// authentication caches after success.
func (r *PricingConfigStore) Delete(ctx context.Context, id int64) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM pricing_configs WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete price configuration: %w", err)
	}
	// Use the affected count to report an absent configuration. An accompanying
	// RowsAffected error is discarded, so zero still selects the missing result
	// after the DELETE has executed.
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return routing.ErrPricingConfigNotFound
	}
	return nil
}

// List reads a filtered page with group associations and both price trees
// loaded in batches. Search treats wildcard characters as literal text, page
// bounds use pagination limits, and a count or child-read failure returns no
// page.
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

	// Bound each page so its child-data lookups stay limited.
	pageSize := params.Limit()
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

	// Load children for the selected page in batches, keeping one query per child
	// level instead of repeating the same reads for each configuration.
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
		// Count a partially filled final page in the total.
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

// pricingConfigListOrderBy returns an order clause from supported column names
// and a normalized direction. Empty or unknown keys use ascending ID, while
// supported keys add ID ties in the same direction.
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

// ListAll reads every configuration in ascending ID order with associated
// groups and both price trees. Empty tables return a nil slice, and any
// child-read failure discards the result used for service cache building.
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

// batchLoadGroupIDs reads associated groups for requested configurations in
// ascending group-ID order. Configurations with no associations have no map
// entry; query, scan, or iteration failures discard the result map.
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

// ExistsByName reports whether any configuration has the supplied exact stored
// name.
func (r *PricingConfigStore) ExistsByName(ctx context.Context, name string) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM pricing_configs WHERE name = $1)`, name,
	).Scan(&exists)
	return exists, err
}

// ExistsByNameExcluding reports whether another configuration has the supplied
// exact stored name.
func (r *PricingConfigStore) ExistsByNameExcluding(ctx context.Context, name string, excludeID int64) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM pricing_configs WHERE name = $1 AND id != $2)`, name, excludeID,
	).Scan(&exists)
	return exists, err
}

// GetGroupIDs reads associated group IDs in ascending order for one
// configuration. An absent configuration or an empty association set yields a
// nil slice; database failures return to the caller.
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

// SetGroupIDs replaces all group links through the connection pool, with empty
// input clearing them. Deletion and insertion are separate committed
// statements, so an insert failure can leave the configuration's associations
// cleared.
func (r *PricingConfigStore) SetGroupIDs(ctx context.Context, pricingConfigID int64, groupIDs []int64) error {
	return setGroupIDsTx(ctx, r.db, pricingConfigID, groupIDs)
}

// GetPricingConfigIDByGroupID reads the configuration bound to a group
// regardless of configuration status. An unassociated group returns zero
// without error, letting the caller decide whether default pricing applies.
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

// GetGroupsInOtherPricingConfigs returns requested groups already bound to a
// different configuration. Empty input skips SQL, and result order is
// unspecified; service callers use the set to reject association conflicts
// before saving.
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
