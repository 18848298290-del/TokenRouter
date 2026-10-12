package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

const usageLogsCleanupBatchSize = 10000

type AggregationStore struct {
	archiveDedup func(context.Context, time.Time) error
	sql          sqlExecutor
	calendar     timezone.Calendar
}

// NewDashboardAggregationRepository creates a rollup store, returning nil for unsupported database drivers.
func NewDashboardAggregationRepository(sqlDB *sql.DB, calendar timezone.Calendar, archive func(context.Context, time.Time) error) *AggregationStore {
	if sqlDB == nil {
		return nil
	}
	if !isPostgresDriver(sqlDB) {
		log.Printf("[DashboardAggregation] 检测到非 PostgreSQL 驱动，已自动禁用预聚合")
		return nil
	}
	return NewAggregationStoreWithSQL(sqlDB, calendar, archive)
}

func NewAggregationStoreWithSQL(sqlq sqlExecutor, calendar timezone.Calendar, archive ...func(context.Context, time.Time) error) *AggregationStore {
	r := &AggregationStore{sql: sqlq, calendar: calendar}
	if len(archive) > 0 {
		r.archiveDedup = archive[0]
	}
	return r
}

func isPostgresDriver(db *sql.DB) bool {
	if db == nil {
		return false
	}
	_, ok := db.Driver().(*pq.Driver)
	return ok
}

func (r *AggregationStore) AggregateRange(ctx context.Context, start, end time.Time) error {
	if r == nil || r.sql == nil {
		return nil
	}
	loc := r.calendar.Location()
	startLocal := start.In(loc)
	endLocal := end.In(loc)
	if !endLocal.After(startLocal) {
		return nil
	}

	hourStart := startLocal.Truncate(time.Hour)
	hourEnd := endLocal.Truncate(time.Hour)
	if endLocal.After(hourEnd) {
		hourEnd = hourEnd.Add(time.Hour)
	}

	dayStart := truncateToDay(startLocal, r.calendar)
	dayEnd := truncateToDay(endLocal, r.calendar)
	if endLocal.After(dayEnd) {
		dayEnd = dayEnd.AddDate(0, 0, 1)
	}

	if db, ok := r.sql.(*sql.DB); ok {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		txRepo := NewAggregationStoreWithSQL(tx, r.calendar)
		if err := txRepo.aggregateRangeInTx(ctx, hourStart, hourEnd, dayStart, dayEnd); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	return r.aggregateRangeInTx(ctx, hourStart, hourEnd, dayStart, dayEnd)
}

func (r *AggregationStore) aggregateRangeInTx(ctx context.Context, hourStart, hourEnd, dayStart, dayEnd time.Time) error {
	// Aggregate complete buckets, including the portion of the final bucket after end.
	if err := r.insertHourlyActiveUsers(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.insertDailyActiveUsers(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.upsertHourlyAggregates(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	return r.upsertDailyAggregates(ctx, dayStart, dayEnd)
}

func (r *AggregationStore) RecomputeRange(ctx context.Context, start, end time.Time) error {
	if r == nil || r.sql == nil {
		return nil
	}
	loc := r.calendar.Location()
	startLocal := start.In(loc)
	endLocal := end.In(loc)
	if !endLocal.After(startLocal) {
		return nil
	}

	hourStart := startLocal.Truncate(time.Hour)
	// Cleanup includes its end instant, so an exact-hour endpoint also affects the bucket starting there.
	hourEnd := endLocal.Truncate(time.Hour).Add(time.Hour)

	dayStart := truncateToDay(startLocal, r.calendar)
	dayEnd := truncateToDay(endLocal, r.calendar).AddDate(0, 0, 1)

	// Start a transaction for *sql.DB; other executors retain their caller-provided transaction behavior.
	if db, ok := r.sql.(*sql.DB); ok {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		txRepo := NewAggregationStoreWithSQL(tx, r.calendar)
		if err := txRepo.recomputeRangeInTx(ctx, hourStart, hourEnd, dayStart, dayEnd); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	return r.recomputeRangeInTx(ctx, hourStart, hourEnd, dayStart, dayEnd)
}

func (r *AggregationStore) recomputeRangeInTx(ctx context.Context, hourStart, hourEnd, dayStart, dayEnd time.Time) error {
	// Clear affected buckets before rebuilding so deleted records can reduce active-user counts.
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_hourly WHERE bucket_start >= $1 AND bucket_start < $2", hourStart, hourEnd); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_hourly_users WHERE bucket_start >= $1 AND bucket_start < $2", hourStart, hourEnd); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_daily WHERE bucket_date >= $1::date AND bucket_date < $2::date", dayStart, dayEnd); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_daily_users WHERE bucket_date >= $1::date AND bucket_date < $2::date", dayStart, dayEnd); err != nil {
		return err
	}

	if err := r.insertHourlyActiveUsers(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	// Daily active-user rows were cleared for whole days; rebuild those full days to retain unaffected hours.
	if err := r.insertDailyActiveUsers(ctx, dayStart, dayEnd); err != nil {
		return err
	}
	if err := r.upsertHourlyAggregates(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	return r.upsertDailyAggregates(ctx, dayStart, dayEnd)
}

func (r *AggregationStore) GetAggregationWatermark(ctx context.Context) (time.Time, error) {
	var ts time.Time
	query := "SELECT last_aggregated_at FROM usage_dashboard_aggregation_watermark WHERE id = 1"
	if err := scanSingleRow(ctx, r.sql, query, nil, &ts); err != nil {
		if err == sql.ErrNoRows {
			return time.Unix(0, 0).UTC(), nil
		}
		return time.Time{}, err
	}
	return ts.UTC(), nil
}

func (r *AggregationStore) UpdateAggregationWatermark(ctx context.Context, aggregatedAt time.Time) error {
	query := `
		INSERT INTO usage_dashboard_aggregation_watermark (id, last_aggregated_at, updated_at)
		VALUES (1, $1, NOW())
		ON CONFLICT (id)
		DO UPDATE SET last_aggregated_at = EXCLUDED.last_aggregated_at, updated_at = EXCLUDED.updated_at
	`
	_, err := r.sql.ExecContext(ctx, query, aggregatedAt.UTC())
	return err
}

func (r *AggregationStore) CleanupAggregates(ctx context.Context, hourlyCutoff, dailyCutoff time.Time) error {
	hourlyCutoffUTC := hourlyCutoff.UTC()
	dailyCutoffUTC := dailyCutoff.UTC()
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_hourly WHERE bucket_start < $1", hourlyCutoffUTC); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_hourly_users WHERE bucket_start < $1", hourlyCutoffUTC); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_daily WHERE bucket_date < $1::date", dailyCutoffUTC); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_daily_users WHERE bucket_date < $1::date", dailyCutoffUTC); err != nil {
		return err
	}
	return nil
}

func (r *AggregationStore) CleanupUsageLogs(ctx context.Context, cutoff time.Time) error {
	isPartitioned, err := r.isUsageLogsPartitioned(ctx)
	if err != nil {
		return err
	}
	if isPartitioned {
		return r.dropUsageLogsPartitions(ctx, cutoff)
	}
	for {
		res, err := r.sql.ExecContext(ctx, `
			WITH victims AS (
				SELECT ctid
				FROM usage_logs
				WHERE created_at < $1
				LIMIT $2
			)
			DELETE FROM usage_logs
			WHERE ctid IN (SELECT ctid FROM victims)
		`, cutoff.UTC(), usageLogsCleanupBatchSize)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected < usageLogsCleanupBatchSize {
			return nil
		}
	}
}

// CleanupUsageBillingDedup invokes the billing archive function supplied by app.
func (r *AggregationStore) CleanupUsageBillingDedup(ctx context.Context, cutoff time.Time) error {
	if r.archiveDedup == nil {
		return fmt.Errorf("billing dedup archive is not bound")
	}
	return r.archiveDedup(ctx, cutoff)
}

func (r *AggregationStore) EnsureUsageLogsPartitions(ctx context.Context, now time.Time) error {
	isPartitioned, err := r.isUsageLogsPartitioned(ctx)
	if err != nil || !isPartitioned {
		return err
	}
	monthStart := truncateToMonthUTC(now)
	prevMonth := monthStart.AddDate(0, -1, 0)
	nextMonth := monthStart.AddDate(0, 1, 0)

	for _, m := range []time.Time{prevMonth, monthStart, nextMonth} {
		if err := r.createUsageLogsPartition(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

func (r *AggregationStore) insertHourlyActiveUsers(ctx context.Context, start, end time.Time) error {
	tzName := r.calendar.Location().String()
	query := `
		INSERT INTO usage_dashboard_hourly_users (bucket_start, user_id)
		SELECT DISTINCT
			date_trunc('hour', created_at AT TIME ZONE $3) AT TIME ZONE $3 AS bucket_start,
			user_id
		FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
		ON CONFLICT DO NOTHING
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, tzName)
	return err
}

func (r *AggregationStore) insertDailyActiveUsers(ctx context.Context, start, end time.Time) error {
	tzName := r.calendar.Location().String()
	query := `
		INSERT INTO usage_dashboard_daily_users (bucket_date, user_id)
		SELECT DISTINCT
			(bucket_start AT TIME ZONE $3)::date AS bucket_date,
			user_id
		FROM usage_dashboard_hourly_users
		WHERE bucket_start >= $1 AND bucket_start < $2
		ON CONFLICT DO NOTHING
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, tzName)
	return err
}

func (r *AggregationStore) upsertHourlyAggregates(ctx context.Context, start, end time.Time) error {
	tzName := r.calendar.Location().String()
	query := `
		WITH hourly AS (
			SELECT
				date_trunc('hour', created_at AT TIME ZONE $3) AT TIME ZONE $3 AS bucket_start,
				COUNT(*) AS total_requests,
				COALESCE(SUM(input_tokens), 0) AS input_tokens,
				COALESCE(SUM(output_tokens), 0) AS output_tokens,
				COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
				COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
				COALESCE(SUM(total_cost), 0) AS total_cost,
				COALESCE(SUM(actual_cost), 0) AS actual_cost,
				COALESCE(SUM(COALESCE(provider_stats_cost, total_cost) * COALESCE(provider_rate_multiplier, 1)), 0) AS provider_cost,
				COALESCE(SUM(COALESCE(duration_ms, 0)), 0) AS total_duration_ms
			FROM usage_logs
			WHERE created_at >= $1 AND created_at < $2
			GROUP BY 1
		),
		user_counts AS (
			SELECT bucket_start, COUNT(*) AS active_users
			FROM usage_dashboard_hourly_users
			WHERE bucket_start >= $1 AND bucket_start < $2
			GROUP BY bucket_start
		)
		INSERT INTO usage_dashboard_hourly (
			bucket_start,
			total_requests,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			total_cost,
			actual_cost,
			provider_cost,
			total_duration_ms,
			active_users,
			computed_at
		)
		SELECT
			hourly.bucket_start,
			hourly.total_requests,
			hourly.input_tokens,
			hourly.output_tokens,
			hourly.cache_creation_tokens,
			hourly.cache_read_tokens,
			hourly.total_cost,
			hourly.actual_cost,
			hourly.provider_cost,
			hourly.total_duration_ms,
			COALESCE(user_counts.active_users, 0) AS active_users,
			NOW()
		FROM hourly
		LEFT JOIN user_counts ON user_counts.bucket_start = hourly.bucket_start
		ON CONFLICT (bucket_start)
		DO UPDATE SET
			total_requests = EXCLUDED.total_requests,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			total_cost = EXCLUDED.total_cost,
			actual_cost = EXCLUDED.actual_cost,
			provider_cost = EXCLUDED.provider_cost,
			total_duration_ms = EXCLUDED.total_duration_ms,
			active_users = EXCLUDED.active_users,
			computed_at = EXCLUDED.computed_at
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, tzName)
	return err
}

func (r *AggregationStore) upsertDailyAggregates(ctx context.Context, start, end time.Time) error {
	tzName := r.calendar.Location().String()
	query := `
		WITH daily AS (
			SELECT
				(bucket_start AT TIME ZONE $5)::date AS bucket_date,
				COALESCE(SUM(total_requests), 0) AS total_requests,
				COALESCE(SUM(input_tokens), 0) AS input_tokens,
				COALESCE(SUM(output_tokens), 0) AS output_tokens,
				COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
				COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
				COALESCE(SUM(total_cost), 0) AS total_cost,
				COALESCE(SUM(actual_cost), 0) AS actual_cost,
				COALESCE(SUM(provider_cost), 0) AS provider_cost,
				COALESCE(SUM(total_duration_ms), 0) AS total_duration_ms
			FROM usage_dashboard_hourly
			WHERE bucket_start >= $1 AND bucket_start < $2
			GROUP BY (bucket_start AT TIME ZONE $5)::date
		),
		user_counts AS (
			SELECT bucket_date, COUNT(*) AS active_users
			FROM usage_dashboard_daily_users
			WHERE bucket_date >= $3::date AND bucket_date < $4::date
			GROUP BY bucket_date
		)
		INSERT INTO usage_dashboard_daily (
			bucket_date,
			total_requests,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			total_cost,
			actual_cost,
			provider_cost,
			total_duration_ms,
			active_users,
			computed_at
		)
		SELECT
			daily.bucket_date,
			daily.total_requests,
			daily.input_tokens,
			daily.output_tokens,
			daily.cache_creation_tokens,
			daily.cache_read_tokens,
			daily.total_cost,
			daily.actual_cost,
			daily.provider_cost,
			daily.total_duration_ms,
			COALESCE(user_counts.active_users, 0) AS active_users,
			NOW()
		FROM daily
		LEFT JOIN user_counts ON user_counts.bucket_date = daily.bucket_date
		ON CONFLICT (bucket_date)
		DO UPDATE SET
			total_requests = EXCLUDED.total_requests,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			total_cost = EXCLUDED.total_cost,
			actual_cost = EXCLUDED.actual_cost,
			provider_cost = EXCLUDED.provider_cost,
			total_duration_ms = EXCLUDED.total_duration_ms,
			active_users = EXCLUDED.active_users,
			computed_at = EXCLUDED.computed_at
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, start, end, tzName)
	return err
}

func (r *AggregationStore) isUsageLogsPartitioned(ctx context.Context) (bool, error) {
	query := `
		SELECT EXISTS(
			SELECT 1
			FROM pg_partitioned_table pt
			JOIN pg_class c ON c.oid = pt.partrelid
			WHERE c.relname = 'usage_logs'
		)
	`
	var partitioned bool
	if err := scanSingleRow(ctx, r.sql, query, nil, &partitioned); err != nil {
		return false, err
	}
	return partitioned, nil
}

func (r *AggregationStore) dropUsageLogsPartitions(ctx context.Context, cutoff time.Time) error {
	rows, err := r.sql.QueryContext(ctx, `
		SELECT c.relname
		FROM pg_inherits
		JOIN pg_class c ON c.oid = pg_inherits.inhrelid
		JOIN pg_class p ON p.oid = pg_inherits.inhparent
		WHERE p.relname = 'usage_logs'
	`)
	if err != nil {
		return err
	}
	defer func() {
		_ = rows.Close()
	}()

	cutoffMonth := truncateToMonthUTC(cutoff)
	var obsolete []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if !strings.HasPrefix(name, "usage_logs_") {
			continue
		}
		suffix := strings.TrimPrefix(name, "usage_logs_")
		month, err := time.Parse("200601", suffix)
		if err != nil {
			continue
		}
		month = month.UTC()
		if month.Before(cutoffMonth) {
			obsolete = append(obsolete, name)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, name := range obsolete {
		// Close the rows first so a transaction can reuse its connection to drop partitions and rebuild activity.
		if _, err := r.sql.ExecContext(ctx, "SELECT usage_drop_partition_with_activity($1)", pq.QuoteIdentifier(name)); err != nil {
			return err
		}
	}
	return nil
}

func (r *AggregationStore) createUsageLogsPartition(ctx context.Context, month time.Time) error {
	monthStart := truncateToMonthUTC(month)
	nextMonth := monthStart.AddDate(0, 1, 0)
	name := fmt.Sprintf("usage_logs_%s", monthStart.Format("200601"))
	query := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s PARTITION OF usage_logs FOR VALUES FROM (%s) TO (%s)",
		pq.QuoteIdentifier(name),
		pq.QuoteLiteral(monthStart.Format("2006-01-02")),
		pq.QuoteLiteral(nextMonth.Format("2006-01-02")),
	)
	_, err := r.sql.ExecContext(ctx, query)
	return err
}

func truncateToDay(t time.Time, calendar timezone.Calendar) time.Time {
	return calendar.StartOfDay(t)
}

func truncateToMonthUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
