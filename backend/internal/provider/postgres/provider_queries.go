package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/sqljson"
	"github.com/lib/pq"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	dbpredicate "github.com/TokenFlux/TokenRouter/ent/predicate"
	dbprovider "github.com/TokenFlux/TokenRouter/ent/provider"
	dbprovidergroup "github.com/TokenFlux/TokenRouter/ent/providergroup"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	acctcore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// provider_queries.go selects provider records for administration, scheduling,
// refresh scans, and model availability. Administrator status filters and
// gateway scheduling eligibility use different views of provider state.
// Management can select active, rate-limited, temporarily paused, or manually
// unschedulable records, while gateway candidates also combine expiry and
// overload windows with persistent scheduling configuration.
// Model-availability checks intentionally use persistent eligibility while
// those transient windows may still prevent execution.
//
// Group queries retain global priority and provider-ID order as they hydrate
// associations. Refresh scanning first selects an ordered ID page and derives
// its cursor from that selection, so deleted or changed rows during hydration
// do not erase scan progress. Raw capacity reads return the smaller field set
// needed for group totals, with an Ent-based path when no SQL executor is
// configured.

// ProviderGroupQueryOptions selects status, platform, and scheduling
// eligibility for memberships.
type ProviderGroupQueryOptions struct {
	// status restricts the stored provider status when nonempty; an empty value
	// permits every status while the remaining membership filters are still applied.
	status string
	// schedulable enables the scheduling switch and transient predicates together.
	// ignoreTransientState suppresses window predicates while schedulable is
	// true, allowing availability readers to select persistently enabled
	// providers during cooldown.
	schedulable          bool // require provider scheduling switch
	ignoreTransientState bool // omit transient eligibility windows
	// platforms limits the selected platform values when nonempty; an empty slice
	// leaves every platform eligible for the remaining membership query filters.
	platforms []string
}

// GetByCRSAccountID returns the root provider whose crs_account_id equals the
// supplied source ID. Empty input, no row, or no hydrated result returns nil
// without error; multiple matches remain an Ent Only error, and shadows are
// excluded.
func (r *ProviderStore) GetByCRSAccountID(ctx context.Context, crsProviderID string) (*acctcore.Record, error) {
	if crsProviderID == "" {
		return nil, nil
	}

	// Match source identity on roots so CRS cannot replace the credentials, type,
	// or proxy inherited by a shadow from its parent through synchronization.
	m, err := r.client.Provider.Query().
		Where(dbprovider.ParentProviderIDIsNil()).
		Where(func(s *entsql.Selector) {
			s.Where(sqljson.ValueEQ(dbprovider.FieldExtra, crsProviderID, sqljson.Path("crs_account_id")))
		}).
		Only(ctx)
	if err != nil {
		// An absent source ID means CRS has no existing root to update; its caller
		// can then decide whether selection permits creating that imported provider.
		if dbent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	providers, err := r.RecordsFromEntities(ctx, []*dbent.Provider{m})
	if err != nil {
		return nil, err
	}
	if len(providers) == 0 {
		return nil, nil
	}
	return &providers[0], nil
}

// FindByExtraField loads nondeleted providers matching one Extra key,
// including supported integer/string equivalents. Results are hydrated after
// matching, and other values use the JSON-path equality operation directly.
//
// String inputs also try base-ten int64 parsing; int and int64 values match
// their decimal string form as well. An integral json.Number matches its
// number and original text, while a nonintegral or out-of-range one uses text
// equality. Keeping those alternatives prevents older string identities from
// disappearing from a lookup whose caller supplies a numeric ID.
func (r *ProviderStore) FindByExtraField(ctx context.Context, key string, value any) ([]acctcore.Record, error) {
	providers, err := r.client.Provider.Query().
		Where(
			dbprovider.DeletedAtIsNil(),
			func(s *entsql.Selector) {
				path := sqljson.Path(key)
				switch v := value.(type) {
				case string:
					preds := []*entsql.Predicate{sqljson.ValueEQ(dbprovider.FieldExtra, v, path)}
					if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
						preds = append(preds, sqljson.ValueEQ(dbprovider.FieldExtra, parsed, path))
					}
					if len(preds) == 1 {
						s.Where(preds[0])
					} else {
						s.Where(entsql.Or(preds...))
					}
				case int:
					s.Where(entsql.Or(
						sqljson.ValueEQ(dbprovider.FieldExtra, v, path),
						sqljson.ValueEQ(dbprovider.FieldExtra, strconv.Itoa(v), path),
					))
				case int64:
					s.Where(entsql.Or(
						sqljson.ValueEQ(dbprovider.FieldExtra, v, path),
						sqljson.ValueEQ(dbprovider.FieldExtra, strconv.FormatInt(v, 10), path),
					))
				// Numeric source identities may have been stored as strings; retain the
				// original text when integer conversion cannot provide a comparable
				// JSON number.
				case json.Number:
					if parsed, err := v.Int64(); err == nil {
						s.Where(entsql.Or(
							sqljson.ValueEQ(dbprovider.FieldExtra, parsed, path),
							sqljson.ValueEQ(dbprovider.FieldExtra, v.String(), path),
						))
					} else {
						s.Where(sqljson.ValueEQ(dbprovider.FieldExtra, v.String(), path))
					}
				default:
					s.Where(sqljson.ValueEQ(dbprovider.FieldExtra, value, path))
				}
			},
		).
		All(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, acctcore.ErrProviderNotFound, nil)
	}

	return r.RecordsFromEntities(ctx, providers)
}

// ListCRSAccountIDs maps nonempty root-provider CRS source IDs to stored
// numeric provider IDs. Raw SQL excludes retained deletions and shadows;
// duplicate source IDs overwrite an earlier map entry in unspecified row
// order, and row failures are returned.
func (r *ProviderStore) ListCRSAccountIDs(ctx context.Context) (map[string]int64, error) {
	// Index root source identities for CRS preview and synchronization; inherited
	// shadow configuration stays attached to its parent instead of becoming
	// an import target.
	rows, err := r.sql.QueryContext(ctx, `
		SELECT id, extra->>'crs_account_id'
		FROM providers
		WHERE deleted_at IS NULL
			AND parent_provider_id IS NULL
			AND extra->>'crs_account_id' IS NOT NULL
			AND extra->>'crs_account_id' != ''
	`)
	if err != nil {
		return nil, err
	}
	// Release the CRS index query before preview handles source entries, keeping
	// the shared executor available for later reads and selected import writes.
	defer func() { _ = rows.Close() }()

	result := make(map[string]int64)
	for rows.Next() {
		var id int64
		var crsID string
		if err := rows.Scan(&id, &crsID); err != nil {
			return nil, err
		}
		result[crsID] = id
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// ListWithFilters returns the administrator's ordered page with total-count
// metadata and hydrated associations. Count operates on a query clone so Ent
// soft-delete traversal does not alter the base filter before the list read;
// this method opens no snapshot transaction around the count and page reads.
func (r *ProviderStore) ListWithFilters(ctx context.Context, params pagination.PaginationParams, platform, providerType, status, search string, groupID int64, privacyMode string) ([]acctcore.Record, *pagination.PaginationResult, error) {
	q := r.ProviderListFilteredQuery(platform, providerType, status, search, groupID, privacyMode)
	// Count a clone so interceptors such as SoftDeleteMixin append predicates
	// to that clone.
	// The list query then starts from the same unmodified filter set.
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	providersQuery := q.
		Offset(params.Offset()).
		Limit(params.Limit())
	for _, order := range ProviderListOrder(params) {
		providersQuery = providersQuery.Order(order)
	}

	providers, err := providersQuery.All(ctx)
	if err != nil {
		return nil, nil, err
	}

	outProviders, err := r.RecordsFromEntities(ctx, providers)
	if err != nil {
		return nil, nil, err
	}
	return outProviders, pagination.ResultFromTotal(int64(total), params), nil
}

// ListAllWithFilters returns the complete hydrated administrator-filter
// result for optional scheduler scoring. The same status, group, and privacy
// filters are used as the paginated list; this path applies no page limit or
// explicit ordering.
func (r *ProviderStore) ListAllWithFilters(ctx context.Context, platform, providerType, status, search string, groupID int64, privacyMode string) ([]acctcore.Record, error) {
	providers, err := r.ProviderListFilteredQuery(platform, providerType, status, search, groupID, privacyMode).All(ctx)
	if err != nil {
		return nil, err
	}
	return r.RecordsFromEntities(ctx, providers)
}

// ListOpsProvidersForStats returns selected runtime fields and hydrated
// associations for live Ops statistics. A missing store/client yields an
// allocated empty slice; platform is trimmed, a positive group filter selects
// memberships, and ID order is ascending.
func (r *ProviderStore) ListOpsProvidersForStats(ctx context.Context, platformFilter string, groupIDFilter *int64) ([]acctcore.Record, error) {
	if r == nil || r.client == nil {
		return []acctcore.Record{}, nil
	}

	q := r.client.Provider.Query()
	if platformFilter = strings.TrimSpace(platformFilter); platformFilter != "" {
		q = q.Where(dbprovider.PlatformEQ(platformFilter))
	}
	if groupIDFilter != nil && *groupIDFilter > 0 {
		q = q.Where(dbprovider.HasProviderGroupsWith(dbprovidergroup.GroupIDEQ(*groupIDFilter)))
	}

	providers, err := q.
		Select(
			dbprovider.FieldID,
			dbprovider.FieldName,
			dbprovider.FieldPlatform,
			dbprovider.FieldConcurrency,
			dbprovider.FieldLoadFactor,
			dbprovider.FieldStatus,
			dbprovider.FieldErrorMessage,
			dbprovider.FieldSchedulable,
			dbprovider.FieldRateLimitResetAt,
			dbprovider.FieldOverloadUntil,
			dbprovider.FieldTempUnschedulableUntil,
		).
		Order(dbent.Asc(dbprovider.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return r.RecordsFromEntities(ctx, providers)
}

// ListByGroup returns active providers linked to groupID in global priority
// and ID order. It leaves schedulable, expiry and transient health filters
// disabled; the consumer decides whether those active records are eligible
// for its operation.
func (r *ProviderStore) ListByGroup(ctx context.Context, groupID int64) ([]acctcore.Record, error) {
	providers, err := r.QueryProvidersByGroup(ctx, groupID, ProviderGroupQueryOptions{
		status: acctcore.StatusActive,
	})
	if err != nil {
		return nil, err
	}
	return providers, nil
}

// ListActive returns active providers ordered by their stored global
// priority. It does not filter the scheduling switch or temporary health
// windows, so consumers apply any request eligibility required by their use.
func (r *ProviderStore) ListActive(ctx context.Context) ([]acctcore.Record, error) {
	providers, err := r.client.Provider.Query().
		Where(dbprovider.StatusEQ(acctcore.StatusActive)).
		Order(dbent.Asc(dbprovider.FieldPriority)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return r.RecordsFromEntities(ctx, providers)
}

// ListOAuthRefreshCandidatePage selects a strictly ascending ID page for the
// requested maintenance platforms and then hydrates matching records.
// Platforms must be nonempty and limit must be one through one thousand;
// optional predicates select active status, setup-token support,
// refresh-token presence and retry cooldown handling.
//
// The cursor and HasMore derive from the selected IDs, including rows that
// disappear before hydration. That keeps later pages advancing even when a
// concurrent deletion reduces the returned provider slice. schedulable=true
// is required, Qoder cosy joins OAuth candidates, and nullable cooldown
// expressions retain healthy rows rather than treating SQL NULL as a
// rejection.
func (r *ProviderStore) ListOAuthRefreshCandidatePage(ctx context.Context, options acctcore.OAuthRefreshPageOptions) (*acctcore.OAuthRefreshCandidatePage, error) {
	if r.sql == nil {
		return nil, errors.New("provider repository SQL executor not configured")
	}
	if len(options.Platforms) == 0 {
		return nil, errors.New("oauth refresh candidate platforms cannot be empty")
	}
	// Bound each maintenance page before its later full-record and
	// association hydration.
	if options.Limit <= 0 || options.Limit > 1000 {
		return nil, errors.New("oauth refresh candidate page limit must be between 1 and 1000")
	}

	// IS NOT TRUE accepts both NULL and FALSE for the retry-cooldown condition.
	// NOT (a AND b) would exclude rows where that expression is NULL,
	// including healthy providers with no temporary pause time; skipped token refreshes
	// could then leave expired access tokens in use.
	// The stored scheduling switch excludes manually disabled rows even when
	// status filtering is omitted; the optional retry predicate addresses
	// temporary pauses.
	query := `
		SELECT id
		FROM providers
		WHERE deleted_at IS NULL
			AND schedulable = TRUE
			AND platform = ANY($1)
			AND id > $2`
	if options.ActiveOnly {
		query += `
			AND status = 'active'`
	}
	if options.IncludeSetupToken {
		query += `
			AND (type IN ('oauth', 'setup-token') OR (platform = 'qoder' AND type = 'cosy'))`
	} else {
		query += `
			AND (type = 'oauth' OR (platform = 'qoder' AND type = 'cosy'))`
	}
	if options.RequireRefreshToken {
		query += `
			AND credentials ? 'refresh_token'
			AND btrim(credentials->>'refresh_token') <> ''`
	}
	if options.ExcludeRetryCooldown {
		query += `
			AND (
				temp_unschedulable_until > NOW()
				AND temp_unschedulable_reason LIKE 'token refresh retry exhausted:%'
			) IS NOT TRUE`
	}
	query += `
		ORDER BY id ASC
		LIMIT $3`

	rows, err := r.sql.QueryContext(ctx, query, pq.Array(options.Platforms), options.AfterID, options.Limit)
	if err != nil {
		return nil, err
	}
	// Keep cleanup registered for early scan failures; normal ID iteration finishes
	// before hydration, and cursor reconstruction retains the complete selected page.
	defer func() { _ = rows.Close() }()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return &acctcore.OAuthRefreshCandidatePage{Providers: []acctcore.Record{}}, nil
	}

	providers, err := r.GetByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	providersByID := make(map[int64]*acctcore.Record, len(providers))
	for _, provider := range providers {
		if provider != nil {
			providersByID[provider.ID] = provider
		}
	}
	out := make([]acctcore.Record, 0, len(providers))
	for _, id := range ids {
		if provider := providersByID[id]; provider != nil {
			out = append(out, *provider)
		}
	}
	page := &acctcore.OAuthRefreshCandidatePage{
		Providers: out,
		HasMore:   len(ids) == options.Limit,
	}
	if len(ids) > 0 {
		page.NextAfterID = ids[len(ids)-1]
	}
	return page, nil
}

// ListByPlatform returns active providers for one platform in global priority
// order. Scheduling switches and transient windows remain available for the
// caller to evaluate separately from this active-status selection.
func (r *ProviderStore) ListByPlatform(ctx context.Context, platform string) ([]acctcore.Record, error) {
	providers, err := r.client.Provider.Query().
		Where(
			dbprovider.PlatformEQ(platform),
			dbprovider.StatusEQ(acctcore.StatusActive),
		).
		Order(dbent.Asc(dbprovider.FieldPriority)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return r.RecordsFromEntities(ctx, providers)
}

// ListSchedulable returns hydrated providers satisfying the shared persistent
// and transient eligibility query. Current wall time supplies expiry,
// rate-limit and overload comparisons, while temporary-pause comparison uses
// database NOW; priority determines their stored selection order.
func (r *ProviderStore) ListSchedulable(ctx context.Context) ([]acctcore.Record, error) {
	providers, err := r.SchedulableProvidersQuery(time.Now()).All(ctx)
	if err != nil {
		return nil, err
	}
	return r.RecordsFromEntities(ctx, providers)
}

// ListSchedulableByGroupID returns active, schedulable group members after
// transient health and expiry filtering. Group querying hydrates associations
// and orders by global provider priority then ID, rather than membership
// insertion order.
func (r *ProviderStore) ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]acctcore.Record, error) {
	return r.QueryProvidersByGroup(ctx, groupID, ProviderGroupQueryOptions{
		status:      acctcore.StatusActive,
		schedulable: true,
	})
}

// ListSchedulableCapacityByGroupIDs returns the smaller provider state used
// for group capacity totals. Positive unique group IDs are selected, and
// empty effective input returns an allocated empty slice; each result still
// belongs to a particular group/provider pair.
//
// A configured SQL executor reads all selected groups with persistent and
// transient eligibility filters and ordered pair IDs. Without that executor,
// the method queries each group through the Ent path and copies supported
// Extra values into the same result shape. Stored session times remain part
// of those observations so capacity consumers can apply their
// platform-specific rules.
func (r *ProviderStore) ListSchedulableCapacityByGroupIDs(ctx context.Context, groupIDs []int64) ([]acctcore.GroupProviderCapacityRow, error) {
	groupIDs = UniquePositiveInt64s(groupIDs)
	if len(groupIDs) == 0 {
		return []acctcore.GroupProviderCapacityRow{}, nil
	}
	// Without raw SQL support, reuse the per-group eligibility reader so callers
	// still receive capacity observations with the same pair identity
	// and field meaning.
	if r.sql == nil {
		rows := make([]acctcore.GroupProviderCapacityRow, 0)
		for _, groupID := range groupIDs {
			providers, err := r.ListSchedulableByGroupID(ctx, groupID)
			if err != nil {
				return nil, err
			}
			for i := range providers {
				acc := &providers[i]
				rows = append(rows, acctcore.GroupProviderCapacityRow{
					GroupID:             groupID,
					ProviderID:          acc.ID,
					Platform:            acc.Platform,
					Concurrency:         acc.Concurrency,
					Extra:               acctcore.CloneValues(acc.Extra),
					SessionWindowStart:  acc.SessionWindowStart,
					SessionWindowEnd:    acc.SessionWindowEnd,
					SessionWindowStatus: acc.SessionWindowStatus,
				})
			}
		}
		return rows, nil
	}

	rows, err := r.sql.QueryContext(ctx, `
		SELECT
			ag.group_id,
			a.id AS provider_id,
			a.platform,
			a.concurrency,
			COALESCE(a.extra, '{}'::jsonb)::text AS extra,
			a.session_window_start,
			a.session_window_end,
			COALESCE(a.session_window_status, '') AS session_window_status
		FROM provider_groups ag
		JOIN providers a ON a.id = ag.provider_id
		WHERE ag.group_id = ANY($1)
			AND a.deleted_at IS NULL
			AND a.status = $2
			AND a.schedulable = TRUE
			AND (a.temp_unschedulable_until IS NULL OR a.temp_unschedulable_until <= $3)
			AND (a.expires_at IS NULL OR a.expires_at > $3 OR a.auto_pause_on_expired = FALSE)
			AND (a.overload_until IS NULL OR a.overload_until <= $3)
			AND (a.rate_limit_reset_at IS NULL OR a.rate_limit_reset_at <= $3)
		ORDER BY ag.group_id ASC, a.priority ASC, a.id ASC
	`, pq.Array(groupIDs), acctcore.StatusActive, time.Now())
	if err != nil {
		return nil, err
	}
	// Consume the capacity result before returning observations, releasing the
	// borrowed query connection while consumers calculate group totals
	// from returned values.
	defer func() { _ = rows.Close() }()

	out := make([]acctcore.GroupProviderCapacityRow, 0)
	for rows.Next() {
		var row acctcore.GroupProviderCapacityRow
		var extraRaw string
		if err := rows.Scan(
			&row.GroupID,
			&row.ProviderID,
			&row.Platform,
			&row.Concurrency,
			&extraRaw,
			&row.SessionWindowStart,
			&row.SessionWindowEnd,
			&row.SessionWindowStatus,
		); err != nil {
			return nil, err
		}
		if extraRaw != "" && extraRaw != "null" {
			var extra map[string]any
			if err := json.Unmarshal([]byte(extraRaw), &extra); err != nil {
				return nil, err
			}
			row.Extra = extra
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ListSchedulableByPlatform returns hydrated eligible providers from the
// supplied platform in global priority order. Persistent status and switch
// combine with pause, expiry, overload and rate-limit predicates; equal
// priorities have no added ID tie-breaker here.
func (r *ProviderStore) ListSchedulableByPlatform(ctx context.Context, platform string) ([]acctcore.Record, error) {
	now := time.Now()
	providers, err := r.client.Provider.Query().
		Where(
			dbprovider.PlatformEQ(platform),
			dbprovider.StatusEQ(acctcore.StatusActive),
			dbprovider.SchedulableEQ(true),
			TempUnschedulablePredicate(),
			NotExpiredPredicate(now),
			dbprovider.Or(dbprovider.OverloadUntilIsNil(), dbprovider.OverloadUntilLTE(now)),
			dbprovider.Or(dbprovider.RateLimitResetAtIsNil(), dbprovider.RateLimitResetAtLTE(now)),
		).
		Order(dbent.Asc(dbprovider.FieldPriority)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return r.RecordsFromEntities(ctx, providers)
}

// ListSchedulableByGroupIDAndPlatform returns eligible group members for one
// platform through the shared membership query. Associations are hydrated
// after filtering, and global provider priority then ID determine order.
func (r *ProviderStore) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]acctcore.Record, error) {
	return r.QueryProvidersByGroup(ctx, groupID, ProviderGroupQueryOptions{
		status:      acctcore.StatusActive,
		schedulable: true,
		platforms:   []string{platform},
	})
}

// ListSchedulableByPlatforms returns hydrated eligible records from the
// selected platforms in global priority order. Empty platform input returns
// nil without querying; the shared health filters combine persistent status
// and switch with transient and expiry windows.
func (r *ProviderStore) ListSchedulableByPlatforms(ctx context.Context, platforms []string) ([]acctcore.Record, error) {
	if len(platforms) == 0 {
		return nil, nil
	}
	now := time.Now()
	providers, err := r.client.Provider.Query().
		Where(
			dbprovider.PlatformIn(platforms...),
			dbprovider.StatusEQ(acctcore.StatusActive),
			dbprovider.SchedulableEQ(true),
			TempUnschedulablePredicate(),
			NotExpiredPredicate(now),
			dbprovider.Or(dbprovider.OverloadUntilIsNil(), dbprovider.OverloadUntilLTE(now)),
			dbprovider.Or(dbprovider.RateLimitResetAtIsNil(), dbprovider.RateLimitResetAtLTE(now)),
		).
		Order(dbent.Asc(dbprovider.FieldPriority)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return r.RecordsFromEntities(ctx, providers)
}

// ListSchedulableByGroupIDAndPlatforms returns eligible selected-platform
// group members in global priority and ID order. Empty platforms return nil
// without querying; the shared membership path applies health and expiry
// predicates before hydration.
func (r *ProviderStore) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, groupID int64, platforms []string) ([]acctcore.Record, error) {
	if len(platforms) == 0 {
		return nil, nil
	}
	return r.QueryProvidersByGroup(ctx, groupID, ProviderGroupQueryOptions{
		status:      acctcore.StatusActive,
		schedulable: true,
		platforms:   platforms,
	})
}

// ListSchedulableUngroupedByPlatform returns eligible providers with no
// memberships for the supplied platform. Persistent status and switch combine
// with transient and expiry predicates, and stored global priority determines
// the returned order.
func (r *ProviderStore) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]acctcore.Record, error) {
	now := time.Now()
	providers, err := r.client.Provider.Query().
		Where(
			dbprovider.PlatformEQ(platform),
			dbprovider.StatusEQ(acctcore.StatusActive),
			dbprovider.SchedulableEQ(true),
			dbprovider.Not(dbprovider.HasProviderGroups()),
			TempUnschedulablePredicate(),
			NotExpiredPredicate(now),
			dbprovider.Or(dbprovider.OverloadUntilIsNil(), dbprovider.OverloadUntilLTE(now)),
			dbprovider.Or(dbprovider.RateLimitResetAtIsNil(), dbprovider.RateLimitResetAtLTE(now)),
		).
		Order(dbent.Asc(dbprovider.FieldPriority)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return r.RecordsFromEntities(ctx, providers)
}

// ListSchedulableUngroupedByPlatforms returns eligible providers from the
// selected platforms with no group memberships. Empty platforms return nil
// without a query; otherwise persistent and transient eligibility are applied
// before global priority ordering and hydration.
func (r *ProviderStore) ListSchedulableUngroupedByPlatforms(ctx context.Context, platforms []string) ([]acctcore.Record, error) {
	if len(platforms) == 0 {
		return nil, nil
	}
	now := time.Now()
	providers, err := r.client.Provider.Query().
		Where(
			dbprovider.PlatformIn(platforms...),
			dbprovider.StatusEQ(acctcore.StatusActive),
			dbprovider.SchedulableEQ(true),
			dbprovider.Not(dbprovider.HasProviderGroups()),
			TempUnschedulablePredicate(),
			NotExpiredPredicate(now),
			dbprovider.Or(dbprovider.OverloadUntilIsNil(), dbprovider.OverloadUntilLTE(now)),
			dbprovider.Or(dbprovider.RateLimitResetAtIsNil(), dbprovider.RateLimitResetAtLTE(now)),
		).
		Order(dbent.Asc(dbprovider.FieldPriority)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return r.RecordsFromEntities(ctx, providers)
}

// ListModelAvailabilityCandidates returns records whose persistent active
// status and scheduling switch permit availability inspection. Empty
// platforms produce an allocated empty slice, and transient pause, expiry,
// overload and rate-limit windows are intentionally omitted.
//
// A nonnil groupID selects that group's members through QueryProvidersByGroup
// and makes includeGrouped irrelevant. With no group, includeGrouped
// determines whether providers with memberships remain in the platform query.
// These are model-inspection candidates rather than execution eligibility,
// allowing the consumer to describe configured availability while requests
// still enforce current health windows.
func (r *ProviderStore) ListModelAvailabilityCandidates(
	ctx context.Context,
	groupID *int64,
	platforms []string,
	includeGrouped bool,
) ([]acctcore.Record, error) {
	if len(platforms) == 0 {
		return []acctcore.Record{}, nil
	}
	if groupID != nil {
		return r.QueryProvidersByGroup(ctx, *groupID, ProviderGroupQueryOptions{
			status:               acctcore.StatusActive,
			schedulable:          true,
			ignoreTransientState: true,
			platforms:            platforms,
		})
	}

	preds := []dbpredicate.Provider{
		dbprovider.StatusEQ(acctcore.StatusActive),
		dbprovider.SchedulableEQ(true),
		dbprovider.PlatformIn(platforms...),
	}
	if !includeGrouped {
		preds = append(preds, dbprovider.Not(dbprovider.HasProviderGroups()))
	}
	providers, err := r.client.Provider.Query().
		Where(preds...).
		Order(dbent.Asc(dbprovider.FieldPriority)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return r.RecordsFromEntities(ctx, providers)
}

// ListShadowsByParent returns Spark-dimension rows attached to parentID using
// the store's ordinary Ent query. Normal soft-delete filtering hides retained
// deletions; these minimally converted records omit hydration and can share
// scalar pointers with their Ent rows.
func (r *ProviderStore) ListShadowsByParent(ctx context.Context, parentID int64) ([]*acctcore.Record, error) {
	rows, err := r.client.Provider.Query().
		Where(dbprovider.ParentProviderIDEQ(parentID), dbprovider.QuotaDimensionEQ(dbprovider.QuotaDimensionSpark)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*acctcore.Record, 0, len(rows))
	for _, m := range rows {
		out = append(out, r.recordFromEntity(m))
	}
	return out, nil
}

// QueryProvidersByGroup returns hydrated membership records in global
// provider priority and ID order. Status and platform restrictions are
// optional, while schedulable enables the scheduling switch and, unless
// suppressed, current transient and expiry checks.
//
// The membership query explicitly filters deleted providers and preloads
// their edges. Results are indexed by provider ID before assembling the
// ordered row list, so missing edges are skipped and repeated links cannot
// duplicate a provider. Hydration retains that order while adding current
// group and proxy details, leaving platform-specific request gates to their
// consumer.
func (r *ProviderStore) QueryProvidersByGroup(ctx context.Context, groupID int64, opts ProviderGroupQueryOptions) ([]acctcore.Record, error) {
	q := r.client.ProviderGroup.Query().
		Where(dbprovidergroup.GroupIDEQ(groupID))

	preds := make([]dbpredicate.Provider, 0, 6)
	preds = append(preds, dbprovider.DeletedAtIsNil())
	if opts.status != "" {
		preds = append(preds, dbprovider.StatusEQ(opts.status))
	}
	if len(opts.platforms) > 0 {
		preds = append(preds, dbprovider.PlatformIn(opts.platforms...))
	}
	if opts.schedulable {
		preds = append(preds, dbprovider.SchedulableEQ(true))
		if !opts.ignoreTransientState {
			now := time.Now()
			preds = append(preds,
				TempUnschedulablePredicate(),
				NotExpiredPredicate(now),
				dbprovider.Or(dbprovider.OverloadUntilIsNil(), dbprovider.OverloadUntilLTE(now)),
				dbprovider.Or(dbprovider.RateLimitResetAtIsNil(), dbprovider.RateLimitResetAtLTE(now)),
			)
		}
	}

	if len(preds) > 0 {
		q = q.Where(dbprovidergroup.HasProviderWith(preds...))
	}

	groups, err := q.
		Order(
			dbprovidergroup.ByProviderField(dbprovider.FieldPriority),
			dbprovidergroup.ByProviderID(),
		).
		WithProvider().
		All(ctx)
	if err != nil {
		return nil, err
	}

	// Record each provider once in membership-query order before hydration, keeping
	// priority/ID ordering stable when duplicate links or missing edges
	// reduce the row set.
	orderedIDs := make([]int64, 0, len(groups))
	providerMap := make(map[int64]*dbent.Provider, len(groups))
	for _, ag := range groups {
		if ag.Edges.Provider == nil {
			continue
		}
		if _, exists := providerMap[ag.ProviderID]; exists {
			continue
		}
		providerMap[ag.ProviderID] = ag.Edges.Provider
		orderedIDs = append(orderedIDs, ag.ProviderID)
	}

	providers := make([]*dbent.Provider, 0, len(orderedIDs))
	for _, id := range orderedIDs {
		if acc, ok := providerMap[id]; ok {
			providers = append(providers, acc)
		}
	}

	return r.RecordsFromEntities(ctx, providers)
}

// ProviderListOrder returns the administrator's supported sort and an ID
// tie-breaker in the same direction. Blank or unknown sort names select name;
// invalid direction defaults ascending, and last-used null placement follows
// PostgreSQL's direction-dependent ordering.
func ProviderListOrder(params pagination.PaginationParams) []func(*entsql.Selector) {
	sortBy := strings.ToLower(strings.TrimSpace(params.SortBy))
	sortOrder := params.NormalizedSortOrder(pagination.SortOrderAsc)

	field := dbprovider.FieldName
	defaultOrder := true
	switch sortBy {
	case "", "name":
		field = dbprovider.FieldName
	case "id":
		field = dbprovider.FieldID
		defaultOrder = false
	case "status":
		field = dbprovider.FieldStatus
		defaultOrder = false
	case "schedulable":
		field = dbprovider.FieldSchedulable
		defaultOrder = false
	case "priority":
		field = dbprovider.FieldPriority
		defaultOrder = false
	case "rate_multiplier":
		field = dbprovider.FieldRateMultiplier
		defaultOrder = false
	case "last_used_at":
		field = dbprovider.FieldLastUsedAt
		defaultOrder = false
	case "expires_at":
		field = dbprovider.FieldExpiresAt
		defaultOrder = false
	case "created_at":
		field = dbprovider.FieldCreatedAt
		defaultOrder = false
	}

	if sortOrder == pagination.SortOrderDesc {
		return []func(*entsql.Selector){dbent.Desc(field), dbent.Desc(dbprovider.FieldID)}
	}
	if defaultOrder {
		return []func(*entsql.Selector){dbent.Asc(dbprovider.FieldName), dbent.Asc(dbprovider.FieldID)}
	}
	return []func(*entsql.Selector){dbent.Asc(field), dbent.Asc(dbprovider.FieldID)}
}

// ProviderListFilteredQuery builds administrator filters while retaining a
// mutable query for count, page and complete-list readers. Empty filters are
// omitted, the ungrouped sentinel selects no memberships, and a positive
// group ID selects linked records.
//
// Active, rate_limited, temp_unschedulable and unschedulable views combine
// stored status, rate-limit and pause state differently; they do not
// constitute the full gateway eligibility check. The unset privacy filter
// matches a missing key or empty string, while other values use equality. Ent
// traversal adds normal soft-delete filtering when the query executes.
func (r *ProviderStore) ProviderListFilteredQuery(platform, providerType, status, search string, groupID int64, privacyMode string) *dbent.ProviderQuery {
	q := r.client.Provider.Query()

	if platform != "" {
		q = q.Where(dbprovider.PlatformEQ(platform))
	}
	if providerType != "" {
		q = q.Where(dbprovider.TypeEQ(providerType))
	}
	if status != "" {
		switch status {
		case acctcore.StatusActive:
			q = q.Where(
				dbprovider.StatusEQ(status),
				dbprovider.SchedulableEQ(true),
				dbprovider.Or(
					dbprovider.RateLimitResetAtIsNil(),
					dbprovider.RateLimitResetAtLTE(time.Now()),
				),
				dbpredicate.Provider(func(s *entsql.Selector) {
					col := s.C("temp_unschedulable_until")
					s.Where(entsql.Or(
						entsql.IsNull(col),
						entsql.LTE(col, entsql.Expr("NOW()")),
					))
				}),
			)
		case "rate_limited":
			q = q.Where(
				dbprovider.StatusEQ(acctcore.StatusActive),
				dbprovider.RateLimitResetAtGT(time.Now()),
				dbpredicate.Provider(func(s *entsql.Selector) {
					col := s.C("temp_unschedulable_until")
					s.Where(entsql.Or(
						entsql.IsNull(col),
						entsql.LTE(col, entsql.Expr("NOW()")),
					))
				}),
			)
		case "temp_unschedulable":
			q = q.Where(
				dbprovider.StatusEQ(acctcore.StatusActive),
				dbprovider.Or(
					dbprovider.RateLimitResetAtIsNil(),
					dbprovider.RateLimitResetAtLTE(time.Now()),
				),
				dbpredicate.Provider(func(s *entsql.Selector) {
					col := s.C("temp_unschedulable_until")
					s.Where(entsql.And(
						entsql.Not(entsql.IsNull(col)),
						entsql.GT(col, entsql.Expr("NOW()")),
					))
				}),
			)
		case "unschedulable":
			q = q.Where(
				dbprovider.StatusEQ(acctcore.StatusActive),
				dbprovider.SchedulableEQ(false),
				dbprovider.Or(
					dbprovider.RateLimitResetAtIsNil(),
					dbprovider.RateLimitResetAtLTE(time.Now()),
				),
				dbpredicate.Provider(func(s *entsql.Selector) {
					col := s.C("temp_unschedulable_until")
					s.Where(entsql.Or(
						entsql.IsNull(col),
						entsql.LTE(col, entsql.Expr("NOW()")),
					))
				}),
			)
		default:
			q = q.Where(dbprovider.StatusEQ(status))
		}
	}
	if search != "" {
		q = q.Where(dbprovider.NameContainsFold(search))
	}
	if groupID == acctcore.ProviderListGroupUngrouped {
		q = q.Where(dbprovider.Not(dbprovider.HasProviderGroups()))
	} else if groupID > 0 {
		q = q.Where(dbprovider.HasProviderGroupsWith(dbprovidergroup.GroupIDEQ(groupID)))
	}
	if privacyMode != "" {
		q = q.Where(dbpredicate.Provider(func(s *entsql.Selector) {
			path := sqljson.Path("privacy_mode")
			switch privacyMode {
			case acctcore.ProviderPrivacyModeUnsetFilter:
				s.Where(entsql.Or(
					entsql.Not(sqljson.HasKey(dbprovider.FieldExtra, path)),
					sqljson.ValueEQ(dbprovider.FieldExtra, "", path),
				))
			default:
				s.Where(sqljson.ValueEQ(dbprovider.FieldExtra, privacyMode, path))
			}
		}))
	}

	return q
}

// SchedulableProvidersQuery builds the shared active/schedulable query
// ordered by global priority. now supplies expiry, rate-limit and overload
// comparisons; temporary-pause filtering uses database NOW, and callers
// execute the returned query with their intended context.
func (r *ProviderStore) SchedulableProvidersQuery(now time.Time) *dbent.ProviderQuery {
	return r.client.Provider.Query().
		Where(
			dbprovider.StatusEQ(acctcore.StatusActive),
			dbprovider.SchedulableEQ(true),
			TempUnschedulablePredicate(),
			NotExpiredPredicate(now),
			dbprovider.Or(dbprovider.OverloadUntilIsNil(), dbprovider.OverloadUntilLTE(now)),
			dbprovider.Or(dbprovider.RateLimitResetAtIsNil(), dbprovider.RateLimitResetAtLTE(now)),
		).
		Order(dbent.Asc(dbprovider.FieldPriority))
}

// TempUnschedulablePredicate admits rows with no temporary pause or a
// deadline at or before database NOW. Database time determines this predicate
// even when another eligibility filter uses an application-supplied clock.
func TempUnschedulablePredicate() dbpredicate.Provider {
	return dbpredicate.Provider(func(s *entsql.Selector) {
		col := s.C("temp_unschedulable_until")
		s.Where(entsql.Or(
			entsql.IsNull(col),
			entsql.LTE(col, entsql.Expr("NOW()")),
		))
	})
}

// NotExpiredPredicate admits a missing expiry, a future expiry, or auto-pause
// disabled at the supplied time. The caller chooses now; an expired provider
// with auto-pause false remains eligible under this predicate.
func NotExpiredPredicate(now time.Time) dbpredicate.Provider {
	return dbprovider.Or(
		dbprovider.ExpiresAtIsNil(),
		dbprovider.ExpiresAtGT(now),
		dbprovider.AutoPauseOnExpiredEQ(false),
	)
}
