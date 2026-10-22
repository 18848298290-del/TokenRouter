package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// ops_repo_request_details.go queries paginated request details by combining
// usage records and final-client error observations.
// Request drilldown combines usage rows labeled success with final-client error
// rows through UNION ALL. Both observations can exist for one request, and the
// query retains that distinction instead of producing one deduplicated HTTP
// outcome. Errors prefer their stored request ID, then client ID, while saved
// execution platform and model values determine outer dimension filtering.
//
// Pagination and times are normalized before count and page queries repeat the
// same combined source independently. SLAOnly restricts the error branch alone;
// it adds no count-token exclusion, and usage rows stay eligible. Request
// lookup expands correlation text to stored IDs, duration bounds reject null
// durations through SQL comparisons, and wildcard search retains user pattern
// characters. Sort validation occurs after the count read, with no error-ID tie
// breaker for equal creation times.

// ListRequestDetails returns a page and separately queried total of combined
// usage and final-client error observations. A nil filter uses one hour, page
// one, and fifty rows; Normalize caps size at one hundred and orders supplied
// endpoints without changing filter fields.
//
// Kind and platform filters are trimmed and folded, only positive identity
// values add predicates, and model uses the saved model rather than requested-model
// display overrides. RequestID uses request_lookup_ids, while search matches
// ID/model/message with retained wildcards. SLAOnly affects error eligibility
// but neither branch gains a count-probe filter. Count executes before sort
// validation and the independent page read, so totals can diverge during
// writes. Nullable numerics preserve zero through new pointers, with unchecked
// native-int narrowing for duration/status, and blank returned platform becomes
// unknown.
func (r *Store) ListRequestDetails(ctx context.Context, filter *ops.OpsRequestDetailFilter) ([]*ops.OpsRequestDetail, int64, error) {
	if r == nil || r.db == nil {
		return nil, 0, fmt.Errorf("nil ops repository")
	}

	page, pageSize, startTime, endTime := filter.Normalize()
	offset := (page - 1) * pageSize

	conditions := make([]string, 0, 16)
	args := make([]any, 0, 24)

	// Reserve the first two parameters for both source ranges, then append outer
	// filters so count and page share the same combined-window arguments.
	args = append(args, startTime.UTC(), endTime.UTC())

	addCondition := func(condition string, values ...any) {
		conditions = append(conditions, condition)
		args = append(args, values...)
	}

	if filter != nil {
		if kind := strings.TrimSpace(strings.ToLower(filter.Kind)); kind != "" && kind != "all" {
			if kind != string(ops.OpsRequestKindSuccess) && kind != string(ops.OpsRequestKindError) {
				return nil, 0, fmt.Errorf("invalid kind")
			}
			addCondition(fmt.Sprintf("kind = $%d", len(args)+1), kind)
		}

		if platform := strings.TrimSpace(strings.ToLower(filter.Platform)); platform != "" {
			addCondition(fmt.Sprintf("platform = $%d", len(args)+1), platform)
		}
		if filter.GroupID != nil && *filter.GroupID > 0 {
			addCondition(fmt.Sprintf("group_id = $%d", len(args)+1), *filter.GroupID)
		}

		if filter.UserID != nil && *filter.UserID > 0 {
			addCondition(fmt.Sprintf("user_id = $%d", len(args)+1), *filter.UserID)
		}
		if filter.APIKeyID != nil && *filter.APIKeyID > 0 {
			addCondition(fmt.Sprintf("api_key_id = $%d", len(args)+1), *filter.APIKeyID)
		}
		if filter.ProviderID != nil && *filter.ProviderID > 0 {
			addCondition(fmt.Sprintf("provider_id = $%d", len(args)+1), *filter.ProviderID)
		}

		if model := strings.TrimSpace(filter.Model); model != "" {
			addCondition(fmt.Sprintf("model = $%d", len(args)+1), model)
		}
		if requestID := strings.TrimSpace(filter.RequestID); requestID != "" {
			addCondition(fmt.Sprintf("request_id = ANY(ARRAY(SELECT id FROM request_lookup_ids($%d)))", len(args)+1), requestID)
		}
		if q := strings.TrimSpace(filter.Query); q != "" {
			like := "%" + strings.ToLower(q) + "%"
			startIdx := len(args) + 1
			addCondition(
				fmt.Sprintf("(LOWER(COALESCE(request_id,'')) LIKE $%d OR LOWER(COALESCE(model,'')) LIKE $%d OR LOWER(COALESCE(message,'')) LIKE $%d)",
					startIdx, startIdx+1, startIdx+2,
				),
				like, like, like,
			)
		}

		if filter.MinDurationMs != nil {
			addCondition(fmt.Sprintf("duration_ms >= $%d", len(args)+1), *filter.MinDurationMs)
		}
		if filter.MaxDurationMs != nil {
			addCondition(fmt.Sprintf("duration_ms <= $%d", len(args)+1), *filter.MaxDurationMs)
		}
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	// Apply SLA exclusions inside the error branch before UNION ALL, leaving usage
	// observations eligible and retaining the two source identities.
	slaErrorWhere := ""
	if filter != nil && filter.SLAOnly {
		slaErrorWhere = "\n    AND " + opsSLACountableSQL(
			"o.status_code",
			"o.upstream_status_code",
			"o.upstream_errors",
			"o.error_owner",
			"o.is_business_limited",
			filter.IgnoredStatusCodes,
		)
	}

	// Keep both source rows through UNION ALL and preserve error identity
	// separately. Errors prefer nonempty stored request_id, then
	// client_request_id; matching text does not collapse a usage/error pair.
	cte := `
WITH combined AS (
  SELECT
    'success'::TEXT AS kind,
    ul.created_at AS created_at,
    ul.request_id AS request_id,
    ul.platform AS platform,
    ul.model AS model,
    ul.duration_ms AS duration_ms,
    NULL::INT AS status_code,
    NULL::BIGINT AS error_id,
    NULL::TEXT AS phase,
    NULL::TEXT AS severity,
    NULL::TEXT AS message,
    ul.user_id AS user_id,
    ul.api_key_id AS api_key_id,
    ul.provider_id AS provider_id,
    ul.group_id AS group_id,
    ul.stream AS stream
  FROM usage_logs ul
  WHERE ul.created_at >= $1 AND ul.created_at < $2

  UNION ALL

  SELECT
    'error'::TEXT AS kind,
    o.created_at AS created_at,
    COALESCE(NULLIF(o.request_id,''), NULLIF(o.client_request_id,''), '') AS request_id,
    COALESCE(NULLIF(o.platform, ''), 'unknown') AS platform,
    o.model AS model,
    o.duration_ms AS duration_ms,
    o.status_code AS status_code,
    o.id AS error_id,
    o.error_phase AS phase,
    o.severity AS severity,
    o.error_message AS message,
    o.user_id AS user_id,
    o.api_key_id AS api_key_id,
    o.provider_id AS provider_id,
    o.group_id AS group_id,
    o.stream AS stream
  FROM ops_error_logs o
  WHERE o.created_at >= $1 AND o.created_at < $2
    AND COALESCE(o.status_code, 0) >= 400
` + slaErrorWhere + `
)
`

	// Read the matching total before sort selection and pagination; the later page
	// statement can observe another committed state without a shared transaction.
	countQuery := fmt.Sprintf(`%s SELECT COUNT(1) FROM combined %s`, cte, where)
	var total int64
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		// Convert a missing aggregate-row result to zero total and continue paging;
		// real COUNT normally supplies a row even when there are no matches.
		if err == sql.ErrNoRows {
			total = 0
		} else {
			return nil, 0, err
		}
	}

	sort := "ORDER BY created_at DESC"
	if filter != nil {
		switch strings.TrimSpace(strings.ToLower(filter.Sort)) {
		case "", "created_at_desc":
			// default
		case "duration_desc":
			sort = "ORDER BY duration_ms DESC NULLS LAST, created_at DESC"
		default:
			return nil, 0, fmt.Errorf("invalid sort")
		}
	}

	listQuery := fmt.Sprintf(`
%s
SELECT
  kind,
  created_at,
  request_id,
  platform,
  model,
  duration_ms,
  status_code,
  error_id,
  phase,
  severity,
  message,
  user_id,
  api_key_id,
  provider_id,
  group_id,
  stream
FROM combined
%s
%s
LIMIT $%d OFFSET $%d
`, cte, where, sort, len(args)+1, len(args)+2)

	listArgs := append(append([]any{}, args...), pageSize, offset)
	rows, err := r.db.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	// Close the page cursor on success or early error; close failures are
	// suppressed while scan and iteration errors reject this page result.
	defer func() { _ = rows.Close() }()

	// Restore valid duration/status zero as a new native-int pointer, retaining
	// SQL absence as nil while performing unchecked width conversion.
	toIntPtr := func(v sql.NullInt64) *int {
		if !v.Valid {
			return nil
		}
		i := int(v.Int64)
		return &i
	}
	toInt64Ptr := func(v sql.NullInt64) *int64 {
		if !v.Valid {
			return nil
		}
		i := v.Int64
		return &i
	}

	out := make([]*ops.OpsRequestDetail, 0, pageSize)
	for rows.Next() {
		var (
			kind      string
			createdAt time.Time
			requestID sql.NullString
			platform  sql.NullString
			model     sql.NullString

			durationMs sql.NullInt64
			statusCode sql.NullInt64
			errorID    sql.NullInt64

			phase    sql.NullString
			severity sql.NullString
			message  sql.NullString

			userID     sql.NullInt64
			apiKeyID   sql.NullInt64
			providerID sql.NullInt64
			groupID    sql.NullInt64

			stream bool
		)

		if err := rows.Scan(
			&kind,
			&createdAt,
			&requestID,
			&platform,
			&model,
			&durationMs,
			&statusCode,
			&errorID,
			&phase,
			&severity,
			&message,
			&userID,
			&apiKeyID,
			&providerID,
			&groupID,
			&stream,
		); err != nil {
			return nil, 0, err
		}

		item := &ops.OpsRequestDetail{
			Kind:      ops.OpsRequestKind(kind),
			CreatedAt: createdAt,
			RequestID: strings.TrimSpace(requestID.String),
			Platform:  strings.TrimSpace(platform.String),
			Model:     strings.TrimSpace(model.String),

			DurationMs: toIntPtr(durationMs),
			StatusCode: toIntPtr(statusCode),
			ErrorID:    toInt64Ptr(errorID),
			Phase:      phase.String,
			Severity:   severity.String,
			Message:    message.String,

			UserID:     toInt64Ptr(userID),
			APIKeyID:   toInt64Ptr(apiKeyID),
			ProviderID: toInt64Ptr(providerID),
			GroupID:    toInt64Ptr(groupID),

			Stream: stream,
		}

		// Normalize blank or absent platform after scanning to unknown, including
		// usage rows whose stored dimension was not replaced inside the error branch.
		if item.Platform == "" {
			item.Platform = "unknown"
		}

		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	return out, total, nil
}
