package postgres

import (
	"context"
	"encoding/json"
	"errors"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	dbprovider "github.com/TokenFlux/TokenRouter/ent/provider"
	acctcore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// provider_mutations.go saves provider configuration and credentials while
// retaining applicable runtime observations. Configuration writes combine
// field intent with the latest locked provider record so administrator edits
// can coexist with runtime observations. The merged record retains dedicated
// activity and health columns, while managed Ollama Extra values are read
// under the row lock and carried forward according to API-key and proxy
// identity.
//
// A locally owned Ent transaction contains the record change and its
// scheduling event, and snapshot synchronization follows its commit. An Ent
// transaction carried in context leaves final publication to the outer caller.
// Credential-only replacement uses SQL to remove observations whose identity
// becomes stale, while preserving current managed state when the relevant
// identity remains unchanged.

// Update writes the supplied provider configuration through the shared
// mutation transaction. Nil input succeeds immediately; callers supplying an
// entire record account for locked managed Extra preservation and dedicated
// runtime columns, and the record may be changed before an error returns.
func (r *ProviderStore) Update(ctx context.Context, provider *acctcore.Record) error {
	return r.updateProvider(ctx, provider, nil)
}

// updateProvider writes configuration and its scheduling event through the
// selected Ent transaction client. A transaction in ctx is reused; otherwise
// it opens one or reuses a transaction-bound store client, and nil provider
// is an immediate success.
//
// The locked write can merge declared fields and replace the supplied record
// before publication or commit. Outbox failure aborts a locally owned
// transaction, while an outer transaction retains its caller's rollback
// obligation. UpdatedAt is assigned after a local commit or participating
// write. Immediate synchronization is skipped for a context transaction; a
// transaction-bound store lacking that context still follows the callback
// branch.
func (r *ProviderStore) updateProvider(ctx context.Context, provider *acctcore.Record, change *acctcore.ConfigurationChange) error {
	if provider == nil {
		return nil
	}

	baseCtx := ctx
	contextTx := dbent.TxFromContext(ctx)
	client := r.client
	var tx *dbent.Tx
	if contextTx != nil {
		// Use the context transaction for both the record save and event write;
		// its outer owner controls rollback and later snapshot publication.
		client = contextTx.Client()
	} else {
		var err error
		tx, err = r.client.Tx(ctx)
		if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
			return err
		}
		if tx != nil {
			// Roll back a locally owned configuration save when its event fails,
			// keeping the locked row and its durable scheduling notification together.
			defer func() { _ = tx.Rollback() }()
			ctx = dbent.NewTxContext(ctx, tx)
			client = tx.Client()
		}
	}

	updated, err := r.updateLockedProvider(ctx, client, provider, change)
	if err != nil {
		return translatePersistenceError(err, acctcore.ErrProviderNotFound, nil)
	}
	// Save the event on the same client after the locked configuration write;
	// failure rejects a locally owned transaction before any snapshot callback can run.
	if err := r.publish(ctx, client, provider.ID, provider.GroupIDs); err != nil {
		return err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return err
		}
	}

	provider.UpdatedAt = updated.UpdatedAt
	// Context transaction owners publish after completion. The remaining branch also
	// includes transaction-bound stores without that context, whose callers
	// coordinate callback timing.
	if contextTx == nil {
		r.afterChange(baseCtx, provider.ID)
	}
	return nil
}

// updateLockedProvider merges declared fields against the current row before
// saving configuration and managed Extra. A supplied change causes a locked
// full read and ApplyConfigurationChange; nil change uses the supplied record
// values while the managed-Extra merge still locks the row.
//
// The managed merge carries current Ollama values according to identity
// rather than accepting stale caller observations. Error status writes
// schedulable=false, while dedicated runtime columns keep their database
// values because this builder omits them. The supplied record receives the
// merged values before Save completes, and the caller owns the surrounding
// transaction and event write.
func (r *ProviderStore) updateLockedProvider(ctx context.Context, client *dbent.Client, provider *acctcore.Record, change *acctcore.ConfigurationChange) (*dbent.Provider, error) {
	if change != nil {
		current, err := r.lockConfigurationRecord(ctx, client, provider.ID)
		if err != nil {
			return nil, err
		}
		merged, err := acctcore.ApplyConfigurationChange(current, provider, *change)
		if err != nil {
			return nil, err
		}
		*provider = *merged
	}
	extra, err := r.LockAndMergeProviderManagedExtra(ctx, client, provider)
	if err != nil {
		return nil, err
	}
	provider.Extra = extra

	schedulable := provider.Schedulable
	if provider.Status == acctcore.StatusError {
		// Persist error status and the disabled switch together so a subsequent
		// state-only recovery still requires its own scheduling decision before
		// eligibility returns.
		schedulable = false
	}

	builder := client.Provider.UpdateOneID(provider.ID).
		SetName(provider.Name).
		SetNillableNotes(provider.Notes).
		SetPlatform(provider.Platform).
		SetType(provider.Type).
		SetCredentials(normalizeJSONMap(provider.Credentials)).
		SetExtra(extra).
		SetConcurrency(provider.Concurrency).
		SetPriority(provider.Priority).
		SetStatus(provider.Status).
		SetErrorMessage(provider.ErrorMessage).
		SetSchedulable(schedulable).
		SetAutoPauseOnExpired(provider.AutoPauseOnExpired)

	if provider.RateMultiplier != nil {
		builder.SetRateMultiplier(*provider.RateMultiplier)
	}
	if provider.LoadFactor != nil {
		builder.SetLoadFactor(*provider.LoadFactor)
	} else {
		builder.ClearLoadFactor()
	}

	if provider.ProxyID != nil {
		builder.SetProxyID(*provider.ProxyID)
	} else {
		builder.ClearProxyID()
	}

	// The builder omits dedicated runtime columns so request observations are retained
	// in SQL, including when configuration was assembled from an earlier caller read.
	if provider.ExpiresAt != nil {
		builder.SetExpiresAt(*provider.ExpiresAt)
	} else {
		builder.ClearExpiresAt()
	}

	if provider.Notes == nil {
		builder.ClearNotes()
	}

	builder.SetQuotaDimension(dbprovider.QuotaDimension(provider.QuotaDimensionOrDefault()))
	builder.SetNillableParentProviderID(provider.ParentProviderID)

	return builder.Save(ctx)
}

// LockAndMergeProviderManagedExtra locks the provider row and returns
// configuration Extra combined with current managed Ollama state. Callers
// hold the supplied transaction through their later configuration save; a
// missing or deleted row returns ErrProviderNotFound.
//
// Supplied managed session, auto-refresh, and snapshot keys are removed from
// a supported-container clone before database values are considered. A
// matching Ollama API-key/base-URL identity retains the current session and
// switch; retaining the usage snapshot also requires the proxy ID to match.
// JSON decoding failures stop the update rather than publishing an incomplete
// managed observation.
func (r *ProviderStore) LockAndMergeProviderManagedExtra(ctx context.Context, client *dbent.Client, provider *acctcore.Record) (map[string]any, error) {
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
			COALESCE(
				platform IN ('openai', 'anthropic')
				AND $2 IN ('openai', 'anthropic')
				AND type = 'apikey'
				AND $3 = 'apikey'
				AND credentials -> 'api_key' IS NOT DISTINCT FROM $4::jsonb -> 'api_key'
				AND `+OllamaCloudBaseURLMatchesSQL("credentials ->> 'base_url'")+`
				AND `+OllamaCloudBaseURLMatchesSQL("$4::jsonb ->> 'base_url'")+`,
				false
			),
			proxy_id IS NOT DISTINCT FROM $5,
			extra -> 'ollama_cloud_usage_session',
			extra -> 'ollama_cloud_usage_auto_refresh',
			extra -> 'ollama_cloud_usage_snapshot'
		FROM providers
		WHERE id = $1 AND deleted_at IS NULL
		FOR NO KEY UPDATE
	`, provider.ID, provider.Platform, provider.Type, string(credentials), proxyID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, acctcore.ErrProviderNotFound
	}

	var (
		ollamaGroupIdentityUnchanged bool
		ollamaProxyIdentityUnchanged bool
		currentOllamaSession         []byte
		currentOllamaAutoRefresh     []byte
		currentOllamaSnapshot        []byte
	)
	if err := rows.Scan(
		&ollamaGroupIdentityUnchanged,
		&ollamaProxyIdentityUnchanged,
		&currentOllamaSession,
		&currentOllamaAutoRefresh,
		&currentOllamaSnapshot,
	); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Discard submitted managed observations before carrying forward locked values;
	// configuration readers can be older than the latest browser-session
	// or usage write.
	extra := acctcore.CloneValues(normalizeJSONMap(provider.Extra))
	acctcore.DiscardDeprecatedExtra(extra)
	for _, key := range []string{
		"ollama_cloud_usage_session",
		"ollama_cloud_usage_auto_refresh",
		"ollama_cloud_usage_snapshot",
	} {
		delete(extra, key)
	}
	// Session identity spans proxies, but a usage observation was fetched through
	// one selected proxy; retain that snapshot only when this proxy ID also matches.
	if r.options.OllamaIdentity(provider) && ollamaGroupIdentityUnchanged {
		for key, raw := range map[string][]byte{
			"ollama_cloud_usage_session":      currentOllamaSession,
			"ollama_cloud_usage_auto_refresh": currentOllamaAutoRefresh,
		} {
			if value, ok, err := DecodeProviderExtraJSON(raw); err != nil {
				return nil, err
			} else if ok {
				extra[key] = value
			}
		}
		if ollamaProxyIdentityUnchanged {
			if snapshot, ok, err := DecodeProviderExtraJSON(currentOllamaSnapshot); err != nil {
				return nil, err
			} else if ok {
				extra["ollama_cloud_usage_snapshot"] = snapshot
			}
		}
	}
	return extra, nil
}

// DecodeProviderExtraJSON decodes one stored Extra value and reports whether
// it is present. Empty bytes and literal null are absent; other malformed
// JSON returns an error, while valid arrays, objects, or scalars retain
// standard encoding/json types.
func DecodeProviderExtraJSON(raw []byte) (any, bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false, err
	}
	return value, true, nil
}

// UpdateCredentials replaces the complete credential object and couples it to
// a provider-change event through the selected Ent transaction client. Nil
// credentials become an empty object; no matching nondeleted row returns
// ErrProviderNotFound.
//
// SQL compares existing identity before replacement: an Ollama API-key or
// allowed-host change removes its managed session and observations, while CN
// credential changes remove the monitor snapshot. Deprecated Extra keys are
// removed on every write. A locally opened transaction commits before
// immediate synchronization; a context transaction leaves commit and
// publication to its caller, and a transaction-bound store without that
// context still takes the immediate callback branch.
func (r *ProviderStore) UpdateCredentials(ctx context.Context, id int64, credentials map[string]any) error {
	payload, err := json.Marshal(normalizeJSONMap(credentials))
	if err != nil {
		return err
	}
	baseCtx := ctx
	contextTx := dbent.TxFromContext(ctx)
	client := r.client
	var tx *dbent.Tx
	if contextTx != nil {
		client = contextTx.Client()
	} else if r.client != nil {
		var txErr error
		tx, txErr = r.client.Tx(ctx)
		if txErr != nil && !errors.Is(txErr, dbent.ErrTxStarted) {
			return txErr
		}
		if tx != nil {
			// Restore the previous credentials and identity-derived observations when
			// the later event fails before this locally owned replacement can commit.
			defer func() { _ = tx.Rollback() }()
			ctx = dbent.NewTxContext(ctx, tx)
			client = tx.Client()
		}
	}
	// Let the database compare the pre-update credential identity while replacing
	// credentials, removing managed observations that would otherwise describe
	// the prior identity.
	result, err := client.ExecContext(ctx, `
		UPDATE providers
		SET
			credentials = $1::jsonb,
			extra = CASE
				-- 凭证整体未变化时不清理 Ollama 状态；废弃提供商扩展键始终从写入结果剔除。
				WHEN platform IN ('openai', 'anthropic')
					AND type = 'apikey'
					AND credentials IS DISTINCT FROM $1::jsonb
					AND (
						credentials -> 'api_key' IS DISTINCT FROM $1::jsonb -> 'api_key'
						OR NOT (
							`+OllamaCloudBaseURLMatchesSQL("credentials ->> 'base_url'")+`
							AND `+OllamaCloudBaseURLMatchesSQL("$1::jsonb ->> 'base_url'")+`
						)
					)
				THEN (COALESCE(extra, '{}'::jsonb)
						- 'upstream_billing_probe'
						- 'upstream_billing_probe_enabled'
						- 'openai_long_context_billing_enabled')
					- 'ollama_cloud_usage_session'
					- 'ollama_cloud_usage_auto_refresh'
					- 'ollama_cloud_usage_snapshot'
				ELSE CASE
					WHEN platform IN ('kimi', 'zhipu', 'deepseek')
						AND type = 'apikey'
						AND credentials IS DISTINCT FROM $1::jsonb
					THEN (COALESCE(extra, '{}'::jsonb)
							- 'upstream_billing_probe'
							- 'upstream_billing_probe_enabled'
							- 'openai_long_context_billing_enabled')
						- 'cn_usage_monitor_snapshot'
					ELSE COALESCE(extra, '{}'::jsonb)
						- 'upstream_billing_probe'
						- 'upstream_billing_probe_enabled'
						- 'openai_long_context_billing_enabled'
				END
			END,
			updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
	`, string(payload), id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return acctcore.ErrProviderNotFound
	}
	// Enlist credential publication with the replacement so a locally owned rollback
	// also restores any observations removed by the identity comparison in SQL.
	if err := r.publish(ctx, client, id, nil); err != nil {
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
	return nil
}
