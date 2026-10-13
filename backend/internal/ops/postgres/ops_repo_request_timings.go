package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lib/pq"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// Request timing metadata is read from the latest matching completed
// http.access row for each stored correlation key. The SQL predicate accepts
// supplied IDs against both client_request_id and request_id, while result
// grouping prefers a nonempty client_request_id and otherwise uses request_id.
// A lookup by an internal ID can therefore return an entry keyed by a different
// stored client ID.
//
// Input is trimmed, bounded, and deduplicated before one batch query. Rows
// whose extracted timing is absent are omitted, but malformed JSON stops the
// complete result with an error. This lookup attaches retained diagnostic
// metadata to usage rows; the chosen correlation key and latest-row ordering
// determine which matched observation survives when several requests share a
// client ID.

// ListRequestTimings returns recognized timing metadata from the latest
// matching completed access log for each stored correlation key. A nil store,
// absent database, or empty usable input returns a newly allocated empty map.
//
// Supplied IDs are trimmed and deduplicated; IDs longer than one hundred
// twenty-eight bytes are omitted before matching both request ID columns. SQL grouping
// prefers a nonempty stored client ID and selects the newest matching
// created_at and row ID. Returned keys are trimmed and can differ from supplied
// internal IDs. A chosen row producing no usable timing is omitted without
// trying an older row; scan, JSON decode, and cursor failures discard the
// assembled result.
func (r *Store) ListRequestTimings(ctx context.Context, clientRequestIDs []string) (map[string]*ops.OpsRequestTiming, error) {
	result := make(map[string]*ops.OpsRequestTiming)
	if r == nil || r.db == nil || len(clientRequestIDs) == 0 {
		return result, nil
	}

	// Trim and deduplicate the requested identifiers before the batch predicate,
	// keeping repeated usage rows from enlarging the SQL array.
	ids := make([]string, 0, len(clientRequestIDs))
	seen := make(map[string]struct{}, len(clientRequestIDs))
	for _, raw := range clientRequestIDs {
		id := strings.TrimSpace(raw)
		// Omit identifiers above the timing lookup's byte limit.
		if id == "" || len(id) > 128 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return result, nil
	}

	// Match either stored identifier but select by the preferred correlation key,
	// so shared client IDs resolve to one newest matching completed access log.
	rows, err := r.db.QueryContext(ctx, `
SELECT DISTINCT ON (COALESCE(NULLIF(l.client_request_id,''), l.request_id))
  COALESCE(NULLIF(l.client_request_id,''), l.request_id),
  COALESCE(l.extra::text, '{}')
FROM ops_system_logs l
WHERE l.component = 'http.access'
  AND l.message = 'http request completed'
  AND (l.client_request_id = ANY($1) OR l.request_id = ANY($1))
ORDER BY COALESCE(NULLIF(l.client_request_id,''), l.request_id), l.created_at DESC, l.id DESC
`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	// Release the batch cursor on success and errors; close failures are
	// suppressed while scanning and iteration determine the returned result.
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var clientRequestID string
		var extraRaw sql.NullString
		if err := rows.Scan(&clientRequestID, &extraRaw); err != nil {
			return nil, err
		}
		var extra map[string]any
		raw := strings.TrimSpace(extraRaw.String)
		if extraRaw.Valid && raw != "" && raw != "null" && raw != "{}" {
			if err := json.Unmarshal([]byte(raw), &extra); err != nil {
				return nil, fmt.Errorf("decode request timing extra: %w", err)
			}
		}
		// Interpret timing only after SQL selects a row; missing recognized fields
		// omit its group instead of searching older matching access logs.
		if timing := ops.OpsRequestTimingFromExtra(extra); timing != nil {
			result[strings.TrimSpace(clientRequestID)] = timing
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
