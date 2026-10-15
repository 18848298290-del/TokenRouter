package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/lib/pq"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// historical_ingress_cleanup.go pages historical ingress-rejection candidates
// and deletes selected IDs before a supplied cutoff.
// Historical cleanup first pages through auth-phase rows older than a cutoff
// and lacking selected provider context. Classification happens in Ops after
// this read; deletion later rechecks the supplied IDs and time cutoff alone, so
// the two statements do not guarantee those rows still match their earlier
// phase or text.

// HistoricalIngressCleanup supplies SQL candidate pages and independent
// deletion for local-ingress cleanup.
type HistoricalIngressCleanup struct {
	// db is borrowed and required for both methods. Construction performs no nil
	// validation, so callers keep a usable pool open throughout scanning and
	// deletion.
	db *sql.DB // borrowed historical cleanup pool
}

// NewHistoricalIngressCleanup retains the supplied SQL pool without validating
// or opening it. Callers supply a usable nonnil pool and keep it open through
// the independent candidate-list and deletion statements.
func NewHistoricalIngressCleanup(db *sql.DB) *HistoricalIngressCleanup {
	return &HistoricalIngressCleanup{db}
}

// ListCandidates pages IDs strictly after cursor and rows strictly before
// cutoff, requiring auth phase and selected absent provider context. Callers
// supply a positive batch size, forwarded unchanged. Classification remains
// outside SQL, and upstream event arrays or count-token flags are not checked.
// Query/scan errors reject the page, while cursor errors can accompany a
// populated slice.
func (s *HistoricalIngressCleanup) ListCandidates(ctx context.Context, cursor int64, before time.Time, batchSize int) ([]ops.HistoricalIngressCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `
			SELECT id, COALESCE(status_code, 0), COALESCE(error_message, ''), COALESCE(error_body, '')
			FROM ops_error_logs
			WHERE id > $1
			  AND created_at < $2
			  AND error_phase = 'auth'
			  AND provider_id IS NULL
			  AND upstream_status_code IS NULL
			  AND COALESCE(upstream_error_message, '') = ''
			  AND COALESCE(upstream_error_detail, '') = ''
			ORDER BY id ASC
			LIMIT $3`, cursor, before, batchSize)
	if err != nil {
		return nil, err
	}
	// Release the candidate cursor after scanning or an early error; close
	// failures are suppressed while scan and iteration errors determine the page
	// result.
	defer func() { _ = rows.Close() }()
	batch := make([]ops.HistoricalIngressCandidate, 0, batchSize)
	for rows.Next() {
		var item ops.HistoricalIngressCandidate
		if err := rows.Scan(&item.ID, &item.StatusCode, &item.Message, &item.Body); err != nil {
			return nil, err
		}
		batch = append(batch, item)
	}
	return batch, rows.Err()
}

// DeleteCandidates deletes supplied IDs that remain older than cutoff and
// returns affected rows. It does not repeat auth/provider/text eligibility
// checks from the earlier page; each call is an independent statement whose
// completion can precede a later failure.
func (s *HistoricalIngressCleanup) DeleteCandidates(ctx context.Context, ids []int64, before time.Time) (int64, error) {
	// Recheck time cutoff at deletion after classification, but use only selected
	// IDs and age; concurrent changes to other eligibility fields remain outside
	// this predicate.
	result, err := s.db.ExecContext(ctx, `DELETE FROM ops_error_logs WHERE id = ANY($1) AND created_at < $2`, pq.Array(ids), before)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
