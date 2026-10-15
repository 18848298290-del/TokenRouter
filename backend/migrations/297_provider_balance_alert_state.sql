-- Keep reminder attempts separate from provider configuration and scheduler snapshots.
-- Claiming a round advances its deadline before any remote request or SMTP attempt.
CREATE TABLE IF NOT EXISTS provider_balance_alert_states (
    provider_id BIGINT PRIMARY KEY REFERENCES providers(id) ON DELETE CASCADE,
    identity_hash TEXT NOT NULL,
    round BIGINT NOT NULL DEFAULT 1,
    last_checked_at TIMESTAMPTZ NOT NULL,
    next_check_at TIMESTAMPTZ NOT NULL,
    outcome JSONB NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_providers_balance_alert_enabled ON providers(id)
    WHERE deleted_at IS NULL AND status = 'active' AND type = 'apikey'
    AND extra->'upstream_balance_alert'->>'enabled' = 'true';
