package postgres

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"

	dbprovider "github.com/TokenFlux/TokenRouter/ent/provider"
	acctcore "github.com/TokenFlux/TokenRouter/internal/provider"
)

func (r *ProviderStore) UpdateLastUsed(ctx context.Context, id int64) error {
	now := r.options.Now()
	_, err := r.client.Provider.Update().
		Where(dbprovider.IDEQ(id)).
		SetLastUsedAt(now).
		Save(ctx)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"last_used": map[string]int64{
			strconv.FormatInt(id, 10): now.Unix(),
		},
	}
	if err := r.enqueue(ctx, r.sql, ProviderLastUsed, &id, nil, payload); err != nil {
		r.observe("[SchedulerOutbox] enqueue last used failed: provider=%d err=%v", id, err)
	}
	return nil
}

func (r *ProviderStore) BatchUpdateLastUsed(ctx context.Context, updates map[int64]time.Time) error {
	if len(updates) == 0 {
		return nil
	}

	ids := make([]int64, 0, len(updates))
	args := make([]any, 0, len(updates)*2+1)
	var caseSQL strings.Builder
	_, _ = caseSQL.WriteString("UPDATE providers SET last_used_at = CASE id")

	idx := 1
	for id, ts := range updates {
		_, _ = caseSQL.WriteString(" WHEN $" + strconv.Itoa(idx) + " THEN $" + strconv.Itoa(idx+1) + "::timestamptz")
		args = append(args, id, ts)
		ids = append(ids, id)
		idx += 2
	}

	_, _ = caseSQL.WriteString(" END, updated_at = NOW() WHERE id = ANY($" + strconv.Itoa(idx) + ") AND deleted_at IS NULL")
	args = append(args, pq.Array(ids))

	_, err := r.sql.ExecContext(ctx, caseSQL.String(), args...)
	if err != nil {
		return err
	}
	lastUsedPayload := make(map[string]int64, len(updates))
	for id, ts := range updates {
		lastUsedPayload[strconv.FormatInt(id, 10)] = ts.Unix()
	}
	payload := map[string]any{"last_used": lastUsedPayload}
	if err := r.enqueue(ctx, r.sql, ProviderLastUsed, nil, nil, payload); err != nil {
		r.observe("[SchedulerOutbox] enqueue batch last used failed: err=%v", err)
	}
	return nil
}

func (r *ProviderStore) SetError(ctx context.Context, id int64, errorMsg string) error {
	// Disable scheduling with error status so snapshot refresh removes the provider from candidates.
	_, err := r.client.Provider.Update().
		Where(dbprovider.IDEQ(id)).
		SetStatus(acctcore.StatusError).
		SetErrorMessage(errorMsg).
		SetSchedulable(false).
		Save(ctx)
	if err != nil {
		return err
	}
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue set error failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return nil
}

func (r *ProviderStore) ClearError(ctx context.Context, id int64) error {
	_, err := r.client.Provider.Update().
		Where(dbprovider.IDEQ(id)).
		SetStatus(acctcore.StatusActive).
		SetErrorMessage("").
		Save(ctx)
	if err != nil {
		return err
	}
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue clear error failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return nil
}

func (r *ProviderStore) SetRateLimited(ctx context.Context, id int64, resetAt time.Time) error {
	now := r.options.Now()
	_, err := r.client.Provider.Update().
		Where(dbprovider.IDEQ(id)).
		SetRateLimitedAt(now).
		SetRateLimitResetAt(resetAt).
		Save(ctx)
	if err != nil {
		return err
	}
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue rate limit failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return nil
}

// SetRateLimitedIfLater atomically extends the provider's rate-limit window.
// Concurrent Grok responses with earlier reset times leave a stored later reset time intact.
func (r *ProviderStore) SetRateLimitedIfLater(ctx context.Context, id int64, resetAt time.Time) error {
	now := r.options.Now()
	updated, err := r.client.Provider.Update().
		Where(
			dbprovider.IDEQ(id),
			dbprovider.Or(
				dbprovider.RateLimitResetAtIsNil(),
				dbprovider.RateLimitResetAtLT(resetAt),
			),
		).
		SetRateLimitedAt(now).
		SetRateLimitResetAt(resetAt).
		Save(ctx)
	if err != nil {
		return err
	}
	if updated == 0 {
		// Another instance may have written a later reset time; refresh the local snapshot
		// even when this call has no update to publish through the outbox.
		r.afterChange(ctx, id)
		return nil
	}
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue extended rate limit failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return nil
}

// ClearRateLimitIfObserved clears the Grok OAuth rate-limit window observed by a successful request.
// Both the set time and reset time must still match the observation.
func (r *ProviderStore) ClearRateLimitIfObserved(ctx context.Context, id int64, observedLimitedAt, observedResetAt time.Time) (bool, error) {
	updated, err := r.client.Provider.Update().
		Where(
			dbprovider.IDEQ(id),
			dbprovider.PlatformEQ(acctcore.PlatformGrok),
			dbprovider.TypeEQ(acctcore.ProviderTypeOAuth),
			dbprovider.RateLimitedAtEQ(observedLimitedAt),
			dbprovider.RateLimitResetAtEQ(observedResetAt),
		).
		ClearRateLimitedAt().
		ClearRateLimitResetAt().
		Save(ctx)
	if err != nil {
		return false, err
	}
	if updated == 0 {
		r.afterChange(ctx, id)
		return false, nil
	}
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue observed rate-limit clear failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return true, nil
}

func (r *ProviderStore) SetModelRateLimit(ctx context.Context, id int64, scope string, resetAt time.Time, reason ...string) error {
	if scope == "" {
		return nil
	}
	now := r.options.Now().UTC()
	payload := map[string]string{
		"rate_limited_at":     now.Format(time.RFC3339),
		"rate_limit_reset_at": resetAt.UTC().Format(time.RFC3339),
	}
	if len(reason) > 0 {
		if value := strings.TrimSpace(reason[0]); value != "" {
			payload["reason"] = value
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	client := clientFromContext(ctx, r.client)
	result, err := client.ExecContext(
		ctx,
		`UPDATE providers SET
			extra = jsonb_set(
				jsonb_set(COALESCE(extra, '{}'::jsonb), '{model_rate_limits}'::text[], COALESCE(extra->'model_rate_limits', '{}'::jsonb), true),
				ARRAY['model_rate_limits', $1]::text[],
				$2::jsonb,
				true
			),
			updated_at = NOW()
		WHERE id = $3 AND deleted_at IS NULL`,
		scope,
		raw,
		id,
	)
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
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue model rate limit failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return nil
}

func (r *ProviderStore) SetOverloaded(ctx context.Context, id int64, until time.Time) error {
	_, err := r.client.Provider.Update().
		Where(dbprovider.IDEQ(id)).
		SetOverloadUntil(until).
		Save(ctx)
	if err != nil {
		return err
	}
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue overload failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return nil
}

func (r *ProviderStore) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	result, err := r.sql.ExecContext(ctx, `
		UPDATE providers
		SET temp_unschedulable_until = $1,
			temp_unschedulable_reason = $2,
			updated_at = NOW()
		WHERE id = $3
			AND deleted_at IS NULL
			AND (temp_unschedulable_until IS NULL OR temp_unschedulable_until < $1)
	`, until, reason, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected <= 0 {
		return nil
	}
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue temp unschedulable failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return nil
}

func (r *ProviderStore) ClearTempUnschedulable(ctx context.Context, id int64) error {
	_, err := r.sql.ExecContext(ctx, `
		UPDATE providers
		SET temp_unschedulable_until = NULL,
			temp_unschedulable_reason = NULL,
			updated_at = NOW()
		WHERE id = $1
			AND deleted_at IS NULL
	`, id)
	if err != nil {
		return err
	}
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue clear temp unschedulable failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return nil
}

func (r *ProviderStore) ClearRateLimit(ctx context.Context, id int64) error {
	_, err := r.client.Provider.Update().
		Where(dbprovider.IDEQ(id)).
		ClearRateLimitedAt().
		ClearRateLimitResetAt().
		ClearOverloadUntil().
		Save(ctx)
	if err != nil {
		return err
	}
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue clear rate limit failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return nil
}

func (r *ProviderStore) ClearAntigravityQuotaScopes(ctx context.Context, id int64) error {
	client := clientFromContext(ctx, r.client)
	result, err := client.ExecContext(
		ctx,
		"UPDATE providers SET extra = COALESCE(extra, '{}'::jsonb) - 'antigravity_quota_scopes', updated_at = NOW() WHERE id = $1 AND deleted_at IS NULL",
		id,
	)
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
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue clear quota scopes failed: provider=%d err=%v", id, err)
	}
	return nil
}

func (r *ProviderStore) ClearModelRateLimits(ctx context.Context, id int64) error {
	client := clientFromContext(ctx, r.client)
	result, err := client.ExecContext(
		ctx,
		"UPDATE providers SET extra = COALESCE(extra, '{}'::jsonb) - 'model_rate_limits', updated_at = NOW() WHERE id = $1 AND deleted_at IS NULL",
		id,
	)
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
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue clear model rate limit failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return nil
}

func (r *ProviderStore) UpdateSessionWindow(ctx context.Context, id int64, start, end *time.Time, status string) error {
	builder := r.client.Provider.Update().
		Where(dbprovider.IDEQ(id)).
		SetSessionWindowStatus(status)
	if start != nil {
		builder.SetSessionWindowStart(*start)
	}
	if end != nil {
		builder.SetSessionWindowEnd(*end)
	}
	_, err := builder.Save(ctx)
	if err != nil {
		return err
	}
	// Publish a scheduler update when either window timestamp is supplied.
	if start != nil || end != nil {
		if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
			r.observe("[SchedulerOutbox] enqueue session window update failed: provider=%d err=%v", id, err)
		}
	}
	return nil
}

func (r *ProviderStore) UpdateSessionWindowEnd(ctx context.Context, id int64, end time.Time) error {
	_, err := r.client.Provider.Update().
		Where(dbprovider.IDEQ(id)).
		SetSessionWindowEnd(end).
		Save(ctx)
	if err != nil {
		return err
	}
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue session window end update failed: provider=%d err=%v", id, err)
	}
	return nil
}

func (r *ProviderStore) SetSchedulable(ctx context.Context, id int64, schedulable bool) error {
	_, err := r.client.Provider.Update().
		Where(dbprovider.IDEQ(id)).
		SetSchedulable(schedulable).
		Save(ctx)
	if err != nil {
		return err
	}
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue schedulable change failed: provider=%d err=%v", id, err)
	}
	if !schedulable {
		r.afterChange(ctx, id)
	}
	return nil
}

func (r *ProviderStore) AutoPauseExpiredProviders(ctx context.Context, now time.Time) (int64, error) {
	rows, err := r.sql.QueryContext(ctx, `
		UPDATE providers
		SET schedulable = FALSE,
			updated_at = NOW()
		WHERE deleted_at IS NULL
			AND schedulable = TRUE
			AND auto_pause_on_expired = TRUE
			AND expires_at IS NOT NULL
			AND expires_at <= $1
		RETURNING id
	`, now)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = rows.Close()
	}()

	providerIDs := make([]int64, 0)
	for rows.Next() {
		var providerID int64
		if err := rows.Scan(&providerID); err != nil {
			return 0, err
		}
		providerIDs = append(providerIDs, providerID)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	if len(providerIDs) > 0 {
		// Refresh scheduling buckets for the paused providers and their groups.
		payload := map[string]any{"provider_ids": providerIDs}
		if err := r.enqueue(ctx, r.sql, ProviderBulkChanged, nil, nil, payload); err != nil {
			r.observe("[SchedulerOutbox] enqueue auto pause provider changes failed: err=%v", err)
		}
	}
	return int64(len(providerIDs)), nil
}

// RevertProxyFallback restores proxy_id from proxy_fallback_origin_id and clears the origin field.
// The update requires a non-null origin on a non-deleted provider.
// Zero affected rows return ErrProviderNotInFallback, including missing providers.
func (r *ProviderStore) RevertProxyFallback(ctx context.Context, providerID int64) error {
	res, err := r.sql.ExecContext(ctx, `
		UPDATE providers SET proxy_id=proxy_fallback_origin_id, proxy_fallback_origin_id=NULL, updated_at=NOW()
		WHERE id=$1 AND proxy_fallback_origin_id IS NOT NULL AND deleted_at IS NULL`, providerID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return acctcore.ErrProviderNotInFallback
	}
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &providerID, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] revert fallback enqueue failed: provider=%d err=%v", providerID, err)
	}
	return nil
}
