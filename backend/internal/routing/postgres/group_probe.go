package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lib/pq"

	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// group_probe.go persists probe leases and outcomes and aggregates group
// availability history.
// Availability probes use durable per-group leases so separate runner instances
// can claim work. Claiming reconciles enabled active groups with schedule rows
// before locking due rows and recording lease owners in one transaction. A
// claimed row is returned only after its configuration decodes and the
// transaction commits; abandoned work becomes eligible when its lease expires.
//
// A finished probe adds one history observation and advances the schedule
// together, clearing the lease at that commit. Market summaries count
// observations by their start time in fixed-duration local buckets, while
// latest status is selected independently across all retained history. An empty
// bucket reports unknown availability, and an older latest observation can
// remain visible outside the counting window.

// GroupAvailabilityProbeStore persists probe leases, observations, and
// marketplace availability summaries in PostgreSQL.
type GroupAvailabilityProbeStore struct {
	// db is borrowed from the application and stays open through runner and
	// summary calls. Store methods create transactions without owning the pool's
	// shutdown.
	db *sql.DB
}

// NewGroupAvailabilityProbeRepository retains the application database for
// probe storage. Construction performs no work; repository operations return
// empty results or success when the retained database is nil, allowing disabled
// probe storage.
func NewGroupAvailabilityProbeRepository(db *sql.DB) routing.GroupAvailabilityProbeRepository {
	return &GroupAvailabilityProbeStore{db: db}
}

// ClaimDue leases enabled active groups whose next run and prior lease are due
// at now. A nonpositive limit selects ten; callers supply the lease deadline
// and instance identity, then execute the returned groups. A nil store or
// database returns no work.
//
// Schedule reconciliation, row locking with SKIP LOCKED, and lease updates
// share one transaction. Competing claimers skip locked schedule rows, and
// configuration decoding must succeed before the claim commits. Returned groups
// are ordered by ID even though due selection uses next-run order. Errors
// before the commit attempt roll back newly assigned leases.
func (r *GroupAvailabilityProbeStore) ClaimDue(ctx context.Context, now time.Time, lockUntil time.Time, lockedBy string, limit int) ([]routing.GroupAvailabilityProbeDueGroup, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	// Failures before the commit attempt roll back reconciliation and lease
	// changes; cleanup remains deferred because early exits can follow successful
	// writes.
	defer func() {
		_ = tx.Rollback()
	}()

	// Seed enabled active groups before removing ineligible schedule rows; both
	// changes participate in this claim transaction and leave existing eligible
	// schedules intact.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO group_availability_probe_states (group_id, next_run_at, created_at, updated_at)
		SELECT id, $1, NOW(), NOW()
		FROM groups
		WHERE deleted_at IS NULL
		  AND status = 'active'
		  AND availability_probe_config @> '{"enabled": true}'::jsonb
		ON CONFLICT (group_id) DO NOTHING
	`, now); err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM group_availability_probe_states s
		WHERE NOT EXISTS (
			SELECT 1
			FROM groups g
			WHERE g.id = s.group_id
			  AND g.deleted_at IS NULL
			  AND g.status = 'active'
			  AND g.availability_probe_config @> '{"enabled": true}'::jsonb
		)
	`); err != nil {
		return nil, err
	}

	// Lock due schedule rows and assign their lease in one query; SKIP LOCKED lets
	// another runner progress on different groups.
	rows, err := tx.QueryContext(ctx, `
		WITH due AS (
			SELECT s.group_id
			FROM group_availability_probe_states s
			JOIN groups g ON g.id = s.group_id
			WHERE g.deleted_at IS NULL
			  AND g.status = 'active'
			  AND g.availability_probe_config @> '{"enabled": true}'::jsonb
			  AND (s.next_run_at IS NULL OR s.next_run_at <= $1)
			  AND (s.locked_until IS NULL OR s.locked_until <= $1)
			ORDER BY COALESCE(s.next_run_at, 'epoch'::timestamptz), s.group_id
			LIMIT $4
			FOR UPDATE OF s SKIP LOCKED
		),
		claimed AS (
			UPDATE group_availability_probe_states s
			SET locked_until = $2,
				locked_by = $3,
				updated_at = NOW()
			FROM due
			WHERE s.group_id = due.group_id
			RETURNING s.group_id
		)
		SELECT g.id, g.name, g.availability_probe_config
		FROM claimed c
		JOIN groups g ON g.id = c.group_id
		ORDER BY g.id
	`, now, lockUntil, lockedBy, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	dueGroups := make([]routing.GroupAvailabilityProbeDueGroup, 0)
	for rows.Next() {
		var item routing.GroupAvailabilityProbeDueGroup
		var rawConfig []byte
		if err := rows.Scan(&item.GroupID, &item.Name, &rawConfig); err != nil {
			return nil, err
		}
		if len(rawConfig) > 0 {
			if err := json.Unmarshal(rawConfig, &item.Config); err != nil {
				return nil, fmt.Errorf("decode availability probe config: %w", err)
			}
		}
		dueGroups = append(dueGroups, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Commit after every claimed configuration decodes. An error returns no groups
	// to the runner, leaving any durable claims for lease-expiry recovery.
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dueGroups, nil
}

// SaveResultAndScheduleNext appends a probe observation and records nextRunAt
// while clearing the group's lease. Nil storage or a nil result returns success
// without a write. The caller supplies the final observation and owns the
// scheduling decision.
//
// History insertion precedes schedule upsert in the same transaction, so an
// upsert failure cannot leave a committed observation with an unchanged
// schedule. Commit publishes both changes together. The update uses GroupID
// alone, so callers must complete their claimed work within its lease; this
// method does not compare lockedBy or reject a stale lease owner.
func (r *GroupAvailabilityProbeStore) SaveResultAndScheduleNext(ctx context.Context, result *routing.GroupAvailabilityProbeResult, nextRunAt time.Time) error {
	if r == nil || r.db == nil || result == nil {
		return nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Roll back a failed history or schedule write before attempting commit, so a
	// successfully inserted observation cannot remain as a separate committed
	// step.
	defer func() {
		_ = tx.Rollback()
	}()

	// Append the final observation before advancing its schedule, retaining the
	// configured request-model identity and completion time in durable history.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO group_availability_probe_results (
			group_id, provider_id, model_id, status, success, latency_ms,
			error_message, started_at, finished_at, created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
	`, result.GroupID, result.ProviderID, result.ModelID, result.Status, result.Success, result.LatencyMs, nullableProbeString(result.ErrorMessage), result.StartedAt, result.FinishedAt); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO group_availability_probe_states (
			group_id, next_run_at, locked_until, locked_by, last_status,
			last_success, last_latency_ms, last_error, last_checked_at, created_at, updated_at
		)
		VALUES ($1, $2, NULL, NULL, $3, $4, $5, $6, $7, NOW(), NOW())
		ON CONFLICT (group_id) DO UPDATE SET
			next_run_at = EXCLUDED.next_run_at,
			locked_until = NULL,
			locked_by = NULL,
			last_status = EXCLUDED.last_status,
			last_success = EXCLUDED.last_success,
			last_latency_ms = EXCLUDED.last_latency_ms,
			last_error = EXCLUDED.last_error,
			last_checked_at = EXCLUDED.last_checked_at,
			updated_at = NOW()
	`, result.GroupID, nextRunAt, result.Status, result.Success, result.LatencyMs, nullableProbeString(result.ErrorMessage), result.FinishedAt); err != nil {
		return err
	}

	// Publish history and the cleared lease together so later claims use the
	// next-run time corresponding to this stored observation.
	return tx.Commit()
}

// GetSummaryByGroupIDs returns window counts and latest probe status for each
// requested group. Window and bucket settings are normalized; empty or invalid
// timezones use UTC. Nil storage or an empty ID list returns an empty map.
//
// Buckets end at the first local bucket boundary at or after now and cover
// whole fixed-duration buckets over the requested nominal day span. Counts use
// started_at in a half-open UTC interval; buckets without observations retain
// nil availability. Latest status is queried separately by started_at and then
// ID, with finished_at as its timestamp, so it can precede the counting window.
// The two queries use separate database reads, and any read failure discards
// the assembled result.
// @project-doc docs/interfaces/model_catalog_and_marketplace.md#group_availability_probe
func (r *GroupAvailabilityProbeStore) GetSummaryByGroupIDs(ctx context.Context, groupIDs []int64, days int, bucketMinutes int, timezoneName string, now time.Time) (map[int64]*routing.GroupAvailabilitySummary, error) {
	out := make(map[int64]*routing.GroupAvailabilitySummary, len(groupIDs))
	if r == nil || r.db == nil || len(groupIDs) == 0 {
		return out, nil
	}
	days, bucketMinutes = routing.NormalizeMarketplaceAvailabilityWindow(days, bucketMinutes)

	loc := time.UTC
	// Retain UTC when the requested location cannot load, keeping bucket
	// timestamps usable for public summaries with invalid timezone settings.
	if timezoneName != "" {
		if parsed, err := time.LoadLocation(timezoneName); err == nil && parsed != nil {
			loc = parsed
		}
	}
	localNow := now.In(loc)
	bucketDuration := time.Duration(bucketMinutes) * time.Minute
	endLocal := nextLocalBucketBoundary(localNow, bucketDuration)
	// Round the nominal day span up to complete fixed-duration buckets; the result
	// includes the current partial bucket when now lies inside it.
	totalMinutes := days * 24 * 60
	bucketCount := (totalMinutes + bucketMinutes - 1) / bucketMinutes
	if bucketCount <= 0 {
		bucketCount = 1
	}
	startLocal := endLocal.Add(-time.Duration(bucketCount) * bucketDuration)
	startUTC := startLocal.UTC()
	endUTC := endLocal.UTC()

	for _, groupID := range groupIDs {
		summary := &routing.GroupAvailabilitySummary{
			WindowDays:    days,
			BucketMinutes: bucketMinutes,
			Days:          make([]routing.GroupAvailabilityBucket, 0, bucketCount),
		}
		for i := range bucketCount {
			bucketStart := startLocal.Add(time.Duration(i) * bucketDuration)
			summary.Days = append(summary.Days, routing.GroupAvailabilityBucket{
				Date: bucketStart.Format(time.RFC3339),
			})
		}
		out[groupID] = summary
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT
			group_id,
			FLOOR((EXTRACT(EPOCH FROM started_at) - EXTRACT(EPOCH FROM $2::timestamptz)) / ($4::double precision * 60))::int AS bucket_index,
			COUNT(*) FILTER (WHERE success = true) AS success_count,
			COUNT(*) AS total_count
		FROM group_availability_probe_results
		WHERE group_id = ANY($1)
		  AND started_at >= $2
		  AND started_at < $3
		GROUP BY group_id, bucket_index
	`, pq.Array(groupIDs), startUTC, endUTC, bucketMinutes)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var groupID int64
		var bucketIndex int
		var successCount int64
		var totalCount int64
		if err := rows.Scan(&groupID, &bucketIndex, &successCount, &totalCount); err != nil {
			return nil, err
		}
		summary, ok := out[groupID]
		if !ok {
			continue
		}
		if bucketIndex < 0 || bucketIndex >= len(summary.Days) {
			continue
		}
		rate := availabilityRate(successCount, totalCount)
		summary.Days[bucketIndex].SuccessCount = successCount
		summary.Days[bucketIndex].TotalCount = totalCount
		summary.Days[bucketIndex].AvailabilityRate = rate
		summary.SuccessCount += successCount
		summary.TotalCount += totalCount
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, summary := range out {
		summary.AvailabilityRate = availabilityRate(summary.SuccessCount, summary.TotalCount)
	}

	// Read latest status outside the counting window so groups with older retained
	// observations still show their last known outcome and completion time.
	lastRows, err := r.db.QueryContext(ctx, `
		SELECT requested.group_id, latest.status, latest.finished_at
		FROM unnest($1::bigint[]) AS requested(group_id)
		CROSS JOIN LATERAL (
			SELECT status, finished_at
			FROM group_availability_probe_results
			WHERE group_id = requested.group_id
			ORDER BY started_at DESC, id DESC
			LIMIT 1
		) latest
	`, pq.Array(groupIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = lastRows.Close() }()

	for lastRows.Next() {
		var groupID int64
		var status string
		var checkedAt time.Time
		if err := lastRows.Scan(&groupID, &status, &checkedAt); err != nil {
			return nil, err
		}
		if summary, ok := out[groupID]; ok {
			summary.LastStatus = status
			t := checkedAt
			summary.LastCheckedAt = &t
		}
	}
	if err := lastRows.Err(); err != nil {
		return nil, err
	}

	return out, nil
}

// nextLocalBucketBoundary returns the first bucket boundary at or after t,
// anchored to midnight in t's location. Nonpositive durations use twenty-four
// hours. An exact boundary is returned unchanged; other times advance to the
// next boundary.
//
// Alignment uses elapsed duration since local midnight rather than absolute
// time truncation, which can offset buckets in non-UTC locations. Buckets then
// advance by fixed durations, including across daylight-saving transitions, so
// they do not promise a calendar-day length. Callers use the result as the
// exclusive end of their history-counting interval.
func nextLocalBucketBoundary(t time.Time, bucketDuration time.Duration) time.Time {
	if bucketDuration <= 0 {
		bucketDuration = 24 * time.Hour
	}
	// Anchor elapsed bucket offsets to midnight in t's location; absolute time
	// truncation could shift the displayed bucket starts in non-UTC zones.
	dayStart := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	bucketStart := dayStart.Add(t.Sub(dayStart) / bucketDuration * bucketDuration)
	if bucketStart.Before(t) {
		return bucketStart.Add(bucketDuration)
	}
	return bucketStart
}

// CleanupOldResults deletes history created before the exclusive before cutoff.
// Nil storage returns success; retention uses created_at rather than the probe
// start time, while schedule rows remain available for later claims.
func (r *GroupAvailabilityProbeStore) CleanupOldResults(ctx context.Context, before time.Time) error {
	if r == nil || r.db == nil {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM group_availability_probe_results
		WHERE created_at < $1
	`, before)
	return err
}

// availabilityRate returns the success fraction, or nil when totalCount is
// nonpositive. Summary consumers use nil for unknown availability,
// distinguishing an empty bucket from an observed bucket with zero successful
// probes.
func availabilityRate(successCount int64, totalCount int64) *float64 {
	if totalCount <= 0 {
		return nil
	}
	value := float64(successCount) / float64(totalCount)
	return &value
}

// nullableProbeString converts an empty probe error into SQL null. Nonempty
// text is stored unchanged, retaining any diagnostic whitespace and error
// content supplied by the probe runner.
func nullableProbeString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
