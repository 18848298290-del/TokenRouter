package postgres

import (
	"context"
	"encoding/json"
	"errors"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	dbprovider "github.com/TokenFlux/TokenRouter/ent/provider"
	acctcore "github.com/TokenFlux/TokenRouter/internal/provider"
)

func (r *ProviderStore) Update(ctx context.Context, provider *acctcore.Record) error {
	return r.updateProvider(ctx, provider, nil)
}

func (r *ProviderStore) updateProvider(ctx context.Context, provider *acctcore.Record, change *acctcore.ConfigurationChange) error {
	if provider == nil {
		return nil
	}

	baseCtx := ctx
	contextTx := dbent.TxFromContext(ctx)
	client := r.client
	var tx *dbent.Tx
	if contextTx != nil {
		client = contextTx.Client()
	} else {
		var err error
		tx, err = r.client.Tx(ctx)
		if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
			return err
		}
		if tx != nil {
			defer func() { _ = tx.Rollback() }()
			ctx = dbent.NewTxContext(ctx, tx)
			client = tx.Client()
		}
	}

	updated, err := r.updateLockedProvider(ctx, client, provider, change)
	if err != nil {
		return translatePersistenceError(err, acctcore.ErrProviderNotFound, nil)
	}
	if err := r.publish(ctx, client, provider.ID, provider.GroupIDs); err != nil {
		return err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return err
		}
	}

	provider.UpdatedAt = updated.UpdatedAt
	// Refresh the provider snapshot after configuration edits, including credentials and model mappings,
	// so gateway reads need not wait for a delayed or failing outbox worker.
	if contextTx == nil {
		r.afterChange(baseCtx, provider.ID)
	}
	return nil
}

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
		// Error status excludes the provider from scheduling.
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

	// Activity, rate-limit, overload, and session-window fields have dedicated runtime writers.
	// Configuration changes merge supplied fields into the locked current record before this update.
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

	extra := acctcore.CloneValues(normalizeJSONMap(provider.Extra))
	acctcore.DiscardDeprecatedExtra(extra)
	for _, key := range []string{
		"ollama_cloud_usage_session",
		"ollama_cloud_usage_auto_refresh",
		"ollama_cloud_usage_snapshot",
	} {
		delete(extra, key)
	}
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
			defer func() { _ = tx.Rollback() }()
			ctx = dbent.NewTxContext(ctx, tx)
			client = tx.Client()
		}
	}
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
