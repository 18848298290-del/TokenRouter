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

// ListRequestTimings returns the latest http.access timing per client request ID, falling back to request ID.
// The batch query avoids per-row queries on the usage list; unmatched requests are omitted.
func (r *Store) ListRequestTimings(ctx context.Context, clientRequestIDs []string) (map[string]*ops.OpsRequestTiming, error) {
	result := make(map[string]*ops.OpsRequestTiming)
	if r == nil || r.db == nil || len(clientRequestIDs) == 0 {
		return result, nil
	}

	ids := make([]string, 0, len(clientRequestIDs))
	seen := make(map[string]struct{}, len(clientRequestIDs))
	for _, raw := range clientRequestIDs {
		id := strings.TrimSpace(raw)
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
		if timing := ops.OpsRequestTimingFromExtra(extra); timing != nil {
			result[strings.TrimSpace(clientRequestID)] = timing
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
