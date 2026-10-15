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

// ollama_usage.go persists shared Ollama usage state and selects due API-key
// groups for refresh.
// Ollama Cloud providers sharing the same API key use one managed
// browser-session identity even when their proxy selections differ. Group
// writes lock the selected proxy and eligible provider rows, compare the
// anchor's complete identity, and optionally require the observed session,
// refresh switch, and snapshot to match a current member before replacing the
// managed values.
//
// Periodic scans combine activity from every eligible member with success
// intervals and failure retry times. They rank one due member per exact key
// before applying the page limit, then attach the group's latest activity to
// that hydrated record so the service can check timing again. Configuration
// and proxy writers reuse the SQL URL rule when clearing observations whose
// identity became stale.

// These SQL fragments share Ollama HTTPS-host eligibility across grouped usage
// reads and identity-aware configuration cleanup, with API Key values
// remaining query parameters.
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

// lockedOllamaCloudUsageMember holds one locked identity member and its
// managed-state observations.
type lockedOllamaCloudUsageMember struct {
	id int64 // locked provider database identifier
	// anchorMatches compares this member with the loaded provider's full credential
	// and proxy identity; the group writer requires at least one matching anchor.
	anchorMatches bool
	// sessionJSON, autoJSON and snapshotJSON hold JSONB text from this locked row,
	// using literal null for missing keys. The expected-state check compares their
	// canonical forms together against the loaded provider's three managed values.
	sessionJSON  string // encrypted session JSON observation
	autoJSON     string // refresh-switch JSON observation
	snapshotJSON string // usage snapshot JSON observation
}

// ListOllamaCloudUsageGroupProviders returns eligible nondeleted providers
// sharing exact nonempty API keys from the supplied Ollama records. Keys are
// query parameters and deduplicated before selection; no usable keys yield an
// allocated empty slice, while absent SQL support returns
// ErrOllamaCloudUsageUnavailable.
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
	// Keep cleanup for group-ID scan failures; normal iteration consumes the
	// selected IDs before their hydrated provider reads need the shared
	// storage executor.
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

// SaveOllamaCloudUsageSession replaces the group's managed keys with supplied
// ciphertext and autoRefresh, clearing its prior snapshot. The caller
// encrypts and validates session content; storage compares the anchor
// identity and proxy, but deliberately skips the expected-managed-state
// comparison for this replacement.
func (r *ProviderStore) SaveOllamaCloudUsageSession(ctx context.Context, provider *acctcore.Record, ciphertext string, autoRefresh bool) error {
	return r.updateOllamaCloudUsageGroup(ctx, provider, map[string]any{
		acctcore.OllamaCloudUsageSessionExtraKey:     ciphertext,
		acctcore.OllamaCloudUsageAutoRefreshExtraKey: autoRefresh,
	}, false)
}

// DeleteOllamaCloudUsageSession clears all three managed session, refresh and
// snapshot keys across the anchor's API-key group. The shared writer compares
// current anchor/proxy identity before removal, and the caller owns outer
// transaction completion when already participating.
func (r *ProviderStore) DeleteOllamaCloudUsageSession(ctx context.Context, provider *acctcore.Record) error {
	return r.updateOllamaCloudUsageGroup(ctx, provider, map[string]any{}, false)
}

// SetOllamaCloudUsageAutoRefresh changes the refresh switch while preserving
// the loaded managed session and snapshot values. A missing nonempty session
// string is rejected, and the group writer requires the observed managed
// state to match a current locked member.
func (r *ProviderStore) SetOllamaCloudUsageAutoRefresh(ctx context.Context, provider *acctcore.Record, enabled bool) error {
	if !ollamaCloudUsageProviderHasSession(provider) {
		return acctcore.ErrOllamaCloudUsageSessionRequired
	}
	payload := ollamaCloudUsageManagedPayload(provider)
	payload[acctcore.OllamaCloudUsageAutoRefreshExtraKey] = enabled
	return r.updateOllamaCloudUsageGroup(ctx, provider, payload, true)
}

// UpdateOllamaCloudUsageSnapshot replaces the group snapshot while retaining
// loaded session and refresh values. Provider and snapshot must be nonnil and
// a nonempty session is required; anchor/proxy identity must match and the
// loaded managed state must jointly match at least one locked member.
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

// DisableOllamaCloudUsageAutoRefresh writes a false refresh switch and
// removes the loaded snapshot from the group replacement payload. A nonempty
// session is required; matching anchor/proxy identity and one locked member's
// managed state protect that replacement from a superseded observation.
func (r *ProviderStore) DisableOllamaCloudUsageAutoRefresh(ctx context.Context, provider *acctcore.Record) error {
	if !ollamaCloudUsageProviderHasSession(provider) {
		return acctcore.ErrOllamaCloudUsageSessionRequired
	}
	payload := ollamaCloudUsageManagedPayload(provider)
	payload[acctcore.OllamaCloudUsageAutoRefreshExtraKey] = false
	delete(payload, acctcore.OllamaCloudUsageSnapshotExtraKey)
	return r.updateOllamaCloudUsageGroup(ctx, provider, payload, true)
}

// ollamaCloudUsageManagedPayload copies the three present managed keys into a
// new top-level map. Nil record or Extra yields an empty map; values remain
// borrowed until serialization, so callers coordinate access to nested
// snapshot or session values.
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

// ollamaCloudUsageProviderHasSession reports whether Extra holds a nonempty
// session string. Nil records or maps and other value types are false; this
// storage guard does not decrypt, validate, or trim the session content.
func ollamaCloudUsageProviderHasSession(provider *acctcore.Record) bool {
	if provider == nil || provider.Extra == nil {
		return false
	}
	value, ok := provider.Extra[acctcore.OllamaCloudUsageSessionExtraKey].(string)
	return ok && value != ""
}

// updateOllamaCloudUsageGroup replaces managed values for the eligible
// records sharing the anchor's exact API key. It rejects nil or unsupported
// input, checks the selected proxy under a shared lock, then locks provider
// members in ID order and requires the anchor's complete credential/proxy
// identity to match.
//
// When requireExpectedState is true, the session, auto-refresh value and
// snapshot from the supplied record must jointly match at least one locked
// member. The update removes all three stored keys before merging payload and
// requires every locked member to be affected. Locally opened transactions
// commit here; context or transaction-bound clients remain caller-owned, and
// this path emits no scheduling event.
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
		// Lock the selected proxy before provider members so its connection identity
		// stays consistent with the anchor comparison and group replacement
		// that follow.
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
		// Compare all three managed values as one observed state; a single matching
		// member keeps the replacement tied to a currently stored group observation.
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
		// Replace the managed key set on exactly the locked members, retaining other
		// Extra keys and checking the affected count before this transaction
		// may commit.
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
	// A context transaction owns the earlier proxy/member locks and later completion;
	// return its apply result so the outer caller decides commit or rollback.
	if dbent.TxFromContext(ctx) != nil {
		return apply(ctx, clientFromContext(ctx, r.client))
	}
	tx, err := r.client.Tx(ctx)
	// A transaction-bound store client also supplies the shared lock lifetime;
	// reusing it leaves completion with its caller rather than opening
	// a nested transaction.
	if errors.Is(err, dbent.ErrTxStarted) {
		return apply(ctx, r.client)
	}
	if err != nil {
		return err
	}
	// Keep the local group transaction recoverable after a failed identity/state
	// comparison or member-count check, releasing all proxy and provider locks on exit.
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	if err := apply(txCtx, tx.Client()); err != nil {
		return err
	}
	return tx.Commit()
}

// lockOllamaCloudUsageGroup locks eligible members of the exact API-key
// identity in ascending ID order and returns their managed JSON observations.
// The caller retains the transaction through its group replacement; an empty
// group returns ErrOllamaCloudUsageIdentityChanged.
//
// Each member includes an anchor-match observation comparing ID, platform,
// type, complete credentials and nullable proxy ID with the loaded provider.
// That allows the group writer to distinguish a valid shared key from an
// anchor whose identity was replaced. Missing managed keys become literal
// null text for later canonical comparison, and the iterator is closed before
// the helper returns.
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
	// Release locked-member result messages before the group UPDATE reuses this
	// transaction connection; provider locks remain held until the surrounding
	// transaction finishes.
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

// canonicalProviderExtraJSON serializes one loaded Extra value and returns
// its canonical JSON text. A missing record, map or key encodes as null; JSON
// marshaling errors stop the caller's managed-state comparison rather than
// substituting an observation.
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

// canonicalJSON normalizes JSON text through standard decoding and
// re-encoding for managed-state comparison. Invalid input returns an empty
// string, and separate failed normalizations can compare equal through that
// sentinel. Decoding uses encoding/json's ordinary interface types, including
// float64 numbers; callers account for that precision during comparisons.
func canonicalJSON(raw string) string {
	var value any
	// Use the same empty sentinel for decode and encode failures, leaving this
	// helper's caller with a string comparison rather than a separate decoding error.
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return ""
	}
	encoded, err := json.Marshal(value)
	// Re-encoding errors select the same empty sentinel as decoding errors;
	// callers receive no separate failure and may compare two failed normalizations.
	if err != nil {
		return ""
	}
	return string(encoded)
}

// ollamaCloudUsageParseRFC3339SQL builds a nullable timestamp-parser
// expression around the trusted query fragments supplied here. Missing,
// malformed or invalid values yield NULL; invalid fetched_at or
// last_attempt_at can select initial refresh, while invalid next_refresh_at
// adds no failure backoff.
//
// Go can emit nine fractional digits, while SQL truncates fractions to six
// for PostgreSQL datetime parsing. A trailing Z becomes +00:00 before silent
// jsonpath evaluation so accepted UTC observations remain parseable. Callers
// supply fixed SQL fragments because the returned expression is embedded
// directly rather than bound as request data.
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

// ListDueOllamaCloudUsageProviders returns at most one due eligible member
// per exact API key, ranked before limit. A nonpositive limit returns an
// allocated empty slice; nonpositive debounce and maxWait select one minute
// and one hour, while absent SQL support is an error.
//
// The query combines group activity with each active row's managed snapshot
// when that row has a string session value and auto-refresh=true. Missing or
// invalid status observations or their required timestamps are initially
// due; success needs later activity and applies the minimum fetch interval,
// while failure needs later activity and respects valid next-refresh backoff.
// An empty session string can pass this SQL selection; the service rechecks
// session configuration before work.
//
// Hydration can omit disappeared IDs. Returned LastUsedAt holds the group
// maximum for OllamaCloudUsageAutoRefreshDueAt to check timing again before work.
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
	// Pass duration values in seconds for make_interval while preserving fractional
	// input, keeping SQL timing in the same units as the service's duration rules.
	debounceSeconds := debounce.Seconds()
	maxWaitSeconds := maxWait.Seconds()
	minFetchIntervalSeconds := acctcore.OllamaCloudUsageMinFetchInterval.Seconds()
	// Apply timing and identity ranking before LIMIT so a not-yet-due member
	// cannot occupy a scan slot needed by another API-key group's due observation.
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
	// Retain cleanup for due-selection failures while keeping the selected IDs
	// and grouped activity values available together for the later hydration step.
	defer func() { _ = rows.Close() }()
	// dueRow retains one selected provider identity and its group activity observation.
	type dueRow struct {
		id int64 // selected due provider identifier
		// groupLastUsed is the eligible API-key group's maximum activity time; nil
		// means no member had activity and clears the hydrated representative's
		// LastUsedAt.
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
		// Attach activity from every eligible key-sharing member to this record;
		// the service's later timing check would otherwise see this provider's own
		// activity while missing the group's more recent use.
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

// lockAndMatchProviderProxyIdentity compares the loaded proxy connection
// under a shared row lock before an Ollama group write. No selected proxy ID
// succeeds immediately; otherwise the caller holds the transaction through
// its later member locks and replacement.
//
// A missing proxy row matches only when the loaded record also has no proxy
// object. Existing rows require that object's ID to match and compare
// protocol, host, port, username, password and status; those fields define
// egress.ProxyConnectionIdentity. Query, scan and iteration failures are
// returned, and result closure completes before group locking continues.
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
	// Finish proxy result messages before acquiring provider-member locks on the
	// same connection; the shared proxy row lock stays with the
	// surrounding transaction.
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

// OllamaCloudBaseURLMatchesSQL returns a case-insensitive allowed-host
// predicate around a trusted SQL expression. It trims surrounding text and
// accepts HTTPS ollama.com or www.ollama.com with optional port 443 and /v1;
// callers supply SQL fragments rather than user text.
func OllamaCloudBaseURLMatchesSQL(expression string) string {
	return OllamaCloudBaseURLMatchSQLPrefix + expression + OllamaCloudBaseURLMatchSQLSuffix
}
