package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

const schedulerOutboxDefaultCleanSize = 5000

type schedulerOutboxRepository struct {
	db *sql.DB
}

type schedulerOutboxCleanupLease struct {
	conn *sql.Conn
}

func NewSchedulerOutboxRepository(db *sql.DB) scheduler.SchedulerOutboxRepository {
	return &schedulerOutboxRepository{db: db}
}

func (r *schedulerOutboxRepository) ListAfterAndReleaseDedup(ctx context.Context, afterID int64, limit int) ([]scheduler.SchedulerOutboxEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
		WITH selected AS MATERIALIZED (
			SELECT id, event_type, provider_id, group_id, payload, created_at
			FROM scheduler_outbox
			WHERE id > $1
			ORDER BY id ASC
			LIMIT $2
			FOR UPDATE
		), released AS (
			UPDATE scheduler_outbox AS o
			SET dedup_key = NULL
			FROM selected AS s
			WHERE o.id = s.id
				AND o.dedup_key IS NOT NULL
			RETURNING o.id
		)
		SELECT s.id, s.event_type, s.provider_id, s.group_id, s.payload, s.created_at
		FROM selected AS s
		CROSS JOIN (SELECT COUNT(*) FROM released) AS release_barrier
		ORDER BY s.id ASC
	`, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()

	events := make([]scheduler.SchedulerOutboxEvent, 0, limit)
	for rows.Next() {
		var (
			payloadRaw []byte
			providerID sql.NullInt64
			groupID    sql.NullInt64
			event      scheduler.SchedulerOutboxEvent
		)
		if err := rows.Scan(&event.ID, &event.EventType, &providerID, &groupID, &payloadRaw, &event.CreatedAt); err != nil {
			return nil, err
		}
		if providerID.Valid {
			v := providerID.Int64
			event.ProviderID = &v
		}
		if groupID.Valid {
			v := groupID.Int64
			event.GroupID = &v
		}
		if len(payloadRaw) > 0 {
			var payload map[string]any
			if err := json.Unmarshal(payloadRaw, &payload); err != nil {
				return nil, err
			}
			// Normalize legacy event payloads to the current provider field names.
			if ids, ok := payload["account_ids"]; ok {
				if _, exists := payload["provider_ids"]; !exists {
					payload["provider_ids"] = ids
				}
				delete(payload, "account_ids")
			}
			event.Payload = payload
		}
		event.EventType = strings.Replace(event.EventType, "account_", "provider_", 1)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func (r *schedulerOutboxRepository) FirstCreatedAtAfter(ctx context.Context, afterID int64) (time.Time, bool, error) {
	var createdAt time.Time
	err := r.db.QueryRowContext(ctx, `
		SELECT created_at
		FROM scheduler_outbox
		WHERE id > $1
		ORDER BY id ASC
		LIMIT 1
	`, afterID).Scan(&createdAt)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return createdAt, true, nil
}

func (r *schedulerOutboxRepository) MaxID(ctx context.Context) (int64, error) {
	var maxID int64
	if err := r.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM scheduler_outbox").Scan(&maxID); err != nil {
		return 0, err
	}
	return maxID, nil
}

func (r *schedulerOutboxRepository) DeleteConsumedUpTo(ctx context.Context, watermark int64, limit int) (int64, error) {
	if watermark <= 0 {
		return 0, nil
	}
	if limit <= 0 {
		limit = schedulerOutboxDefaultCleanSize
	}
	// Keep a ten-second cleanup grace period. Late commits below the watermark can still be missed and are repaired by periodic rebuilds.
	result, err := r.db.ExecContext(ctx, `
		WITH doomed AS (
			SELECT id
			FROM scheduler_outbox
			WHERE id <= $1
				AND created_at < NOW() - INTERVAL '10 seconds'
			ORDER BY id ASC
			LIMIT $2
		)
		DELETE FROM scheduler_outbox o
		USING doomed d
		WHERE o.id = d.id
	`, watermark, limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *schedulerOutboxRepository) TryAcquireCleanupLock(ctx context.Context) (scheduler.SchedulerOutboxCleanupLease, bool, error) {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, false, err
	}

	var acquired bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtext('scheduler_outbox_cleanup'))").Scan(&acquired); err != nil {
		_ = conn.Close()
		return nil, false, err
	}
	if !acquired {
		_ = conn.Close()
		return nil, false, nil
	}
	return &schedulerOutboxCleanupLease{conn: conn}, true, nil
}

func (l *schedulerOutboxCleanupLease) Release() {
	if l == nil || l.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = l.conn.ExecContext(ctx, "SELECT pg_advisory_unlock(hashtext('scheduler_outbox_cleanup'))")
	_ = l.conn.Close()
	l.conn = nil
}

func enqueueSchedulerOutbox(ctx context.Context, exec postgresinfra.Executor, eventType string, providerID *int64, groupID *int64, payload any) error {
	if exec == nil {
		return nil
	}
	var payloadArg any
	var payloadJSON []byte
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		payloadArg = encoded
		payloadJSON = encoded
	}
	query := `
		INSERT INTO scheduler_outbox (event_type, provider_id, group_id, payload)
		VALUES ($1, $2, $3, $4)
	`
	args := []any{eventType, providerID, groupID, payloadArg}
	if schedulerOutboxEventSupportsDedup(eventType) {
		dedupKey := schedulerOutboxDedupKey(eventType, providerID, groupID, payloadJSON)
		query = `
			INSERT INTO scheduler_outbox (event_type, provider_id, group_id, payload, dedup_key)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (dedup_key) WHERE dedup_key IS NOT NULL DO NOTHING
		`
		args = append(args, dedupKey)
	}
	_, err := exec.ExecContext(ctx, query, args...)
	return err
}

func schedulerOutboxDedupKey(eventType string, providerID *int64, groupID *int64, payloadJSON []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(eventType))
	_, _ = h.Write([]byte{0})
	if providerID != nil {
		_, _ = h.Write([]byte(strconv.FormatInt(*providerID, 10)))
	}
	_, _ = h.Write([]byte{0})
	if groupID != nil {
		_, _ = h.Write([]byte(strconv.FormatInt(*groupID, 10)))
	}
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(payloadJSON)
	return fmt.Sprintf("scheduler_outbox:%s", hex.EncodeToString(h.Sum(nil)))
}

func schedulerOutboxEventSupportsDedup(eventType string) bool {
	switch eventType {
	case scheduler.SchedulerOutboxEventProviderChanged,
		scheduler.SchedulerOutboxEventGroupChanged,
		scheduler.SchedulerOutboxEventFullRebuild:
		return true
	default:
		return false
	}
}

// EnqueueProviderQuotaChangedInTx writes quota changes in the caller's transaction, committing or rolling back with funding updates.
func EnqueueProviderQuotaChangedInTx(ctx context.Context, tx *sql.Tx, providerID int64) error {
	return enqueueSchedulerOutbox(ctx, tx, scheduler.SchedulerOutboxEventProviderChanged, &providerID, nil, nil)
}

// EnqueueSchedulerChange writes through exec; the caller manages the transaction and publication after commit.
func EnqueueSchedulerChange(ctx context.Context, exec postgresinfra.Executor, eventType string, providerID, groupID *int64, payload any) error {
	return enqueueSchedulerOutbox(ctx, exec, eventType, providerID, groupID, payload)
}
