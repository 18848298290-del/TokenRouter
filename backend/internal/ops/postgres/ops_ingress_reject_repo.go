package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// Ingress-rejection rows accumulate deltas under a unique minute-and-dimension
// key. A PostgreSQL transaction spans all fixed-size input chunks, and conflict
// updates add request counts while retaining the earliest and latest recorded
// arrival times. Optional caller IDs use stored zero sentinels so unidentified
// requests participate in that key.
//
// Listing applies independently optional bucket and dimension comparisons, then
// reads a total and an ordered page in separate statements. It returns positive
// caller IDs as optional pointers and omits stored zero values. Additive writes
// allow several snapshots to contribute to one aggregate, but resubmitting an
// already committed delta adds it again; a commit error is not resolved through
// a batch identity here.

// ingressRejectUpsertChunkSize bounds each statement's input segment while one
// transaction covers the complete supplied delta batch.
const ingressRejectUpsertChunkSize = 500

// BatchUpsertIngressRejects adds positive nonnil input deltas to their
// minute-and-dimension aggregates in one transaction. A nil store, missing
// database, or empty input is treated as successful omission.
//
// Input segments contain at most five hundred rows before invalid deltas are
// filtered. Nil caller IDs become stored zero, and timestamps use UTC; counts
// and identities are otherwise passed through. On conflict, counts are added
// and first/last times are merged. An execution error invokes deferred
// rollback; a commit error is returned without resolving whether the commit
// occurred, so retrying can repeat deltas.
func (r *Store) BatchUpsertIngressRejects(ctx context.Context, items []*ops.OpsIngressRejectAggregate) error {
	if r == nil || r.db == nil || len(items) == 0 {
		return nil
	}
	// Keep every chunk in one transaction so an execution failure can roll back
	// earlier chunks before any successful return.
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Defer rollback through all early returns; after a successful commit this
	// cleanup is harmless, and rollback errors are suppressed.
	defer func() { _ = tx.Rollback() }()

	// Split the supplied batch into fixed segments before filtering, keeping
	// statement parameter growth bounded even when some entries are skipped.
	for start := 0; start < len(items); start += ingressRejectUpsertChunkSize {
		end := min(start+ingressRejectUpsertChunkSize, len(items))
		valid := make([]*ops.OpsIngressRejectAggregate, 0, end-start)
		for _, item := range items[start:end] {
			if item != nil && item.RequestCount > 0 {
				valid = append(valid, item)
			}
		}
		if len(valid) == 0 {
			continue
		}

		// Builder writes target memory and return nil errors. SQL execution supplies
		// the returned failure path, so construction discards those write results.
		var query strings.Builder
		_, _ = query.WriteString(`INSERT INTO ops_ingress_reject_aggregates
  (bucket_start, reject_reason, route_family, protocol, client_ip, user_id, api_key_id, request_count, first_seen, last_seen)
VALUES `)
		args := make([]any, 0, len(valid)*10)
		for i, item := range valid {
			if i > 0 {
				_ = query.WriteByte(',')
			}
			base := len(args)
			fmt.Fprintf(&query, "($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
				base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10)
			// Encode absent caller IDs as zero to match the nonnull dimension key used
			// by the aggregate's unique constraint.
			var userID, apiKeyID int64
			if item.UserID != nil {
				userID = *item.UserID
			}
			if item.APIKeyID != nil {
				apiKeyID = *item.APIKeyID
			}
			args = append(args, item.BucketStart.UTC(), item.RejectReason, item.RouteFamily, item.Protocol,
				item.ClientIP, userID, apiKeyID, item.RequestCount, item.FirstSeen.UTC(), item.LastSeen.UTC())
		}
		_, _ = query.WriteString(`
ON CONFLICT (bucket_start, reject_reason, route_family, protocol, client_ip, user_id, api_key_id)
DO UPDATE SET request_count = ops_ingress_reject_aggregates.request_count + EXCLUDED.request_count,
              first_seen = LEAST(ops_ingress_reject_aggregates.first_seen, EXCLUDED.first_seen),
              last_seen = GREATEST(ops_ingress_reject_aggregates.last_seen, EXCLUDED.last_seen),
              updated_at = NOW()`)
		if _, err := tx.ExecContext(ctx, query.String(), args...); err != nil {
			return err
		}
	}
	// Commit after all chunk statements succeed; the returned commit error remains
	// ambiguous to the in-memory queue's later retry.
	return tx.Commit()
}

// ListIngressRejects returns matching aggregate rows ordered by bucket and row
// ID descending. A nil filter uses defaults, nonpositive pagination becomes
// page one and size fifty, and size is capped at two hundred.
//
// Bucket cutoffs are independently optional and form an inclusive-start,
// exclusive-end interval. Nonblank dimensions use exact comparisons, client IP
// is cast to inet, and nonnil IDs include stored zero when supplied directly.
// Total and page queries use separate statements, so concurrent writes can
// change membership between them. Newly scanned rows omit nonpositive optional
// IDs and are returned after cursor errors are checked.
func (r *Store) ListIngressRejects(ctx context.Context, filter *ops.OpsIngressRejectFilter) (*ops.OpsIngressRejectList, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		filter = &ops.OpsIngressRejectFilter{}
	}
	page, pageSize := filter.Page, filter.PageSize
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	// Bound aggregate pages to two hundred returned rows.
	if pageSize > 200 {
		pageSize = 200
	}

	clauses := []string{"1=1"}
	args := make([]any, 0)
	add := func(expr string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(expr, len(args)))
	}
	if filter.StartTime != nil {
		add("bucket_start >= $%d", filter.StartTime.UTC())
	}
	if filter.EndTime != nil {
		add("bucket_start < $%d", filter.EndTime.UTC())
	}
	if value := strings.TrimSpace(filter.RejectReason); value != "" {
		add("reject_reason = $%d", value)
	}
	if value := strings.TrimSpace(filter.RouteFamily); value != "" {
		add("route_family = $%d", value)
	}
	if value := strings.TrimSpace(filter.Protocol); value != "" {
		add("protocol = $%d", value)
	}
	if value := strings.TrimSpace(filter.ClientIP); value != "" {
		add("client_ip = $%d::inet", value)
	}
	if filter.UserID != nil {
		add("user_id = $%d", *filter.UserID)
	}
	if filter.APIKeyID != nil {
		add("api_key_id = $%d", *filter.APIKeyID)
	}
	where := "WHERE " + strings.Join(clauses, " AND ")

	// Read the total before the page with the same predicates; separate statements
	// permit concurrent inserts to change the later page view.
	var total int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ops_ingress_reject_aggregates "+where, args...).Scan(&total); err != nil {
		return nil, err
	}
	args = append(args, pageSize, (page-1)*pageSize)
	query := fmt.Sprintf(`SELECT id,bucket_start,reject_reason,route_family,protocol,host(client_ip),user_id,api_key_id,request_count,first_seen,last_seen
FROM ops_ingress_reject_aggregates %s ORDER BY bucket_start DESC,id DESC LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args))
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	// Release the page cursor after scanning or any early error; close failures
	// are suppressed while query and scan errors remain returned.
	defer func() { _ = rows.Close() }()

	result := &ops.OpsIngressRejectList{
		Items: make([]*ops.OpsIngressRejectAggregate, 0, pageSize), Total: total, Page: page, PageSize: pageSize,
	}
	for rows.Next() {
		item := &ops.OpsIngressRejectAggregate{}
		var userID, apiKeyID int64
		if err := rows.Scan(&item.ID, &item.BucketStart, &item.RejectReason, &item.RouteFamily, &item.Protocol,
			&item.ClientIP, &userID, &apiKeyID, &item.RequestCount, &item.FirstSeen, &item.LastSeen); err != nil {
			return nil, err
		}
		// Restore positive IDs as optional values for the API; stored zero or
		// negative identities are omitted from the returned row.
		if userID > 0 {
			item.UserID = &userID
		}
		if apiKeyID > 0 {
			item.APIKeyID = &apiKeyID
		}
		result.Items = append(result.Items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
