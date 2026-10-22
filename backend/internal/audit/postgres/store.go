package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/TokenFlux/TokenRouter/internal/audit"
	sqlutil "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

// store.go persists audit records and clears audit history together with a
// retained clearing trace.
// Clearing audit history needs to retain evidence of the clear itself, including
// how many records were removed. An exclusive table lock keeps the count,
// truncation, and trace insertion inside one transaction while concurrent writers
// wait. The retained trace contains its own copy of the Extra map so adding the
// deleted count does not mutate metadata supplied by the handler.
//
// Ordinary writes use bounded text columns and either one insert or transactional
// COPY batches. List and detail queries share a fixed scanning order, but lists
// remove captured bodies before returning records. Retention deletes bounded sets
// of expired identifiers, allowing each instance to repeat cleanup independently
// without coupling that work to administrative table clearing.

// Audit SQL columns define the positional order shared by inserts and scans.
// Text reads normalize nullable metadata so stored records have usable string fields.
const (
	auditLogInsertColumns = `created_at, actor_user_id, actor_email, actor_role, auth_method,
credential_masked, action, method, path, request_id, client_ip, user_agent,
request_body, status_code, latency_ms, extra`

	auditLogSelectColumns = `
  l.id,
  l.created_at,
  l.actor_user_id,
  COALESCE(l.actor_email, ''),
  COALESCE(l.actor_role, ''),
  COALESCE(l.auth_method, ''),
  COALESCE(l.credential_masked, ''),
  COALESCE(l.action, ''),
  COALESCE(l.method, ''),
  COALESCE(l.path, ''),
  COALESCE(l.request_id, ''),
  COALESCE(l.client_ip, ''),
  COALESCE(l.user_agent, ''),
  COALESCE(l.request_body, ''),
  l.status_code,
  l.latency_ms,
  COALESCE(l.extra::text, '{}')`
)

// Store persists audit records through raw SQL.
// Deletion is exposed as retention batches or full clearing; callers authorize full clearing with 2FA.
type Store struct {
	db *sql.DB // shared audit database pool
}

// NewAuditLogRepository creates the audit store using the supplied database pool.
func NewAuditLogRepository(db *sql.DB) *Store {
	return &Store{db: db}
}

// auditLogInsertValues returns SQL values in auditLogInsertColumns order for a
// nonnil record. It supplies a missing timestamp, omits nonpositive actor IDs,
// bounds metadata text, and replaces unencodable Extra values with an empty object.
func auditLogInsertValues(log *audit.AuditLog) []any {
	createdAt := log.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	extraJSON := "{}"
	if len(log.Extra) > 0 {
		// Extra is optional observation metadata. An encoding failure leaves an empty
		// object so unsupported values cannot prevent storing the actor and request outcome.
		if encoded, err := json.Marshal(log.Extra); err == nil {
			extraJSON = string(encoded)
		}
	}
	// Bound metadata to the database column widths before either insertion path,
	// while RequestBody retains the redactor's separate byte limit and truncation marker.
	return []any{
		createdAt.UTC(),
		sqlutil.NullPositiveInt64(log.ActorUserID),
		sqlutil.TruncateText(log.ActorEmail, 255),
		sqlutil.TruncateText(log.ActorRole, 32),
		sqlutil.TruncateText(log.AuthMethod, 32),
		sqlutil.TruncateText(log.CredentialMasked, 160),
		sqlutil.TruncateText(log.Action, 128),
		sqlutil.TruncateText(log.Method, 16),
		sqlutil.TruncateText(log.Path, 512),
		sqlutil.TruncateText(log.RequestID, 64),
		sqlutil.TruncateText(log.ClientIP, 64),
		sqlutil.TruncateText(log.UserAgent, 512),
		log.RequestBody,
		log.StatusCode,
		log.LatencyMs,
		extraJSON,
	}
}

// BatchInsert writes all nonnil records using one transactional COPY and returns
// the number submitted to the COPY statement. Empty input succeeds immediately,
// and missing repository configuration returns an error.
//
// Preparation, row submission, COPY flush, and statement closing precede commit.
// Failures before commit roll back the transaction, so a positive returned count
// on error describes attempted rows rather than committed records. The writer
// uses that error to count the whole failed batch and discards it after reporting.
func (r *Store) BatchInsert(ctx context.Context, logs []*audit.AuditLog) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("nil audit log repository")
	}
	if len(logs) == 0 {
		return 0, nil
	}

	// Keep COPY setup, every row, its final flush, and commit in one transaction;
	// any preparation or submission failure rolls back the batch as a single unit.
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	stmt, err := tx.PrepareContext(ctx, pq.CopyIn(
		"audit_logs",
		"created_at", "actor_user_id", "actor_email", "actor_role", "auth_method",
		"credential_masked", "action", "method", "path", "request_id", "client_ip", "user_agent",
		"request_body", "status_code", "latency_ms", "extra",
	))
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}

	var inserted int64
	for _, log := range logs {
		if log == nil {
			continue
		}
		if _, err := stmt.ExecContext(ctx, auditLogInsertValues(log)...); err != nil {
			// Release the COPY statement before abandoning its transaction, so a failed
			// row cannot leave the connection occupied by an unfinished streaming operation.
			_ = stmt.Close()
			_ = tx.Rollback()
			return inserted, err
		}
		inserted++
	}

	// An empty Exec flushes lib/pq's COPY buffer before the statement is closed.
	// Submitted counts include buffered rows, so flush and close precede successful persistence.
	if _, err := stmt.ExecContext(ctx); err != nil {
		_ = stmt.Close()
		_ = tx.Rollback()
		return inserted, err
	}
	if err := stmt.Close(); err != nil {
		_ = tx.Rollback()
		return inserted, err
	}
	if err := tx.Commit(); err != nil {
		return inserted, err
	}
	return inserted, nil
}

// Insert synchronously stores one audit record using the common insertion value
// conversion. Nil records and unavailable repositories return errors; callers use
// the returned error to decide whether the observation was persisted.
func (r *Store) Insert(ctx context.Context, log *audit.AuditLog) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("nil audit log repository")
	}
	if log == nil {
		return fmt.Errorf("nil audit log")
	}
	query := `INSERT INTO audit_logs (` + auditLogInsertColumns + `)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`
	_, err := r.db.ExecContext(ctx, query, auditLogInsertValues(log)...)
	return err
}

// buildAuditLogsWhere returns parameterized predicates and arguments for a nonnil
// filter. Times are inclusive, text substrings escape LIKE metacharacters, and
// request identifiers resolve through request_lookup_ids before matching stored request IDs.
func buildAuditLogsWhere(filter *audit.AuditLogFilter) (string, []any) {
	clauses := make([]string, 0, 10)
	args := make([]any, 0, 10)
	clauses = append(clauses, "1=1")

	if filter.StartTime != nil {
		args = append(args, filter.StartTime.UTC())
		clauses = append(clauses, "l.created_at >= $"+strconv.Itoa(len(args)))
	}
	if filter.EndTime != nil {
		args = append(args, filter.EndTime.UTC())
		clauses = append(clauses, "l.created_at <= $"+strconv.Itoa(len(args)))
	}
	if filter.ActorUserID != nil {
		args = append(args, *filter.ActorUserID)
		clauses = append(clauses, "l.actor_user_id = $"+strconv.Itoa(len(args)))
	}
	if v := strings.TrimSpace(filter.ActorEmail); v != "" {
		args = append(args, "%"+sqlutil.EscapeLikePattern(v)+"%")
		clauses = append(clauses, "l.actor_email ILIKE $"+strconv.Itoa(len(args)))
	}
	if v := strings.TrimSpace(filter.AuthMethod); v != "" {
		args = append(args, v)
		clauses = append(clauses, "l.auth_method = $"+strconv.Itoa(len(args)))
	}
	if v := strings.TrimSpace(filter.Action); v != "" {
		args = append(args, "%"+sqlutil.EscapeLikePattern(v)+"%")
		clauses = append(clauses, "l.action ILIKE $"+strconv.Itoa(len(args)))
	}
	if v := strings.TrimSpace(filter.Method); v != "" {
		args = append(args, strings.ToUpper(v))
		clauses = append(clauses, "l.method = $"+strconv.Itoa(len(args)))
	}
	if v := strings.TrimSpace(filter.ClientIP); v != "" {
		args = append(args, v)
		clauses = append(clauses, "l.client_ip = $"+strconv.Itoa(len(args)))
	}
	if filter.Success != nil {
		// Audit success follows the HTTP error split, treating every status below
		// 400 as successful even when the response redirects or has no body.
		if *filter.Success {
			clauses = append(clauses, "l.status_code < 400")
		} else {
			clauses = append(clauses, "l.status_code >= 400")
		}
	}
	if v := strings.TrimSpace(filter.RequestID); v != "" {
		// Resolve external aliases and compatible billing identifiers before filtering,
		// so the audit lookup uses the same platform IDs as request-detail searches.
		args = append(args, v)
		clauses = append(clauses, "l.request_id = ANY(ARRAY(SELECT id FROM request_lookup_ids($"+strconv.Itoa(len(args))+")))")
	}
	if v := strings.TrimSpace(filter.Query); v != "" {
		args = append(args, "%"+sqlutil.EscapeLikePattern(v)+"%")
		idx := strconv.Itoa(len(args))
		clauses = append(clauses, "(l.path ILIKE $"+idx+" OR l.action ILIKE $"+idx+" OR l.actor_email ILIKE $"+idx+" OR l.credential_masked ILIKE $"+idx+")")
	}

	return "WHERE " + strings.Join(clauses, " AND "), args
}

// scanAuditLogRow decodes the fixed auditLogSelectColumns order through the
// supplied scan function. Nullable actor IDs remain absent, and malformed or empty
// Extra text leaves optional metadata unset while retaining the rest of the record.
func scanAuditLogRow(scan func(dest ...any) error) (*audit.AuditLog, error) {
	item := &audit.AuditLog{}
	var actorUserID sql.NullInt64
	var extraRaw string
	if err := scan(
		&item.ID,
		&item.CreatedAt,
		&actorUserID,
		&item.ActorEmail,
		&item.ActorRole,
		&item.AuthMethod,
		&item.CredentialMasked,
		&item.Action,
		&item.Method,
		&item.Path,
		&item.RequestID,
		&item.ClientIP,
		&item.UserAgent,
		&item.RequestBody,
		&item.StatusCode,
		&item.LatencyMs,
		&extraRaw,
	); err != nil {
		return nil, err
	}
	if actorUserID.Valid {
		v := actorUserID.Int64
		item.ActorUserID = &v
	}
	extraRaw = strings.TrimSpace(extraRaw)
	if extraRaw != "" && extraRaw != "null" && extraRaw != "{}" {
		extra := make(map[string]any)
		// Optional metadata decoding cannot hide the actor and outcome from detail
		// queries; invalid stored JSON leaves Extra unset so the record remains inspectable.
		if err := json.Unmarshal([]byte(extraRaw), &extra); err == nil {
			item.Extra = extra
		}
	}
	return item, nil
}

// List returns a newest-first page and total matching the supplied audit filter.
// Nil filters select page one with fifty records; page size is capped at two
// hundred, and returned records omit captured request bodies. Count and page
// queries run separately, so concurrent writes can change the page after counting.
func (r *Store) List(ctx context.Context, filter *audit.AuditLogFilter) (*audit.AuditLogList, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil audit log repository")
	}
	if filter == nil {
		filter = &audit.AuditLogFilter{}
	}

	page := filter.Page
	if page <= 0 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		// Bound record count even when a caller bypasses HTTP pagination validation.
		pageSize = 200
	}

	where, args := buildAuditLogsWhere(filter)
	countSQL := "SELECT COUNT(*) FROM audit_logs l " + where
	var total int
	if err := r.db.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, err
	}

	offset := (page - 1) * pageSize
	argsWithLimit := append(args, pageSize, offset)
	query := "SELECT" + auditLogSelectColumns + "\nFROM audit_logs l\n" + where + `
ORDER BY l.created_at DESC, l.id DESC
LIMIT $` + strconv.Itoa(len(args)+1) + ` OFFSET $` + strconv.Itoa(len(args)+2)

	rows, err := r.db.QueryContext(ctx, query, argsWithLimit...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	logs := make([]*audit.AuditLog, 0, pageSize)
	for rows.Next() {
		item, err := scanAuditLogRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		// Omit bodies from list responses to reduce payload size; details include the full record.
		item.RequestBody = ""
		logs = append(logs, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &audit.AuditLogList{
		Logs:     logs,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

// GetByID returns the full stored record for the supplied audit identifier.
// Missing rows return ErrAuditLogNotFound; unavailable repository and query errors
// remain distinguishable for callers handling the administrator detail response.
func (r *Store) GetByID(ctx context.Context, id int64) (*audit.AuditLog, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil audit log repository")
	}
	query := "SELECT" + auditLogSelectColumns + "\nFROM audit_logs l WHERE l.id = $1"
	row := r.db.QueryRowContext(ctx, query, id)
	item, err := scanAuditLogRow(row.Scan)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, audit.ErrAuditLogNotFound
		}
		return nil, err
	}
	return item, nil
}

// Count returns the total audit row count from the configured database.
// Missing repository configuration returns an error instead of a misleading zero
// that callers might interpret as an empty audit history.
func (r *Store) Count(ctx context.Context) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("nil audit log repository")
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_logs").Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

// TruncateAll removes audit records through a single PostgreSQL TRUNCATE statement.
// It retains sequence behavior and creates no trace; production administrative
// clearing uses ClearWithTrace to keep evidence of the clear operation.
func (r *Store) TruncateAll(ctx context.Context) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("nil audit log repository")
	}
	_, err := r.db.ExecContext(ctx, "TRUNCATE TABLE audit_logs")
	return err
}

// DeleteBefore removes a bounded set of records strictly older than the UTC
// cutoff and returns the affected row count. Nonpositive batch sizes choose five
// thousand; callers repeat until zero while concurrent deletions remain harmless.
func (r *Store) DeleteBefore(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("nil audit log repository")
	}
	if batchSize <= 0 {
		batchSize = 5000
	}
	res, err := r.db.ExecContext(ctx, `
WITH batch AS (
  SELECT id FROM audit_logs WHERE created_at < $1 ORDER BY id LIMIT $2
)
DELETE FROM audit_logs WHERE id IN (SELECT id FROM batch)`, cutoff.UTC(), batchSize)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ClearWithTrace removes audit history and retains the supplied nonnil trace in
// one PostgreSQL transaction, returning the deleted count after commit. Callers
// verify authorization before invoking this store.
//
// An exclusive table lock prevents concurrent writers from changing the count
// between counting and truncation. Trace insertion shares that transaction, so
// insertion failure rolls back the clear. The trace's Extra map is copied before
// adding deleted_rows, keeping the handler's metadata unchanged by that addition.
func (r *Store) ClearWithTrace(ctx context.Context, trace *audit.AuditLog) (int64, error) {
	if trace == nil {
		return 0, fmt.Errorf("audit clear trace is required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	// Roll back any early exit before commit. Hold the exclusive table lock across
	// counting, truncation, and trace insertion so the trace describes exactly this clear.
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, "LOCK TABLE audit_logs IN ACCESS EXCLUSIVE MODE"); err != nil {
		return 0, err
	}
	var count int64
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_logs").Scan(&count); err != nil {
		return 0, fmt.Errorf("count audit logs: %w", err)
	}
	if _, err = tx.ExecContext(ctx, "TRUNCATE TABLE audit_logs"); err != nil {
		return 0, fmt.Errorf("truncate audit logs: %w", err)
	}
	copied := *trace
	// Add transaction-specific metadata to a new map because the supplied trace
	// can still be referenced by the handler that requested the administrative clear.
	copied.Extra = make(map[string]any, len(trace.Extra)+1)
	maps.Copy(copied.Extra, trace.Extra)
	copied.Extra["deleted_rows"] = count
	query := `INSERT INTO audit_logs (` + auditLogInsertColumns + `) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`
	if _, err = tx.ExecContext(ctx, query, auditLogInsertValues(&copied)...); err != nil {
		return 0, fmt.Errorf("persist audit clear trace: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}
