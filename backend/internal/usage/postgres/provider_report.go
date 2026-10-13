package postgres

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Provider reports need daily history, model totals, and both inbound and
// upstream endpoint totals for the same raw request range. One GROUPING SETS
// query emits four representations of each request, identified by a grouping
// tag so records enter the corresponding result collection. User charges and
// provider statistics cost remain separate, including the recorded provider
// multiplier.
//
// Duration sums and nonnull counts are accumulated from daily rows alone;
// adding every grouping would count each observation four times. Results are
// sorted for report consumers after scanning. A database-backed reader runs in
// a read-only transaction with JIT disabled locally, while a supplied executor
// is read directly using its existing session and transaction settings.

// providerReportData holds sorted provider breakdowns and the duration inputs
// used by report summaries.
type providerReportData struct {
	// history, models, endpoints, and upstreamEndpoints contain separate
	// groupings of the same scoped requests; adding across these collections
	// would count the same request more than once.
	// chronological daily usage history
	history           []ProviderUsageHistory
	models            []ModelStat    // models ordered by token total
	endpoints         []EndpointStat // inbound endpoints by request count
	upstreamEndpoints []EndpointStat // upstream endpoints by request count
	// durationSum and durationCount accumulate daily grouping rows alone.
	// Their ratio is the overall measured-request average even though the query
	// returns the same duration observations in several other groupings.
	durationSum   int64 // measured duration sum in milliseconds
	durationCount int64 // requests with nonnull durations
}

// readProviderReport reads provider breakdowns for [start, end) on the supplied
// executor when no database handle exists. With a database handle, it opens a
// read-only transaction, sets transaction-local JIT off, and returns results
// only after commit succeeds. Query errors leave cleanup to deferred rollback.
func (r *Store) readProviderReport(ctx context.Context, id int64, start, end time.Time) (*providerReportData, error) {
	if r.db == nil {
		// The supplied executor keeps its existing session and transaction
		// settings. Setting JIT locally requires the transaction created on the
		// database handle below.
		return r.scanProviderReport(ctx, id, start, end)
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	// Rollback releases the transaction on a JIT-setting or query failure; a
	// successful commit completes it before the deferred cleanup attempt.
	defer func() { _ = tx.Rollback() }()
	// Set JIT within this transaction so the report query uses that setting
	// without changing later queries on the pooled connection.
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

// scanProviderReport returns four sorted breakdowns over one provider's raw
// [start, end) usage. Empty results contain initialized empty slices. SQL dates
// use the database session timezone. Models prefer trimmed internal model,
// then historical requested_model, and blank endpoint labels become unknown.
//
// Grouping tags distinguish daily, model, inbound, and upstream rows
// representing the same requests. Only daily rows contribute to overall
// duration inputs, counting nonnull timings independently of requests. Daily
// ActualCost stores provider cost and UserCost stores user charges; model and
// endpoint ActualCost instead store user charges.
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
	// Keep all four grouping sets on the same result stream until scanning
	// finishes; deferred closing releases that stream while retaining scan and
	// iteration errors.
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
			// SQL emits date-only labels for daily rows. A parse error leaves the
			// zero date for display formatting while retaining the SQL Date string.
			date, _ := time.Parse(time.DateOnly, label)
			data.history = append(data.history, ProviderUsageHistory{Date: label, Label: date.Format("01/02"), Requests: requests, Tokens: tokens, Cost: cost, ActualCost: providerCost, UserCost: userCost})
			// Each request also appears in model and endpoint groupings. Accumulate
			// duration inputs here so the overall average counts each measured
			// request once across the four representations.
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
