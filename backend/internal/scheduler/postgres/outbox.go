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

// outbox.go stores scheduler change events in PostgreSQL and coordinates
// polling, deduplication release, and cleanup.
// Scheduler events stay in PostgreSQL until a poller advances its Redis
// watermark. Pending idempotent events share a deduplication key, and
// selecting a batch releases that key in the same SQL statement so a later
// change can enqueue another event while the selected work is being handled.
//
// Selection decodes the returned rows after deduplication release, which can
// survive a decoding failure. Event IDs order polling but do not represent
// transaction commit order. Bounded deletion keeps recent rows for ten
// seconds; periodic snapshot rebuilds repair commits missed below the
// watermark. Cleanup uses an advisory lock on a pinned connection, returned
// by the lease owner after cleanup.

// schedulerOutboxDefaultCleanSize bounds one deletion statement when cleanup
// callers supply a nonpositive batch limit.
const schedulerOutboxDefaultCleanSize = 5000

// schedulerOutboxRepository reads persistent scheduling events and manages
// bounded consumed-event cleanup.
type schedulerOutboxRepository struct {
	// db is borrowed from application storage; constructing or releasing cleanup
	// leases leaves ownership of the database pool with its caller.
	db *sql.DB // shared database connection pool
}

// schedulerOutboxCleanupLease retains the connection holding the
// session-scoped cleanup advisory lock.
type schedulerOutboxCleanupLease struct {
	// conn stays pinned while the advisory lock is held and becomes nil after
	// sequential release returns it to the pool.
	conn *sql.Conn // pinned cleanup lock connection
}

// NewSchedulerOutboxRepository constructs event storage over the caller's
// database pool. The caller keeps the pool open while this repository and its
// pinned cleanup leases are in use.
func NewSchedulerOutboxRepository(db *sql.DB) scheduler.SchedulerOutboxRepository {
	return &schedulerOutboxRepository{db: db}
}

// ListAfterAndReleaseDedup returns events with IDs above afterID in ascending
// order and releases their pending deduplication keys. A nonpositive limit
// selects one hundred rows.
//
// The SQL statement locks and materializes the selected rows before clearing
// their keys, allowing another matching change to enqueue during event
// handling. Row scanning and payload decoding happen afterward, so a decoding
// error can follow successful deduplication release. Legacy account payload
// names become provider names, with existing provider_ids taking precedence.
// IDs describe polling order; a transaction committed later below afterID is
// outside this query.
func (r *schedulerOutboxRepository) ListAfterAndReleaseDedup(ctx context.Context, afterID int64, limit int) ([]scheduler.SchedulerOutboxEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	// Release selected dedup keys before returning rows so another matching
	// change can be persisted while SnapshotService processes this batch.
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
	// Close the result cursor after scans and decoding failures; discard its
	// close error so the returned failure describes the query and decoding work.
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
			// Older rows use account_ids; rename that payload member for current event
			// handlers while retaining provider_ids when both names are supplied.
			if ids, ok := payload["account_ids"]; ok {
				if _, exists := payload["provider_ids"]; !exists {
					payload["provider_ids"] = ids
				}
				delete(payload, "account_ids")
			}
			event.Payload = payload
		}
		// Translate the first legacy account_ event-name occurrence so persisted
		// events reach the provider handlers used by current snapshot rebuilding.
		event.EventType = strings.Replace(event.EventType, "account_", "provider_", 1)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// FirstCreatedAtAfter returns the creation time of the lowest visible event
// ID above afterID. An empty query returns found=false and a zero time, which
// lets lag checks distinguish absence from a database failure.
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

// MaxID returns the greatest currently visible scheduling event ID. An empty
// table returns zero, so backlog checks can compare their watermark with
// visible rows even before the first event is inserted.
func (r *schedulerOutboxRepository) MaxID(ctx context.Context) (int64, error) {
	var maxID int64
	if err := r.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM scheduler_outbox").Scan(&maxID); err != nil {
		return 0, err
	}
	return maxID, nil
}

// DeleteConsumedUpTo deletes at most limit rows at or below the consumed
// watermark and returns the affected count. A nonpositive watermark skips the
// statement; a nonpositive limit selects the default cleanup batch.
//
// The SQL age condition retains rows created within ten seconds because an
// earlier allocated ID can commit after a poll. This grace delays deletion
// but cannot make polling revisit IDs below its watermark. SnapshotService
// relies on periodic rebuilds to recover that missed event ordering.
func (r *schedulerOutboxRepository) DeleteConsumedUpTo(ctx context.Context, watermark int64, limit int) (int64, error) {
	if watermark <= 0 {
		return 0, nil
	}
	if limit <= 0 {
		limit = schedulerOutboxDefaultCleanSize
	}
	// Use PostgreSQL's creation time and server clock for the deletion cutoff;
	// ID order alone cannot distinguish recently committed rows from older
	// consumed work.
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

// TryAcquireCleanupLock attempts the session-scoped outbox cleanup lock on a
// pinned database connection. A successful caller releases the returned lease
// after cleanup; contention returns acquired=false with no lease.
func (r *schedulerOutboxRepository) TryAcquireCleanupLock(ctx context.Context) (scheduler.SchedulerOutboxCleanupLease, bool, error) {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, false, err
	}

	var acquired bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtext('scheduler_outbox_cleanup'))").Scan(&acquired); err != nil {
		// Return the pinned connection when the advisory-lock query fails so a
		// failed attempt releases its pool capacity before reporting the query
		// error.
		_ = conn.Close()
		return nil, false, err
	}
	if !acquired {
		// Another cleanup caller holds the advisory lock; return this unused pinned
		// connection before reporting contention with no cleanup lease.
		_ = conn.Close()
		return nil, false, nil
	}
	return &schedulerOutboxCleanupLease{conn: conn}, true, nil
}

// Release asks the driver to unlock with a detached two-second context, then
// returns the pinned connection to the pool. The deadline requests query
// cancellation; completion follows the driver's response or failure. Calls
// after sequential release are harmless, and callers serialize lease access.
func (l *schedulerOutboxCleanupLease) Release() {
	if l == nil || l.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Attempt unlock before pool return because PostgreSQL advisory locks belong
	// to the session. Unlock and close errors are discarded, leaving a failed
	// ownership release unconfirmed.
	_, _ = l.conn.ExecContext(ctx, "SELECT pg_advisory_unlock(hashtext('scheduler_outbox_cleanup'))")
	_ = l.conn.Close()
	l.conn = nil
}

// enqueueSchedulerOutbox encodes payload and inserts a scheduling event
// through the supplied executor. A nil executor skips insertion, while
// transaction commit and subsequent publication belong to the caller.
//
// Provider changes, group changes and full rebuilds use a hash of the event
// identity and exact encoded payload to coalesce pending duplicates. The
// partial conflict clause leaves a matching pending event in place. Other
// event types insert each observation, and an untyped nil payload contributes
// no JSON bytes to the deduplication hash.
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
	// Use the partial unique dedup index for coalescible state changes so
	// selected events can release their keys and later changes enqueue again.
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

// schedulerOutboxDedupKey hashes the event type, nullable provider and group
// identities, and supplied JSON bytes into a persisted pending-event key.
// Callers pass the same bytes that insertion uses so deduplication
// distinguishes different payload representations.
//
// NUL separators keep adjacent identity components from merging into an
// ambiguous decimal string. A missing identity contributes an empty
// component, while a pointer to zero contributes the digit zero. Nil payload
// bytes and the JSON text null therefore produce different hashes even when
// both originate from nil-shaped Go values.
func schedulerOutboxDedupKey(eventType string, providerID *int64, groupID *int64, payloadJSON []byte) string {
	h := sha256.New()
	// Separate event and nullable identity components before adding exact
	// payload bytes; SHA256's in-memory writes cannot produce a transport or
	// persistence error.
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

// schedulerOutboxEventSupportsDedup reports whether pending copies of this
// event type can coalesce.
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

// EnqueueProviderQuotaChangedInTx records a provider-change event in the
// caller's transaction. The caller commits or rolls back it together with
// quota changes, then handles any publication after commit.
func EnqueueProviderQuotaChangedInTx(ctx context.Context, tx *sql.Tx, providerID int64) error {
	return enqueueSchedulerOutbox(ctx, tx, scheduler.SchedulerOutboxEventProviderChanged, &providerID, nil, nil)
}

// EnqueueSchedulerChange writes an event through the supplied executor and
// returns its insertion error. The caller controls any transaction and
// publishes the persisted change after that transaction commits.
func EnqueueSchedulerChange(ctx context.Context, exec postgresinfra.Executor, eventType string, providerID, groupID *int64, payload any) error {
	return enqueueSchedulerOutbox(ctx, exec, eventType, providerID, groupID, payload)
}
