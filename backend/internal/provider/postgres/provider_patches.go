package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	acctcore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// provider_patches.go applies Extra and bulk configuration patches to current
// provider rows. Extra patches merge JSONB keys into current database values
// so independent observations can coexist. Scheduler-relevant keys join their
// outbox event in an Ent transaction; neutral observation keys still trigger a
// fresh provider snapshot outside a context transaction so cached credentials
// and other concurrent fields are read together.
//
// Bulk patches combine common fields with per-provider protocol changes. SQL
// evaluates existing credential and proxy values when deciding which Ollama or
// CN observations become stale, and retains a valid stored Codex fingerprint
// seed when identity creation is requested. The dedicated CN snapshot path
// compares updated_at and inserts its scheduling event with the matching
// update.

// These seed constraints recognize the canonical nonzero UUID retained as an
// OpenAI OAuth client fingerprint across concurrent configuration and Extra writes.
const (
	codexFingerprintSeedCanonicalPattern = "^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"
	codexFingerprintNilSeed              = "00000000-0000-0000-0000-000000000000"
)

var (
	// These prefixes classify related display observations together, allowing new
	// window fields to skip bucket events while still refreshing provider snapshots.
	schedulerNeutralExtraKeyPrefixes = []string{
		"codex_primary_",
		"codex_secondary_",
		"codex_5h_",
		"codex_7d_",
		"codex_reset_credit_",
		"passive_usage_",
		"ollama_cloud_usage",
		"cn_usage_monitor",
	}

	// These exact keys identify remaining usage display observations whose values
	// do not change candidate routing; dedicated monitor writes may publish separately.
	schedulerNeutralExtraKeys = map[string]struct{}{
		"codex_usage_updated_at":                {},
		"grok_billing_snapshot":                 {},
		"qoder_quota_snapshot":                  {},
		"qoder_quota_updated_at":                {},
		"session_window_utilization":            {},
		acctcore.CNUsageMonitorSnapshotExtraKey: {},
	}
)

// UpdateExtra merges supplied Extra keys through the shared patch path.
// Deprecated keys can be deleted from the caller's map before serialization;
// empty effective patches succeed, while scheduler-relevant keys use the
// transaction and event path.
func (r *ProviderStore) UpdateExtra(ctx context.Context, id int64, updates map[string]any) error {
	return r.updateExtra(ctx, id, updates, nil)
}

// updateExtra merges effective Extra keys into the latest JSONB object and
// optionally compares a usage identity. Deprecated keys are removed from the
// supplied map and caller-provided Codex seeds are stripped; an empty result
// returns success before checking row existence or version.
//
// Scheduler-relevant keys enlist a change event in the selected Ent
// transaction, with local commit before snapshot synchronization. Neutral
// keys skip that event but still reload the snapshot outside a context
// transaction. SQL retains unrelated keys, clears CN snapshots on identity
// configuration patches, and obtains any required Codex seed from current
// stored state rather than the caller's patch.
func (r *ProviderStore) updateExtra(ctx context.Context, id int64, updates map[string]any, version *acctcore.UsageObservationVersion) error {
	acctcore.DiscardDeprecatedExtra(updates)
	updates = stripCodexFingerprintSeedFromExtraUpdate(updates)
	if len(updates) == 0 {
		return nil
	}

	// Serialize the effective patch after key filtering; JSONB merge later uses
	// current stored values so independent observations on other keys remain available.
	payload, err := json.Marshal(updates)
	if err != nil {
		return err
	}

	durableSchedulerChange := ShouldEnqueueSchedulerOutboxForExtraUpdates(updates)
	baseCtx := ctx
	contextTx := dbent.TxFromContext(ctx)
	client := clientFromContext(ctx, r.client)
	var tx *dbent.Tx
	if durableSchedulerChange && contextTx == nil {
		var txErr error
		tx, txErr = r.client.Tx(ctx)
		if txErr != nil && !errors.Is(txErr, dbent.ErrTxStarted) {
			return txErr
		}
		if tx != nil {
			// Roll back this scheduler-relevant merge if its subsequent event fails;
			// a neutral observation skips opening this local transaction and its event.
			defer func() { _ = tx.Rollback() }()
			ctx = dbent.NewTxContext(ctx, tx)
			client = tx.Client()
		}
	}
	extraExpression := "(COALESCE(extra, '{}'::jsonb) - 'upstream_billing_probe' - 'upstream_billing_probe_enabled' - 'openai_long_context_billing_enabled') || $1::jsonb"
	if cnUsageMonitorIdentityExtraPatch(updates) {
		extraExpression = "(" + extraExpression + ") - '" + acctcore.CNUsageMonitorSnapshotExtraKey + "'"
	}
	if acctcore.ShouldEnsureCodexFingerprintSeedForExtraUpdates(updates) {
		extraExpression = ensureCodexFingerprintSeedSQL(extraExpression)
	}
	where := "id = $2 AND deleted_at IS NULL"
	args := []any{string(payload), id}
	if version != nil {
		condition, values, err := usageObservationPredicate(*version, 3, false)
		if err != nil {
			return err
		}
		where += " AND " + condition
		args = append(args, values...)
	}
	result, err := client.ExecContext(ctx, "UPDATE providers SET extra = "+extraExpression+", updated_at = NOW() WHERE "+where, args...)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		if version != nil {
			return acctcore.ErrUsageObservationChanged
		}
		return acctcore.ErrProviderNotFound
	}
	if durableSchedulerChange {
		if err := r.enqueue(ctx, client, ProviderChanged, &id, nil, nil); err != nil {
			return err
		}
		if tx != nil {
			if err := tx.Commit(); err != nil {
				return err
			}
		}
		if contextTx == nil {
			r.afterChange(baseCtx, id)
		}
	} else {
		// Reload neutral observations from the database after this write so sticky
		// sessions see current fields together with concurrent credential
		// or configuration changes.
		// A context transaction waits for its owner to finish before publication.
		if dbent.TxFromContext(ctx) == nil {
			r.afterChange(ctx, id)
		}
	}
	return nil
}

// UpdateCNUsageMonitorSnapshotCAS stores a nonnil CN observation while
// updated_at still equals the observed version. Nil snapshot or no matching
// row returns false; clearExtraKey is trimmed and removes that key after the
// snapshot merge when nonempty.
//
// The modifying CTE inserts its scheduler event from the same matching
// update, so a statement failure rejects both effects. The selected Ent
// transaction is committed when locally owned and remains caller-owned when
// supplied through ctx. Immediate synchronization follows a local commit or
// the noncontext path, and direct SQL event naming requires a configured
// Events adapter.
func (r *ProviderStore) UpdateCNUsageMonitorSnapshotCAS(
	ctx context.Context,
	providerID int64,
	expectedUpdatedAt time.Time,
	snapshot *acctcore.CNUsageMonitorSnapshot,
	clearExtraKey string,
) (bool, error) {
	if snapshot == nil {
		return false, nil
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return false, err
	}

	baseCtx := ctx
	contextTx := dbent.TxFromContext(ctx)
	client := clientFromContext(ctx, r.client)
	var tx *dbent.Tx
	if contextTx == nil {
		tx, err = r.client.Tx(ctx)
		if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
			return false, err
		}
		if tx != nil {
			// The CAS statement already couples snapshot and event; this local
			// rollback also releases its transaction after a mismatch or
			// failed statement.
			defer func() { _ = tx.Rollback() }()
			ctx = dbent.NewTxContext(ctx, tx)
			client = tx.Client()
		}
	}
	result, err := client.ExecContext(ctx, `
		WITH updated AS (
			UPDATE providers
			SET extra = CASE
					WHEN $1 = '' THEN COALESCE(extra, '{}'::jsonb) || jsonb_build_object($2, $3::jsonb)
					ELSE (COALESCE(extra, '{}'::jsonb) || jsonb_build_object($2, $3::jsonb)) - $1
				END,
				updated_at = NOW()
			WHERE id = $4
				AND deleted_at IS NULL
				AND updated_at = $5
			RETURNING id
		)
		INSERT INTO scheduler_outbox (event_type, provider_id, group_id, payload)
		SELECT $6, updated.id, NULL, NULL FROM updated
	`, strings.TrimSpace(clearExtraKey), acctcore.CNUsageMonitorSnapshotExtraKey, string(payload),
		providerID, expectedUpdatedAt, r.eventName(ProviderChanged))
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 0 {
		return false, nil
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return false, err
		}
	}
	if contextTx == nil {
		r.afterChange(baseCtx, providerID)
	}
	return true, nil
}

// BulkUpdate applies supplied common fields and per-provider protocol patches
// to nondeleted selected IDs. Empty IDs or an effective update with no set
// clauses returns zero; proxy zero clears the selection, and nonpositive load
// factor clears its column.
//
// Credential and Extra keys merge against current JSONB values. Existing
// API-key, base-URL, and proxy observations decide which Ollama or CN
// snapshots become stale. Protocol patches remove retired credential keys.
// A protocol-only patch leaves existing Extra route overrides untouched;
// those overrides are removed when an Extra, identity, or seed update also
// builds the Extra expression. Codex identity creation reuses a valid stored seed.
//
// The update and bulk event share a locally opened or context Ent transaction
// when available. Immediate synchronization is reserved for disabling changes
// outside a context transaction, while returned rows describe the database
// update count.
func (r *ProviderStore) BulkUpdate(ctx context.Context, ids []int64, updates acctcore.ProviderBulkUpdate) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	acctcore.DiscardDeprecatedExtra(updates.Extra)
	updates.Extra = stripCodexFingerprintSeedFromExtraUpdate(updates.Extra)

	setClauses := make([]string, 0, 8)
	args := make([]any, 0, 8)

	idx := 1
	ollamaProxyIdentityChanged := ""
	if updates.Name != nil {
		setClauses = append(setClauses, "name = $"+strconv.Itoa(idx))
		args = append(args, *updates.Name)
		idx++
	}
	if updates.ProxyID != nil {
		// Zero requests direct selection and supplies a distinct identity predicate,
		// allowing SQL to clear Ollama observations fetched through a
		// previously assigned proxy.
		if *updates.ProxyID == 0 {
			setClauses = append(setClauses, "proxy_id = NULL")
			ollamaProxyIdentityChanged = "proxy_id IS NOT NULL"
		} else {
			proxyPlaceholder := "$" + strconv.Itoa(idx)
			setClauses = append(setClauses, "proxy_id = "+proxyPlaceholder)
			ollamaProxyIdentityChanged = "proxy_id IS DISTINCT FROM " + proxyPlaceholder
			args = append(args, *updates.ProxyID)
			idx++
		}
	}
	if updates.Concurrency != nil {
		setClauses = append(setClauses, "concurrency = $"+strconv.Itoa(idx))
		args = append(args, *updates.Concurrency)
		idx++
	}
	if updates.Priority != nil {
		setClauses = append(setClauses, "priority = $"+strconv.Itoa(idx))
		args = append(args, *updates.Priority)
		idx++
	}
	if updates.RateMultiplier != nil {
		setClauses = append(setClauses, "rate_multiplier = $"+strconv.Itoa(idx))
		args = append(args, *updates.RateMultiplier)
		idx++
	}
	if updates.LoadFactor != nil {
		if *updates.LoadFactor <= 0 {
			setClauses = append(setClauses, "load_factor = NULL")
		} else {
			setClauses = append(setClauses, "load_factor = $"+strconv.Itoa(idx))
			args = append(args, *updates.LoadFactor)
			idx++
		}
	}
	if updates.Status != nil {
		setClauses = append(setClauses, "status = $"+strconv.Itoa(idx))
		args = append(args, *updates.Status)
		idx++
	}
	if updates.Schedulable != nil {
		setClauses = append(setClauses, "schedulable = $"+strconv.Itoa(idx))
		args = append(args, *updates.Schedulable)
		idx++
	}
	// Merge common credential keys before each provider's protocol patch, keeping
	// unspecified current keys while retiring the legacy protocol fields in that path.
	credentialPlaceholder := ""
	if len(updates.Credentials) > 0 {
		payload, err := json.Marshal(updates.Credentials)
		if err != nil {
			return 0, err
		}
		credentialPlaceholder = "$" + strconv.Itoa(idx)
		credentialExpression := "COALESCE(credentials, '{}'::jsonb) || " + credentialPlaceholder + "::jsonb"
		if len(updates.ProtocolUpdates) > 0 {
			encoded, encodeErr := json.Marshal(updates.ProtocolUpdates)
			if encodeErr != nil {
				return 0, encodeErr
			}
			credentialExpression = "(" + credentialExpression + " || COALESCE($" + strconv.Itoa(idx+1) + "::jsonb -> id::text, '{}'::jsonb)) - 'api_protocol' - 'openai_workload_capabilities' - 'openai_capabilities'"
			args = append(args, payload, encoded)
			idx += 2
		} else {
			args = append(args, payload)
			idx++
		}
		setClauses = append(setClauses, "credentials = "+credentialExpression)
	}

	if len(updates.Credentials) == 0 && len(updates.ProtocolUpdates) > 0 {
		payload, err := json.Marshal(updates.ProtocolUpdates)
		if err != nil {
			return 0, err
		}
		setClauses = append(setClauses, "credentials = (COALESCE(credentials, '{}'::jsonb) || COALESCE($"+strconv.Itoa(idx)+"::jsonb -> id::text, '{}'::jsonb)) - 'api_protocol' - 'openai_workload_capabilities' - 'openai_capabilities'")
		args = append(args, payload)
		idx++
	}

	ollamaGroupIdentityChanges := make([]string, 0, 2)
	if _, ok := updates.Credentials["api_key"]; ok {
		ollamaGroupIdentityChanges = append(ollamaGroupIdentityChanges, "credentials -> 'api_key' IS DISTINCT FROM "+credentialPlaceholder+"::jsonb -> 'api_key'")
	}
	if _, ok := updates.Credentials["base_url"]; ok {
		ollamaGroupIdentityChanges = append(ollamaGroupIdentityChanges,
			"NOT ("+OllamaCloudBaseURLMatchesSQL("credentials ->> 'base_url'")+
				" AND "+OllamaCloudBaseURLMatchesSQL(credentialPlaceholder+"::jsonb ->> 'base_url'")+")")
	}

	// CN observations depend on credentials, proxy and query/TLS settings; mentioning
	// those fields invalidates the snapshot even when the supplied value is unchanged.
	cnUsageIdentityChanged := len(updates.Credentials) > 0 || updates.ProxyID != nil || cnUsageMonitorIdentityExtraPatch(updates.Extra)
	if len(updates.Extra) > 0 || len(ollamaGroupIdentityChanges) > 0 || ollamaProxyIdentityChanged != "" || cnUsageIdentityChanged || updates.EnsureCodexFingerprintSeed {
		extraExpression := "COALESCE(extra, '{}'::jsonb) - 'upstream_billing_probe' - 'upstream_billing_probe_enabled' - 'openai_long_context_billing_enabled'"
		if len(updates.Extra) > 0 {
			payload, err := json.Marshal(updates.Extra)
			if err != nil {
				return 0, err
			}
			extraExpression += " || $" + strconv.Itoa(idx) + "::jsonb"
			args = append(args, payload)
			idx++
			if ollamaCloudUsageSnapshotClearRequested(updates.Extra) {
				extraExpression = "(" + extraExpression + ") - 'ollama_cloud_usage_snapshot'"
			}
		}
		eligibleProvider := "platform IN ('openai', 'anthropic') AND type = 'apikey'"
		groupIdentityChanged := ""
		if len(ollamaGroupIdentityChanges) > 0 {
			groupIdentityChanged = "(" + eligibleProvider + " AND (" + strings.Join(ollamaGroupIdentityChanges, " OR ") + "))"
		}
		snapshotIdentityChanged := groupIdentityChanged
		if ollamaProxyIdentityChanged != "" {
			proxyChanged := "(" + eligibleProvider + " AND " + ollamaProxyIdentityChanged + ")"
			if snapshotIdentityChanged == "" {
				snapshotIdentityChanged = proxyChanged
			} else {
				snapshotIdentityChanged = "(" + snapshotIdentityChanged + " OR " + proxyChanged + ")"
			}
		}
		if groupIdentityChanged != "" {
			extraExpression = "CASE" +
				" WHEN " + groupIdentityChanged + " THEN (" + extraExpression + ") - 'ollama_cloud_usage_session' - 'ollama_cloud_usage_auto_refresh' - 'ollama_cloud_usage_snapshot'" +
				" WHEN " + snapshotIdentityChanged + " THEN (" + extraExpression + ") - 'ollama_cloud_usage_snapshot'" +
				" ELSE " + extraExpression + " END"
		} else if snapshotIdentityChanged != "" {
			extraExpression = "CASE WHEN " + snapshotIdentityChanged + " THEN (" + extraExpression + ") - 'ollama_cloud_usage_snapshot' ELSE " + extraExpression + " END"
		}
		if cnUsageIdentityChanged {
			extraExpression = "CASE WHEN platform IN ('kimi', 'zhipu', 'deepseek') AND type = 'apikey'" +
				" THEN (" + extraExpression + ") - '" + acctcore.CNUsageMonitorSnapshotExtraKey + "' ELSE " + extraExpression + " END"
		}
		if updates.EnsureCodexFingerprintSeed {
			extraExpression = ensureCodexFingerprintSeedSQL(extraExpression)
		}
		if len(updates.ProtocolUpdates) > 0 {
			extraExpression = "(" + extraExpression + ") - 'openai_text_route_mode' - 'openai_responses_mode'"
		}
		setClauses = append(setClauses, "extra = "+extraExpression)
	}

	if len(setClauses) == 0 {
		return 0, nil
	}

	setClauses = append(setClauses, "updated_at = NOW()")

	whereClause := " WHERE id = ANY($" + strconv.Itoa(idx) + ") AND deleted_at IS NULL"
	args = append(args, pq.Array(ids))
	query := "UPDATE providers SET " + strings.Join(setClauses, ", ") + whereClause

	baseCtx := ctx
	contextTx := dbent.TxFromContext(ctx)
	exec := r.sql
	var tx *dbent.Tx
	if contextTx != nil {
		exec = contextTx.Client()
	} else if r.client != nil {
		var txErr error
		tx, txErr = r.client.Tx(ctx)
		if txErr != nil && !errors.Is(txErr, dbent.ErrTxStarted) {
			return 0, txErr
		}
		if tx != nil {
			// Keep all matching bulk rows and their one event together, rolling
			// back the locally opened batch if event insertion fails after the update.
			defer func() { _ = tx.Rollback() }()
			ctx = dbent.NewTxContext(ctx, tx)
			exec = tx.Client()
		}
	}

	result, err := exec.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	// Only matching database rows create a bulk event, using the submitted ID list
	// so the scheduler can reconcile all selected records after the update commits.
	if rows > 0 {
		payload := map[string]any{"provider_ids": ids}
		if err := r.enqueue(ctx, exec, ProviderBulkChanged, nil, nil, payload); err != nil {
			return 0, err
		}
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return 0, err
		}
	}
	// Publish disabling changes promptly outside a context transaction, while other
	// bulk edits rely on their outbox event and an outer owner publishes after commit.
	if rows > 0 && contextTx == nil {
		shouldSync := false
		if updates.Status != nil && (*updates.Status == acctcore.StatusError || *updates.Status == acctcore.StatusDisabled) {
			shouldSync = true
		}
		if updates.Schedulable != nil && !*updates.Schedulable {
			shouldSync = true
		}
		if shouldSync {
			r.afterChanges(baseCtx, ids)
		}
	}
	return rows, nil
}

// codexFingerprintSeedValidSQL returns the canonical nonzero UUID predicate
// for a stored seed.
func codexFingerprintSeedValidSQL(extraExpr string) string {
	value := "(" + extraExpr + " ->> 'codex_fingerprint_seed')"
	return "(" + value + " ~ '" + codexFingerprintSeedCanonicalPattern + "' AND " + value + " <> '" + codexFingerprintNilSeed + "')"
}

// ensureCodexFingerprintSeedSQL builds the OpenAI OAuth Extra expression that
// retains a valid stored seed or generates one. Other platform/type pairs use
// extraExpr directly, and callers supply trusted SQL expressions inside a row
// update.
//
// The validity test reads the current row's extra rather than the incoming
// merged expression. A canonical nonzero UUID is retained even when a patch
// strips its own seed key; missing or invalid stored values select
// gen_random_uuid. That row-level decision lets concurrent updates keep the
// durable client identity chosen by the earlier matching write.
func ensureCodexFingerprintSeedSQL(extraExpr string) string {
	return "CASE WHEN platform = 'openai' AND type = 'oauth' THEN " +
		"jsonb_set(" + extraExpr + ", '{codex_fingerprint_seed}', " +
		"CASE WHEN " + codexFingerprintSeedValidSQL("extra") +
		" THEN to_jsonb(extra ->> 'codex_fingerprint_seed') ELSE to_jsonb(gen_random_uuid()::text) END, true) " +
		"ELSE " + extraExpr + " END"
}

// stripCodexFingerprintSeedFromExtraUpdate removes a caller-supplied seed
// from a new top-level map when present. Nil or seed-free input is returned
// directly; retained values stay shared, so this helper does not establish
// nested ownership.
func stripCodexFingerprintSeedFromExtraUpdate(extra map[string]any) map[string]any {
	if extra == nil {
		return nil
	}
	if _, exists := extra["codex_fingerprint_seed"]; !exists {
		return extra
	}
	stripped := make(map[string]any, len(extra)-1)
	for key, value := range extra {
		if key != "codex_fingerprint_seed" {
			stripped[key] = value
		}
	}
	return stripped
}

// ShouldEnqueueSchedulerOutboxForExtraUpdates reports whether any supplied
// key affects scheduling configuration. Empty input or wholly neutral keys
// return false; values are not inspected, so a null value still participates
// through its key's classification.
func ShouldEnqueueSchedulerOutboxForExtraUpdates(updates map[string]any) bool {
	if len(updates) == 0 {
		return false
	}
	for key := range updates {
		if IsSchedulerNeutralExtraKey(key) {
			continue
		}
		return true
	}
	return false
}

// IsSchedulerNeutralExtraKey recognizes display-observation keys after
// trimming their surrounding whitespace. Empty names are relevant rather than
// neutral; exact known keys and the configured observation prefixes are
// neutral, and values do not affect this classification.
func IsSchedulerNeutralExtraKey(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	if _, ok := schedulerNeutralExtraKeys[key]; ok {
		return true
	}
	for _, prefix := range schedulerNeutralExtraKeyPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// cnUsageMonitorIdentityExtraPatch reports whether the patch mentions
// usage-query or TLS identity settings. Presence triggers invalidation even
// when the supplied value is nil or unchanged, because the snapshot belongs
// to the earlier query identity.
func cnUsageMonitorIdentityExtraPatch(updates map[string]any) bool {
	for _, key := range []string{
		acctcore.UpstreamUsageQueryExtraKey,
		"enable_tls_fingerprint",
		"tls_fingerprint_profile_id",
		"tls_fingerprint_router_id",
	} {
		if _, ok := updates[key]; ok {
			return true
		}
	}
	return false
}

// ollamaCloudUsageSnapshotClearRequested reports a present snapshot key with
// a nil value. BulkUpdate uses that explicit null as removal of the managed
// snapshot; absence and nonnil values leave this request false.
func ollamaCloudUsageSnapshotClearRequested(extra map[string]any) bool {
	value, ok := extra[acctcore.OllamaCloudUsageSnapshotExtraKey]
	return ok && value == nil
}
