package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"

	infra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// ops_repo.go stores and queries Ops errors and system logs, including
// resolution and cleanup audit records.
// Ops error lists can display an upstream status even when the final client
// request recovered successfully. The final status still controls ordinary
// request-error admission, with the cyber_policy exception retaining streamed
// rejections and provider-health filters opting into recovered attempts. Model
// filtering and ordering also differ: filtering uses the saved requested model
// whenever it is nonnull, while ordering treats a blank requested model as
// absent.
//
// Writes encode optional values according to their consumers. General numeric
// encoders map zero to NULL, but the upstream status encoder preserves explicit
// zero for credential acquisition failures. Error batches and system-log COPY
// batches commit as transactions, returning attempted counts when later work
// fails. Count and page reads run separately, so live writes can change their
// totals. Correlation filters resolve request lookup candidates while an exact
// local request record suppresses matches through external client IDs.

// insertOpsErrorLogSQL fixes error-column and placeholder order for the shared
// single-row and prepared transactional batch writes.
const insertOpsErrorLogSQL = `
INSERT INTO ops_error_logs (
  request_id,
  client_request_id,
  user_id,
  api_key_id,
  provider_id,
  group_id,
  client_ip,
  platform,
  model,
  request_path,
  stream,
  inbound_endpoint,
  upstream_endpoint,
  requested_model,
  upstream_model,
  request_type,
  user_agent,
  error_phase,
  error_type,
  severity,
  status_code,
  is_business_limited,
  is_count_tokens,
  error_message,
  error_body,
  error_source,
  error_owner,
  upstream_status_code,
  upstream_error_message,
  upstream_error_detail,
  upstream_errors,
  auth_latency_ms,
  routing_latency_ms,
  upstream_latency_ms,
  response_latency_ms,
  time_to_first_token_ms,
  created_at,
  api_key_prefix
) VALUES (
  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38
)`

// likePatternReplacer makes fuzzy model names literal inside PostgreSQL LIKE
// patterns while surrounding percent signs provide substring matching.
var likePatternReplacer = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// Store executes Ops queries and transactional writes through the caller's SQL
// pool.
type Store struct {
	// db is the borrowed connection pool. Constructors retain it, callers keep it
	// usable for queries, and repository methods leave pool closure to its owner.
	db *sql.DB
}

// NewOpsRepository retains db for Ops persistence and query methods. It accepts
// a nil pool without validation; subsequent methods report an unavailable
// repository, and callers own the pool's lifetime.
func NewOpsRepository(db *sql.DB) *Store {
	return &Store{db: db}
}

// InsertErrorLog inserts one prepared observation and returns its generated ID.
// OpsService supplies defaults, timestamps, and sanitization before this call;
// the repository performs nullable encoding and relies on database constraints
// for validity.
func (r *Store) InsertErrorLog(ctx context.Context, input *ops.OpsInsertErrorLogInput) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("nil ops repository")
	}
	if input == nil {
		return 0, fmt.Errorf("nil input")
	}

	var id int64
	err := r.db.QueryRowContext(
		ctx,
		insertOpsErrorLogSQL+" RETURNING id",
		opsInsertErrorLogArgs(input)...,
	).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// BatchInsertErrorLogs prepares one insert and executes each nonnil observation
// in a transaction. Empty input returns zero without opening a transaction; nil
// members are skipped. Inputs are expected to have passed OpsService
// preparation, as the repository applies nullable encoding but supplies no
// error timestamp or classification defaults.
//
// A failed execution rolls back the transaction even when the returned count
// includes previously executed rows. That count represents attempts within the
// transaction and is usable as a committed count only on success.
// Prepared-statement close and rollback errors are suppressed; a commit error
// is returned and leaves final persistence outcome dependent on the database.
func (r *Store) BatchInsertErrorLogs(ctx context.Context, inputs []*ops.OpsInsertErrorLogInput) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("nil ops repository")
	}
	if len(inputs) == 0 {
		return 0, nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	// Retain the transaction error through execution and commit so deferred
	// rollback removes all uncommitted batch rows; rollback errors leave the
	// primary failure authoritative.
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	stmt, err := tx.PrepareContext(ctx, insertOpsErrorLogSQL)
	if err != nil {
		return 0, err
	}
	// Close the prepared statement after execution or an early return. Close
	// errors are suppressed because transaction execution and commit determine the
	// reported write result.
	defer func() {
		_ = stmt.Close()
	}()

	var inserted int64
	for _, input := range inputs {
		if input == nil {
			continue
		}
		if _, err = stmt.ExecContext(ctx, opsInsertErrorLogArgs(input)...); err != nil {
			return inserted, err
		}
		inserted++
	}

	// Commit after every nonnil observation has executed. On error, inserted
	// counts attempted rows and deferred rollback still runs, without proving the
	// commit outcome.
	if err = tx.Commit(); err != nil {
		return inserted, err
	}
	return inserted, nil
}

// opsInsertErrorLogArgs encodes a nonnil prepared observation in the shared
// insert's exact column order. Blank platforms become unknown; general
// zero-valued optional integers become NULL, while an explicit upstream status
// zero survives for provider credential failures.
func opsInsertErrorLogArgs(input *ops.OpsInsertErrorLogInput) []any {
	// Store unknown when the observation lacks a platform. Queries retain this
	// saved dimension even if current providers or group membership change later.
	platform := strings.TrimSpace(input.Platform)
	if platform == "" {
		platform = "unknown"
	}
	return []any{
		opsNullString(input.RequestID),
		opsNullString(input.ClientRequestID),
		opsNullInt64(input.UserID),
		opsNullInt64(input.APIKeyID),
		opsNullInt64(input.ProviderID),
		opsNullInt64(input.GroupID),
		opsNullString(input.ClientIP),
		opsNullString(platform),
		opsNullString(input.Model),
		opsNullString(input.RequestPath),
		input.Stream,
		opsNullString(input.InboundEndpoint),
		opsNullString(input.UpstreamEndpoint),
		opsNullString(input.RequestedModel),
		opsNullString(input.UpstreamModel),
		opsNullInt16(input.RequestType),
		opsNullString(input.UserAgent),
		input.ErrorPhase,
		input.ErrorType,
		opsNullString(input.Severity),
		opsNullInt(input.StatusCode),
		input.IsBusinessLimited,
		input.IsCountTokens,
		opsNullString(input.ErrorMessage),
		opsNullString(input.ErrorBody),
		opsNullString(input.ErrorSource),
		opsNullString(input.ErrorOwner),
		opsNullableIntPointer(input.UpstreamStatusCode),
		opsNullString(input.UpstreamErrorMessage),
		opsNullString(input.UpstreamErrorDetail),
		opsNullString(input.UpstreamErrorsJSON),
		opsNullInt64(input.AuthLatencyMs),
		opsNullInt64(input.RoutingLatencyMs),
		opsNullInt64(input.UpstreamLatencyMs),
		opsNullInt64(input.ResponseLatencyMs),
		opsNullInt64(input.TimeToFirstTokenMs),
		input.CreatedAt,
		opsNullString(input.APIKeyPrefix),
	}
}

// opsErrorLogsOrderBy returns a validated sort expression with ID ordering in
// the same direction to break ties. Unknown columns select creation time, only
// asc selects ascending order, and model sorting falls back from blank
// requested model to execution model.
func opsErrorLogsOrderBy(filter *ops.OpsErrorLogFilter) string {
	sortBy := ""
	sortOrder := ""
	if filter != nil {
		sortBy = strings.ToLower(strings.TrimSpace(filter.SortBy))
		sortOrder = strings.ToLower(strings.TrimSpace(filter.SortOrder))
	}

	var column string
	switch sortBy {
	case "model":
		column = "COALESCE(NULLIF(TRIM(e.requested_model), ''), e.model)"
	case "status_code":
		// Match the displayed status when sorting recovered attempts: a present
		// upstream code wins over the final client code, including a stored zero.
		column = "COALESCE(e.upstream_status_code, e.status_code, 0)"
	default:
		column = "e.created_at"
	}

	dir := "DESC"
	if sortOrder == "asc" {
		dir = "ASC"
	}
	return fmt.Sprintf("%s %s, e.id %s", column, dir, dir)
}

// ListErrorLogs returns error observations and current joined display names
// after applying normalized page limits. A nil filter becomes an empty filter;
// nonpositive page and size become one and twenty, and size is capped at five
// hundred. Filters are read without mutation, and error ordering adds an ID tie
// breaker.
//
// Count and page are separate statements and can observe different writes.
// Displayed status prefers a nonnull upstream status over final client status,
// even when zero; stored account_auth phases are returned as provider_auth.
// Optional IDs and timestamps receive independent scalar pointers, while
// integer status and request type conversions are unchecked. Query, scan, and
// iteration failures reject the list.
func (r *Store) ListErrorLogs(ctx context.Context, filter *ops.OpsErrorLogFilter) (*ops.OpsErrorLogList, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		filter = &ops.OpsErrorLogFilter{}
	}

	page := filter.Page
	if page <= 0 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	// Limit the returned page while its separate count still covers every matching
	// observation, so pagination does not redefine the reported total.
	if pageSize > 500 {
		pageSize = 500
	}

	where, args := buildOpsErrorLogsWhere(filter)
	countSQL := "SELECT COUNT(*) FROM ops_error_logs e " + where

	var total int
	if err := r.db.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, err
	}

	offset := (page - 1) * pageSize
	argsWithLimit := append(args, pageSize, offset)
	selectSQL := `
SELECT
  e.id,
  e.created_at,
  e.error_phase,
  e.error_type,
  COALESCE(e.error_owner, ''),
  COALESCE(e.error_source, ''),
  e.severity,
  COALESCE(e.upstream_status_code, e.status_code, 0),
  COALESCE(e.platform, ''),
  COALESCE(e.model, ''),
  COALESCE(e.resolved, false),
  e.resolved_at,
  e.resolved_by_user_id,
  COALESCE(u2.email, ''),
  COALESCE(e.client_request_id, ''),
  COALESCE(e.request_id, ''),
  COALESCE(e.error_message, ''),
  e.user_id,
  COALESCE(u.email, ''),
  e.api_key_id,
  e.provider_id,
  COALESCE(a.name, ''),
  e.group_id,
  COALESCE(g.name, ''),
  CASE WHEN e.client_ip IS NULL THEN NULL ELSE host(e.client_ip) END,
  COALESCE(e.request_path, ''),
  e.stream,
  COALESCE(e.inbound_endpoint, ''),
  COALESCE(e.upstream_endpoint, ''),
  COALESCE(e.requested_model, ''),
  COALESCE(e.upstream_model, ''),
  COALESCE(e.user_agent, ''),
  e.request_type,
  COALESCE(ak.name, ''),
  ak.deleted_at
FROM ops_error_logs e
LEFT JOIN providers a ON e.provider_id = a.id
LEFT JOIN groups g ON e.group_id = g.id
LEFT JOIN users u ON e.user_id = u.id
LEFT JOIN users u2 ON e.resolved_by_user_id = u2.id
LEFT JOIN api_keys ak ON ak.id = e.api_key_id
` + where + `
ORDER BY ` + opsErrorLogsOrderBy(filter) + `
LIMIT $` + itoa(len(args)+1) + ` OFFSET $` + itoa(len(args)+2)

	rows, err := r.db.QueryContext(ctx, selectSQL, argsWithLimit...)
	if err != nil {
		return nil, err
	}
	// Close the result cursor on success or early scan failure. Cleanup errors are
	// suppressed, while rows.Err still reports iteration failures.
	defer func() { _ = rows.Close() }()

	out := make([]*ops.OpsErrorLog, 0, pageSize)
	for rows.Next() {
		var item ops.OpsErrorLog
		var statusCode sql.NullInt64
		var clientIP sql.NullString
		var userID sql.NullInt64
		var apiKeyID sql.NullInt64
		var providerID sql.NullInt64
		var providerName string
		var groupID sql.NullInt64
		var groupName string
		var userEmail string
		var resolvedAt sql.NullTime
		var resolvedBy sql.NullInt64
		var resolvedByName string
		var requestType sql.NullInt64
		var apiKeyName string
		var apiKeyDeletedAt sql.NullTime
		if err := rows.Scan(
			&item.ID,
			&item.CreatedAt,
			&item.Phase,
			&item.Type,
			&item.Owner,
			&item.Source,
			&item.Severity,
			&statusCode,
			&item.Platform,
			&item.Model,
			&item.Resolved,
			&resolvedAt,
			&resolvedBy,
			&resolvedByName,
			&item.ClientRequestID,
			&item.RequestID,
			&item.Message,
			&userID,
			&userEmail,
			&apiKeyID,
			&providerID,
			&providerName,
			&groupID,
			&groupName,
			&clientIP,
			&item.RequestPath,
			&item.Stream,
			&item.InboundEndpoint,
			&item.UpstreamEndpoint,
			&item.RequestedModel,
			&item.UpstreamModel,
			&item.UserAgent,
			&requestType,
			&apiKeyName,
			&apiKeyDeletedAt,
		); err != nil {
			return nil, err
		}
		// Allocate scalars for valid nullable observations, preserving zero values
		// separately from missing SQL data; status and request-type narrowing have no
		// range checks.
		if resolvedAt.Valid {
			t := resolvedAt.Time
			item.ResolvedAt = &t
		}
		if resolvedBy.Valid {
			v := resolvedBy.Int64
			item.ResolvedByUserID = &v
		}
		item.ResolvedByUserName = resolvedByName
		item.StatusCode = int(statusCode.Int64)
		if clientIP.Valid {
			s := clientIP.String
			item.ClientIP = &s
		}
		if userID.Valid {
			v := userID.Int64
			item.UserID = &v
		}
		item.UserEmail = userEmail
		if apiKeyID.Valid {
			v := apiKeyID.Int64
			item.APIKeyID = &v
		}
		if providerID.Valid {
			v := providerID.Int64
			item.ProviderID = &v
		}
		item.ProviderName = providerName
		if groupID.Valid {
			v := groupID.Int64
			item.GroupID = &v
		}
		item.GroupName = groupName
		if requestType.Valid {
			v := int16(requestType.Int64)
			item.RequestType = &v
		}
		item.APIKeyName = apiKeyName
		item.APIKeyDeleted = apiKeyDeletedAt.Valid
		// Return the provider_auth vocabulary while retaining stored legacy rows. The
		// filter builder accepts both phase spellings so queries and displayed phases
		// agree.
		if item.Phase == "account_auth" {
			item.Phase = "provider_auth"
		}
		out = append(out, &item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &ops.OpsErrorLogList{
		Errors:   out,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

// GetErrorLogByID loads one positive error identity with stored diagnostic data
// and current joined names. Missing rows return the database's no-rows error
// for OpsService to classify. The upstream status remains independently
// nullable, while the displayed status prefers it over final client status,
// including an explicit zero.
//
// Saved account_auth becomes provider_auth, and upstream JSON null becomes an
// empty string for downstream parsing. Optional IDs, timestamps, timings, and
// request type become scalar pointers; integer narrowing is unchecked. API-key
// prefix comes from the saved observation and can remain available after joined
// key metadata disappears.
func (r *Store) GetErrorLogByID(ctx context.Context, id int64) (*ops.OpsErrorLogDetail, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if id <= 0 {
		return nil, fmt.Errorf("invalid id")
	}

	q := `
SELECT
  e.id,
  e.created_at,
  e.error_phase,
  e.error_type,
  COALESCE(e.error_owner, ''),
  COALESCE(e.error_source, ''),
  e.severity,
  COALESCE(e.upstream_status_code, e.status_code, 0),
  COALESCE(e.platform, ''),
  COALESCE(e.model, ''),
  COALESCE(e.resolved, false),
  e.resolved_at,
  e.resolved_by_user_id,
  COALESCE(e.client_request_id, ''),
  COALESCE(e.request_id, ''),
  COALESCE(e.error_message, ''),
  COALESCE(e.error_body, ''),
  e.upstream_status_code,
  COALESCE(e.upstream_error_message, ''),
  COALESCE(e.upstream_error_detail, ''),
  COALESCE(e.upstream_errors::text, ''),
  e.is_business_limited,
  e.user_id,
  COALESCE(u.email, ''),
  e.api_key_id,
  e.provider_id,
  COALESCE(a.name, ''),
  e.group_id,
  COALESCE(g.name, ''),
  CASE WHEN e.client_ip IS NULL THEN NULL ELSE host(e.client_ip) END,
  COALESCE(e.request_path, ''),
  e.stream,
  COALESCE(e.inbound_endpoint, ''),
  COALESCE(e.upstream_endpoint, ''),
  COALESCE(e.requested_model, ''),
  COALESCE(e.upstream_model, ''),
  e.request_type,
  COALESCE(e.user_agent, ''),
  e.auth_latency_ms,
  e.routing_latency_ms,
  e.upstream_latency_ms,
  e.response_latency_ms,
  e.time_to_first_token_ms,
  COALESCE(e.api_key_prefix, ''),
  COALESCE(ak.name, ''),
  ak.deleted_at
FROM ops_error_logs e
LEFT JOIN users u ON e.user_id = u.id
LEFT JOIN providers a ON e.provider_id = a.id
LEFT JOIN groups g ON e.group_id = g.id
LEFT JOIN api_keys ak ON ak.id = e.api_key_id
WHERE e.id = $1
LIMIT 1`

	var out ops.OpsErrorLogDetail
	var statusCode sql.NullInt64
	var upstreamStatusCode sql.NullInt64
	var resolvedAt sql.NullTime
	var resolvedBy sql.NullInt64
	var clientIP sql.NullString
	var userID sql.NullInt64
	var apiKeyID sql.NullInt64
	var providerID sql.NullInt64
	var groupID sql.NullInt64
	var authLatency sql.NullInt64
	var routingLatency sql.NullInt64
	var upstreamLatency sql.NullInt64
	var responseLatency sql.NullInt64
	var ttft sql.NullInt64
	var requestType sql.NullInt64
	var detailAPIKeyName string
	var detailAPIKeyDeletedAt sql.NullTime

	err := r.db.QueryRowContext(ctx, q, id).Scan(
		&out.ID,
		&out.CreatedAt,
		&out.Phase,
		&out.Type,
		&out.Owner,
		&out.Source,
		&out.Severity,
		&statusCode,
		&out.Platform,
		&out.Model,
		&out.Resolved,
		&resolvedAt,
		&resolvedBy,
		&out.ClientRequestID,
		&out.RequestID,
		&out.Message,
		&out.ErrorBody,
		&upstreamStatusCode,
		&out.UpstreamErrorMessage,
		&out.UpstreamErrorDetail,
		&out.UpstreamErrors,
		&out.IsBusinessLimited,
		&userID,
		&out.UserEmail,
		&apiKeyID,
		&providerID,
		&out.ProviderName,
		&groupID,
		&out.GroupName,
		&clientIP,
		&out.RequestPath,
		&out.Stream,
		&out.InboundEndpoint,
		&out.UpstreamEndpoint,
		&out.RequestedModel,
		&out.UpstreamModel,
		&requestType,
		&out.UserAgent,
		&authLatency,
		&routingLatency,
		&upstreamLatency,
		&responseLatency,
		&ttft,
		&out.APIKeyPrefix,
		&detailAPIKeyName,
		&detailAPIKeyDeletedAt,
	)
	if err != nil {
		return nil, err
	}

	// Restore valid SQL scalars into independent pointers while leaving absence
	// nil. Display status and request type narrow integer widths unchecked after
	// the database scan.
	out.StatusCode = int(statusCode.Int64)
	if resolvedAt.Valid {
		t := resolvedAt.Time
		out.ResolvedAt = &t
	}
	if resolvedBy.Valid {
		v := resolvedBy.Int64
		out.ResolvedByUserID = &v
	}
	if clientIP.Valid {
		s := clientIP.String
		out.ClientIP = &s
	}
	if upstreamStatusCode.Valid {
		v := int(upstreamStatusCode.Int64)
		out.UpstreamStatusCode = &v
	}
	if userID.Valid {
		v := userID.Int64
		out.UserID = &v
	}
	if apiKeyID.Valid {
		v := apiKeyID.Int64
		out.APIKeyID = &v
	}
	if providerID.Valid {
		v := providerID.Int64
		out.ProviderID = &v
	}
	if groupID.Valid {
		v := groupID.Int64
		out.GroupID = &v
	}
	if authLatency.Valid {
		v := authLatency.Int64
		out.AuthLatencyMs = &v
	}
	if routingLatency.Valid {
		v := routingLatency.Int64
		out.RoutingLatencyMs = &v
	}
	if upstreamLatency.Valid {
		v := upstreamLatency.Int64
		out.UpstreamLatencyMs = &v
	}
	if responseLatency.Valid {
		v := responseLatency.Int64
		out.ResponseLatencyMs = &v
	}
	if ttft.Valid {
		v := ttft.Int64
		out.TimeToFirstTokenMs = &v
	}
	if requestType.Valid {
		v := int16(requestType.Int64)
		out.RequestType = &v
	}
	out.APIKeyName = detailAPIKeyName
	out.APIKeyDeleted = detailAPIKeyDeletedAt.Valid

	// Translate the legacy credential phase for current readers before normalizing
	// attempt JSON. A stored JSON null becomes empty so ParseOpsUpstreamErrors
	// sees no attempts.
	if out.Phase == "account_auth" {
		out.Phase = "provider_auth"
	}
	out.UpstreamErrors = strings.TrimSpace(out.UpstreamErrors)
	if out.UpstreamErrors == "null" {
		out.UpstreamErrors = ""
	}

	return &out, nil
}

// UpdateErrorResolution writes resolution state, timestamp, and optional actor
// for one positive error identity. A supplied nonzero timestamp is converted to
// UTC and used even when resolved is false; otherwise true selects the current
// UTC time and false selects NULL.
//
// The actor is encoded independently, including an explicit zero, so clearing
// resolution does not itself clear a supplied actor. One UPDATE writes all
// three values, but RowsAffected is not inspected: a missing row can return
// success. OpsService performs a separate existence read before this call,
// which can race with deletion.
func (r *Store) UpdateErrorResolution(ctx context.Context, errorID int64, resolved bool, resolvedByUserID *int64, resolvedAt *time.Time) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("nil ops repository")
	}
	if errorID <= 0 {
		return fmt.Errorf("invalid error id")
	}

	q := `
UPDATE ops_error_logs
SET
  resolved = $2,
  resolved_at = $3,
  resolved_by_user_id = $4
WHERE id = $1`

	at := sql.NullTime{}
	if resolvedAt != nil && !resolvedAt.IsZero() {
		at = sql.NullTime{Time: resolvedAt.UTC(), Valid: true}
	} else if resolved {
		now := time.Now().UTC()
		at = sql.NullTime{Time: now, Valid: true}
	}

	_, err := r.db.ExecContext(
		ctx,
		q,
		errorID,
		resolved,
		at,
		nullInt64(resolvedByUserID),
	)
	return err
}

// BatchInsertSystemLogs streams nonnil valid observations through PostgreSQL
// COPY in one transaction. Empty input returns zero immediately; blank levels
// or messages are skipped. Levels are lowercased, components default to app,
// empty metadata defaults to an object, and timestamps are supplied when zero
// and converted to UTC.
//
// After all rows are submitted, COPY is flushed and closed before commit.
// Submission, flush, or close failure rolls back the transaction; cleanup
// errors on those paths are suppressed. A returned count on error includes
// earlier submitted rows and does not establish persistence. A commit failure
// is returned with that attempted count, and caller-provided metadata is
// validated by PostgreSQL rather than parsed here.
func (r *Store) BatchInsertSystemLogs(ctx context.Context, inputs []*ops.OpsInsertSystemLogInput) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("nil ops repository")
	}
	if len(inputs) == 0 {
		return 0, nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	stmt, err := tx.PrepareContext(ctx, pq.CopyIn(
		"ops_system_logs",
		"created_at",
		"host",
		"level",
		"component",
		"message",
		"request_id",
		"client_request_id",
		"user_id",
		"api_key_id",
		"provider_id",
		"platform",
		"model",
		"extra",
	))
	if err != nil {
		// COPY setup failed before submission. Roll back the open transaction and
		// keep the setup error, suppressing cleanup failure.
		_ = tx.Rollback()
		return 0, err
	}

	var inserted int64
	for _, input := range inputs {
		if input == nil {
			continue
		}
		createdAt := input.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		component := strings.TrimSpace(input.Component)
		level := strings.ToLower(strings.TrimSpace(input.Level))
		message := strings.TrimSpace(input.Message)
		if level == "" || message == "" {
			continue
		}
		if component == "" {
			component = "app"
		}
		extra := strings.TrimSpace(input.ExtraJSON)
		if extra == "" {
			extra = "{}"
		}
		if _, err := stmt.ExecContext(
			ctx,
			createdAt.UTC(),
			opsNullString(input.Host),
			level,
			component,
			message,
			opsNullString(input.RequestID),
			opsNullString(input.ClientRequestID),
			opsNullInt64(input.UserID),
			opsNullInt64(input.APIKeyID),
			opsNullInt64(input.ProviderID),
			opsNullString(input.Platform),
			opsNullString(input.Model),
			extra,
		); err != nil {
			// Submission failed after earlier rows may have entered COPY buffers. Close
			// the statement, then roll back the whole batch; neither cleanup error
			// replaces the submission error.
			_ = stmt.Close()
			_ = tx.Rollback()
			return inserted, err
		}
		inserted++
	}

	// Flush COPY buffers, close the statement, and then commit. Flush or close
	// failure triggers rollback, and attempted counts remain separate from
	// persisted rows.
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

// ListSystemLogs returns system observations ordered by descending timestamp
// and ID with matching total count. A nil filter selects all observations;
// nonpositive page and size become one and fifty, and size is capped at two
// hundred. The filter's recognized conditions are applied identically to
// separate count and page statements.
//
// Concurrent writes can change data between those reads. Nullable identity
// values become independent scalar pointers, while empty, null, empty-object,
// or undecodable metadata leaves Extra absent. Metadata decode failure is
// deliberately suppressed so one unusable extra object does not discard the log
// row; query, scan, and cursor errors reject the response.
func (r *Store) ListSystemLogs(ctx context.Context, filter *ops.OpsSystemLogFilter) (*ops.OpsSystemLogList, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		filter = &ops.OpsSystemLogFilter{}
	}

	page := filter.Page
	if page <= 0 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	// Bound returned system observations while the independent count retains the
	// complete matching set for pagination metadata.
	if pageSize > 200 {
		pageSize = 200
	}

	where, args, _ := buildOpsSystemLogsWhere(filter)
	countSQL := "SELECT COUNT(*) FROM ops_system_logs l " + where
	var total int
	if err := r.db.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, err
	}

	offset := (page - 1) * pageSize
	argsWithLimit := append(args, pageSize, offset)
	query := `
SELECT
  l.id,
  l.created_at,
  COALESCE(l.host, ''),
  l.level,
  COALESCE(l.component, ''),
  COALESCE(l.message, ''),
  COALESCE(l.request_id, ''),
  COALESCE(l.client_request_id, ''),
  l.user_id,
  l.api_key_id,
  l.provider_id,
  COALESCE(l.platform, ''),
  COALESCE(l.model, ''),
  COALESCE(l.extra::text, '{}')
FROM ops_system_logs l
` + where + `
ORDER BY l.created_at DESC, l.id DESC
LIMIT $` + itoa(len(args)+1) + ` OFFSET $` + itoa(len(args)+2)

	rows, err := r.db.QueryContext(ctx, query, argsWithLimit...)
	if err != nil {
		return nil, err
	}
	// Close the result cursor on success or early scan failure. Cleanup errors are
	// suppressed, while rows.Err still reports iteration failures.
	defer func() { _ = rows.Close() }()

	logs := make([]*ops.OpsSystemLog, 0, pageSize)
	for rows.Next() {
		item := &ops.OpsSystemLog{}
		var userID sql.NullInt64
		var apiKeyID sql.NullInt64
		var providerID sql.NullInt64
		var extraRaw string
		if err := rows.Scan(
			&item.ID,
			&item.CreatedAt,
			&item.Host,
			&item.Level,
			&item.Component,
			&item.Message,
			&item.RequestID,
			&item.ClientRequestID,
			&userID,
			&apiKeyID,
			&providerID,
			&item.Platform,
			&item.Model,
			&extraRaw,
		); err != nil {
			return nil, err
		}
		if userID.Valid {
			v := userID.Int64
			item.UserID = &v
		}
		if apiKeyID.Valid {
			v := apiKeyID.Int64
			item.APIKeyID = &v
		}
		if providerID.Valid {
			v := providerID.Int64
			item.ProviderID = &v
		}
		// Metadata is supplementary to the indexed log fields. Empty or invalid
		// object JSON leaves Extra absent, so malformed metadata does not reject the
		// row.
		extraRaw = strings.TrimSpace(extraRaw)
		if extraRaw != "" && extraRaw != "null" && extraRaw != "{}" {
			extra := make(map[string]any)
			if err := json.Unmarshal([]byte(extraRaw), &extra); err == nil {
				item.Extra = extra
			}
		}
		logs = append(logs, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &ops.OpsSystemLogList{
		Logs:     logs,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

// DeleteSystemLogs removes observations matching the combined effective filter
// and returns RowsAffected. At least one condition is required; blank text, zero
// times, and nonpositive
// IDs do not constrain deletion; audit insertion is a separate OpsService
// operation after this statement succeeds.
func (r *Store) DeleteSystemLogs(ctx context.Context, filter *ops.OpsSystemLogCleanupFilter) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		filter = &ops.OpsSystemLogCleanupFilter{}
	}

	where, args, hasConstraint := buildOpsSystemLogsCleanupWhere(filter)
	if !hasConstraint {
		return 0, fmt.Errorf("cleanup requires at least one filter condition")
	}

	query := "DELETE FROM ops_system_logs l " + where
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// InsertSystemLogCleanupAudit writes the supplied cleanup record with a current
// UTC timestamp when CreatedAt is zero. The delete has already run
// independently, so failure here cannot undo removed system observations.
func (r *Store) InsertSystemLogCleanupAudit(ctx context.Context, input *ops.OpsSystemLogCleanupAudit) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("nil ops repository")
	}
	if input == nil {
		return fmt.Errorf("nil input")
	}
	createdAt := input.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO ops_system_log_cleanup_audits (
  created_at,
  operator_id,
  conditions,
  deleted_rows
) VALUES ($1,$2,$3,$4)
`, createdAt.UTC(), input.OperatorID, input.Conditions, input.DeletedRows)
	return err
}

// escapeLikePattern escapes PostgreSQL's default backslash, percent, and
// underscore pattern characters. Fuzzy model filtering adds its own outer
// percent signs afterward, while free-text and user-email filters retain caller
// wildcard characters.
func escapeLikePattern(s string) string {
	return likePatternReplacer.Replace(s)
}

// buildOpsErrorLogsWhere builds bound SQL predicates for a nonnil filter
// supplied by ListErrorLogs. Ordinary admission requires final client status of
// at least four hundred or cyber_policy; qualifying provider-health filters can
// include recovered rows. Default and unknown views use SLA-countable rules,
// excluded uses business-limit rules, and all adds neither classification
// restriction.
//
// Positive identity values add conjunctive restrictions. Explicit phase
// matching accepts stored account_auth for provider_auth, while phase/type
// arrays remain caller-supplied except that legacy addition. Display-status
// filtering prefers upstream status; explicit status lists take precedence over
// the Other category. Model filtering prefers any nonnull requested model,
// including blank, unlike model ordering's blank fallback. Fuzzy model text is
// escaped, but free text and user-email searches retain LIKE wildcards. Request
// correlation uses lookup candidates with exact-local precedence.
func buildOpsErrorLogsWhere(filter *ops.OpsErrorLogFilter) (string, []any) {
	clauses := make([]string, 0, 12)
	args := make([]any, 0, 12)
	clauses = append(clauses, "1=1")

	phaseFilter := ""
	if filter != nil {
		phaseFilter = strings.TrimSpace(strings.ToLower(filter.Phase))
	}
	resolvedFilter := (*bool)(nil)
	if filter != nil {
		resolvedFilter = filter.Resolved
	}
	// Provider-health opt-in admits recovered attempts only for eligible phases.
	// Keep cyber_policy rows under ordinary admission because streamed policy
	// rejections can retain final status 200.
	if !opsFilterIncludesRecoveredProviderRows(filter, phaseFilter) {
		clauses = append(clauses, "(COALESCE(e.status_code, 0) >= 400 OR e.error_type = 'cyber_policy')")
	}

	if filter.StartTime != nil && !filter.StartTime.IsZero() {
		args = append(args, filter.StartTime.UTC())
		clauses = append(clauses, "e.created_at >= $"+itoa(len(args)))
	}
	if filter.EndTime != nil && !filter.EndTime.IsZero() {
		args = append(args, filter.EndTime.UTC())
		// Use an exclusive end so adjacent requested windows partition observations
		// without double counting records exactly at the next window's start.
		clauses = append(clauses, "e.created_at < $"+itoa(len(args)))
	}
	if p := strings.TrimSpace(filter.Platform); p != "" {
		args = append(args, p)
		clauses = append(clauses, "e.platform = $"+itoa(len(args)))
	}
	if filter.GroupID != nil && *filter.GroupID > 0 {
		args = append(args, *filter.GroupID)
		clauses = append(clauses, "e.group_id = $"+itoa(len(args)))
	}
	if filter.ProviderID != nil && *filter.ProviderID > 0 {
		args = append(args, *filter.ProviderID)
		clauses = append(clauses, "e.provider_id = $"+itoa(len(args)))
	}
	if phase := phaseFilter; phase != "" {
		args = append(args, phase)
		condition := "e.error_phase = $" + itoa(len(args))
		// Include account_auth alongside provider_auth using direct phase predicates.
		// This reads retained legacy rows while the response exposes the current
		// credential-phase spelling.
		if phase == "provider_auth" {
			args = append(args, "account_auth")
			condition = "(" + condition + " OR e.error_phase = $" + itoa(len(args)) + ")"
		}
		clauses = append(clauses, condition)
	}
	if filter != nil {
		if owner := strings.TrimSpace(strings.ToLower(filter.Owner)); owner != "" {
			args = append(args, owner)
			clauses = append(clauses, "LOWER(COALESCE(e.error_owner,'')) = $"+itoa(len(args)))
		}
		if source := strings.TrimSpace(strings.ToLower(filter.Source)); source != "" {
			args = append(args, source)
			clauses = append(clauses, "LOWER(COALESCE(e.error_source,'')) = $"+itoa(len(args)))
		}
	}
	if resolvedFilter != nil {
		args = append(args, *resolvedFilter)
		clauses = append(clauses, "COALESCE(e.resolved,false) = $"+itoa(len(args)))
	}

	// Apply shared business-limit and SLA rules independently of displayed
	// upstream status. Upstream 429/529 remains countable when an ignored client
	// status does not classify it as excluded.
	ignoredStatusCodes := []int(nil)
	if filter != nil {
		ignoredStatusCodes = filter.IgnoredStatusCodes
	}
	businessLimitedSQL := opsBusinessLimitedSQL("e.status_code", "e.upstream_status_code", "e.upstream_errors", "e.error_owner", "e.is_business_limited", ignoredStatusCodes)
	slaCountableSQL := opsSLACountableSQL("e.status_code", "e.upstream_status_code", "e.upstream_errors", "e.error_owner", "e.is_business_limited", ignoredStatusCodes)
	view := ""
	if filter != nil {
		view = strings.ToLower(strings.TrimSpace(filter.View))
	}
	switch view {
	case "", "errors":
		clauses = append(clauses, slaCountableSQL)
	case "excluded":
		clauses = append(clauses, businessLimitedSQL)
	case "all":
		// no-op
	default:
		// Unknown view names select SLA-countable errors, matching empty-view
		// behavior so malformed selectors do not expose every excluded observation.
		clauses = append(clauses, slaCountableSQL)
	}
	if len(filter.StatusCodes) > 0 {
		args = append(args, pq.Array(filter.StatusCodes))
		clauses = append(clauses, "COALESCE(e.upstream_status_code, e.status_code, 0) = ANY($"+itoa(len(args))+")")
	} else if filter.StatusCodesOther {
		// Other excludes the dashboard's common displayed-status list. A nonempty
		// explicit status list takes precedence and never reaches this inverse
		// category.
		known := []int{400, 401, 403, 404, 409, 422, 429, 500, 502, 503, 504, 529}
		args = append(args, pq.Array(known))
		clauses = append(clauses, "NOT (COALESCE(e.upstream_status_code, e.status_code, 0) = ANY($"+itoa(len(args))+"))")
	}
	// Correlate internal and external IDs through request_lookup_ids with
	// exact-local precedence. A supplied ClientRequestID adds its own conjunctive
	// saved-column restriction afterward.
	if rid := strings.TrimSpace(filter.RequestID); rid != "" {
		args = append(args, rid)
		clauses = append(clauses, opsRequestIDCondition("e", len(args)))
	}
	if crid := strings.TrimSpace(filter.ClientRequestID); crid != "" {
		args = append(args, crid)
		clauses = append(clauses, "COALESCE(e.client_request_id,'') = $"+itoa(len(args)))
	}

	if q := strings.TrimSpace(filter.Query); q != "" {
		like := "%" + q + "%"
		args = append(args, like)
		n := itoa(len(args))
		clauses = append(clauses, "(e.request_id ILIKE $"+n+" OR e.client_request_id ILIKE $"+n+" OR e.error_message ILIKE $"+n+")")
	}

	if userQuery := strings.TrimSpace(filter.UserQuery); userQuery != "" {
		like := "%" + userQuery + "%"
		args = append(args, like)
		n := itoa(len(args))
		clauses = append(clauses, "EXISTS (SELECT 1 FROM users u WHERE u.id = e.user_id AND u.email ILIKE $"+n+")")
	}

	if filter.UserID != nil && *filter.UserID > 0 {
		args = append(args, *filter.UserID)
		n := itoa(len(args))
		clauses = append(clauses, "e.user_id = $"+n)
	}
	if filter.APIKeyID != nil && *filter.APIKeyID > 0 {
		args = append(args, *filter.APIKeyID)
		clauses = append(clauses, "e.api_key_id = $"+itoa(len(args)))
	}
	if m := strings.TrimSpace(filter.Model); m != "" {
		// Escape caller model wildcards before adding substring markers. COALESCE
		// treats a nonnull blank requested model as present, unlike ordering's
		// blank-model fallback.
		if filter.ModelFuzzy {
			args = append(args, "%"+escapeLikePattern(m)+"%")
			clauses = append(clauses, "COALESCE(e.requested_model, e.model, '') ILIKE $"+itoa(len(args)))
		} else {
			args = append(args, m)
			clauses = append(clauses, "COALESCE(e.requested_model, e.model, '') = $"+itoa(len(args)))
		}
	}
	if filter.ExcludeCountTokens {
		clauses = append(clauses, "COALESCE(e.is_count_tokens, false) = false")
	}
	if len(filter.ErrorPhasesAny) > 0 {
		phases := append([]string(nil), filter.ErrorPhasesAny...)
		// Copy the supplied phase slice before adding the legacy credential spelling,
		// so compatibility expansion leaves caller slice contents unchanged.
		if slices.Contains(filter.ErrorPhasesAny, "provider_auth") {
			phases = append(phases, "account_auth")
		}
		args = append(args, pq.Array(phases))
		clauses = append(clauses, "e.error_phase = ANY($"+itoa(len(args))+")")
	}
	if len(filter.ErrorTypesAny) > 0 {
		args = append(args, pq.Array(filter.ErrorTypesAny))
		clauses = append(clauses, "e.error_type = ANY($"+itoa(len(args))+")")
	}

	return "WHERE " + strings.Join(clauses, " AND "), args
}

// opsFilterIncludesRecoveredProviderRows reports whether
// IncludeRecoveredUpstream can remove final-client-status admission for
// provider health. Nil filters and a false opt-in retain ordinary request-error
// visibility. A nonempty normalized explicit phase takes precedence over phase
// arrays and must be upstream or provider_auth.
//
// When that explicit phase is empty, a nonempty array must contain only those
// eligible normalized phases. Any other entry keeps the status guard, so a
// mixed provider/request selection cannot expose recovered successes through
// the provider option. Predicate generation still applies its own exact phase
// values afterward; this helper determines admission alone.
func opsFilterIncludesRecoveredProviderRows(filter *ops.OpsErrorLogFilter, phaseFilter string) bool {
	if filter == nil || !filter.IncludeRecoveredUpstream {
		return false
	}
	// An explicit phase wins over the category array. Eligibility here cannot
	// widen mixed categories when that phase selects a non-provider request.
	if phaseFilter != "" {
		return phaseFilter == "upstream" || phaseFilter == "provider_auth"
	}
	if len(filter.ErrorPhasesAny) == 0 {
		return false
	}
	sawProviderPhase := false
	// Require every requested category to describe provider work before lifting
	// final-status admission. Rejecting one other phase retains visibility rules
	// for the whole mixed selection.
	for _, rawPhase := range filter.ErrorPhasesAny {
		switch strings.TrimSpace(strings.ToLower(rawPhase)) {
		case "upstream", "provider_auth":
			sawProviderPhase = true
		default:
			return false
		}
	}
	return sawProviderPhase
}

// opsRequestIDCondition builds a correlation predicate using a trusted SQL
// alias and positive placeholder number. request_lookup_ids supplies candidate
// IDs; when the searched value names an exact local request record,
// client_request_id candidates are suppressed.
//
// Stored request_id matches remain available, so an external alias collision
// does not broaden an exact local lookup through the client-ID column. The
// caller binds the searched value once even though the generated expression
// references that placeholder repeatedly. Database lookup rules determine
// candidate expansion rather than string manipulation here.
func opsRequestIDCondition(alias string, parameter int) string {
	// Use database candidate expansion together with a separate exact-local
	// existence guard. Repeated placeholders read one bound value while preventing
	// client-alias collision expansion.
	lookup := fmt.Sprintf("ARRAY(SELECT id FROM request_lookup_ids($%d))", parameter)
	return fmt.Sprintf("(%s.request_id = ANY(%s) OR (%s.client_request_id = ANY(%s)"+
		" AND NOT EXISTS (SELECT 1 FROM request_records exact_request WHERE exact_request.request_id=$%d)))",
		alias, lookup, alias, lookup, parameter)
}

// buildOpsSystemLogsWhere returns SQL, ordered arguments, and whether at least
// one effective condition was added. Nil and empty filters produce an
// unrestricted predicate with false. Only nonzero time endpoints, positive
// identities, and nonblank trimmed text count as constraints; level matching
// folds case while other exact text fields keep it.
//
// RequestID resolves lookup candidates with exact-local precedence, while
// ClientRequestID independently matches its saved column and combines
// conjunctively. Free text searches messages, both correlation columns, and
// serialized metadata with caller LIKE wildcards intact. DeleteSystemLogs uses
// the constraint flag to reject an unrestricted delete; listing may
// intentionally use the same unrestricted predicate.
func buildOpsSystemLogsWhere(filter *ops.OpsSystemLogFilter) (string, []any, bool) {
	clauses := make([]string, 0, 10)
	args := make([]any, 0, 10)
	clauses = append(clauses, "1=1")
	hasConstraint := false

	if filter != nil && filter.StartTime != nil && !filter.StartTime.IsZero() {
		args = append(args, filter.StartTime.UTC())
		clauses = append(clauses, "l.created_at >= $"+itoa(len(args)))
		hasConstraint = true
	}
	if filter != nil && filter.EndTime != nil && !filter.EndTime.IsZero() {
		args = append(args, filter.EndTime.UTC())
		clauses = append(clauses, "l.created_at < $"+itoa(len(args)))
		hasConstraint = true
	}
	if filter != nil {
		if v := strings.TrimSpace(filter.Host); v != "" {
			args = append(args, v)
			clauses = append(clauses, "l.host = $"+itoa(len(args)))
			hasConstraint = true
		}
		if v := strings.ToLower(strings.TrimSpace(filter.Level)); v != "" {
			args = append(args, v)
			clauses = append(clauses, "LOWER(COALESCE(l.level,'')) = $"+itoa(len(args)))
			hasConstraint = true
		}
		if v := strings.TrimSpace(filter.Component); v != "" {
			args = append(args, v)
			clauses = append(clauses, "COALESCE(l.component,'') = $"+itoa(len(args)))
			hasConstraint = true
		}
		if v := strings.TrimSpace(filter.RequestID); v != "" {
			args = append(args, v)
			clauses = append(clauses, opsRequestIDCondition("l", len(args)))
			hasConstraint = true
		}
		if v := strings.TrimSpace(filter.ClientRequestID); v != "" {
			args = append(args, v)
			clauses = append(clauses, "COALESCE(l.client_request_id,'') = $"+itoa(len(args)))
			hasConstraint = true
		}
		if filter.UserID != nil && *filter.UserID > 0 {
			args = append(args, *filter.UserID)
			clauses = append(clauses, "l.user_id = $"+itoa(len(args)))
			hasConstraint = true
		}
		if filter.APIKeyID != nil && *filter.APIKeyID > 0 {
			args = append(args, *filter.APIKeyID)
			clauses = append(clauses, "l.api_key_id = $"+itoa(len(args)))
			hasConstraint = true
		}
		if filter.ProviderID != nil && *filter.ProviderID > 0 {
			args = append(args, *filter.ProviderID)
			clauses = append(clauses, "l.provider_id = $"+itoa(len(args)))
			hasConstraint = true
		}
		if v := strings.TrimSpace(filter.Platform); v != "" {
			args = append(args, v)
			clauses = append(clauses, "COALESCE(l.platform,'') = $"+itoa(len(args)))
			hasConstraint = true
		}
		if v := strings.TrimSpace(filter.Model); v != "" {
			args = append(args, v)
			clauses = append(clauses, "COALESCE(l.model,'') = $"+itoa(len(args)))
			hasConstraint = true
		}
		if v := strings.TrimSpace(filter.Query); v != "" {
			like := "%" + v + "%"
			args = append(args, like)
			n := itoa(len(args))
			clauses = append(clauses, "(l.message ILIKE $"+n+" OR COALESCE(l.request_id,'') ILIKE $"+n+" OR COALESCE(l.client_request_id,'') ILIKE $"+n+" OR COALESCE(l.extra::text,'') ILIKE $"+n+")")
			hasConstraint = true
		}
	}

	return "WHERE " + strings.Join(clauses, " AND "), args, hasConstraint
}

// buildOpsSystemLogsCleanupWhere maps cleanup dimensions into the shared
// system-log predicate builder. Nil input becomes empty, pointer values are
// read as supplied, and only recognized effective conditions satisfy the
// deletion guard.
func buildOpsSystemLogsCleanupWhere(filter *ops.OpsSystemLogCleanupFilter) (string, []any, bool) {
	if filter == nil {
		filter = &ops.OpsSystemLogCleanupFilter{}
	}
	listFilter := &ops.OpsSystemLogFilter{
		StartTime:       filter.StartTime,
		EndTime:         filter.EndTime,
		Host:            filter.Host,
		Level:           filter.Level,
		Component:       filter.Component,
		RequestID:       filter.RequestID,
		ClientRequestID: filter.ClientRequestID,
		UserID:          filter.UserID,
		APIKeyID:        filter.APIKeyID,
		ProviderID:      filter.ProviderID,
		Platform:        filter.Platform,
		Model:           filter.Model,
		Query:           filter.Query,
	}
	return buildOpsSystemLogsWhere(listFilter)
}

// opsNullString trims concrete string or string-pointer values and returns
// nullable SQL text. Nil, blank, and unsupported types become NULL; this
// generic encoder does not accept separately defined string types through
// reflection.
func opsNullString(v any) any {
	switch s := v.(type) {
	case nil:
		return sql.NullString{}
	case *string:
		if s == nil || strings.TrimSpace(*s) == "" {
			return sql.NullString{}
		}
		return sql.NullString{String: strings.TrimSpace(*s), Valid: true}
	case string:
		if strings.TrimSpace(s) == "" {
			return sql.NullString{}
		}
		return sql.NullString{String: strings.TrimSpace(s), Valid: true}
	default:
		return sql.NullString{}
	}
}

// opsNullInt64 encodes an optional integer for IDs, timings, and other
// zero-as-missing fields. Nil and zero become NULL, negative values remain
// valid, and callers choose another encoder when zero is meaningful.
func opsNullInt64(v *int64) any {
	if v == nil || *v == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}

// opsNullInt encodes concrete int, int-pointer, and int64-pointer values for
// zero-as-missing fields. Nil, zero, and unsupported types become NULL;
// negative values survive, and supplied supported integers are converted to SQL
// int64.
func opsNullInt(v any) any {
	switch n := v.(type) {
	case nil:
		return sql.NullInt64{}
	case *int:
		if n == nil || *n == 0 {
			return sql.NullInt64{}
		}
		return sql.NullInt64{Int64: int64(*n), Valid: true}
	case *int64:
		if n == nil || *n == 0 {
			return sql.NullInt64{}
		}
		return sql.NullInt64{Int64: *n, Valid: true}
	case int:
		if n == 0 {
			return sql.NullInt64{}
		}
		return sql.NullInt64{Int64: int64(n), Valid: true}
	default:
		return sql.NullInt64{}
	}
}

// opsNullableIntPointer encodes a status or pool count while preserving an
// explicit zero. Nil means unavailable; provider credential failures use zero
// upstream status to distinguish absent inference from a missing observation.
func opsNullableIntPointer(v *int) any {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*v), Valid: true}
}

// opsNullInt16 encodes optional request type as a SQL integer, preserving zero
// and negative supplied values. Only nil becomes NULL; validation of the
// request-type enumeration belongs to the caller.
func opsNullInt16(v *int16) any {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*v), Valid: true}
}

// itoa returns decimal text for repository SQL parameter indexes.
func itoa(v int) string { return strconv.Itoa(v) }

// nullInt64 encodes a supplied integer as nullable SQL data. Nil becomes NULL,
// while zero and negative values remain present for resolution actor fields
// through infra.NullableInt64.
func nullInt64(v *int64) sql.NullInt64 { return infra.NullableInt64(v) }
