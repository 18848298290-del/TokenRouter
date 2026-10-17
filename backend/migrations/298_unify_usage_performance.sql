-- Usage performance has one persistent owner. Adding a nullable JSON column and
-- dropping obsolete columns changes metadata without rewriting historical rows.
-- Stop old application writers before applying this incompatible schema change.
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '30s';

ALTER TABLE usage_logs ADD COLUMN timings JSONB, DROP COLUMN first_token_ms;
ALTER TABLE ops_error_logs DROP COLUMN time_to_first_token_ms;

-- Discard old latency aggregates while retaining every other metric.
ALTER TABLE ops_system_metrics
    DROP COLUMN ttft_p50_ms,
    DROP COLUMN ttft_p90_ms,
    DROP COLUMN ttft_p95_ms,
    DROP COLUMN ttft_p99_ms,
    DROP COLUMN ttft_avg_ms,
    DROP COLUMN ttft_max_ms;
ALTER TABLE ops_system_metrics
    ADD COLUMN ttft_p50_ms INT,
    ADD COLUMN ttft_p90_ms INT,
    ADD COLUMN ttft_p95_ms INT,
    ADD COLUMN ttft_p99_ms INT,
    ADD COLUMN ttft_avg_ms DOUBLE PRECISION,
    ADD COLUMN ttft_max_ms INT;

-- Discard old latency aggregates while retaining every other metric.
ALTER TABLE ops_metrics_hourly
    DROP COLUMN ttft_p50_ms,
    DROP COLUMN ttft_p90_ms,
    DROP COLUMN ttft_p95_ms,
    DROP COLUMN ttft_p99_ms,
    DROP COLUMN ttft_avg_ms,
    DROP COLUMN ttft_max_ms,
    DROP COLUMN ttft_sample_count;
ALTER TABLE ops_metrics_hourly
    ADD COLUMN ttft_p50_ms INT,
    ADD COLUMN ttft_p90_ms INT,
    ADD COLUMN ttft_p95_ms INT,
    ADD COLUMN ttft_p99_ms INT,
    ADD COLUMN ttft_avg_ms DOUBLE PRECISION,
    ADD COLUMN ttft_max_ms INT,
    ADD COLUMN ttft_sample_count BIGINT NOT NULL DEFAULT 0;

-- Discard old latency aggregates while retaining every other metric.
ALTER TABLE ops_metrics_daily
    DROP COLUMN ttft_p50_ms,
    DROP COLUMN ttft_p90_ms,
    DROP COLUMN ttft_p95_ms,
    DROP COLUMN ttft_p99_ms,
    DROP COLUMN ttft_avg_ms,
    DROP COLUMN ttft_max_ms,
    DROP COLUMN ttft_sample_count;
ALTER TABLE ops_metrics_daily
    ADD COLUMN ttft_p50_ms INT,
    ADD COLUMN ttft_p90_ms INT,
    ADD COLUMN ttft_p95_ms INT,
    ADD COLUMN ttft_p99_ms INT,
    ADD COLUMN ttft_avg_ms DOUBLE PRECISION,
    ADD COLUMN ttft_max_ms INT,
    ADD COLUMN ttft_sample_count BIGINT NOT NULL DEFAULT 0;

-- An existing global choice wins. The old semantic mode becomes first-event
-- observation, whose unified predicate deliberately includes lifecycle metadata.
INSERT INTO settings(key, value)
SELECT 'ttft_mode', CASE WHEN lower(trim(value)) = 'visible' THEN 'first_output' ELSE 'first_event' END
FROM settings WHERE key = 'openai_ttft_mode'
ON CONFLICT(key) DO NOTHING;
DELETE FROM settings WHERE key = 'openai_ttft_mode';
