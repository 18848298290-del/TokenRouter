package postgres

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// providerReportData holds four report breakdowns collected in one scan of a shared range.
type providerReportData struct {
	history           []ProviderUsageHistory
	models            []ModelStat
	endpoints         []EndpointStat
	upstreamEndpoints []EndpointStat
	durationSum       int64
	durationCount     int64
}

// readProviderReport disables JIT in a dedicated read-only transaction when a database handle is available.
func (r *Store) readProviderReport(ctx context.Context, id int64, start, end time.Time) (*providerReportData, error) {
	if r.db == nil {
		return r.scanProviderReport(ctx, id, start, end)
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SET LOCAL jit = off"); err != nil {
		return nil, err
	}
	reader := &Store{sql: tx, calendar: r.calendar}
	data, err := reader.scanProviderReport(ctx, id, start, end)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return data, nil
}

// scanProviderReport uses GROUPING SETS for daily, model, and endpoint totals and counts non-null durations for averaging.
func (r *Store) scanProviderReport(ctx context.Context, id int64, start, end time.Time) (*providerReportData, error) {
	query := `WITH scoped AS (
	 SELECT TO_CHAR(created_at,'YYYY-MM-DD') AS day,
	 ` + resolveModelDimensionExpression(usage.ModelSourceRequested) + ` AS model_name,
	 COALESCE(NULLIF(TRIM(inbound_endpoint),''),'unknown') AS inbound,
	 COALESCE(NULLIF(TRIM(upstream_endpoint),''),'unknown') AS upstream,
	 input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,total_cost,actual_cost,
	 COALESCE(provider_stats_cost,total_cost)*COALESCE(provider_rate_multiplier,1) AS provider_cost,duration_ms
	 FROM usage_logs WHERE provider_id=$1 AND created_at >= $2 AND created_at < $3
	)
	SELECT CASE WHEN GROUPING(day)=0 THEN 'day' WHEN GROUPING(model_name)=0 THEN 'model'
	 WHEN GROUPING(inbound)=0 THEN 'inbound' ELSE 'upstream' END,
	 COALESCE(day,model_name,inbound,upstream),COUNT(*),
	 COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0),
	 COALESCE(SUM(cache_creation_tokens),0),COALESCE(SUM(cache_read_tokens),0),
	 COALESCE(SUM(total_cost),0),COALESCE(SUM(actual_cost),0),COALESCE(SUM(provider_cost),0),
	 COALESCE(SUM(duration_ms),0),COUNT(duration_ms)
	FROM scoped GROUP BY GROUPING SETS ((day),(model_name),(inbound),(upstream))`
	rows, err := r.sql.QueryContext(ctx, query, id, start, end)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	data := &providerReportData{history: make([]ProviderUsageHistory, 0), models: make([]ModelStat, 0), endpoints: make([]EndpointStat, 0), upstreamEndpoints: make([]EndpointStat, 0)}
	for rows.Next() {
		var kind, label string
		var requests, input, output, creation, read, duration, count int64
		var cost, userCost, providerCost float64
		if err := rows.Scan(&kind, &label, &requests, &input, &output, &creation, &read, &cost, &userCost, &providerCost, &duration, &count); err != nil {
			return nil, err
		}
		tokens := input + output + creation + read
		switch kind {
		case "day":
			date, _ := time.Parse(time.DateOnly, label)
			data.history = append(data.history, ProviderUsageHistory{Date: label, Label: date.Format("01/02"), Requests: requests, Tokens: tokens, Cost: cost, ActualCost: providerCost, UserCost: userCost})
			data.durationSum += duration
			data.durationCount += count
		case "model":
			data.models = append(data.models, ModelStat{Model: label, Requests: requests, InputTokens: input, OutputTokens: output, CacheCreationTokens: creation, CacheReadTokens: read, TotalTokens: tokens, Cost: cost, ActualCost: userCost, ProviderCost: providerCost})
		case "inbound":
			data.endpoints = append(data.endpoints, EndpointStat{Endpoint: label, Requests: requests, TotalTokens: tokens, Cost: cost, ActualCost: userCost})
		case "upstream":
			data.upstreamEndpoints = append(data.upstreamEndpoints, EndpointStat{Endpoint: label, Requests: requests, TotalTokens: tokens, Cost: cost, ActualCost: userCost})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(data.history, func(i, j int) bool { return data.history[i].Date < data.history[j].Date })
	sort.Slice(data.models, func(i, j int) bool { return data.models[i].TotalTokens > data.models[j].TotalTokens })
	sort.Slice(data.endpoints, func(i, j int) bool { return data.endpoints[i].Requests > data.endpoints[j].Requests })
	sort.Slice(data.upstreamEndpoints, func(i, j int) bool { return data.upstreamEndpoints[i].Requests > data.upstreamEndpoints[j].Requests })
	return data, nil
}
