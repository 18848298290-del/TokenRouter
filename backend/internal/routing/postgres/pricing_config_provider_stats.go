package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/lib/pq"

	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// batchLoadProviderStatsPricingRules reads provider-cost rules and their prices for multiple configurations.
func (r *PricingConfigStore) batchLoadProviderStatsPricingRules(ctx context.Context, pricingConfigIDs []int64) (map[int64][]routing.ProviderStatsPricingRule, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, pricing_config_id, name, group_ids, provider_ids, sort_order, created_at, updated_at
		 FROM pricing_config_provider_stats_pricing_rules WHERE pricing_config_id = ANY($1) ORDER BY pricing_config_id, sort_order, id`,
		pq.Array(pricingConfigIDs),
	)
	if err != nil {
		return nil, fmt.Errorf("batch load provider stats pricing rules: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var allRules []routing.ProviderStatsPricingRule
	var ruleIDs []int64
	for rows.Next() {
		var rule routing.ProviderStatsPricingRule
		if err := rows.Scan(
			&rule.ID, &rule.PricingConfigID, &rule.Name,
			pq.Array(&rule.GroupIDs), pq.Array(&rule.ProviderIDs),
			&rule.SortOrder, &rule.CreatedAt, &rule.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan provider stats pricing rule: %w", err)
		}
		ruleIDs = append(ruleIDs, rule.ID)
		allRules = append(allRules, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate provider stats pricing rules: %w", err)
	}

	pricingMap, err := r.batchLoadProviderStatsModelPricing(ctx, ruleIDs)
	if err != nil {
		return nil, err
	}

	result := make(map[int64][]routing.ProviderStatsPricingRule, len(pricingConfigIDs))
	for i := range allRules {
		allRules[i].Pricing = pricingMap[allRules[i].ID]
		result[allRules[i].PricingConfigID] = append(result[allRules[i].PricingConfigID], allRules[i])
	}

	return result, nil
}

// batchLoadProviderStatsModelPricing reads model prices for multiple provider-cost rules.
func (r *PricingConfigStore) batchLoadProviderStatsModelPricing(ctx context.Context, ruleIDs []int64) (map[int64][]routing.ModelPricingEntry, error) {
	if len(ruleIDs) == 0 {
		return make(map[int64][]routing.ModelPricingEntry), nil
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT id, rule_id, models, billing_mode, price_multiplier, input_price, output_price,
		        cache_write_price, cache_write_1h_price, cache_read_price, image_output_price, per_request_price, created_at, updated_at
		 FROM pricing_config_provider_stats_model_pricing WHERE rule_id = ANY($1) ORDER BY rule_id, id`,
		pq.Array(ruleIDs),
	)
	if err != nil {
		return nil, fmt.Errorf("batch load provider stats model pricing: %w", err)
	}
	defer func() { _ = rows.Close() }()

	pricingMap := make(map[int64][]routing.ModelPricingEntry, len(ruleIDs))
	for rows.Next() {
		var p routing.ModelPricingEntry
		var ruleID int64
		var modelsJSON []byte
		if err := rows.Scan(
			&p.ID, &ruleID, &modelsJSON, &p.BillingMode, &p.PriceMultiplier,
			&p.InputPrice, &p.OutputPrice, &p.CacheWritePrice, &p.CacheWrite1hPrice, &p.CacheReadPrice,
			&p.ImageOutputPrice, &p.PerRequestPrice, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan provider stats model pricing: %w", err)
		}
		if err := json.Unmarshal(modelsJSON, &p.Models); err != nil {
			p.Models = []string{}
		}
		pricingMap[ruleID] = append(pricingMap[ruleID], p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate provider stats model pricing: %w", err)
	}

	// Load intervals for all pricing entries.
	var allPricingIDs []int64
	for _, pricings := range pricingMap {
		for _, p := range pricings {
			allPricingIDs = append(allPricingIDs, p.ID)
		}
	}
	if len(allPricingIDs) > 0 {
		intervalsMap, err := r.batchLoadProviderStatsIntervals(ctx, allPricingIDs)
		if err != nil {
			return nil, err
		}
		for ruleID, pricings := range pricingMap {
			for i := range pricings {
				pricings[i].Intervals = intervalsMap[pricings[i].ID]
			}
			pricingMap[ruleID] = pricings
		}
	}

	return pricingMap, nil
}

// loadProviderStatsPricingRules reads one configuration's provider-cost rules for GetByID.
func (r *PricingConfigStore) loadProviderStatsPricingRules(ctx context.Context, pricingConfigID int64) ([]routing.ProviderStatsPricingRule, error) {
	result, err := r.batchLoadProviderStatsPricingRules(ctx, []int64{pricingConfigID})
	if err != nil {
		return nil, err
	}
	return result[pricingConfigID], nil
}

// replaceProviderStatsPricingRulesTx replaces a configuration's provider-cost rules in the supplied transaction.
func replaceProviderStatsPricingRulesTx(ctx context.Context, tx *sql.Tx, pricingConfigID int64, rules []routing.ProviderStatsPricingRule) error {
	// The foreign-key cascade deletes the rules' model prices.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM pricing_config_provider_stats_pricing_rules WHERE pricing_config_id = $1`, pricingConfigID,
	); err != nil {
		return fmt.Errorf("delete old provider stats pricing rules: %w", err)
	}

	for i := range rules {
		rules[i].PricingConfigID = pricingConfigID
		if err := createProviderStatsPricingRuleTx(ctx, tx, &rules[i]); err != nil {
			return fmt.Errorf("insert provider stats pricing rule: %w", err)
		}
	}
	return nil
}

// createProviderStatsPricingRuleTx inserts a provider-cost rule and its prices in the supplied transaction.
func createProviderStatsPricingRuleTx(ctx context.Context, tx *sql.Tx, rule *routing.ProviderStatsPricingRule) error {
	err := tx.QueryRowContext(ctx,
		`INSERT INTO pricing_config_provider_stats_pricing_rules (pricing_config_id, name, group_ids, provider_ids, sort_order)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id, created_at, updated_at`,
		rule.PricingConfigID, rule.Name, pq.Array(rule.GroupIDs), pq.Array(rule.ProviderIDs), rule.SortOrder,
	).Scan(&rule.ID, &rule.CreatedAt, &rule.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert provider stats pricing rule: %w", err)
	}

	for j := range rule.Pricing {
		if err := createProviderStatsModelPricingTx(ctx, tx, rule.ID, &rule.Pricing[j]); err != nil {
			return err
		}
	}
	return nil
}

// createProviderStatsModelPricingTx inserts a provider-cost price entry and its intervals.
func createProviderStatsModelPricingTx(ctx context.Context, tx *sql.Tx, ruleID int64, pricing *routing.ModelPricingEntry) error {
	modelsJSON, err := json.Marshal(pricing.Models)
	if err != nil {
		return fmt.Errorf("marshal models: %w", err)
	}
	billingMode := pricing.BillingMode
	if billingMode == "" {
		billingMode = routing.BillingModeToken
	}
	err = tx.QueryRowContext(ctx,
		`INSERT INTO pricing_config_provider_stats_model_pricing (rule_id, models, billing_mode, price_multiplier, input_price, output_price, cache_write_price, cache_write_1h_price, cache_read_price, image_output_price, per_request_price)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id, created_at, updated_at`,
		ruleID, modelsJSON, billingMode,
		pricing.PriceMultiplier, pricing.InputPrice, pricing.OutputPrice, pricing.CacheWritePrice, pricing.CacheWrite1hPrice, pricing.CacheReadPrice,
		pricing.ImageOutputPrice, pricing.PerRequestPrice,
	).Scan(&pricing.ID, &pricing.CreatedAt, &pricing.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert provider stats model pricing: %w", err)
	}

	for i := range pricing.Intervals {
		iv := &pricing.Intervals[i]
		iv.PricingID = pricing.ID
		if err := createProviderStatsIntervalTx(ctx, tx, iv); err != nil {
			return err
		}
	}
	return nil
}

// createProviderStatsIntervalTx inserts an interval for a provider-cost price entry.
func createProviderStatsIntervalTx(ctx context.Context, tx *sql.Tx, iv *routing.PricingInterval) error {
	return tx.QueryRowContext(ctx,
		`INSERT INTO pricing_config_provider_stats_pricing_intervals
		 (pricing_id, min_tokens, max_tokens, tier_label, input_price, output_price, cache_write_price, cache_write_1h_price, cache_read_price, per_request_price, sort_order)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id, created_at, updated_at`,
		iv.PricingID, iv.MinTokens, iv.MaxTokens, iv.TierLabel,
		iv.InputPrice, iv.OutputPrice, iv.CacheWritePrice, iv.CacheWrite1hPrice, iv.CacheReadPrice,
		iv.PerRequestPrice, iv.SortOrder,
	).Scan(&iv.ID, &iv.CreatedAt, &iv.UpdatedAt)
}

// batchLoadProviderStatsIntervals loads intervals for provider stats pricing entries.
func (r *PricingConfigStore) batchLoadProviderStatsIntervals(ctx context.Context, pricingIDs []int64) (map[int64][]routing.PricingInterval, error) {
	if len(pricingIDs) == 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, pricing_id, min_tokens, max_tokens, tier_label,
		        input_price, output_price, cache_write_price, cache_write_1h_price, cache_read_price,
		        per_request_price, sort_order, created_at, updated_at
		 FROM pricing_config_provider_stats_pricing_intervals
		 WHERE pricing_id = ANY($1) ORDER BY pricing_id, sort_order, id`,
		pq.Array(pricingIDs),
	)
	if err != nil {
		return nil, fmt.Errorf("batch load provider stats pricing intervals: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make(map[int64][]routing.PricingInterval)
	for rows.Next() {
		var iv routing.PricingInterval
		if err := rows.Scan(
			&iv.ID, &iv.PricingID, &iv.MinTokens, &iv.MaxTokens, &iv.TierLabel,
			&iv.InputPrice, &iv.OutputPrice, &iv.CacheWritePrice, &iv.CacheWrite1hPrice, &iv.CacheReadPrice,
			&iv.PerRequestPrice, &iv.SortOrder, &iv.CreatedAt, &iv.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan provider stats pricing interval: %w", err)
		}
		result[iv.PricingID] = append(result[iv.PricingID], iv)
	}
	return result, rows.Err()
}
