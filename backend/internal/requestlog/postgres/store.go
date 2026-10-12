package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/requestlog"
)

// Store persists request snapshots and rejects updates with older revisions.
type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Save(ctx context.Context, records []telemetry.RequestRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO request_records
 (request_id,parent_request_id,user_id,team_id,started_at,updated_at,aliases,record,revision)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
 ON CONFLICT(request_id) DO UPDATE SET parent_request_id=COALESCE(NULLIF(request_records.parent_request_id,''),EXCLUDED.parent_request_id),
 user_id=COALESCE(NULLIF(EXCLUDED.user_id,0),request_records.user_id),team_id=COALESCE(NULLIF(EXCLUDED.team_id,0),request_records.team_id),updated_at=EXCLUDED.updated_at,revision=EXCLUDED.revision,
 aliases=ARRAY(SELECT DISTINCT unnest(request_records.aliases || EXCLUDED.aliases)),
 record=(request_records.record || (EXCLUDED.record - 'started_at' - 'aliases')) || jsonb_build_object(
 'started_at', request_records.record->'started_at',
 'parent_request_id', COALESCE(NULLIF(request_records.parent_request_id,''),EXCLUDED.parent_request_id),
 'aliases', (SELECT COALESCE(jsonb_agg(DISTINCT item),'[]'::jsonb) FROM jsonb_array_elements(COALESCE(request_records.record->'aliases','[]'::jsonb) || COALESCE(EXCLUDED.record->'aliases','[]'::jsonb)) item),
 'state', CASE WHEN request_records.record->>'state' IN ('failed','canceled') OR (EXCLUDED.record->>'state'='running' AND request_records.record ? 'finished_at') THEN request_records.record->>'state' ELSE EXCLUDED.record->>'state' END)
 WHERE request_records.revision < EXCLUDED.revision`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, record := range records {
		body, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("%w: %w", requestlog.ErrInvalidRecord, err)
		}
		aliases := make([]string, 0, len(record.Aliases))
		for _, alias := range record.Aliases {
			if alias.Kind != "related" {
				aliases = append(aliases, alias.Value)
			}
		}
		if _, err = stmt.ExecContext(ctx, record.RequestID, record.ParentRequestID, record.UserID, record.TeamID,
			record.StartedAt, record.UpdatedAt, pq.Array(aliases), body, record.UpdatedAt.UnixNano()); err != nil {
			// SQLSTATE class 22 identifies invalid data; the batch writer isolates invalid snapshots with per-record retries.
			if pgErr, ok := errors.AsType[*pq.Error](err); ok && pgErr.Code.Class() == "22" {
				return fmt.Errorf("%w: %w", requestlog.ErrInvalidRecord, err)
			}
			return err
		}
	}
	return tx.Commit()
}

// Find checks ownership for each source; an external alias may identify multiple requests.
func (s *Store) Find(ctx context.Context, id string, userID int64, admin bool) ([]requestlog.Detail, error) {
	// Reuse candidate IDs and the exact-match flag across sources to keep exact queries within the resolved request set.
	var ids []string
	var exact bool
	if err := s.db.QueryRowContext(ctx, `SELECT ARRAY(SELECT id FROM request_lookup_ids($1)),
 EXISTS(SELECT 1 FROM request_records WHERE request_id=btrim($1))`, id).Scan(pq.Array(&ids), &exact); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT record FROM request_records r
 WHERE (r.request_id = ANY($1::text[])
 OR (r.parent_request_id <> '' AND r.parent_request_id = ANY($1::text[])))
 AND ($3 OR (r.user_id > 0 AND (r.user_id=$2 OR r.team_id IN
 (SELECT team_id FROM team_memberships WHERE user_id=$2 AND role='owner' AND left_at IS NULL))))
 ORDER BY r.started_at DESC LIMIT 101`, pq.Array(ids), userID, admin)
	if err != nil {
		return nil, err
	}
	result := make([]requestlog.Detail, 0)
	for rows.Next() {
		var body []byte
		if err = rows.Scan(&body); err != nil {
			_ = rows.Close()
			return nil, err
		}
		var detail requestlog.Detail
		if err = json.Unmarshal(body, &detail.RequestRecord); err != nil {
			_ = rows.Close()
			return nil, err
		}
		result = append(result, detail)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	// Build historical usage and error details with their actual IDs; missing snapshots remain marked Legacy.
	index := make(map[string]int, len(result))
	for i := range result {
		index[result[i].RequestID] = i
	}
	if err = s.findUsage(ctx, ids, id, userID, admin, exact, &result, index); err != nil {
		return nil, err
	}
	if err = s.findErrors(ctx, ids, userID, admin, exact, &result, index); err != nil {
		return nil, err
	}
	if admin {
		if err = s.findAudit(ctx, ids, &result, index); err != nil {
			return nil, err
		}
	}
	if err = s.findTasks(ctx, result, userID, admin); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) findUsage(ctx context.Context, ids []string, searchID string, userID int64, admin, exact bool, result *[]requestlog.Detail, index map[string]int) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id,COALESCE(request_id,''),created_at,user_id,COALESCE(team_id,0),api_key_id,
 COALESCE(requested_model,model),input_tokens,output_tokens,actual_cost,COALESCE(duration_ms,0)
 FROM usage_logs WHERE (request_id = ANY($1::text[]) OR (NOT $5 AND upstream_request_id=$4))
 AND ($3 OR user_id=$2 OR team_id IN (SELECT team_id FROM team_memberships WHERE user_id=$2 AND role='owner' AND left_at IS NULL))
 ORDER BY created_at DESC LIMIT 101`, pq.Array(ids), userID, admin, searchID, exact)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var record telemetry.RequestRecord
		var usage requestlog.Usage
		if err = rows.Scan(&usage.ID, &record.RequestID, &record.StartedAt, &record.UserID, &record.TeamID, &record.APIKeyID,
			&usage.Model, &usage.InputTokens, &usage.OutputTokens, &usage.ActualCost, &record.DurationMs); err != nil {
			return err
		}
		record.Model = usage.Model
		record.State = "usage_recorded"
		i := ensureDetail(result, index, record)
		(*result)[i].Usage = append((*result)[i].Usage, usage)
	}
	return rows.Err()
}

func (s *Store) findErrors(ctx context.Context, ids []string, userID int64, admin, exact bool, result *[]requestlog.Detail, index map[string]int) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id,COALESCE(NULLIF(request_id,''),client_request_id,''),created_at,
 COALESCE(user_id,0),COALESCE(api_key_id,0),COALESCE(model,''),COALESCE(status_code,0),COALESCE(error_phase,'')
 FROM ops_error_logs WHERE (request_id = ANY($1::text[]) OR (NOT $4 AND client_request_id = ANY($1::text[])))
 AND ($3 OR user_id=$2) ORDER BY created_at DESC LIMIT 101`, pq.Array(ids), userID, admin, exact)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var record telemetry.RequestRecord
		var failure requestlog.Failure
		if err = rows.Scan(&failure.ID, &record.RequestID, &record.StartedAt, &record.UserID, &record.APIKeyID, &record.Model, &failure.Status, &failure.Phase); err != nil {
			return err
		}
		record.State = "failed"
		record.Status = failure.Status
		i := ensureDetail(result, index, record)
		(*result)[i].Errors = append((*result)[i].Errors, failure)
	}
	return rows.Err()
}

func (s *Store) findAudit(ctx context.Context, ids []string, result *[]requestlog.Detail, index map[string]int) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id,request_id,created_at,method,path,status_code FROM audit_logs
 WHERE request_id = ANY($1::text[]) ORDER BY created_at DESC LIMIT 101`, pq.Array(ids))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var record telemetry.RequestRecord
		var auditID int64
		if err = rows.Scan(&auditID, &record.RequestID, &record.StartedAt, &record.Method, &record.Path, &record.Status); err != nil {
			return err
		}
		record.State = "completed"
		if record.Status >= 400 {
			record.State = "failed"
		}
		i := ensureDetail(result, index, record)
		(*result)[i].AuditIDs = append((*result)[i].AuditIDs, auditID)
	}
	return rows.Err()
}

func ensureDetail(result *[]requestlog.Detail, index map[string]int, record telemetry.RequestRecord) int {
	if i, ok := index[record.RequestID]; ok {
		return i
	}
	i := len(*result)
	*result = append(*result, requestlog.Detail{RequestRecord: record, Legacy: true})
	return i
}

// Cleanup deletes expired summaries in batches; billing and usage have separate retention jobs.
func (s *Store) Cleanup(ctx context.Context, cutoff time.Time) error {
	for {
		result, err := s.db.ExecContext(ctx, `DELETE FROM request_records WHERE request_id IN
        (SELECT request_id FROM request_records WHERE started_at<$1 ORDER BY started_at LIMIT 10000)`, cutoff)
		if err != nil {
			return fmt.Errorf("delete expired requests: %w", err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count < 10000 {
			return nil
		}
	}
}

// findTasks refreshes states from task tables, including failure and cancellation outcomes.
func (s *Store) findTasks(ctx context.Context, details []requestlog.Detail, userID int64, admin bool) error {
	creativeIDs, batchIDs := []string{}, []string{}
	positions := make(map[string][]int)
	for i := range details {
		for _, alias := range details[i].Aliases {
			if alias.Kind != "task" {
				continue
			}
			positions[alias.Value] = append(positions[alias.Value], i)
			if strings.HasPrefix(alias.Value, "crun_") {
				creativeIDs = append(creativeIDs, alias.Value)
			}
			if strings.HasPrefix(alias.Value, "imgbatch_") {
				batchIDs = append(batchIDs, alias.Value)
			}
		}
	}
	queries := []struct {
		ids []string
		sql string
	}{
		{creativeIDs, `SELECT run_id,status,COALESCE(completed_at,cancelled_at) FROM creative_runs WHERE run_id=ANY($1) AND ($3 OR user_id=$2)`},
		{batchIDs, `SELECT batch_id,status,finished_at FROM batch_image_jobs WHERE batch_id=ANY($1) AND ($3 OR user_id=$2 OR team_id IN (SELECT team_id FROM team_memberships WHERE user_id=$2 AND role='owner' AND left_at IS NULL))`},
	}
	for _, query := range queries {
		if len(query.ids) == 0 {
			continue
		}
		rows, err := s.db.QueryContext(ctx, query.sql, pq.Array(query.ids), userID, admin)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, state string
			var finished sql.NullTime
			if err = rows.Scan(&id, &state, &finished); err != nil {
				_ = rows.Close()
				return err
			}
			for _, i := range positions[id] {
				switch state {
				case "succeeded", "completed", "acked", "output_deleted":
					details[i].State = "completed"
				case "failed", "expired", "result_lost":
					details[i].State = "failed"
				case "cancelled", "canceled":
					details[i].State = "canceled"
				default:
					details[i].State = "running"
				}
				if finished.Valid {
					at := finished.Time
					details[i].FinishedAt = &at
					details[i].DurationMs = at.Sub(details[i].StartedAt).Milliseconds()
				}
			}
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
