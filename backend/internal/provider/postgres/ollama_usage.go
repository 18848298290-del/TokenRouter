package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/lib/pq"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	acctcore "github.com/TokenFlux/TokenRouter/internal/provider"
)

const (
	OllamaCloudBaseURLRegexSQL       = `^[hH][tT][tT][pP][sS]://([wW][wW][wW]\.)?[oO][lL][lL][aA][mM][aA]\.[cC][oO][mM](:443)?(/v1)?$`
	OllamaCloudBaseURLMatchSQLPrefix = "btrim("
	OllamaCloudBaseURLMatchSQLSuffix = ") ~ '" + OllamaCloudBaseURLRegexSQL + "'"
	OllamaCloudUsageEligibleSQL      = `
	platform IN ('openai', 'anthropic')
	AND type = 'apikey'
	AND ` + OllamaCloudBaseURLMatchSQLPrefix + `credentials ->> 'base_url'` + OllamaCloudBaseURLMatchSQLSuffix + `
	AND jsonb_typeof(credentials -> 'api_key') = 'string'
`
)

type lockedOllamaCloudUsageMember struct {
	id            int64
	anchorMatches bool
	sessionJSON   string
	autoJSON      string
	snapshotJSON  string
}

// ListOllamaCloudUsageGroupProviders resolves matching identity groups with one ID query and one batch load.
// API keys are passed as query parameters.
func (r *ProviderStore) ListOllamaCloudUsageGroupProviders(ctx context.Context, providers []*acctcore.Record) ([]acctcore.Record, error) {
	if r == nil || r.sql == nil {
		return nil, acctcore.ErrOllamaCloudUsageUnavailable
	}
	keys := make([]string, 0, len(providers))
	seen := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		if !r.options.OllamaIdentity(provider) || provider.Credentials == nil {
			continue
		}
		apiKey, ok := provider.Credentials["api_key"].(string)
		if !ok || apiKey == "" {
			continue
		}
		if _, duplicate := seen[apiKey]; duplicate {
			continue
		}
		seen[apiKey] = struct{}{}
		keys = append(keys, apiKey)
	}
	if len(keys) == 0 {
		return []acctcore.Record{}, nil
	}
	rows, err := r.sql.QueryContext(ctx, `
		SELECT id
		FROM providers
		WHERE deleted_at IS NULL
			AND `+OllamaCloudUsageEligibleSQL+`
			AND credentials ->> 'api_key' = ANY($1)
		ORDER BY id
	`, pq.Array(keys))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0, len(keys))
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
	hydrated, err := r.GetByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	result := make([]acctcore.Record, 0, len(hydrated))
	for _, provider := range hydrated {
		if provider != nil {
			result = append(result, *provider)
		}
	}
	return result, nil
}

func (r *ProviderStore) SaveOllamaCloudUsageSession(ctx context.Context, provider *acctcore.Record, ciphertext string, autoRefresh bool) error {
	return r.updateOllamaCloudUsageGroup(ctx, provider, map[string]any{
		acctcore.OllamaCloudUsageSessionExtraKey:     ciphertext,
		acctcore.OllamaCloudUsageAutoRefreshExtraKey: autoRefresh,
	}, false)
}

func (r *ProviderStore) DeleteOllamaCloudUsageSession(ctx context.Context, provider *acctcore.Record) error {
	return r.updateOllamaCloudUsageGroup(ctx, provider, map[string]any{}, false)
}

func (r *ProviderStore) SetOllamaCloudUsageAutoRefresh(ctx context.Context, provider *acctcore.Record, enabled bool) error {
	if !ollamaCloudUsageProviderHasSession(provider) {
		return acctcore.ErrOllamaCloudUsageSessionRequired
	}
	payload := ollamaCloudUsageManagedPayload(provider)
	payload[acctcore.OllamaCloudUsageAutoRefreshExtraKey] = enabled
	return r.updateOllamaCloudUsageGroup(ctx, provider, payload, true)
}

func (r *ProviderStore) UpdateOllamaCloudUsageSnapshot(ctx context.Context, provider *acctcore.Record, snapshot *acctcore.OllamaCloudUsageSnapshot) error {
	if provider == nil || snapshot == nil {
		return acctcore.ErrProviderNilInput
	}
	if !ollamaCloudUsageProviderHasSession(provider) {
		return acctcore.ErrOllamaCloudUsageSessionRequired
	}
	payload := ollamaCloudUsageManagedPayload(provider)
	payload[acctcore.OllamaCloudUsageSnapshotExtraKey] = snapshot
	return r.updateOllamaCloudUsageGroup(ctx, provider, payload, true)
}

// DisableOllamaCloudUsageAutoRefresh disables refresh for the shared identity group using the loaded identity as a CAS condition.
// An API key change prevents this operation from disabling the new identity group.
func (r *ProviderStore) DisableOllamaCloudUsageAutoRefresh(ctx context.Context, provider *acctcore.Record) error {
	if !ollamaCloudUsageProviderHasSession(provider) {
		return acctcore.ErrOllamaCloudUsageSessionRequired
	}
	payload := ollamaCloudUsageManagedPayload(provider)
	payload[acctcore.OllamaCloudUsageAutoRefreshExtraKey] = false
	delete(payload, acctcore.OllamaCloudUsageSnapshotExtraKey)
	return r.updateOllamaCloudUsageGroup(ctx, provider, payload, true)
}

func ollamaCloudUsageManagedPayload(provider *acctcore.Record) map[string]any {
	payload := make(map[string]any, 3)
	if provider == nil || provider.Extra == nil {
		return payload
	}
	for _, key := range []string{
		acctcore.OllamaCloudUsageSessionExtraKey,
		acctcore.OllamaCloudUsageAutoRefreshExtraKey,
		acctcore.OllamaCloudUsageSnapshotExtraKey,
	} {
		if value, ok := provider.Extra[key]; ok {
			payload[key] = value
		}
	}
	return payload
}

func ollamaCloudUsageProviderHasSession(provider *acctcore.Record) bool {
	if provider == nil || provider.Extra == nil {
		return false
	}
	value, ok := provider.Extra[acctcore.OllamaCloudUsageSessionExtraKey].(string)
	return ok && value != ""
}

func (r *ProviderStore) updateOllamaCloudUsageGroup(
	ctx context.Context,
	provider *acctcore.Record,
	payload map[string]any,
	requireExpectedState bool,
) error {
	if provider == nil {
		return acctcore.ErrProviderNilInput
	}
	if r == nil || r.client == nil || !r.options.OllamaIdentity(provider) {
		return acctcore.ErrOllamaCloudUsageUnavailable
	}
	apiKey, ok := provider.Credentials["api_key"].(string)
	if !ok || apiKey == "" {
		return acctcore.ErrOllamaCloudUsageProviderInvalid
	}
	apply := func(txCtx context.Context, client *dbent.Client) error {
		matchesProxy, err := lockAndMatchProviderProxyIdentity(txCtx, client, provider)
		if err != nil {
			return err
		}
		if !matchesProxy {
			return acctcore.ErrOllamaCloudUsageIdentityChanged
		}
		members, err := lockOllamaCloudUsageGroup(txCtx, client, provider, apiKey)
		if err != nil {
			return err
		}
		anchorMatches := false
		for _, member := range members {
			anchorMatches = anchorMatches || member.anchorMatches
		}
		if !anchorMatches {
			return acctcore.ErrOllamaCloudUsageIdentityChanged
		}
		if requireExpectedState {
			expectedSession, err := canonicalProviderExtraJSON(provider, acctcore.OllamaCloudUsageSessionExtraKey)
			if err != nil {
				return err
			}
			expectedAuto, err := canonicalProviderExtraJSON(provider, acctcore.OllamaCloudUsageAutoRefreshExtraKey)
			if err != nil {
				return err
			}
			expectedSnapshot, err := canonicalProviderExtraJSON(provider, acctcore.OllamaCloudUsageSnapshotExtraKey)
			if err != nil {
				return err
			}
			stateMatches := false
			for _, member := range members {
				if canonicalJSON(member.sessionJSON) == expectedSession &&
					canonicalJSON(member.autoJSON) == expectedAuto &&
					canonicalJSON(member.snapshotJSON) == expectedSnapshot {
					stateMatches = true
					break
				}
			}
			if !stateMatches {
				return acctcore.ErrOllamaCloudUsageIdentityChanged
			}
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		memberIDs := make([]int64, len(members))
		for index := range members {
			memberIDs[index] = members[index].id
		}
		result, err := client.ExecContext(txCtx, `
			UPDATE providers
			SET extra = (COALESCE(extra, '{}'::jsonb)
					- 'ollama_cloud_usage_session'
					- 'ollama_cloud_usage_auto_refresh'
					- 'ollama_cloud_usage_snapshot') || $1::jsonb,
				updated_at = NOW()
			WHERE deleted_at IS NULL
				AND `+OllamaCloudUsageEligibleSQL+`
				AND credentials ->> 'api_key' = $2
				AND id = ANY($3)
		`, string(encoded), apiKey, pq.Array(memberIDs))
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != int64(len(members)) {
			return acctcore.ErrOllamaCloudUsageIdentityChanged
		}
		return nil
	}
	if dbent.TxFromContext(ctx) != nil {
		return apply(ctx, clientFromContext(ctx, r.client))
	}
	tx, err := r.client.Tx(ctx)
	if errors.Is(err, dbent.ErrTxStarted) {
		return apply(ctx, r.client)
	}
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	if err := apply(txCtx, tx.Client()); err != nil {
		return err
	}
	return tx.Commit()
}

func lockOllamaCloudUsageGroup(
	ctx context.Context,
	client *dbent.Client,
	provider *acctcore.Record,
	apiKey string,
) ([]lockedOllamaCloudUsageMember, error) {
	credentials, err := json.Marshal(normalizeJSONMap(provider.Credentials))
	if err != nil {
		return nil, err
	}
	var proxyID any
	if provider.ProxyID != nil {
		proxyID = *provider.ProxyID
	}
	rows, err := client.QueryContext(ctx, `
		SELECT
			id,
			id = $2
				AND platform = $3
				AND type = $4
				AND credentials = $5::jsonb
				AND proxy_id IS NOT DISTINCT FROM $6,
			COALESCE((extra -> 'ollama_cloud_usage_session')::text, 'null'),
			COALESCE((extra -> 'ollama_cloud_usage_auto_refresh')::text, 'null'),
			COALESCE((extra -> 'ollama_cloud_usage_snapshot')::text, 'null')
		FROM providers
		WHERE deleted_at IS NULL
			AND `+OllamaCloudUsageEligibleSQL+`
			AND credentials ->> 'api_key' = $1
		ORDER BY id
		FOR NO KEY UPDATE
	`, apiKey, provider.ID, provider.Platform, provider.Type, string(credentials), proxyID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	members := make([]lockedOllamaCloudUsageMember, 0, 1)
	for rows.Next() {
		var member lockedOllamaCloudUsageMember
		if err := rows.Scan(&member.id, &member.anchorMatches, &member.sessionJSON, &member.autoJSON, &member.snapshotJSON); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return nil, acctcore.ErrOllamaCloudUsageIdentityChanged
	}
	return members, nil
}

func canonicalProviderExtraJSON(provider *acctcore.Record, key string) (string, error) {
	var value any
	if provider != nil && provider.Extra != nil {
		value = provider.Extra[key]
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return canonicalJSON(string(raw)), nil
}

func canonicalJSON(raw string) string {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return ""
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// ollamaCloudUsageParseRFC3339SQL builds an RFC3339/Nano parser expression.
// Missing or invalid timestamps yield NULL and are treated as an initial refresh.
//
// Normalize values before passing them to jsonpath:
//  1. Truncate fractional seconds to six digits because .datetime() accepts microseconds while Go can emit nine digits.
//  2. Replace a trailing "Z" with "+00:00" for PostgreSQL versions whose jsonpath parser
//     does not accept the UTC "Z" suffix used by this service.
//     Otherwise valid timestamps can silently become NULL, causing due-time queries
//     to treat the records as initial refreshes.
//
// The silent jsonpath call also returns NULL for invalid dates such as 2026-02-30.
// These records remain eligible for an initial refresh.
func ollamaCloudUsageParseRFC3339SQL(expression string) string {
	return `CASE
		WHEN ` + expression + ` IS NULL THEN NULL
		WHEN ` + expression + ` ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})$'
			THEN jsonb_path_query_first_tz(
				to_jsonb(regexp_replace(
					regexp_replace(
						` + expression + `,
						'(\.[0-9]{6})[0-9]+(Z|[+-][0-9]{2}:[0-9]{2})$',
						'\1\2'
					),
					'Z$',
					'+00:00'
				)),
				'$.datetime()', '{}'::jsonb, true
			) #>> '{}'
		ELSE NULL
	END`
}

// ListDueOllamaCloudUsageProviders returns at most one due provider per exact API key.
// Debounce, maximum wait, and failure backoff are applied before LIMIT;
// groups that are not due remain available for later scans.
// Each record's LastUsedAt contains the group's MAX(last_used_at), allowing the service
// to recheck whether a refresh is due before execution.
//
// Keep these rules synchronized with acctcore.ollamaCloudUsageAutoRefreshDueAt:
//   - Missing or invalid snapshots and timestamps are treated as an initial refresh.
//   - After success, activity must be later than fetched_at, with
//     due_at = GREATEST(LEAST(last_used+debounce, fetched+maxWait), fetched+minFetchInterval).
//   - After failure or unauthorized status, activity must be later than last_attempt and uses the same LEAST formula.
//     due_at is at least a valid next_refresh_at; missing or invalid values add no backoff.
func (r *ProviderStore) ListDueOllamaCloudUsageProviders(
	ctx context.Context,
	now time.Time,
	debounce, maxWait time.Duration,
	limit int,
) ([]acctcore.Record, error) {
	if limit <= 0 {
		return []acctcore.Record{}, nil
	}
	if r == nil || r.sql == nil {
		return nil, errors.New("provider repository SQL executor not configured")
	}
	if debounce <= 0 {
		debounce = time.Minute
	}
	if maxWait <= 0 {
		maxWait = time.Hour
	}
	debounceSeconds := debounce.Seconds()
	maxWaitSeconds := maxWait.Seconds()
	minFetchIntervalSeconds := acctcore.OllamaCloudUsageMinFetchInterval.Seconds()
	rows, err := r.sql.QueryContext(ctx, `
		WITH eligible AS (
			SELECT id,
				credentials ->> 'api_key' AS api_key,
				last_used_at,
				extra -> 'ollama_cloud_usage_snapshot' AS snapshot
			FROM providers
			WHERE deleted_at IS NULL
				AND status = 'active'
				AND `+OllamaCloudUsageEligibleSQL+`
				AND jsonb_typeof(extra -> 'ollama_cloud_usage_session') = 'string'
				AND extra @> '{"ollama_cloud_usage_auto_refresh": true}'::jsonb
		), group_activity AS (
			SELECT credentials ->> 'api_key' AS api_key,
				MAX(last_used_at) AS group_last_used_at
			FROM providers
			WHERE deleted_at IS NULL
				AND `+OllamaCloudUsageEligibleSQL+`
				AND jsonb_typeof(credentials -> 'api_key') = 'string'
			GROUP BY credentials ->> 'api_key'
		), joined AS (
			SELECT e.id, e.api_key, e.snapshot, g.group_last_used_at,
				e.snapshot #>> '{status}' AS status,
				e.snapshot #>> '{fetched_at}' AS fetched_at,
				e.snapshot #>> '{last_attempt_at}' AS last_attempt_at,
				e.snapshot #>> '{next_refresh_at}' AS next_refresh_at
			FROM eligible e
			JOIN group_activity g ON g.api_key = e.api_key
		), parsed AS MATERIALIZED (
			SELECT id, api_key, snapshot, group_last_used_at, status,
				`+ollamaCloudUsageParseRFC3339SQL("fetched_at")+` AS parsed_fetched_at,
				`+ollamaCloudUsageParseRFC3339SQL("last_attempt_at")+` AS parsed_last_attempt_at,
				`+ollamaCloudUsageParseRFC3339SQL("next_refresh_at")+` AS parsed_next_refresh_at
			FROM joined
		), timed AS (
			SELECT *,
				CASE
					WHEN status = 'ok'
						AND parsed_fetched_at IS NOT NULL
						AND group_last_used_at IS NOT NULL
						AND group_last_used_at > parsed_fetched_at::timestamptz
					THEN GREATEST(
						LEAST(
							group_last_used_at + make_interval(secs => $2::double precision),
							parsed_fetched_at::timestamptz + make_interval(secs => $3::double precision)
						),
						parsed_fetched_at::timestamptz + make_interval(secs => $5::double precision)
					)
					WHEN status IN ('failed', 'unauthorized')
						AND parsed_last_attempt_at IS NOT NULL
						AND group_last_used_at IS NOT NULL
						AND group_last_used_at > parsed_last_attempt_at::timestamptz
					THEN GREATEST(
						LEAST(
							group_last_used_at + make_interval(secs => $2::double precision),
							parsed_last_attempt_at::timestamptz + make_interval(secs => $3::double precision)
						),
						COALESCE(parsed_next_refresh_at::timestamptz, '-infinity'::timestamptz)
					)
					ELSE NULL
				END AS activity_due_at
			FROM parsed
		), candidates AS (
			SELECT *,
				CASE
					WHEN snapshot IS NULL OR snapshot = 'null'::jsonb OR status IS NULL
						OR status NOT IN ('ok', 'failed', 'unauthorized') THEN 0
					WHEN status = 'ok' AND parsed_fetched_at IS NULL THEN 0
					WHEN status IN ('failed', 'unauthorized') AND parsed_last_attempt_at IS NULL THEN 0
					WHEN activity_due_at IS NOT NULL AND $1 >= activity_due_at THEN 1
					ELSE NULL
				END AS due_class,
				activity_due_at AS due_at
			FROM timed
		), ranked AS (
			SELECT id, api_key, group_last_used_at, due_class, due_at,
				row_number() OVER (
					PARTITION BY api_key
					ORDER BY due_class,
						due_at NULLS FIRST,
						id
				) AS group_rank
			FROM candidates
			WHERE due_class IS NOT NULL
		)
		SELECT id, group_last_used_at
		FROM ranked
		WHERE group_rank = 1
		ORDER BY due_class, due_at NULLS FIRST, id
		LIMIT $4
	`, now.UTC(), debounceSeconds, maxWaitSeconds, limit, minFetchIntervalSeconds)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	type dueRow struct {
		id            int64
		groupLastUsed *time.Time
	}
	rowsOut := make([]dueRow, 0, limit)
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var row dueRow
		if err := rows.Scan(&row.id, &row.groupLastUsed); err != nil {
			return nil, err
		}
		rowsOut = append(rowsOut, row)
		ids = append(ids, row.id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	providers, err := r.GetByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*acctcore.Record, len(providers))
	for _, provider := range providers {
		if provider != nil {
			byID[provider.ID] = provider
		}
	}
	result := make([]acctcore.Record, 0, len(rowsOut))
	for _, row := range rowsOut {
		provider := byID[row.id]
		if provider == nil {
			continue
		}
		// Use the group's MAX(last_used_at) for the service's due-time check.
		if row.groupLastUsed != nil {
			ts := row.groupLastUsed.UTC()
			provider.LastUsedAt = &ts
		} else {
			provider.LastUsedAt = nil
		}
		result = append(result, *provider)
	}
	return result, nil
}

func lockAndMatchProviderProxyIdentity(ctx context.Context, client *dbent.Client, provider *acctcore.Record) (bool, error) {
	if provider.ProxyID == nil {
		return true, nil
	}
	rows, err := client.QueryContext(ctx, `
		SELECT protocol, host, port, COALESCE(username, ''), COALESCE(password, ''), status
		FROM proxies
		WHERE id = $1 AND deleted_at IS NULL
		FOR SHARE
	`, *provider.ProxyID)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return false, err
		}
		return provider.Proxy == nil, nil
	}
	if provider.Proxy == nil || provider.Proxy.ID != *provider.ProxyID {
		return false, nil
	}
	var current egress.ProxyConnectionIdentity
	if err := rows.Scan(&current.Protocol, &current.Host, &current.Port, &current.Username, &current.Password, &current.Status); err != nil {
		return false, err
	}
	return current == egress.ProxyConnectionIdentityFromProxy(provider.Proxy), rows.Err()
}

// OllamaCloudBaseURLMatchesSQL builds a SQL predicate matching Ollama Cloud URLs.
func OllamaCloudBaseURLMatchesSQL(expression string) string {
	return OllamaCloudBaseURLMatchSQLPrefix + expression + OllamaCloudBaseURLMatchSQLSuffix
}
