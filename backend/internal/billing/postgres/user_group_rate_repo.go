package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/lib/pq"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

// user_group_rate_repo.go reads and updates per-user group rates and RPM
// overrides.
// User billing rates and RPM limits occupy separate nullable columns in the
// same user/group row. Clearing one column needs to retain the other, while a
// row with both columns NULL can be removed. User reads omit NULL rates; group
// reads join non-deleted users for administrator display.
//
// Replacement operations first clear omitted values, remove empty rows and
// then upsert supplied values. These writes use the bound SQL database through
// separate statements, so an error can leave earlier steps persisted. Paired
// arrays passed to unnest keep identifiers and values aligned during each bulk
// insert.

// GroupRateStore reads and mutates user/group multiplier and RPM override
// rows.
type GroupRateStore struct {
	sql sqlExecutor // bound database SQL operations
}

// NewUserGroupRateRepository binds the supplied SQL database for group
// override operations. The caller owns its lifetime; mutations issue separate
// statements through this database and do not create a transaction themselves.
func NewUserGroupRateRepository(sqlDB *sql.DB) *GroupRateStore {
	return &GroupRateStore{sql: sqlDB}
}

// GetByUserID returns this user's non-NULL billing rates indexed by group ID.
func (r *GroupRateStore) GetByUserID(ctx context.Context, userID int64) (map[int64]float64, error) {
	query := `SELECT group_id, rate_multiplier FROM user_group_rate_multipliers WHERE user_id = $1 AND rate_multiplier IS NOT NULL`
	rows, err := r.sql.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := make(map[int64]float64)
	for rows.Next() {
		var groupID int64
		var rate float64
		if err := rows.Scan(&groupID, &rate); err != nil {
			return nil, err
		}
		result[groupID] = rate
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// GetByUserIDs returns non-NULL billing rates for each distinct positive user
// ID. Invalid IDs are ignored, valid IDs without rates receive allocated empty
// maps, and query or row-decoding errors return an error.
func (r *GroupRateStore) GetByUserIDs(ctx context.Context, userIDs []int64) (map[int64]map[int64]float64, error) {
	result := make(map[int64]map[int64]float64, len(userIDs))
	if len(userIDs) == 0 {
		return result, nil
	}

	uniqueIDs := make([]int64, 0, len(userIDs))
	seen := make(map[int64]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if userID <= 0 {
			continue
		}
		if _, exists := seen[userID]; exists {
			continue
		}
		seen[userID] = struct{}{}
		uniqueIDs = append(uniqueIDs, userID)
		result[userID] = make(map[int64]float64)
	}
	if len(uniqueIDs) == 0 {
		return result, nil
	}

	rows, err := r.sql.QueryContext(ctx, `
		SELECT user_id, group_id, rate_multiplier
		FROM user_group_rate_multipliers
		WHERE user_id = ANY($1) AND rate_multiplier IS NOT NULL
	`, pq.Array(uniqueIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var userID int64
		var groupID int64
		var rate float64
		if err := rows.Scan(&userID, &groupID, &rate); err != nil {
			return nil, err
		}
		if _, ok := result[userID]; !ok {
			result[userID] = make(map[int64]float64)
		}
		result[userID][groupID] = rate
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// GetByGroupID returns both overrides with non-deleted user display data
// ordered by user ID.
func (r *GroupRateStore) GetByGroupID(ctx context.Context, groupID int64) ([]billing.UserGroupRateEntry, error) {
	query := `
		SELECT ugr.user_id, u.username, u.email, COALESCE(u.notes, ''), u.status, ugr.rate_multiplier, ugr.rpm_override
		FROM user_group_rate_multipliers ugr
		JOIN users u ON u.id = ugr.user_id AND u.deleted_at IS NULL
		WHERE ugr.group_id = $1
		ORDER BY ugr.user_id
	`
	rows, err := r.sql.QueryContext(ctx, query, groupID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var result []billing.UserGroupRateEntry
	for rows.Next() {
		var entry billing.UserGroupRateEntry
		var rate sql.NullFloat64
		var rpm sql.NullInt32
		if err := rows.Scan(&entry.UserID, &entry.UserName, &entry.UserEmail, &entry.UserNotes, &entry.UserStatus, &rate, &rpm); err != nil {
			return nil, err
		}
		if rate.Valid {
			v := rate.Float64
			entry.RateMultiplier = &v
		}
		if rpm.Valid {
			v := int(rpm.Int32)
			entry.RPMOverride = &v
		}
		result = append(result, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// GetByUserAndGroup returns the stored billing multiplier for the user/group
// pair. A missing row or SQL NULL returns nil with no error; callers select
// the group default for that absence.
func (r *GroupRateStore) GetByUserAndGroup(ctx context.Context, userID, groupID int64) (*float64, error) {
	query := `SELECT rate_multiplier FROM user_group_rate_multipliers WHERE user_id = $1 AND group_id = $2`
	var rate sql.NullFloat64
	err := postgresinfra.ScanSingleRow(ctx, r.sql, query, []any{userID, groupID}, &rate)
	// A missing user/group row represents an unset override, so return nil and
	// let the consumer select its effective default.
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !rate.Valid {
		return nil, nil
	}
	v := rate.Float64
	return &v, nil
}

// GetRPMOverrideByUserAndGroup returns the stored RPM override for this
// user/group pair. Missing rows and SQL NULL return nil; a stored zero returns
// a pointer so scheduler admission can distinguish disabled group limiting
// from inheritance.
func (r *GroupRateStore) GetRPMOverrideByUserAndGroup(ctx context.Context, userID, groupID int64) (*int, error) {
	query := `SELECT rpm_override FROM user_group_rate_multipliers WHERE user_id = $1 AND group_id = $2`
	var rpm sql.NullInt32
	err := postgresinfra.ScanSingleRow(ctx, r.sql, query, []any{userID, groupID}, &rpm)
	// A missing user/group row represents an unset override, so return nil and
	// let the consumer select its effective default.
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !rpm.Valid {
		return nil, nil
	}
	v := int(rpm.Int32)
	return &v, nil
}

// SyncUserGroupRates updates the supplied user rates while retaining RPM
// values. An empty map clears all rates; a nonempty map clears nil entries,
// upserts supplied amounts and leaves omitted groups unchanged. Rows with both
// values unset are removed after clearing.
//
// The clear, cleanup and upsert statements run separately on the bound
// database. Errors stop later statements, so earlier successful statements can
// remain persisted. Group IDs and rate arrays are assembled together for SQL
// unnest to keep each amount paired with its group.
func (r *GroupRateStore) SyncUserGroupRates(ctx context.Context, userID int64, rates map[int64]*float64) error {
	// Clearing every rate precedes empty-row cleanup; retaining RPM values keeps
	// admission overrides readable after the user's billing defaults are
	// restored.
	if len(rates) == 0 {
		if _, err := r.sql.ExecContext(ctx, `
			UPDATE user_group_rate_multipliers
			SET rate_multiplier = NULL, updated_at = NOW()
			WHERE user_id = $1
		`, userID); err != nil {
			return err
		}
		_, err := r.sql.ExecContext(ctx,
			`DELETE FROM user_group_rate_multipliers WHERE user_id = $1 AND rate_multiplier IS NULL AND rpm_override IS NULL`,
			userID)
		return err
	}

	var clearGroupIDs []int64
	upsertGroupIDs := make([]int64, 0, len(rates))
	upsertRates := make([]float64, 0, len(rates))
	for groupID, rate := range rates {
		if rate == nil {
			clearGroupIDs = append(clearGroupIDs, groupID)
		} else {
			upsertGroupIDs = append(upsertGroupIDs, groupID)
			upsertRates = append(upsertRates, *rate)
		}
	}

	// Clear named nil rates and then remove rows with no remaining RPM override;
	// omitted groups retain their stored values throughout this update.
	if len(clearGroupIDs) > 0 {
		if _, err := r.sql.ExecContext(ctx, `
			UPDATE user_group_rate_multipliers
			SET rate_multiplier = NULL, updated_at = NOW()
			WHERE user_id = $1 AND group_id = ANY($2)
		`, userID, pq.Array(clearGroupIDs)); err != nil {
			return err
		}
		if _, err := r.sql.ExecContext(ctx,
			`DELETE FROM user_group_rate_multipliers WHERE user_id = $1 AND group_id = ANY($2) AND rate_multiplier IS NULL AND rpm_override IS NULL`,
			userID, pq.Array(clearGroupIDs)); err != nil {
			return err
		}
	}

	// Upsert the paired group IDs and amounts after clearing; conflict updates
	// touch the rate column and timestamp while retaining existing RPM values.
	if len(upsertGroupIDs) > 0 {
		now := time.Now()
		_, err := r.sql.ExecContext(ctx, `
			INSERT INTO user_group_rate_multipliers (user_id, group_id, rate_multiplier, created_at, updated_at)
			SELECT
				$1::bigint,
				data.group_id,
				data.rate_multiplier,
				$2::timestamptz,
				$2::timestamptz
			FROM unnest($3::bigint[], $4::double precision[]) AS data(group_id, rate_multiplier)
			ON CONFLICT (user_id, group_id)
			DO UPDATE SET
				rate_multiplier = EXCLUDED.rate_multiplier,
				updated_at = EXCLUDED.updated_at
		`, userID, now, pq.Array(upsertGroupIDs), pq.Array(upsertRates))
		if err != nil {
			return err
		}
	}

	return nil
}

// SyncGroupRateMultipliers replaces the group's user billing rates while
// retaining RPM values. Omitted users have their rates cleared, empty override
// rows are removed, and supplied user amounts are upserted with one timestamp.
// An empty entry list therefore clears every rate.
//
// These statements use the bound database separately. A later failure can
// leave earlier clearing or cleanup persisted, and callers cannot infer an
// all-or-nothing replacement from the error. Parallel SQL arrays retain the
// supplied user and rate pairing.
func (r *GroupRateStore) SyncGroupRateMultipliers(ctx context.Context, groupID int64, entries []billing.GroupRateMultiplierInput) error {
	keepUserIDs := make([]int64, 0, len(entries))
	for _, e := range entries {
		keepUserIDs = append(keepUserIDs, e.UserID)
	}

	// Clear omitted user rates before cleanup and upsert; rows with configured
	// RPM values remain available to scheduler admission.
	if len(keepUserIDs) == 0 {
		if _, err := r.sql.ExecContext(ctx, `
			UPDATE user_group_rate_multipliers
			SET rate_multiplier = NULL, updated_at = NOW()
			WHERE group_id = $1
		`, groupID); err != nil {
			return err
		}
	} else {
		if _, err := r.sql.ExecContext(ctx, `
			UPDATE user_group_rate_multipliers
			SET rate_multiplier = NULL, updated_at = NOW()
			WHERE group_id = $1 AND user_id <> ALL($2)
		`, groupID, pq.Array(keepUserIDs)); err != nil {
			return err
		}
	}

	// After clearing, remove rows with neither override set; a row carrying the
	// other setting remains to serve that consumer.
	if _, err := r.sql.ExecContext(ctx, `
		DELETE FROM user_group_rate_multipliers
		WHERE group_id = $1 AND rate_multiplier IS NULL AND rpm_override IS NULL
	`, groupID); err != nil {
		return err
	}

	if len(entries) == 0 {
		return nil
	}

	userIDs := make([]int64, len(entries))
	rates := make([]float64, len(entries))
	for i, e := range entries {
		userIDs[i] = e.UserID
		rates[i] = e.RateMultiplier
	}
	now := time.Now()
	_, err := r.sql.ExecContext(ctx, `
		INSERT INTO user_group_rate_multipliers (user_id, group_id, rate_multiplier, created_at, updated_at)
		SELECT data.user_id, $1::bigint, data.rate_multiplier, $2::timestamptz, $2::timestamptz
		FROM unnest($3::bigint[], $4::double precision[]) AS data(user_id, rate_multiplier)
		ON CONFLICT (user_id, group_id)
		DO UPDATE SET rate_multiplier = EXCLUDED.rate_multiplier, updated_at = EXCLUDED.updated_at
	`, groupID, now, pq.Array(userIDs), pq.Array(rates))
	return err
}

// SyncGroupRPMOverrides replaces the group's user RPM values while retaining
// billing rates. Omitted users and supplied nil values are cleared, empty rows
// are removed, and remaining supplied values are upserted. Present zero values
// remain stored overrides.
//
// The statements execute separately on the bound database, so a failure can
// retain earlier successful changes. RPM values are converted to int32 for SQL
// integer arrays; callers must keep values representable in that type, since
// this method does not check the upper bound.
func (r *GroupRateStore) SyncGroupRPMOverrides(ctx context.Context, groupID int64, entries []billing.GroupRPMOverrideInput) error {
	keepUserIDs := make([]int64, 0, len(entries))
	var clearUserIDs []int64
	upsertUserIDs := make([]int64, 0, len(entries))
	upsertValues := make([]int32, 0, len(entries))
	for _, e := range entries {
		keepUserIDs = append(keepUserIDs, e.UserID)
		if e.RPMOverride == nil {
			clearUserIDs = append(clearUserIDs, e.UserID)
		} else {
			upsertUserIDs = append(upsertUserIDs, e.UserID)
			// SQL stores RPM as an integer; this narrowing requires representable
			// caller values, since larger Go ints can wrap during conversion.
			upsertValues = append(upsertValues, int32(*e.RPMOverride))
		}
	}

	// Clear omitted users' RPM values before processing supplied nil values,
	// leaving billing multipliers available throughout the replacement sequence.
	if len(keepUserIDs) == 0 {
		if _, err := r.sql.ExecContext(ctx, `
			UPDATE user_group_rate_multipliers
			SET rpm_override = NULL, updated_at = NOW()
			WHERE group_id = $1
		`, groupID); err != nil {
			return err
		}
	} else {
		if _, err := r.sql.ExecContext(ctx, `
			UPDATE user_group_rate_multipliers
			SET rpm_override = NULL, updated_at = NOW()
			WHERE group_id = $1 AND user_id <> ALL($2)
		`, groupID, pq.Array(keepUserIDs)); err != nil {
			return err
		}
	}

	// Supplied nil values request clearing even though those users are in the
	// keep list; their billing rates retain independent meaning.
	if len(clearUserIDs) > 0 {
		if _, err := r.sql.ExecContext(ctx, `
			UPDATE user_group_rate_multipliers
			SET rpm_override = NULL, updated_at = NOW()
			WHERE group_id = $1 AND user_id = ANY($2)
		`, groupID, pq.Array(clearUserIDs)); err != nil {
			return err
		}
	}

	// After clearing, remove rows with neither override set; a row carrying the
	// other setting remains to serve that consumer.
	if _, err := r.sql.ExecContext(ctx, `
		DELETE FROM user_group_rate_multipliers
		WHERE group_id = $1 AND rate_multiplier IS NULL AND rpm_override IS NULL
	`, groupID); err != nil {
		return err
	}

	// Upsert the remaining user/RPM pairs after clearing and cleanup; conflict
	// updates retain existing billing rates and refresh the stored timestamp.
	if len(upsertUserIDs) > 0 {
		now := time.Now()
		_, err := r.sql.ExecContext(ctx, `
			INSERT INTO user_group_rate_multipliers (user_id, group_id, rpm_override, created_at, updated_at)
			SELECT data.user_id, $1::bigint, data.rpm_override, $2::timestamptz, $2::timestamptz
			FROM unnest($3::bigint[], $4::integer[]) AS data(user_id, rpm_override)
			ON CONFLICT (user_id, group_id)
			DO UPDATE SET rpm_override = EXCLUDED.rpm_override, updated_at = EXCLUDED.updated_at
		`, groupID, now, pq.Array(upsertUserIDs), pq.Array(upsertValues))
		if err != nil {
			return err
		}
	}

	return nil
}

// ClearGroupRPMOverrides clears the group RPM column and removes rows whose
// rate is also NULL. Existing non-NULL billing rates retain their rows and
// values, while later reads treat a cleared RPM as an inherited group setting.
//
// Clearing and cleanup are separate statements on the bound database. Cleanup
// failure returns an error after the first statement may already have
// persisted, so callers should reread state when they need the result of a
// failed operation.
func (r *GroupRateStore) ClearGroupRPMOverrides(ctx context.Context, groupID int64) error {
	// Clear RPM first so inherited group limits become readable; the following
	// cleanup deletes only rows that also lack a billing multiplier.
	if _, err := r.sql.ExecContext(ctx, `
		UPDATE user_group_rate_multipliers
		SET rpm_override = NULL, updated_at = NOW()
		WHERE group_id = $1
	`, groupID); err != nil {
		return err
	}
	_, err := r.sql.ExecContext(ctx, `
		DELETE FROM user_group_rate_multipliers
		WHERE group_id = $1 AND rate_multiplier IS NULL AND rpm_override IS NULL
	`, groupID)
	return err
}

// DeleteByGroupID deletes every billing and RPM override row for the group.
func (r *GroupRateStore) DeleteByGroupID(ctx context.Context, groupID int64) error {
	_, err := r.sql.ExecContext(ctx, `DELETE FROM user_group_rate_multipliers WHERE group_id = $1`, groupID)
	return err
}

// DeleteByUserID deletes every billing and RPM override row for the user.
func (r *GroupRateStore) DeleteByUserID(ctx context.Context, userID int64) error {
	_, err := r.sql.ExecContext(ctx, `DELETE FROM user_group_rate_multipliers WHERE user_id = $1`, userID)
	return err
}
