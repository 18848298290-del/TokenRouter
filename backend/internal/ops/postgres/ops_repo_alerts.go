package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// ops_repo_alerts.go persists alert rules, events, silences, and delivery
// markers and reconstructs their query results.
// Alert events retain nullable measurements and dimensions alongside
// independent status and email markers. A missing event becomes nil for
// evaluator reads, while a missing rule update or deletion returns
// sql.ErrNoRows for administration to classify. Active-event selection searches
// firing status alone; it does not establish one active event per rule or
// atomically reserve a future insert.
//
// JSON maps cross PostgreSQL as nullable text parameters. Nil maps become SQL
// NULL and present empty maps remain objects; failed map decoding leaves
// optional filters or dimensions absent while retaining the row. Silences match
// the exact nullable rule/platform/group/region scope, so missing group or
// region represents its own scope rather than every value. Cursor paging
// combines firing timestamp with ID to continue tied rows in descending order,
// and updates execute independently of evaluator decisions or mail delivery.

// opsAlertEventRow permits a SQL row or compatible fixture to supply one event
// scan.
type opsAlertEventRow interface {
	// Scan assigns one event row's selected columns into supplied destinations.
	Scan(dest ...any) error
}

// ListAlertRules returns saved rules in descending ID order with nonnil empty
// output when no rules exist. Nullable last-trigger timestamps become scalar
// pointers, while absent or undecodable JSON filters stay nil and do not reject
// an otherwise scanned rule.
func (r *Store) ListAlertRules(ctx context.Context) ([]*ops.OpsAlertRule, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}

	q := `
SELECT
  id,
  name,
  COALESCE(description, ''),
  enabled,
  COALESCE(severity, ''),
  metric_type,
  operator,
  threshold,
  window_minutes,
  sustained_minutes,
  cooldown_minutes,
  COALESCE(notify_email, true),
  filters,
  last_triggered_at,
  created_at,
  updated_at
FROM ops_alert_rules
ORDER BY id DESC`

	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	// Close the cursor after success or an early scan error. Cleanup failures are
	// suppressed, while iteration errors still reject this row set.
	defer func() { _ = rows.Close() }()

	out := []*ops.OpsAlertRule{}
	for rows.Next() {
		var rule ops.OpsAlertRule
		var filtersRaw []byte
		var lastTriggeredAt sql.NullTime
		if err := rows.Scan(
			&rule.ID,
			&rule.Name,
			&rule.Description,
			&rule.Enabled,
			&rule.Severity,
			&rule.MetricType,
			&rule.Operator,
			&rule.Threshold,
			&rule.WindowMinutes,
			&rule.SustainedMinutes,
			&rule.CooldownMinutes,
			&rule.NotifyEmail,
			&filtersRaw,
			&lastTriggeredAt,
			&rule.CreatedAt,
			&rule.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if lastTriggeredAt.Valid {
			v := lastTriggeredAt.Time
			rule.LastTriggeredAt = &v
		}
		// Filter JSON is optional to the scanned rule. Ignore failed object decoding
		// and keep scalar fields available rather than rejecting this stored row.
		if len(filtersRaw) > 0 && string(filtersRaw) != "null" {
			var decoded map[string]any
			if err := json.Unmarshal(filtersRaw, &decoded); err == nil {
				rule.Filters = decoded
			}
		}
		out = append(out, &rule)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateAlertRule inserts all editable fields and returns the saved rule with
// database timestamps. Nil input is rejected; text is trimmed and Filters is
// encoded before writing. The HTTP adapter supplies allowed metrics, defaults,
// and ranges, while this method leaves those values to database constraints.
func (r *Store) CreateAlertRule(ctx context.Context, input *ops.OpsAlertRule) (*ops.OpsAlertRule, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if input == nil {
		return nil, fmt.Errorf("nil input")
	}

	// Encode caller-owned filters before the SQL write, keeping nil distinct from
	// an empty object. Unsupported mutable values fail serialization before
	// persistence begins.
	filtersArg, err := opsNullJSONMap(input.Filters)
	if err != nil {
		return nil, err
	}

	q := `
INSERT INTO ops_alert_rules (
  name,
  description,
  enabled,
  severity,
  metric_type,
  operator,
  threshold,
  window_minutes,
  sustained_minutes,
  cooldown_minutes,
  notify_email,
  filters,
  created_at,
  updated_at
) VALUES (
  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NOW(),NOW()
)
RETURNING
  id,
  name,
  COALESCE(description, ''),
  enabled,
  COALESCE(severity, ''),
  metric_type,
  operator,
  threshold,
  window_minutes,
  sustained_minutes,
  cooldown_minutes,
  COALESCE(notify_email, true),
  filters,
  last_triggered_at,
  created_at,
  updated_at`

	var out ops.OpsAlertRule
	var filtersRaw []byte
	var lastTriggeredAt sql.NullTime

	if err := r.db.QueryRowContext(
		ctx,
		q,
		strings.TrimSpace(input.Name),
		strings.TrimSpace(input.Description),
		input.Enabled,
		strings.TrimSpace(input.Severity),
		strings.TrimSpace(input.MetricType),
		strings.TrimSpace(input.Operator),
		input.Threshold,
		input.WindowMinutes,
		input.SustainedMinutes,
		input.CooldownMinutes,
		input.NotifyEmail,
		filtersArg,
	).Scan(
		&out.ID,
		&out.Name,
		&out.Description,
		&out.Enabled,
		&out.Severity,
		&out.MetricType,
		&out.Operator,
		&out.Threshold,
		&out.WindowMinutes,
		&out.SustainedMinutes,
		&out.CooldownMinutes,
		&out.NotifyEmail,
		&filtersRaw,
		&lastTriggeredAt,
		&out.CreatedAt,
		&out.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if lastTriggeredAt.Valid {
		v := lastTriggeredAt.Time
		out.LastTriggeredAt = &v
	}
	// Filter JSON is optional to the scanned rule. Ignore failed object decoding
	// and keep scalar fields available rather than rejecting this stored row.
	if len(filtersRaw) > 0 && string(filtersRaw) != "null" {
		var decoded map[string]any
		if err := json.Unmarshal(filtersRaw, &decoded); err == nil {
			out.Filters = decoded
		}
	}

	return &out, nil
}

// UpdateAlertRule replaces all editable fields for a positive rule identity and
// returns the saved row. Nil input or nonpositive ID is rejected, while other
// validation belongs to the caller. The existing created and last-trigger
// timestamps remain stored; a missing ID yields sql.ErrNoRows.
func (r *Store) UpdateAlertRule(ctx context.Context, input *ops.OpsAlertRule) (*ops.OpsAlertRule, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if input == nil {
		return nil, fmt.Errorf("nil input")
	}
	if input.ID <= 0 {
		return nil, fmt.Errorf("invalid id")
	}

	// Encode caller-owned filters before the SQL write, keeping nil distinct from
	// an empty object. Unsupported mutable values fail serialization before
	// persistence begins.
	filtersArg, err := opsNullJSONMap(input.Filters)
	if err != nil {
		return nil, err
	}

	q := `
UPDATE ops_alert_rules
SET
  name = $2,
  description = $3,
  enabled = $4,
  severity = $5,
  metric_type = $6,
  operator = $7,
  threshold = $8,
  window_minutes = $9,
  sustained_minutes = $10,
  cooldown_minutes = $11,
  notify_email = $12,
  filters = $13,
  updated_at = NOW()
WHERE id = $1
RETURNING
  id,
  name,
  COALESCE(description, ''),
  enabled,
  COALESCE(severity, ''),
  metric_type,
  operator,
  threshold,
  window_minutes,
  sustained_minutes,
  cooldown_minutes,
  COALESCE(notify_email, true),
  filters,
  last_triggered_at,
  created_at,
  updated_at`

	var out ops.OpsAlertRule
	var filtersRaw []byte
	var lastTriggeredAt sql.NullTime

	if err := r.db.QueryRowContext(
		ctx,
		q,
		input.ID,
		strings.TrimSpace(input.Name),
		strings.TrimSpace(input.Description),
		input.Enabled,
		strings.TrimSpace(input.Severity),
		strings.TrimSpace(input.MetricType),
		strings.TrimSpace(input.Operator),
		input.Threshold,
		input.WindowMinutes,
		input.SustainedMinutes,
		input.CooldownMinutes,
		input.NotifyEmail,
		filtersArg,
	).Scan(
		&out.ID,
		&out.Name,
		&out.Description,
		&out.Enabled,
		&out.Severity,
		&out.MetricType,
		&out.Operator,
		&out.Threshold,
		&out.WindowMinutes,
		&out.SustainedMinutes,
		&out.CooldownMinutes,
		&out.NotifyEmail,
		&filtersRaw,
		&lastTriggeredAt,
		&out.CreatedAt,
		&out.UpdatedAt,
	); err != nil {
		return nil, err
	}

	if lastTriggeredAt.Valid {
		v := lastTriggeredAt.Time
		out.LastTriggeredAt = &v
	}
	// Filter JSON is optional to the scanned rule. Ignore failed object decoding
	// and keep scalar fields available rather than rejecting this stored row.
	if len(filtersRaw) > 0 && string(filtersRaw) != "null" {
		var decoded map[string]any
		if err := json.Unmarshal(filtersRaw, &decoded); err == nil {
			out.Filters = decoded
		}
	}

	return &out, nil
}

// DeleteAlertRule deletes one positive rule identity and returns sql.ErrNoRows
// when no row was affected. Events and silences are not deleted by this
// statement; administration handles the returned error through its not-found
// classifier.
func (r *Store) DeleteAlertRule(ctx context.Context, id int64) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("nil ops repository")
	}
	if id <= 0 {
		return fmt.Errorf("invalid id")
	}

	res, err := r.db.ExecContext(ctx, "DELETE FROM ops_alert_rules WHERE id = $1", id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ListAlertEvents returns a descending fired-at/ID selection after applying
// saved event criteria and an optional complete cursor. A nil filter is
// unrestricted; nonpositive limit selects one hundred and larger limits are
// capped at five hundred. No total count is read.
//
// Nullable measurement zero remains a present pointer, missing resolution
// remains nil, and undecodable dimensions leave that map absent without
// rejecting the row. The cursor requires both nonzero timestamp and positive
// ID, so tied firing times continue below the previous ID. Query, scan, and
// iteration failures reject the whole selection, while input fields remain
// read-only.
func (r *Store) ListAlertEvents(ctx context.Context, filter *ops.OpsAlertEventFilter) ([]*ops.OpsAlertEvent, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		filter = &ops.OpsAlertEventFilter{}
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	// Bound one history page; this selection has no separate total-count query.
	if limit > 500 {
		limit = 500
	}

	where, args := buildOpsAlertEventsWhere(filter)
	args = append(args, limit)
	limitArg := "$" + itoa(len(args))

	q := `
SELECT
  id,
  COALESCE(rule_id, 0),
  COALESCE(severity, ''),
  COALESCE(status, ''),
  COALESCE(title, ''),
  COALESCE(description, ''),
  metric_value,
  threshold_value,
  dimensions,
  fired_at,
  resolved_at,
  email_sent,
  created_at
FROM ops_alert_events
` + where + `
ORDER BY fired_at DESC, id DESC
LIMIT ` + limitArg

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	// Close the cursor after success or an early scan error. Cleanup failures are
	// suppressed, while iteration errors still reject this row set.
	defer func() { _ = rows.Close() }()

	out := []*ops.OpsAlertEvent{}
	for rows.Next() {
		var ev ops.OpsAlertEvent
		var metricValue sql.NullFloat64
		var thresholdValue sql.NullFloat64
		var dimensionsRaw []byte
		var resolvedAt sql.NullTime
		if err := rows.Scan(
			&ev.ID,
			&ev.RuleID,
			&ev.Severity,
			&ev.Status,
			&ev.Title,
			&ev.Description,
			&metricValue,
			&thresholdValue,
			&dimensionsRaw,
			&ev.FiredAt,
			&resolvedAt,
			&ev.EmailSent,
			&ev.CreatedAt,
		); err != nil {
			return nil, err
		}
		// Restore present measurements into separate pointers before optional
		// dimensions decoding; stored zero remains an observation rather than being
		// merged with missing SQL data.
		if metricValue.Valid {
			v := metricValue.Float64
			ev.MetricValue = &v
		}
		if thresholdValue.Valid {
			v := thresholdValue.Float64
			ev.ThresholdValue = &v
		}
		if resolvedAt.Valid {
			v := resolvedAt.Time
			ev.ResolvedAt = &v
		}
		// Dimensions supplement the scalar event. Failed object decoding is ignored
		// so listing can retain the observation even when metadata cannot be
		// displayed.
		if len(dimensionsRaw) > 0 && string(dimensionsRaw) != "null" {
			var decoded map[string]any
			if err := json.Unmarshal(dimensionsRaw, &decoded); err == nil {
				ev.Dimensions = decoded
			}
		}
		out = append(out, &ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// GetAlertEventByID loads one positive event identity through the shared
// scanner. sql.ErrNoRows becomes a nil event with nil error for OpsService to
// translate into not found, while other query or scan failures propagate.
func (r *Store) GetAlertEventByID(ctx context.Context, eventID int64) (*ops.OpsAlertEvent, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if eventID <= 0 {
		return nil, fmt.Errorf("invalid event id")
	}

	q := `
SELECT
  id,
  COALESCE(rule_id, 0),
  COALESCE(severity, ''),
  COALESCE(status, ''),
  COALESCE(title, ''),
  COALESCE(description, ''),
  metric_value,
  threshold_value,
  dimensions,
  fired_at,
  resolved_at,
  email_sent,
  created_at
FROM ops_alert_events
WHERE id = $1`

	row := r.db.QueryRowContext(ctx, q, eventID)
	ev, err := scanOpsAlertEvent(row)
	if err != nil {
		// Translate missing history to nil for caller decisions. Keep other
		// scan/query failures visible so they are not mistaken for an absent event.
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return ev, nil
}

// GetActiveAlertEvent selects the latest firing event for a positive rule
// identity, ordered by fired_at with no ID tie breaker. Missing history becomes
// nil with nil error; multiple firing rows can exist, and this read does not
// reserve an insertion.
func (r *Store) GetActiveAlertEvent(ctx context.Context, ruleID int64) (*ops.OpsAlertEvent, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if ruleID <= 0 {
		return nil, fmt.Errorf("invalid rule id")
	}

	q := `
SELECT
  id,
  COALESCE(rule_id, 0),
  COALESCE(severity, ''),
  COALESCE(status, ''),
  COALESCE(title, ''),
  COALESCE(description, ''),
  metric_value,
  threshold_value,
  dimensions,
  fired_at,
  resolved_at,
  email_sent,
  created_at
FROM ops_alert_events
WHERE rule_id = $1 AND status = $2
ORDER BY fired_at DESC
LIMIT 1`

	row := r.db.QueryRowContext(ctx, q, ruleID, ops.OpsAlertStatusFiring)
	ev, err := scanOpsAlertEvent(row)
	if err != nil {
		// Translate missing history to nil for caller decisions. Keep other
		// scan/query failures visible so they are not mistaken for an absent event.
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return ev, nil
}

// GetLatestAlertEvent selects the latest event for a positive rule identity
// regardless of status, ordered by fired_at without an ID tie breaker. Missing
// history becomes nil with nil error; the evaluator uses the returned firing
// time for cooldown.
func (r *Store) GetLatestAlertEvent(ctx context.Context, ruleID int64) (*ops.OpsAlertEvent, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if ruleID <= 0 {
		return nil, fmt.Errorf("invalid rule id")
	}

	q := `
SELECT
  id,
  COALESCE(rule_id, 0),
  COALESCE(severity, ''),
  COALESCE(status, ''),
  COALESCE(title, ''),
  COALESCE(description, ''),
  metric_value,
  threshold_value,
  dimensions,
  fired_at,
  resolved_at,
  email_sent,
  created_at
FROM ops_alert_events
WHERE rule_id = $1
ORDER BY fired_at DESC
LIMIT 1`

	row := r.db.QueryRowContext(ctx, q, ruleID)
	ev, err := scanOpsAlertEvent(row)
	if err != nil {
		// Translate missing history to nil for caller decisions. Keep other
		// scan/query failures visible so they are not mistaken for an absent event.
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return ev, nil
}

// CreateAlertEvent inserts the supplied observation and returns its saved row
// through the shared scanner. Nil input is rejected and dimensions encode
// before insertion. Rule zero becomes SQL NULL, measurements preserve
// pointer-valued zero, and FiredAt is stored as supplied rather than defaulted
// here.
//
// Caller-provided status, severity, title, FiredAt, ResolvedAt, and mail marker
// determine their stored values; the database supplies CreatedAt. The
// statement does not reserve a rule or check an
// existing active event; evaluator admission is performed earlier and
// independently. Its generated ID distinguishes each insertion, while rule
// last_triggered_at is not updated by this method. Failed optional dimensions
// decoding after a successful insert leaves that map absent.
func (r *Store) CreateAlertEvent(ctx context.Context, event *ops.OpsAlertEvent) (*ops.OpsAlertEvent, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if event == nil {
		return nil, fmt.Errorf("nil event")
	}

	// Serialize caller-owned dimensions before insertion, retaining NULL versus an
	// empty object. An unsupported value rejects this event before any row is
	// written.
	dimensionsArg, err := opsNullJSONMap(event.Dimensions)
	if err != nil {
		return nil, err
	}

	q := `
INSERT INTO ops_alert_events (
  rule_id,
  severity,
  status,
  title,
  description,
  metric_value,
  threshold_value,
  dimensions,
  fired_at,
  resolved_at,
  email_sent,
  created_at
) VALUES (
  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NOW()
)
RETURNING
  id,
  COALESCE(rule_id, 0),
  COALESCE(severity, ''),
  COALESCE(status, ''),
  COALESCE(title, ''),
  COALESCE(description, ''),
  metric_value,
  threshold_value,
  dimensions,
  fired_at,
  resolved_at,
  email_sent,
  created_at`

	row := r.db.QueryRowContext(
		ctx,
		q,
		opsNullInt64(&event.RuleID),
		opsNullString(event.Severity),
		opsNullString(event.Status),
		opsNullString(event.Title),
		opsNullString(event.Description),
		opsNullFloat64(event.MetricValue),
		opsNullFloat64(event.ThresholdValue),
		dimensionsArg,
		event.FiredAt,
		opsNullTime(event.ResolvedAt),
		event.EmailSent,
	)
	return scanOpsAlertEvent(row)
}

// UpdateAlertEventStatus writes trimmed nonblank status and nullable resolution
// time for a positive event identity. Nil or zero time becomes SQL NULL.
// OpsService restricts administrator states; direct storage calls do not
// enforce that enumeration, and missing rows can succeed because RowsAffected
// is ignored.
func (r *Store) UpdateAlertEventStatus(ctx context.Context, eventID int64, status string, resolvedAt *time.Time) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("nil ops repository")
	}
	if eventID <= 0 {
		return fmt.Errorf("invalid event id")
	}
	if strings.TrimSpace(status) == "" {
		return fmt.Errorf("invalid status")
	}

	q := `
UPDATE ops_alert_events
SET status = $2,
    resolved_at = $3
WHERE id = $1`

	_, err := r.db.ExecContext(ctx, q, eventID, strings.TrimSpace(status), opsNullTime(resolvedAt))
	return err
}

// UpdateAlertEventEmailSent writes the supplied mail marker for a positive
// event identity. This update is separate from mail callback acceptance and
// does not prove SMTP delivery; missing rows can return success because
// RowsAffected is not inspected.
func (r *Store) UpdateAlertEventEmailSent(ctx context.Context, eventID int64, emailSent bool) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("nil ops repository")
	}
	if eventID <= 0 {
		return fmt.Errorf("invalid event id")
	}

	_, err := r.db.ExecContext(ctx, "UPDATE ops_alert_events SET email_sent = $2 WHERE id = $1", eventID, emailSent)
	return err
}

// CreateAlertSilence inserts an exact saved scope with a positive rule,
// nonblank platform, and nonzero deadline. Platform and optional text are
// trimmed; group zero and missing group both become SQL NULL. A deadline in the
// past is accepted by this storage method.
//
// Nil or blank region becomes SQL NULL, which later matches only the equally
// missing region through IsAlertSilenced rather than acting as a wildcard.
// Nonzero negative group or actor values remain supplied. Returned valid
// optional values receive new scalar pointers, and database created_at is
// returned alongside the supplied deadline and encoded scope.
func (r *Store) CreateAlertSilence(ctx context.Context, input *ops.OpsAlertSilence) (*ops.OpsAlertSilence, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if input == nil {
		return nil, fmt.Errorf("nil input")
	}
	if input.RuleID <= 0 {
		return nil, fmt.Errorf("invalid rule_id")
	}
	platform := strings.TrimSpace(input.Platform)
	if platform == "" {
		return nil, fmt.Errorf("invalid platform")
	}
	if input.Until.IsZero() {
		return nil, fmt.Errorf("invalid until")
	}

	q := `
INSERT INTO ops_alert_silences (
  rule_id,
  platform,
  group_id,
  region,
  until,
  reason,
  created_by,
  created_at
) VALUES (
  $1,$2,$3,$4,$5,$6,$7,NOW()
)
RETURNING id, rule_id, platform, group_id, region, until, COALESCE(reason,''), created_by, created_at`

	row := r.db.QueryRowContext(
		ctx,
		q,
		input.RuleID,
		platform,
		opsNullInt64(input.GroupID),
		opsNullString(input.Region),
		input.Until,
		opsNullString(input.Reason),
		opsNullInt64(input.CreatedBy),
	)

	var out ops.OpsAlertSilence
	var groupID sql.NullInt64
	var region sql.NullString
	var createdBy sql.NullInt64
	if err := row.Scan(
		&out.ID,
		&out.RuleID,
		&out.Platform,
		&groupID,
		&region,
		&out.Until,
		&out.Reason,
		&createdBy,
		&out.CreatedAt,
	); err != nil {
		return nil, err
	}
	// Restore each nullable scope or actor as a separate scalar pointer after SQL
	// encoding. Missing dimensions remain nil for later exact-scope comparisons.
	if groupID.Valid {
		v := groupID.Int64
		out.GroupID = &v
	}
	if region.Valid {
		v := strings.TrimSpace(region.String)
		if v != "" {
			out.Region = &v
		}
	}
	if createdBy.Valid {
		v := createdBy.Int64
		out.CreatedBy = &v
	}
	return &out, nil
}

// IsAlertSilenced reports whether an exact rule/platform/group/region scope has
// a deadline strictly after now. Positive rule identity is required; blank
// platform returns false, and zero now selects current UTC time. Group zero and
// nil both encode as SQL NULL, while nil or blank region also becomes NULL.
//
// IS NOT DISTINCT FROM compares nullable dimensions as values, so absent group
// or region does not silence every populated scope. Equality at the deadline is
// expired. Missing lookup returns false without error, while query failure
// remains visible so the evaluator can apply its own fail-open notification
// policy.
func (r *Store) IsAlertSilenced(ctx context.Context, ruleID int64, platform string, groupID *int64, region *string, now time.Time) (bool, error) {
	if r == nil || r.db == nil {
		return false, fmt.Errorf("nil ops repository")
	}
	if ruleID <= 0 {
		return false, fmt.Errorf("invalid rule id")
	}
	platform = strings.TrimSpace(platform)
	if platform == "" {
		return false, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	q := `
SELECT 1
FROM ops_alert_silences
WHERE rule_id = $1
  AND platform = $2
  AND (group_id IS NOT DISTINCT FROM $3)
  AND (region IS NOT DISTINCT FROM $4)
  AND until > $5
LIMIT 1`

	var dummy int
	err := r.db.QueryRowContext(ctx, q, ruleID, platform, opsNullInt64(groupID), opsNullString(region), now).Scan(&dummy)
	if err != nil {
		// An absent exact-scope row means notification is not silenced at this time.
		// Other database failures remain errors for the evaluator's selected policy.
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// scanOpsAlertEvent decodes one compatible event row into newly allocated
// observation storage. The row must supply the event columns in the shared
// SELECT/RETURNING order; scan errors propagate, including sql.ErrNoRows for
// callers to translate. Nullable metric and threshold zero remain present,
// while missing values and resolution time stay nil.
//
// Dimensions are decoded into a fresh map when object JSON is valid. Empty,
// JSON null, or malformed dimensions remain absent, and decode failure is
// suppressed because stored scalar event data can still be read. Returned
// scalar pointers are independent of scan locals; no default firing time or
// business-state validation occurs.
func scanOpsAlertEvent(row opsAlertEventRow) (*ops.OpsAlertEvent, error) {
	var ev ops.OpsAlertEvent
	var metricValue sql.NullFloat64
	var thresholdValue sql.NullFloat64
	var dimensionsRaw []byte
	var resolvedAt sql.NullTime

	if err := row.Scan(
		&ev.ID,
		&ev.RuleID,
		&ev.Severity,
		&ev.Status,
		&ev.Title,
		&ev.Description,
		&metricValue,
		&thresholdValue,
		&dimensionsRaw,
		&ev.FiredAt,
		&resolvedAt,
		&ev.EmailSent,
		&ev.CreatedAt,
	); err != nil {
		return nil, err
	}
	// Allocate each valid measurement and resolution scalar separately, preserving
	// SQL absence as nil while retaining recorded zero values for event display.
	if metricValue.Valid {
		v := metricValue.Float64
		ev.MetricValue = &v
	}
	if thresholdValue.Valid {
		v := thresholdValue.Float64
		ev.ThresholdValue = &v
	}
	if resolvedAt.Valid {
		v := resolvedAt.Time
		ev.ResolvedAt = &v
	}
	// Read optional object dimensions after scalar values. Invalid metadata is
	// suppressed, keeping the event available for status/cooldown decisions even
	// when dimensions are absent.
	if len(dimensionsRaw) > 0 && string(dimensionsRaw) != "null" {
		var decoded map[string]any
		if err := json.Unmarshal(dimensionsRaw, &decoded); err == nil {
			ev.Dimensions = decoded
		}
	}
	return &ev, nil
}

// buildOpsAlertEventsWhere returns bound predicates and ordered arguments for
// saved event filters. Nil input yields an unrestricted condition. Nonzero
// endpoints form a half-open firing-time range; text comparisons use trimmed
// case-sensitive values and EmailSent preserves either supplied boolean.
//
// A cursor adds descending timestamp/ID continuation only when both values are
// usable, matching ListAlertEvents tie ordering. Platform and positive GroupID
// compare JSON text extracted from dimensions, with group formatted as decimal
// text; differently encoded group values can fail that equality. Filters
// combine conjunctively, and provided enum text is not validated by this
// builder.
func buildOpsAlertEventsWhere(filter *ops.OpsAlertEventFilter) (string, []any) {
	clauses := []string{"1=1"}
	args := []any{}

	if filter == nil {
		return "WHERE " + strings.Join(clauses, " AND "), args
	}

	if status := strings.TrimSpace(filter.Status); status != "" {
		args = append(args, status)
		clauses = append(clauses, "status = $"+itoa(len(args)))
	}
	if severity := strings.TrimSpace(filter.Severity); severity != "" {
		args = append(args, severity)
		clauses = append(clauses, "severity = $"+itoa(len(args)))
	}
	if filter.EmailSent != nil {
		args = append(args, *filter.EmailSent)
		clauses = append(clauses, "email_sent = $"+itoa(len(args)))
	}
	if filter.StartTime != nil && !filter.StartTime.IsZero() {
		args = append(args, *filter.StartTime)
		clauses = append(clauses, "fired_at >= $"+itoa(len(args)))
	}
	if filter.EndTime != nil && !filter.EndTime.IsZero() {
		args = append(args, *filter.EndTime)
		clauses = append(clauses, "fired_at < $"+itoa(len(args)))
	}

	// Continue below both cursor components when firing times tie. An incomplete
	// cursor adds no condition, matching the listing's descending timestamp and ID
	// order.
	if filter.BeforeFiredAt != nil && !filter.BeforeFiredAt.IsZero() && filter.BeforeID != nil && *filter.BeforeID > 0 {
		args = append(args, *filter.BeforeFiredAt)
		tsArg := "$" + itoa(len(args))
		args = append(args, *filter.BeforeID)
		idArg := "$" + itoa(len(args))
		clauses = append(clauses, fmt.Sprintf("(fired_at < %s OR (fired_at = %s AND id < %s))", tsArg, tsArg, idArg))
	}
	// Compare saved JSON dimensions as extracted text. Decimal group encoding
	// matches evaluator-built dimensions and avoids inferring current group or
	// platform membership.
	if platform := strings.TrimSpace(filter.Platform); platform != "" {
		args = append(args, platform)
		clauses = append(clauses, "(dimensions->>'platform') = $"+itoa(len(args)))
	}
	if filter.GroupID != nil && *filter.GroupID > 0 {
		args = append(args, fmt.Sprintf("%d", *filter.GroupID))
		clauses = append(clauses, "(dimensions->>'group_id') = $"+itoa(len(args)))
	}

	return "WHERE " + strings.Join(clauses, " AND "), args
}

// opsNullJSONMap encodes a supplied map as nullable JSON text for PostgreSQL.
// Nil becomes SQL NULL, present empty maps remain encoded objects, and
// unsupported JSON values return a marshal error before persistence.
func opsNullJSONMap(v map[string]any) (any, error) {
	if v == nil {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return sql.NullString{}, nil
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}
