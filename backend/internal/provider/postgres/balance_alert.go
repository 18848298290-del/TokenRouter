package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// balance_alert.go persists reminder deadlines and the latest query or mail
// outcome independently of provider configuration. Candidate discovery reads a
// partial index over enabled API-key providers. Each claim compares the provider
// version used to prepare the query and atomically advances the stored deadline,
// so concurrent instances cannot both consume one scheduled attempt.
//
// Claims are committed before upstream or SMTP work starts. A process failure
// therefore consumes that round until the next deadline. Completion compares
// both round number and identity hash, preventing a delayed worker from
// overwriting a newer attempt. These writes do not update providers or publish
// scheduler outbox events; administrator reads combine stored state with a
// current query-identity check in the provider service.

// ListBalanceAlertCandidates returns active API-key IDs with explicitly enabled reminders.
func (r *ProviderStore) ListBalanceAlertCandidates(ctx context.Context) ([]int64, error) {
	rows, err := r.sql.QueryContext(ctx, `SELECT id FROM providers WHERE deleted_at IS NULL AND status='active' AND type='apikey' AND extra->'upstream_balance_alert'->>'enabled'='true' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	// Scanning reports query errors; closing here releases any unread rows after
	// an early scan failure without replacing the original diagnostic returned below.
	defer func() { _ = rows.Close() }()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ClaimBalanceAlert atomically consumes a due or newly configured reminder round.
// The provider version is checked against the prepared record before inserting.
// Concurrent inserts serialize on the primary key, and only a due deadline or
// different configuration identity can replace an existing claim. NextCheckAt
// advances from the current attempt time, preventing downtime catch-up sends.
// The round remains consumed after a worker failure; callers may execute remote
// work only when this method returns a nonnil state with no error.
func (r *ProviderStore) ClaimBalanceAlert(ctx context.Context, p *provider.Record, identity string, c provider.BalanceAlertConfig, now time.Time) (*provider.BalanceAlertState, error) {
	next := now.Add(time.Duration(c.IntervalMinutes) * time.Minute)
	state := &provider.BalanceAlertState{ProviderID: p.ID, Identity: identity}
	// Persist the deadline before contacting the upstream or SMTP server. A
	// crash after this statement cannot cause another instance to replay this round.
	rows, err := r.sql.QueryContext(ctx, `INSERT INTO provider_balance_alert_states (provider_id,identity_hash,last_checked_at,next_check_at)
 SELECT id,$2,$3,$4 FROM providers WHERE id=$1 AND updated_at=$5 AND deleted_at IS NULL AND status='active'
 ON CONFLICT(provider_id) DO UPDATE SET identity_hash=EXCLUDED.identity_hash,
 round=provider_balance_alert_states.round+1,last_checked_at=EXCLUDED.last_checked_at,
 next_check_at=EXCLUDED.next_check_at,outcome='{}'::jsonb
 WHERE provider_balance_alert_states.next_check_at <= $3 OR provider_balance_alert_states.identity_hash <> $2
 RETURNING round,last_checked_at,next_check_at`, p.ID, identity, now, next, p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	// Scanning reports query errors; closing here releases any unread rows after
	// an early scan failure without replacing the original diagnostic returned below.
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return nil, rows.Err()
	}
	if err := rows.Scan(&state.Round, &state.LastCheckedAt, &state.NextCheckAt); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return state, nil
}

// FinishBalanceAlert stores the result only while its round and identity still own the row.
// A stale completion changes nothing. Payload encoding excludes the private
// identity hash, which remains in its dedicated database column for comparisons.
func (r *ProviderStore) FinishBalanceAlert(ctx context.Context, state *provider.BalanceAlertState) error {
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = r.sql.ExecContext(ctx, `UPDATE provider_balance_alert_states SET outcome=$4 WHERE provider_id=$1 AND round=$2 AND identity_hash=$3`, state.ProviderID, state.Round, state.Identity, string(payload))
	return err
}

// GetBalanceAlertState reads the latest attempt or returns nil before any claim.
// Scheduling columns override the saved outcome's metadata, keeping in-progress
// attempts visible even when no query or email outcome has been persisted.
func (r *ProviderStore) GetBalanceAlertState(ctx context.Context, id int64) (*provider.BalanceAlertState, error) {
	var payload []byte
	var identity string
	var round int64
	var checked, next time.Time
	rows, err := r.sql.QueryContext(ctx, `SELECT identity_hash,round,last_checked_at,next_check_at,outcome FROM provider_balance_alert_states WHERE provider_id=$1`, id)
	if err != nil {
		return nil, err
	}
	// Scanning reports query errors; closing here releases any unread rows after
	// an early scan failure without replacing the original diagnostic returned below.
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return nil, rows.Err()
	}
	if err := rows.Scan(&identity, &round, &checked, &next, &payload); err != nil {
		return nil, err
	}
	state := &provider.BalanceAlertState{}
	if err := json.Unmarshal(payload, state); err != nil {
		return nil, err
	}
	state.ProviderID, state.Identity, state.Round = id, identity, round
	state.LastCheckedAt, state.NextCheckAt = &checked, &next
	return state, nil
}
