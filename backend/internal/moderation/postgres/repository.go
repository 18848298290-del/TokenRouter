package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// repository.go stores moderation audit records and applies transactional
// Cyber warning policies.
// Ordinary audit rows and retained image metadata are inserted together, but
// ordinary hashes, account changes, and notices already occur outside this
// store transaction. Cyber policy instead locks the actor before inserting
// warning and media, counts with the new warning present, writes optional
// disabled status through the same identity transaction participant, and
// commits its policy fields together.
//
// Assigned IDs and time fields reach caller-owned values before commit and can
// remain after a failed operation. Rollback is attempted on earlier returns; an
// uncertain commit requires reconciliation. Administrator detail reads load
// media metadata separately from the owner row, and summaries or pages use
// separate queries. Retention deletes are also independent commands, so a later
// error can leave earlier deletions applied.

// Store persists ordinary review records and coordinates Cyber policy with
// identity on a shared SQL transaction.
type Store struct {
	// users is required when a Cyber transaction carries a valid actor ID. It
	// returns identity operations bound to that same transaction; Store owns
	// commit and post-return effects remain with moderation.
	users UserParticipation // same-transaction identity participant factory
	// db is retained and closed by app. Ordinary operations require a usable
	// database; CleanupExpiredLogs alone accepts a missing Store or database as a
	// zero-delete result.
	db *sql.DB // application-owned shared SQL database
}

// sqlQueryRower accepts either a database or transaction for shared warning and
// media insertion.
type sqlQueryRower interface {
	// QueryRowContext returns a row whose Scan receives insertion identity or a
	// count result. The owning insertion supplies context and stable argument values;
	// a transaction implementation keeps those writes in its caller-owned transaction.
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	// ExecContext supplies statement execution for a database or transaction.
	// The insertion helpers currently use QueryRowContext; this method remains part
	// of their required capability even though those helpers do not invoke it.
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// UserStatusTx supplies identity locking and status writes bound to the
// transaction owned by moderation Store.
type UserStatusTx interface {
	// LockUser obtains the actor row lock and returns stored status plus whether
	// that row exists. The caller retains the transaction through warning insertion
	// and policy application, then owns commit or rollback of the combined work.
	LockUser(context.Context, int64) (string, bool, error)
	// SetDisabled requests disabled status inside the same transaction and reports
	// whether the participant observed a newly changed row. The moderation caller
	// uses that boolean for postcommit invalidation and notification after successful commit.
	SetDisabled(context.Context, int64) (bool, error)
}

// UserParticipation creates identity status operations bound to the supplied
// moderation-owned SQL transaction.
type UserParticipation func(*sql.Tx) UserStatusTx

// NewContentModerationRepository binds the shared DB and same-transaction
// identity factory. Construction opens no transaction or worker; a usable
// factory is required for valid-actor Cyber operations, and app retains
// ownership of DB closing.
func NewContentModerationRepository(db *sql.DB, users UserParticipation) *Store {
	return &Store{db: db, users: users}
}

// CreateLog writes an ordinary audit row and its retained media within one SQL
// transaction. Nil logs are a no-op. JSON encoding occurs before the
// transaction, then insertion assigns ID and CreatedAt before each media entry
// receives the shared log-ID pointer and is inserted.
//
// Errors before commit leave a deferred rollback attempt; commit errors can
// leave the database outcome uncertain. Caller-owned row and media values can
// retain precommit identities on any returned failure and are not proof of
// durable storage. Ordinary user actions, hashes, and notices run before this
// method in the service and are outside this transaction.
func (r *Store) CreateLog(ctx context.Context, log *moderation.ContentModerationLog) error {
	if log == nil {
		return nil
	}
	categoryScores, err := json.Marshal(log.CategoryScores)
	if err != nil {
		return fmt.Errorf("marshal moderation category scores: %w", err)
	}
	thresholdSnapshot, err := json.Marshal(log.ThresholdSnapshot)
	if err != nil {
		return fmt.Errorf("marshal moderation thresholds: %w", err)
	}
	inputItems, err := json.Marshal(log.InputItems)
	if err != nil {
		return fmt.Errorf("marshal moderation input items: %w", err)
	}
	failedUnits, err := json.Marshal(log.FailedUnits)
	if err != nil {
		return fmt.Errorf("marshal moderation failed units: %w", err)
	}
	var userID any
	if log.UserID != nil {
		userID = *log.UserID
	}
	var billingUserID any
	if log.BillingUserID != nil {
		billingUserID = *log.BillingUserID
	}
	var teamID any
	if log.TeamID != nil {
		teamID = *log.TeamID
	}
	var apiKeyID any
	if log.APIKeyID != nil {
		apiKeyID = *log.APIKeyID
	}
	var groupID any
	if log.GroupID != nil {
		groupID = *log.GroupID
	}
	var latency any
	if log.UpstreamLatencyMS != nil {
		latency = *log.UpstreamLatencyMS
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin content moderation log tx: %w", err)
	}
	// Leave rollback armed through every earlier return; after successful commit
	// it is inert. Its ignored error cannot resolve an uncertain commit outcome.
	defer func() { _ = tx.Rollback() }()
	err = tx.QueryRowContext(ctx, `
INSERT INTO content_moderation_logs (
    request_id, user_id, user_email, billing_user_id, team_id, api_key_id, api_key_name, group_id, group_name,
    endpoint, provider, model, mode, action, flagged, highest_category, highest_score,
	    category_scores, threshold_snapshot, input_excerpt, upstream_latency_ms, error,
	    violation_count, auto_banned, email_sent, queue_delay_ms, matched_keyword,
	    source, input_items, content_complete, audit_complete, text_unit_count, image_unit_count,
	    failed_unit_count, failed_units
	) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9,
    $10, $11, $12, $13, $14, $15, $16, $17,
    $18::jsonb, $19::jsonb, $20, $21, $22,
	    $23, $24, $25, $26, $27,
	    $28, $29::jsonb, $30, $31, $32, $33,
	    $34, $35::jsonb
	) RETURNING id, created_at`,
		log.RequestID, userID, log.UserEmail, billingUserID, teamID, apiKeyID, log.APIKeyName, groupID, log.GroupName,
		log.Endpoint, log.Provider, log.Model, log.Mode, log.Action, log.Flagged, log.HighestCategory, log.HighestScore,
		string(categoryScores), string(thresholdSnapshot), log.InputExcerpt, latency, log.Error,
		log.ViolationCount, log.AutoBanned, log.EmailSent, nullableIntPtr(log.QueueDelayMS), log.MatchedKeyword,
		log.Source, string(inputItems), log.ContentComplete, log.AuditComplete, log.TextUnitCount, log.ImageUnitCount,
		log.FailedUnitCount, string(failedUnits),
	).Scan(&log.ID, &log.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert content moderation log: %w", err)
	}
	// Assign the parent identity before inserting each media row in this same
	// transaction; failed child insertion returns before the parent can be committed here.
	for index := range log.Media {
		log.Media[index].LogID = &log.ID
		if err := insertContentModerationMedia(ctx, tx, &log.Media[index]); err != nil {
			return err
		}
	}
	// Commit after the ordinary row and every media insert succeeds. A commit
	// error leaves the caller responsible for reconciling whether the database
	// applied these writes.
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit content moderation log tx: %w", err)
	}
	return nil
}

// ListLogs returns newest-first ordinary summaries and pagination from separate
// count and page reads. Page and size default to one and twenty, size caps at
// one hundred, and SQL ordering ignores requested sort order. Full input,
// failed-unit detail, and media bytes are omitted from these summaries.
func (r *Store) ListLogs(ctx context.Context, filter moderation.ContentModerationLogFilter) ([]moderation.ContentModerationLog, *pagination.PaginationResult, error) {
	where, args := buildContentModerationLogWhere(filter)
	whereSQL := "WHERE " + strings.Join(where, " AND ")

	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM content_moderation_logs l "+whereSQL, args...).Scan(&total); err != nil {
		return nil, nil, fmt.Errorf("count content moderation logs: %w", err)
	}

	params := filter.Pagination
	if params.Page <= 0 {
		params.Page = 1
	}
	if params.PageSize <= 0 {
		params.PageSize = 20
	}
	// Bound administrator summary page size before decoding retained result rows.
	if params.PageSize > 100 {
		params.PageSize = 100
	}
	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, params.Limit(), params.Offset())
	rows, err := r.db.QueryContext(ctx, `
SELECT
    l.id, l.request_id, l.user_id, l.user_email, l.billing_user_id, l.team_id,
    l.api_key_id, l.api_key_name, l.group_id, l.group_name,
    l.endpoint, l.provider, l.model, l.mode, l.action, l.flagged, l.highest_category, l.highest_score,
    l.category_scores, l.threshold_snapshot, l.input_excerpt, l.upstream_latency_ms, l.error,
	    l.violation_count, l.auto_banned, l.email_sent, COALESCE(u.status, ''), l.queue_delay_ms, l.matched_keyword,
	    l.source, l.content_complete, l.audit_complete, l.text_unit_count, l.image_unit_count, l.failed_unit_count,
	    l.created_at
FROM content_moderation_logs l
LEFT JOIN users u ON u.id = l.user_id `+whereSQL+`
ORDER BY l.created_at DESC, l.id DESC
LIMIT $`+fmt.Sprint(len(queryArgs)-1)+` OFFSET $`+fmt.Sprint(len(queryArgs)),
		queryArgs...,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("list content moderation logs: %w", err)
	}
	// Release the query rows after scanning or any early failure; close errors
	// do not replace the earlier query, scan, or iteration result returned here.
	defer func() { _ = rows.Close() }()

	items := make([]moderation.ContentModerationLog, 0)
	for rows.Next() {
		var item moderation.ContentModerationLog
		var userID, billingUserID, teamID, apiKeyID, groupID, latency, queueDelay sql.NullInt64
		var scoresRaw, thresholdsRaw []byte
		if err := rows.Scan(
			&item.ID,
			&item.RequestID,
			&userID,
			&item.UserEmail,
			&billingUserID,
			&teamID,
			&apiKeyID,
			&item.APIKeyName,
			&groupID,
			&item.GroupName,
			&item.Endpoint,
			&item.Provider,
			&item.Model,
			&item.Mode,
			&item.Action,
			&item.Flagged,
			&item.HighestCategory,
			&item.HighestScore,
			&scoresRaw,
			&thresholdsRaw,
			&item.InputExcerpt,
			&latency,
			&item.Error,
			&item.ViolationCount,
			&item.AutoBanned,
			&item.EmailSent,
			&item.UserStatus,
			&queueDelay,
			&item.MatchedKeyword,
			&item.Source,
			&item.ContentComplete,
			&item.AuditComplete,
			&item.TextUnitCount,
			&item.ImageUnitCount,
			&item.FailedUnitCount,
			&item.CreatedAt,
		); err != nil {
			return nil, nil, fmt.Errorf("scan content moderation log: %w", err)
		}
		if userID.Valid {
			v := userID.Int64
			item.UserID = &v
		}
		if billingUserID.Valid {
			v := billingUserID.Int64
			item.BillingUserID = &v
		}
		if teamID.Valid {
			v := teamID.Int64
			item.TeamID = &v
		}
		if apiKeyID.Valid {
			v := apiKeyID.Int64
			item.APIKeyID = &v
		}
		if groupID.Valid {
			v := groupID.Int64
			item.GroupID = &v
		}
		if latency.Valid {
			v := int(latency.Int64)
			item.UpstreamLatencyMS = &v
		}
		if queueDelay.Valid {
			v := int(queueDelay.Int64)
			item.QueueDelayMS = &v
		}
		// Stored JSON can have an incompatible shape despite valid jsonb encoding;
		// keep initialized or partially decoded observations when these decode errors are ignored.
		item.CategoryScores = map[string]float64{}
		_ = json.Unmarshal(scoresRaw, &item.CategoryScores)
		item.ThresholdSnapshot = map[string]float64{}
		_ = json.Unmarshal(thresholdsRaw, &item.ThresholdSnapshot)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate content moderation logs: %w", err)
	}
	return items, paginationResultFromTotal(total, params), nil
}

// GetLog loads full ordinary review material and separately queries media
// metadata for the same ID. It joins current user status, retains initialized
// or partially decoded JSON after ignored decode errors, and returns wrapped
// missing-row errors to the service classifier.
func (r *Store) GetLog(ctx context.Context, id int64) (*moderation.ContentModerationLog, error) {
	var item moderation.ContentModerationLog
	var userID, billingUserID, teamID, apiKeyID, groupID, latency, queueDelay sql.NullInt64
	var scoresRaw, thresholdsRaw, inputItemsRaw, failedUnitsRaw []byte
	err := r.db.QueryRowContext(ctx, `
SELECT
    l.id, l.request_id, l.user_id, l.user_email, l.billing_user_id, l.team_id,
    l.api_key_id, l.api_key_name, l.group_id, l.group_name,
    l.endpoint, l.provider, l.model, l.mode, l.action, l.flagged, l.highest_category, l.highest_score,
    l.category_scores, l.threshold_snapshot, l.input_excerpt, l.upstream_latency_ms, l.error,
    l.violation_count, l.auto_banned, l.email_sent, COALESCE(u.status, ''), l.queue_delay_ms, l.matched_keyword,
    l.source, l.input_items, l.content_complete, l.audit_complete, l.text_unit_count, l.image_unit_count,
    l.failed_unit_count, l.failed_units, l.created_at
FROM content_moderation_logs l
LEFT JOIN users u ON u.id = l.user_id
WHERE l.id = $1
`, id).Scan(
		&item.ID, &item.RequestID, &userID, &item.UserEmail, &billingUserID, &teamID,
		&apiKeyID, &item.APIKeyName, &groupID, &item.GroupName,
		&item.Endpoint, &item.Provider, &item.Model, &item.Mode, &item.Action, &item.Flagged, &item.HighestCategory, &item.HighestScore,
		&scoresRaw, &thresholdsRaw, &item.InputExcerpt, &latency, &item.Error,
		&item.ViolationCount, &item.AutoBanned, &item.EmailSent, &item.UserStatus, &queueDelay, &item.MatchedKeyword,
		&item.Source, &inputItemsRaw, &item.ContentComplete, &item.AuditComplete, &item.TextUnitCount, &item.ImageUnitCount,
		&item.FailedUnitCount, &failedUnitsRaw, &item.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("get content moderation log: %w", err)
	}
	assignContentModerationNullableIDs(&item, userID, billingUserID, teamID, apiKeyID, groupID, latency, queueDelay)
	// Retain scalar review data after JSON shape failures; ignored errors can
	// leave partial decoded maps or items, so completeness labels stay the stored
	// observations.
	item.CategoryScores = map[string]float64{}
	item.ThresholdSnapshot = map[string]float64{}
	_ = json.Unmarshal(scoresRaw, &item.CategoryScores)
	_ = json.Unmarshal(thresholdsRaw, &item.ThresholdSnapshot)
	_ = json.Unmarshal(inputItemsRaw, &item.InputItems)
	_ = json.Unmarshal(failedUnitsRaw, &item.FailedUnits)
	item.Media, err = r.listContentModerationMedia(ctx, "log_id", id)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// assignContentModerationNullableIDs assigns present SQL identifiers and timing
// values to independent pointers on the supplied log. Absent values leave
// fields unchanged; GetLog supplies a fresh zero-valued log so those absences
// remain nil, and timing integers are converted to int.
func assignContentModerationNullableIDs(item *moderation.ContentModerationLog, userID sql.NullInt64, billingUserID sql.NullInt64, teamID sql.NullInt64, apiKeyID sql.NullInt64, groupID sql.NullInt64, latency sql.NullInt64, queueDelay sql.NullInt64) {
	if userID.Valid {
		value := userID.Int64
		item.UserID = &value
	}
	if billingUserID.Valid {
		value := billingUserID.Int64
		item.BillingUserID = &value
	}
	if teamID.Valid {
		value := teamID.Int64
		item.TeamID = &value
	}
	if apiKeyID.Valid {
		value := apiKeyID.Int64
		item.APIKeyID = &value
	}
	if groupID.Valid {
		value := groupID.Int64
		item.GroupID = &value
	}
	if latency.Valid {
		value := int(latency.Int64)
		item.UpstreamLatencyMS = &value
	}
	if queueDelay.Valid {
		value := int(queueDelay.Int64)
		item.QueueDelayMS = &value
	}
}

// CountFlaggedByUserSince counts ordinary flagged actor rows at or after since,
// strictly after the latest stored auto-ban. It excludes hash_block actions and
// returns zero for nonpositive IDs; the service adds the current yet-unwritten
// hit outside this read.
func (r *Store) CountFlaggedByUserSince(ctx context.Context, userID int64, since time.Time) (int, error) {
	if userID <= 0 {
		return 0, nil
	}
	var count int
	err := r.db.QueryRowContext(ctx, `
WITH last_auto_ban AS (
    SELECT MAX(created_at) AS at
    FROM content_moderation_logs
    WHERE user_id = $1 AND auto_banned = TRUE
)
SELECT COUNT(*)
FROM content_moderation_logs
WHERE user_id = $1
  AND flagged = TRUE
  AND action <> 'hash_block'
  AND created_at >= $2
  AND created_at > COALESCE((SELECT at FROM last_auto_ban), '-infinity'::timestamptz)
`, userID, since).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count user content moderation flagged logs: %w", err)
	}
	return count, nil
}

// CreateCyberWarning inserts warning and media sequentially through the
// database, defaulting a nonpositive ViolationCount to one. Nil warnings are a
// no-op. It creates no transaction or user-policy work, so later media failure
// can follow an already committed warning row.
func (r *Store) CreateCyberWarning(ctx context.Context, warning *moderation.ContentModerationCyberWarning) error {
	if warning == nil {
		return nil
	}
	if warning.ViolationCount <= 0 {
		warning.ViolationCount = 1
	}
	return insertContentModerationCyberWarning(ctx, r.db, warning)
}

// CreateCyberWarningAndApplyUserBan writes warning/media and account-policy
// effects in one transaction and returns the participant's new-disablement
// observation after successful commit. Nil warnings return false, nil. A valid
// actor is locked before insertion; the inserted warning participates in the
// window count before optional status and warning policy fields are written.
//
// Step failures leave deferred rollback to be attempted. Commit errors require
// outcome reconciliation, and supplied structs can retain identities or policy
// fields written before the failure. The caller applies authentication
// invalidation and notices after successful return using the committed warning
// identity.
// @project-doc docs/domains/content_moderation.md#moderation_transactions
func (r *Store) CreateCyberWarningAndApplyUserBan(ctx context.Context, warning *moderation.ContentModerationCyberWarning, policy moderation.ContentModerationCyberWarningPolicy) (bool, error) {
	if warning == nil {
		return false, nil
	}
	if warning.ViolationCount <= 0 {
		warning.ViolationCount = 1
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin content moderation cyber warning tx: %w", err)
	}
	// Leave rollback armed through every earlier return; after successful commit
	// it is inert. Its ignored error cannot resolve an uncertain commit outcome.
	defer func() { _ = tx.Rollback() }()

	// Lock the actor before warning insertion and counting, keeping competing Cyber
	// updates for the same user behind this transaction until its commit or rollback.
	userStatus, userLocked, err := r.lockCyberWarningUserTx(ctx, tx, warning)
	if err != nil {
		return false, err
	}
	if err := insertContentModerationCyberWarning(ctx, tx, warning); err != nil {
		return false, err
	}

	autoBanJustApplied, err := r.applyCyberWarningUserBanTx(ctx, tx, warning, policy, userStatus, userLocked)
	if err != nil {
		return false, err
	}
	// Commit after every intended row and policy step succeeds; a commit error
	// leaves the caller responsible for reconciling whether the database applied the transaction.
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit content moderation cyber warning tx: %w", err)
	}
	return autoBanJustApplied, nil
}

// insertContentModerationCyberWarning inserts serialized warning evidence
// through a required DB or transaction and fills its ID and CreatedAt. It then
// links each media item to that warning ID and inserts it. Callers supply
// nonnil warning data and the intended transaction ownership.
//
// A DB caller can retain the warning after later media failure; a transaction
// caller can roll back the combined writes. In either case, supplied values
// retain any identity assigned before failure. The method does not commit, roll
// back, lock users, or apply account policy itself.
func insertContentModerationCyberWarning(ctx context.Context, q sqlQueryRower, warning *moderation.ContentModerationCyberWarning) error {
	inputItems, err := json.Marshal(warning.InputItems)
	if err != nil {
		return fmt.Errorf("marshal cyber warning input items: %w", err)
	}
	failedUnits, err := json.Marshal(warning.FailedUnits)
	if err != nil {
		return fmt.Errorf("marshal cyber warning failed units: %w", err)
	}
	var userID any
	if warning.UserID != nil {
		userID = *warning.UserID
	}
	var billingUserID any
	if warning.BillingUserID != nil {
		billingUserID = *warning.BillingUserID
	}
	var teamID any
	if warning.TeamID != nil {
		teamID = *warning.TeamID
	}
	var apiKeyID any
	if warning.APIKeyID != nil {
		apiKeyID = *warning.APIKeyID
	}
	var groupID any
	if warning.GroupID != nil {
		groupID = *warning.GroupID
	}
	var providerID any
	if warning.ProviderID != nil {
		providerID = *warning.ProviderID
	}
	err = q.QueryRowContext(ctx, `
INSERT INTO content_moderation_cyber_warnings (
    request_id, user_id, user_email, billing_user_id, team_id, api_key_id, api_key_name, group_id, group_name,
    provider_id, provider_name, endpoint, model, upstream_status, warning_text, prompt_excerpt,
	    violation_count, auto_banned, email_sent,
	    source, input_items, content_complete, audit_complete, text_unit_count, image_unit_count,
	    failed_unit_count, failed_units
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9,
    $10, $11, $12, $13, $14, $15, $16,
	    $17, $18, $19,
	    $20, $21::jsonb, $22, $23, $24, $25,
	    $26, $27::jsonb
) RETURNING id, created_at`,
		warning.RequestID, userID, warning.UserEmail, billingUserID, teamID, apiKeyID, warning.APIKeyName, groupID, warning.GroupName,
		providerID, warning.ProviderName, warning.Endpoint, warning.Model, warning.UpstreamStatus, warning.WarningText, warning.PromptExcerpt,
		warning.ViolationCount, warning.AutoBanned, warning.EmailSent,
		warning.Source, string(inputItems), warning.ContentComplete, warning.AuditComplete, warning.TextUnitCount, warning.ImageUnitCount,
		warning.FailedUnitCount, string(failedUnits),
	).Scan(&warning.ID, &warning.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert content moderation cyber warning: %w", err)
	}
	// Children share the newly assigned warning identity; the supplied DB or Tx
	// decides whether earlier insertions survive a later media error on this path.
	for index := range warning.Media {
		warning.Media[index].CyberWarningID = &warning.ID
		if err := insertContentModerationMedia(ctx, q, &warning.Media[index]); err != nil {
			return err
		}
	}
	return nil
}

// lockCyberWarningUserTx obtains status and actor-row existence through the
// same-transaction identity factory. Missing or nonpositive actor pointers
// return an unlocked result; valid actors require the factory, and errors
// return before warning insertion.
func (r *Store) lockCyberWarningUserTx(ctx context.Context, tx *sql.Tx, warning *moderation.ContentModerationCyberWarning) (string, bool, error) {
	if warning == nil || warning.UserID == nil || *warning.UserID <= 0 {
		return "", false, nil
	}
	status, locked, err := r.users(tx).LockUser(ctx, *warning.UserID)
	if err != nil {
		return "", false, fmt.Errorf("lock cyber warning user: %w", err)
	}
	return status, locked, nil
}

// applyCyberWarningUserBanTx counts and applies Cyber policy after the current
// warning is inserted and its actor row is locked. Missing actor data or an
// unlocked row skips policy work. Positive windows count matching warnings
// after the prior auto-ban, excluding the current warning from that prior-ban
// lookup; nonpositive windows keep count one.
//
// Enabled threshold policy marks AutoBanned and requests disablement unless
// stored status is already disabled. Warning count and ban fields are updated
// in the same transaction, while the returned boolean is the identity
// participant's new-change observation. This path does not check actor role and
// leaves commit and subsequent effects to the outer method.
func (r *Store) applyCyberWarningUserBanTx(ctx context.Context, tx *sql.Tx, warning *moderation.ContentModerationCyberWarning, policy moderation.ContentModerationCyberWarningPolicy, userStatus string, userLocked bool) (bool, error) {
	if warning == nil || warning.UserID == nil || *warning.UserID <= 0 || !userLocked {
		return false, nil
	}
	userID := *warning.UserID

	// Count with the new row already present, but exclude that row when locating
	// the earlier auto-ban cutoff so caller-supplied ban metadata cannot reset its own count.
	count := 1
	if policy.WindowHours > 0 {
		// Convert policy hours into a relative window at this evaluation time.
		since := time.Now().Add(-time.Duration(policy.WindowHours) * time.Hour)
		err := tx.QueryRowContext(ctx, `
WITH last_auto_ban AS (
    SELECT MAX(created_at) AS at
    FROM content_moderation_cyber_warnings
    WHERE user_id = $1 AND auto_banned = TRUE AND id <> $3
)
SELECT COUNT(*)
FROM content_moderation_cyber_warnings
WHERE user_id = $1
  AND created_at >= $2
  AND created_at > COALESCE((SELECT at FROM last_auto_ban), '-infinity'::timestamptz)
`, userID, since, warning.ID).Scan(&count)
		if err != nil {
			return false, fmt.Errorf("count cyber warnings in tx: %w", err)
		}
	}

	warning.ViolationCount = count
	autoBanJustApplied := false
	if policy.AutoBanEnabled && policy.BanThreshold > 0 && count >= policy.BanThreshold {
		warning.AutoBanned = true
		if userStatus != moderation.StatusDisabled {
			applied, err := r.users(tx).SetDisabled(ctx, userID)
			if err != nil {
				return false, fmt.Errorf("disable cyber warning user: %w", err)
			}
			autoBanJustApplied = applied
		}
	}
	// Save computed warning count and threshold observation in the same transaction
	// as any identity status write; a later outer commit decides their durable outcome.
	if _, err := tx.ExecContext(ctx, `
UPDATE content_moderation_cyber_warnings
SET violation_count = $2, auto_banned = $3
WHERE id = $1
`, warning.ID, warning.ViolationCount, warning.AutoBanned); err != nil {
		return false, fmt.Errorf("update cyber warning policy fields: %w", err)
	}
	return autoBanJustApplied, nil
}

// ListCyberWarnings returns newest-first warning summaries after separate count
// and page queries. Page and size default and clamp like ordinary lists, and
// SQL ordering ignores supplied sort order; full input, failed units, and media
// are loaded by GetCyberWarning.
func (r *Store) ListCyberWarnings(ctx context.Context, filter moderation.ContentModerationCyberWarningFilter) ([]moderation.ContentModerationCyberWarning, *pagination.PaginationResult, error) {
	where, args := buildContentModerationCyberWhere(filter)
	whereSQL := "WHERE " + strings.Join(where, " AND ")

	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM content_moderation_cyber_warnings w "+whereSQL, args...).Scan(&total); err != nil {
		return nil, nil, fmt.Errorf("count content moderation cyber warnings: %w", err)
	}

	params := filter.Pagination
	if params.Page <= 0 {
		params.Page = 1
	}
	if params.PageSize <= 0 {
		params.PageSize = 20
	}
	// Bound administrator summary page size before decoding retained result rows.
	if params.PageSize > 100 {
		params.PageSize = 100
	}
	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, params.Limit(), params.Offset())
	rows, err := r.db.QueryContext(ctx, `
SELECT
    w.id, w.request_id, w.user_id, w.user_email, w.billing_user_id, w.team_id,
    w.api_key_id, w.api_key_name, w.group_id, w.group_name,
    w.provider_id, w.provider_name, w.endpoint, w.model, w.upstream_status, w.warning_text, w.prompt_excerpt,
	    w.violation_count, w.auto_banned, w.email_sent, COALESCE(u.status, ''),
	    w.source, w.content_complete, w.audit_complete, w.text_unit_count, w.image_unit_count, w.failed_unit_count,
	    w.created_at
FROM content_moderation_cyber_warnings w
LEFT JOIN users u ON u.id = w.user_id `+whereSQL+`
ORDER BY w.created_at DESC, w.id DESC
LIMIT $`+fmt.Sprint(len(queryArgs)-1)+` OFFSET $`+fmt.Sprint(len(queryArgs)),
		queryArgs...,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("list content moderation cyber warnings: %w", err)
	}
	// Release the query rows after scanning or any early failure; close errors
	// do not replace the earlier query, scan, or iteration result returned here.
	defer func() { _ = rows.Close() }()

	items := make([]moderation.ContentModerationCyberWarning, 0)
	for rows.Next() {
		var item moderation.ContentModerationCyberWarning
		var userID, billingUserID, teamID, apiKeyID, groupID, providerID sql.NullInt64
		if err := rows.Scan(
			&item.ID,
			&item.RequestID,
			&userID,
			&item.UserEmail,
			&billingUserID,
			&teamID,
			&apiKeyID,
			&item.APIKeyName,
			&groupID,
			&item.GroupName,
			&providerID,
			&item.ProviderName,
			&item.Endpoint,
			&item.Model,
			&item.UpstreamStatus,
			&item.WarningText,
			&item.PromptExcerpt,
			&item.ViolationCount,
			&item.AutoBanned,
			&item.EmailSent,
			&item.UserStatus,
			&item.Source,
			&item.ContentComplete,
			&item.AuditComplete,
			&item.TextUnitCount,
			&item.ImageUnitCount,
			&item.FailedUnitCount,
			&item.CreatedAt,
		); err != nil {
			return nil, nil, fmt.Errorf("scan content moderation cyber warning: %w", err)
		}
		if userID.Valid {
			v := userID.Int64
			item.UserID = &v
		}
		if billingUserID.Valid {
			v := billingUserID.Int64
			item.BillingUserID = &v
		}
		if teamID.Valid {
			v := teamID.Int64
			item.TeamID = &v
		}
		if apiKeyID.Valid {
			v := apiKeyID.Int64
			item.APIKeyID = &v
		}
		if groupID.Valid {
			v := groupID.Int64
			item.GroupID = &v
		}
		if providerID.Valid {
			v := providerID.Int64
			item.ProviderID = &v
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate content moderation cyber warnings: %w", err)
	}
	return items, paginationResultFromTotal(total, params), nil
}

// GetCyberWarning loads raw warning review data with current user status and
// separately queried media metadata. JSON decode errors retain any decoded
// prefix values; missing rows return wrapped errors recognized by the service,
// and image bytes remain a separate retrieval.
func (r *Store) GetCyberWarning(ctx context.Context, id int64) (*moderation.ContentModerationCyberWarning, error) {
	var item moderation.ContentModerationCyberWarning
	var userID, billingUserID, teamID, apiKeyID, groupID, providerID sql.NullInt64
	var inputItemsRaw, failedUnitsRaw []byte
	err := r.db.QueryRowContext(ctx, `
SELECT
    w.id, w.request_id, w.user_id, w.user_email, w.billing_user_id, w.team_id,
    w.api_key_id, w.api_key_name, w.group_id, w.group_name,
    w.provider_id, w.provider_name, w.endpoint, w.model, w.upstream_status, w.warning_text, w.prompt_excerpt,
    w.violation_count, w.auto_banned, w.email_sent, COALESCE(u.status, ''),
    w.source, w.input_items, w.content_complete, w.audit_complete, w.text_unit_count, w.image_unit_count,
    w.failed_unit_count, w.failed_units, w.created_at
FROM content_moderation_cyber_warnings w
LEFT JOIN users u ON u.id = w.user_id
WHERE w.id = $1
`, id).Scan(
		&item.ID, &item.RequestID, &userID, &item.UserEmail, &billingUserID, &teamID,
		&apiKeyID, &item.APIKeyName, &groupID, &item.GroupName,
		&providerID, &item.ProviderName, &item.Endpoint, &item.Model, &item.UpstreamStatus, &item.WarningText, &item.PromptExcerpt,
		&item.ViolationCount, &item.AutoBanned, &item.EmailSent, &item.UserStatus,
		&item.Source, &inputItemsRaw, &item.ContentComplete, &item.AuditComplete, &item.TextUnitCount, &item.ImageUnitCount,
		&item.FailedUnitCount, &failedUnitsRaw, &item.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("get content moderation cyber warning: %w", err)
	}
	if userID.Valid {
		value := userID.Int64
		item.UserID = &value
	}
	if billingUserID.Valid {
		value := billingUserID.Int64
		item.BillingUserID = &value
	}
	if teamID.Valid {
		value := teamID.Int64
		item.TeamID = &value
	}
	if apiKeyID.Valid {
		value := apiKeyID.Int64
		item.APIKeyID = &value
	}
	if groupID.Valid {
		value := groupID.Int64
		item.GroupID = &value
	}
	if providerID.Valid {
		value := providerID.Int64
		item.ProviderID = &value
	}
	// Retain the warning row even when its JSON payload shape cannot fully decode;
	// any decoded prefix remains available alongside the stored completion observations.
	_ = json.Unmarshal(inputItemsRaw, &item.InputItems)
	_ = json.Unmarshal(failedUnitsRaw, &item.FailedUnits)
	item.Media, err = r.listContentModerationMedia(ctx, "cyber_warning_id", id)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// insertContentModerationMedia inserts one supplied snapshot through a DB or
// caller-owned transaction, assigning ID and CreatedAt. Nil media is a no-op;
// ownership pointers are serialized as supplied, and the schema requires
// exactly one log or warning owner.
func insertContentModerationMedia(ctx context.Context, q sqlQueryRower, media *moderation.ContentModerationMedia) error {
	if media == nil {
		return nil
	}
	var logID, cyberWarningID any
	if media.LogID != nil {
		logID = *media.LogID
	}
	if media.CyberWarningID != nil {
		cyberWarningID = *media.CyberWarningID
	}
	err := q.QueryRowContext(ctx, `
INSERT INTO content_moderation_media (
    log_id, cyber_warning_id, source_index, source, mime_type, sha256, byte_size,
    original_ref, snapshot_status, snapshot_error, content
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING id, created_at
`, logID, cyberWarningID, media.SourceIndex, media.Source, media.MIMEType, media.SHA256, media.ByteSize,
		media.OriginalRef, media.SnapshotStatus, media.SnapshotError, media.Content,
	).Scan(&media.ID, &media.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert content moderation media: %w", err)
	}
	return nil
}

// listContentModerationMedia returns snapshot metadata ordered by source index
// and ID for one owner. Only log_id and cyber_warning_id are accepted column
// selectors; binary content and owner pointers are omitted from the returned
// entries.
func (r *Store) listContentModerationMedia(ctx context.Context, ownerColumn string, ownerID int64) ([]moderation.ContentModerationMedia, error) {
	if ownerColumn != "log_id" && ownerColumn != "cyber_warning_id" {
		return nil, errors.New("invalid content moderation media owner")
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT id, source_index, source, mime_type, sha256, byte_size, original_ref, snapshot_status, snapshot_error, created_at
FROM content_moderation_media
WHERE `+ownerColumn+` = $1
ORDER BY source_index ASC, id ASC
`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list content moderation media: %w", err)
	}
	// Release the query rows after scanning or any early failure; close errors
	// do not replace the earlier query, scan, or iteration result returned here.
	defer func() { _ = rows.Close() }()
	items := make([]moderation.ContentModerationMedia, 0)
	for rows.Next() {
		var item moderation.ContentModerationMedia
		if err := rows.Scan(&item.ID, &item.SourceIndex, &item.Source, &item.MIMEType, &item.SHA256, &item.ByteSize, &item.OriginalRef, &item.SnapshotStatus, &item.SnapshotError, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan content moderation media: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate content moderation media: %w", err)
	}
	return items, nil
}

// GetMediaContent loads the stored binary snapshot and retrieval metadata for a
// media ID. The caller enforces administrator authorization, and the service
// separately checks ready status and nonempty content before HTTP delivery.
func (r *Store) GetMediaContent(ctx context.Context, id int64) (*moderation.ContentModerationMedia, error) {
	var item moderation.ContentModerationMedia
	err := r.db.QueryRowContext(ctx, `
SELECT id, source_index, source, mime_type, sha256, byte_size, original_ref, snapshot_status, snapshot_error, content, created_at
FROM content_moderation_media
WHERE id = $1
`, id).Scan(&item.ID, &item.SourceIndex, &item.Source, &item.MIMEType, &item.SHA256, &item.ByteSize, &item.OriginalRef, &item.SnapshotStatus, &item.SnapshotError, &item.Content, &item.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("get content moderation media: %w", err)
	}
	return &item, nil
}

// CountCyberWarningsByUserSince counts stored actor warnings at or after since
// and strictly after the latest recorded auto-ban. Nonpositive IDs return zero;
// the transaction policy helper uses its own current-warning-aware query
// instead of this separate read.
func (r *Store) CountCyberWarningsByUserSince(ctx context.Context, userID int64, since time.Time) (int, error) {
	if userID <= 0 {
		return 0, nil
	}
	var count int
	err := r.db.QueryRowContext(ctx, `
WITH last_auto_ban AS (
    SELECT MAX(created_at) AS at
    FROM content_moderation_cyber_warnings
    WHERE user_id = $1 AND auto_banned = TRUE
)
SELECT COUNT(*)
FROM content_moderation_cyber_warnings
WHERE user_id = $1
  AND created_at >= $2
  AND created_at > COALESCE((SELECT at FROM last_auto_ban), '-infinity'::timestamptz)
`, userID, since).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count user content moderation cyber warnings: %w", err)
	}
	return count, nil
}

// MarkCyberWarningEmailSent records successful notice delivery for a positive
// warning ID. Nonpositive IDs are a no-op and affected rows are not checked, so
// successful execution can return nil when the row is already absent.
func (r *Store) MarkCyberWarningEmailSent(ctx context.Context, id int64) error {
	if id <= 0 {
		return nil
	}
	if _, err := r.db.ExecContext(ctx, `
UPDATE content_moderation_cyber_warnings
SET email_sent = TRUE
WHERE id = $1
`, id); err != nil {
		return fmt.Errorf("mark content moderation cyber warning email sent: %w", err)
	}
	return nil
}

// GetCyberSummary returns matching warning totals followed by user and provider
// grouping observations. Total request counts exclude blank request IDs;
// distinct users and providers exclude null IDs, while group queries can still
// include their null group.
//
// Each grouped list caps at one hundred and chooses a maximum recorded display
// name/address plus Shanghai local latest-time text. Queries run separately, so
// concurrent mutations can separate their observed totals. Any query or scan
// failure returns an error instead of the partially assembled result.
func (r *Store) GetCyberSummary(ctx context.Context, filter moderation.ContentModerationCyberWarningFilter) (*moderation.ContentModerationCyberSummary, error) {
	where, args := buildContentModerationCyberWhere(filter)
	whereSQL := "WHERE " + strings.Join(where, " AND ")
	result := &moderation.ContentModerationCyberSummary{}
	if err := r.db.QueryRowContext(ctx, `
SELECT
    COUNT(*),
    COUNT(DISTINCT NULLIF(request_id, '')),
    COUNT(DISTINCT user_id),
    COUNT(DISTINCT provider_id)
FROM content_moderation_cyber_warnings w `+whereSQL, args...).Scan(
		&result.Events,
		&result.Requests,
		&result.Users,
		&result.Providers,
	); err != nil {
		return nil, fmt.Errorf("summary content moderation cyber warnings: %w", err)
	}
	// Read capped actor groups separately from totals; concurrent warning writes
	// can change the grouped sample after the total-count observation was obtained.
	userRows, err := r.db.QueryContext(ctx, `
SELECT
    COUNT(*) AS count,
    w.user_id,
    COALESCE(NULLIF(MAX(w.user_email), ''), '无法关联用户') AS user_email,
    COALESCE(string_agg(DISTINCT CASE WHEN w.api_key_id IS NOT NULL THEN w.api_key_id::text || ' / ' || COALESCE(NULLIF(w.api_key_name, ''), '未命名') END, ', '), '-') AS api_keys,
    to_char(MAX(w.created_at AT TIME ZONE 'Asia/Shanghai'), 'YYYY-MM-DD HH24:MI:SS') AS last_seen
FROM content_moderation_cyber_warnings w `+whereSQL+`
GROUP BY w.user_id
ORDER BY count DESC, last_seen DESC
LIMIT 100`, args...)
	if err != nil {
		return nil, fmt.Errorf("summary content moderation cyber warnings by user: %w", err)
	}
	// Close actor rows on completion or any later failure; provider-query failure
	// can still return before this deferred resource release occurs on the error path.
	defer func() { _ = userRows.Close() }()
	for userRows.Next() {
		var item moderation.ContentModerationCyberUserSummary
		var userID sql.NullInt64
		if err := userRows.Scan(&item.Count, &userID, &item.UserEmail, &item.APIKeys, &item.LastSeen); err != nil {
			return nil, fmt.Errorf("scan content moderation cyber user summary: %w", err)
		}
		if userID.Valid {
			v := userID.Int64
			item.UserID = &v
		}
		result.ByUser = append(result.ByUser, item)
	}
	if err := userRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate content moderation cyber user summary: %w", err)
	}

	// Retain only the largest matching provider groups for administrator display.
	providerRows, err := r.db.QueryContext(ctx, `
SELECT
    COUNT(*) AS count,
    w.provider_id,
    COALESCE(NULLIF(MAX(w.provider_name), ''), 'unknown') AS provider_name,
    COUNT(DISTINCT w.user_id) AS users,
    to_char(MAX(w.created_at AT TIME ZONE 'Asia/Shanghai'), 'YYYY-MM-DD HH24:MI:SS') AS last_seen
FROM content_moderation_cyber_warnings w `+whereSQL+`
GROUP BY w.provider_id
ORDER BY count DESC, last_seen DESC
LIMIT 100`, args...)
	if err != nil {
		return nil, fmt.Errorf("summary content moderation cyber warnings by provider: %w", err)
	}
	// Release provider rows after the grouped scan even when a scan or iteration
	// error prevents the partially assembled summary from being returned to its caller.
	defer func() { _ = providerRows.Close() }()
	for providerRows.Next() {
		var item moderation.ContentModerationCyberProviderSummary
		var providerID sql.NullInt64
		if err := providerRows.Scan(&item.Count, &providerID, &item.ProviderName, &item.Users, &item.LastSeen); err != nil {
			return nil, fmt.Errorf("scan content moderation cyber provider summary: %w", err)
		}
		if providerID.Valid {
			v := providerID.Int64
			item.ProviderID = &v
		}
		result.ByProvider = append(result.ByProvider, item)
	}
	if err := providerRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate content moderation cyber provider summary: %w", err)
	}
	return result, nil
}

// CleanupExpiredLogs deletes flagged logs, unflagged logs, and Cyber warnings
// using their supplied creation cutoffs. Nil Store or DB returns a zero-delete
// result timestamped at entry. With a DB, each delete commits independently; a
// later failure leaves earlier successful deletions applied and returns no
// result.
//
// Cyber warnings share the hit cutoff, and schema owner references cascade
// removal to media. RowsAffected errors are suppressed for displayed counts, so
// returned numbers describe available driver observations. FinishedAt is
// refreshed after all three commands succeed; the service only publishes
// observations from that successful result.
func (r *Store) CleanupExpiredLogs(ctx context.Context, hitBefore time.Time, nonHitBefore time.Time) (*moderation.ContentModerationCleanupResult, error) {
	result := &moderation.ContentModerationCleanupResult{FinishedAt: time.Now()}
	if r == nil || r.db == nil {
		return result, nil
	}
	// These deletes are independent commands: hit deletion survives any later
	// non-hit or warning failure, while the service keeps its previous successful cleanup display.
	hitExec, err := r.db.ExecContext(ctx, `
DELETE FROM content_moderation_logs
WHERE flagged = TRUE AND created_at < $1
`, hitBefore)
	if err != nil {
		return nil, fmt.Errorf("delete expired hit content moderation logs: %w", err)
	}
	// Deletion already succeeded; retain the count returned by the driver while
	// suppressing its count-read error, which cannot undo this earlier database operation.
	result.DeletedHit, _ = hitExec.RowsAffected()

	nonHitExec, err := r.db.ExecContext(ctx, `
DELETE FROM content_moderation_logs
WHERE flagged = FALSE AND created_at < $1
`, nonHitBefore)
	if err != nil {
		return nil, fmt.Errorf("delete expired non-hit content moderation logs: %w", err)
	}
	// Keep successful non-hit deletion and the reported driver count while ignoring
	// any count-read error; later deletion steps remain responsible for their own results.
	result.DeletedNonHit, _ = nonHitExec.RowsAffected()

	// Cyber warnings use the hit cutoff, and schema owner references cascade media
	// deletion; this command is still independent of the two earlier log deletes.
	cyberExec, err := r.db.ExecContext(ctx, `
DELETE FROM content_moderation_cyber_warnings
WHERE created_at < $1
`, hitBefore)
	if err != nil {
		return nil, fmt.Errorf("delete expired content moderation cyber warnings: %w", err)
	}
	// This warning deletion has already completed; retain its reported driver count
	// while suppressing any count-read error in the otherwise successful cleanup result.
	result.DeletedCyber, _ = cyberExec.RowsAffected()

	result.FinishedAt = time.Now()
	return result, nil
}

// nullableIntPtr returns the pointed integer for a SQL argument or nil to
// encode SQL NULL. It keeps present zero values distinct from absent timing,
// and callers provide the timing unit with the field.
func nullableIntPtr(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

// buildContentModerationLogWhere returns predicates and numbered arguments for
// ordinary list filtering. It keeps individual blocking actions distinct and
// supports blocked as their combined compatibility value. Request IDs expand
// through request_lookup_ids, search retains SQL wildcards inside percent
// wrapping, and nonzero time limits are inclusive.
func buildContentModerationLogWhere(filter moderation.ContentModerationLogFilter) ([]string, []any) {
	where := []string{"l.id IS NOT NULL"}
	args := make([]any, 0)
	add := func(expr string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(expr, len(args)))
	}
	switch strings.ToLower(strings.TrimSpace(filter.Result)) {
	case "hit", "flagged":
		// The hit filter selects flagged allowed observations; exclude all three block
		// actions so administrator hit review remains distinct from blocked review across action modes.
		where = append(where, "l.flagged = TRUE AND l.action NOT IN ('block', 'keyword_block', 'hash_block')")
	case "blocked":
		// The combined blocked selector accepts all block action labels, while the
		// singular selectors below give administrator review an action-specific result set.
		where = append(where, "l.action IN ('block', 'keyword_block', 'hash_block')")
	case "block":
		where = append(where, "l.action = 'block'")
	case "keyword_block":
		where = append(where, "l.action = 'keyword_block'")
	case "hash_block":
		where = append(where, "l.action = 'hash_block'")
	case "pass", "allow":
		where = append(where, "l.flagged = FALSE AND l.error = ''")
	case "error":
		where = append(where, "l.error <> ''")
	}
	if filter.GroupID != nil {
		add("l.group_id = $%d", *filter.GroupID)
	}
	if endpoint := strings.TrimSpace(filter.Endpoint); endpoint != "" {
		add("l.endpoint = $%d", endpoint)
	}
	if id := strings.TrimSpace(filter.RequestID); id != "" {
		add("l.request_id = ANY(ARRAY(SELECT id FROM request_lookup_ids($%d)))", id)
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		like := "%" + search + "%"
		args = append(args, like, like, like, like, like)
		idx := len(args) - 4
		where = append(where, fmt.Sprintf("(l.request_id ILIKE $%d OR l.user_email ILIKE $%d OR l.api_key_name ILIKE $%d OR l.model ILIKE $%d OR l.input_excerpt ILIKE $%d)", idx, idx+1, idx+2, idx+3, idx+4))
	}
	if filter.From != nil && !filter.From.IsZero() {
		add("l.created_at >= $%d", *filter.From)
	}
	if filter.To != nil && !filter.To.IsZero() {
		add("l.created_at <= $%d", *filter.To)
	}
	return where, args
}

// buildContentModerationCyberWhere returns common warning predicates and
// numbered arguments for lists and summary queries. Request IDs expand through
// request_lookup_ids; optional actor/provider IDs compare exactly, search keeps
// wildcard behavior, and nonzero lower/upper creation limits are inclusive.
func buildContentModerationCyberWhere(filter moderation.ContentModerationCyberWarningFilter) ([]string, []any) {
	where := []string{"w.id IS NOT NULL"}
	args := make([]any, 0)
	add := func(expr string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(expr, len(args)))
	}
	if filter.UserID != nil {
		add("w.user_id = $%d", *filter.UserID)
	}
	if filter.ProviderID != nil {
		add("w.provider_id = $%d", *filter.ProviderID)
	}
	if id := strings.TrimSpace(filter.RequestID); id != "" {
		add("w.request_id = ANY(ARRAY(SELECT id FROM request_lookup_ids($%d)))", id)
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		like := "%" + search + "%"
		args = append(args, like, like, like, like, like, like, like)
		idx := len(args) - 6
		where = append(where, fmt.Sprintf("(w.request_id ILIKE $%d OR w.user_email ILIKE $%d OR w.api_key_name ILIKE $%d OR w.provider_name ILIKE $%d OR w.model ILIKE $%d OR w.warning_text ILIKE $%d OR w.prompt_excerpt ILIKE $%d)", idx, idx+1, idx+2, idx+3, idx+4, idx+5, idx+6))
	}
	if filter.From != nil && !filter.From.IsZero() {
		add("w.created_at >= $%d", *filter.From)
	}
	if filter.To != nil && !filter.To.IsZero() {
		add("w.created_at <= $%d", *filter.To)
	}
	return where, args
}

// paginationResultFromTotal delegates total/page metadata construction to the
// shared pagination helper for repository lists.
func paginationResultFromTotal(total int64, params pagination.PaginationParams) *pagination.PaginationResult {
	return pagination.ResultFromTotal(total, params)
}
