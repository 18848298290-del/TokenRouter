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

// provider_states.go writes provider activity, health windows, and scheduling
// switches independently of configuration saves. Unconditional setters coexist
// with Grok's conditional limit writers, which stop earlier responses from
// shortening a later reset and clear only the timestamp pair observed by a
// successful request.
//
// State statements generally precede a separate outbox attempt, with failures
// reported through the observer. Immediate snapshot synchronization is chosen
// per operation; session-window changes use full provider events and activity
// uses last-used events, while disabling publishes promptly. Raw SQL guards
// retained deletion rows, and ordinary Ent updates use the predicates shown at
// each operation. A supplied SQL transaction retains caller-owned completion;
// ignored notification errors can still abort that transaction.

// UpdateLastUsed stores the injected current time for one provider and emits
// its Unix-seconds activity observation. The Ent update can match no row and
// still succeed; the separate event failure is observed after persistence,
// and this path skips immediate snapshot synchronization.
func (r *ProviderStore) UpdateLastUsed(ctx context.Context, id int64) error {
	now := r.options.Now()
	_, err := r.client.Provider.Update().
		Where(dbprovider.IDEQ(id)).
		SetLastUsedAt(now).
		Save(ctx)
	if err != nil {
		return err
	}
	// Encode activity with a decimal ID key and Unix-second time for the
	// scheduler's specialized last-used update rather than a complete-record
	// replacement.
	payload := map[string]any{
		"last_used": map[string]int64{
			strconv.FormatInt(id, 10): now.Unix(),
		},
	}
	// The activity statement can match no row; keep its specialized observation
	// available to the event path and report notification failure separately
	// from persistence.
	if err := r.enqueue(ctx, r.sql, ProviderLastUsed, &id, nil, payload); err != nil {
		r.observe("[SchedulerOutbox] enqueue last used failed: provider=%d err=%v", id, err)
	}
	return nil
}

// BatchUpdateLastUsed assigns every supplied timestamp to its nondeleted
// provider through one CASE update. Empty input succeeds, and the values
// replace stored last-used time directly rather than taking a maximum.
//
// The event's last_used map uses decimal provider IDs and Unix seconds, while
// the database columns retain timestamp precision. The separate event attempt
// follows the state statement on r.sql and is observed on failure. The method
// does not inspect affected rows, so its payload can include requested IDs
// that were missing or deleted when the SQL update ran.
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
	// Event timestamps use Unix seconds, so subsecond activity remains stored in
	// the database while the specialized activity payload carries its
	// whole-second observation.
	lastUsedPayload := make(map[string]int64, len(updates))
	for id, ts := range updates {
		lastUsedPayload[strconv.FormatInt(id, 10)] = ts.Unix()
	}
	payload := map[string]any{"last_used": lastUsedPayload}
	// The supplied map determines this payload, including IDs not matched by the
	// state update; notification errors are observed after the batch statement
	// has succeeded.
	if err := r.enqueue(ctx, r.sql, ProviderLastUsed, nil, nil, payload); err != nil {
		r.observe("[SchedulerOutbox] enqueue batch last used failed: err=%v", err)
	}
	return nil
}

// SetError writes error status, the supplied message, and schedulable=false
// through Ent before attempting its notification. A zero-row update can still
// succeed; event errors are observed after the state operation, followed by
// current snapshot synchronization.
func (r *ProviderStore) SetError(ctx context.Context, id int64, errorMsg string) error {
	// Store the disabled switch with error status so a later message/status-only
	// recovery cannot silently restore this provider's manual scheduling eligibility.
	_, err := r.client.Provider.Update().
		Where(dbprovider.IDEQ(id)).
		SetStatus(acctcore.StatusError).
		SetErrorMessage(errorMsg).
		SetSchedulable(false).
		Save(ctx)
	if err != nil {
		return err
	}
	// Publish the completed error state for candidate removal, observing event
	// failure before the immediate snapshot callback attempts to reload
	// that disabled record.
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue set error failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return nil
}

// ClearError writes active status and an empty error message before
// attempting its notification. The stored scheduling switch and health
// windows remain unchanged, zero affected rows succeed, and notification
// errors are observed before current snapshot synchronization.
func (r *ProviderStore) ClearError(ctx context.Context, id int64) error {
	_, err := r.client.Provider.Update().
		Where(dbprovider.IDEQ(id)).
		SetStatus(acctcore.StatusActive).
		SetErrorMessage("").
		Save(ctx)
	if err != nil {
		return err
	}
	// The active status does not restore the scheduling switch; a complete change
	// event reloads both that retained switch and the cleared message after
	// this state write.
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue clear error failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return nil
}

// SetRateLimited replaces the provider's limit observation time and reset
// with the supplied deadline. It can shorten an existing window; the later
// event is best-effort, and snapshot synchronization follows whether that
// event succeeds or is observed as failed.
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
	// The new limit pair is written before this best-effort event; a later
	// snapshot reload still runs when notification fails so local readers can see it.
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue rate limit failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return nil
}

// SetRateLimitedIfLater extends a stored reset only when the supplied
// deadline is later or the reset is absent. The Ent predicate and update act
// together, keeping a concurrently written longer reset from being shortened.
//
// A zero-row result still synchronizes the latest snapshot because another
// instance may have installed that later window. A changed row attempts its
// event separately before the same synchronization, and outbox failure is
// observed after the state write. The supplied reset is used directly; the
// method imposes no future-deadline validation.
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
		// Another instance may have written a later reset time; refresh the
		// local snapshot
		// even when this call has no update to publish through the outbox.
		r.afterChange(ctx, id)
		return nil
	}
	// A matching extension needs its event after the state update, while the
	// earlier zero-match branch already synchronizes another instance's current window.
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue extended rate limit failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return nil
}

// ClearRateLimitIfObserved clears only the Grok OAuth limit timestamps that
// still equal the successful request's observed pair. It leaves overload and
// other health fields intact, and false distinguishes a missing or changed
// pair from an applied clear.
//
// Both changed and unchanged rows synchronize the latest provider snapshot so
// callers can observe a newer window written elsewhere. A matching clear also
// attempts its scheduling event separately, with failure observed after the
// state change. Identity comparison here is limited to platform, type, and
// those timestamps rather than complete credentials.
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
	// Notify the matching timestamp-pair recovery after its clear, leaving any
	// later window that failed the predicate available to the zero-match
	// snapshot branch.
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue observed rate-limit clear failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return true, nil
}

// SetModelRateLimit replaces one named model/scope entry while retaining
// other Extra keys in SQL. Empty scope succeeds without a write; timestamps
// use UTC RFC3339 text, and only reason[0] is considered when supplied.
// Its trimmed text is included only when nonblank.
//
// The state SQL uses an Ent client from context when available and returns
// ErrProviderNotFound for no nondeleted row. Its event still uses r.sql and
// is attempted afterward, so callers account for the executor wiring when
// participating in a transaction. Event failure is observed, then immediate
// synchronization reloads the current record.
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
	// The state used a context client when present, but this notification uses
	// r.sql; transaction participants therefore retain their executor
	// and publication obligations.
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue model rate limit failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return nil
}

// SetOverloaded replaces the provider's overload deadline through Ent and
// then attempts a scheduling event. The deadline is used unchanged and
// zero-row updates succeed; event failure is observed before synchronizing
// the latest provider snapshot.
func (r *ProviderStore) SetOverloaded(ctx context.Context, id int64, until time.Time) error {
	_, err := r.client.Provider.Update().
		Where(dbprovider.IDEQ(id)).
		SetOverloadUntil(until).
		Save(ctx)
	if err != nil {
		return err
	}
	// Attempt propagation after the overload update; notification errors are observed,
	// and the following callback attempts a current-storage reload using
	// the caller's context.
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue overload failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return nil
}

// SetTempUnschedulable installs a deadline and reason only when the
// nondeleted row has no pause or a strictly earlier deadline. Equal or longer
// stored pauses, and absent rows, return success without an event or
// immediate synchronization.
//
// The SQL predicate and update keep concurrent shorter responses from
// replacing a longer pause's metadata. A matching write attempts its event
// separately and observes notification failure after the state statement,
// then synchronizes the current snapshot. until is supplied directly, so the
// caller determines whether that deadline is still useful.
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
	// Only an extended pause reaches publication, keeping a shorter response from
	// emitting an event or replacing the reason stored with a longer deadline.
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue temp unschedulable failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return nil
}

// ClearTempUnschedulable removes the pause deadline and reason from a
// nondeleted provider. No matching row succeeds; its separate event is still
// attempted and failures are observed before the current snapshot is
// synchronized.
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
	// Attempt notification even after a no-match clear, letting a changed cache
	// reconcile with current storage while event failure is reported separately.
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue clear temp unschedulable failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return nil
}

// ClearRateLimit removes rate-limit and overload columns through Ent before
// attempting its event. The operation preserves the scheduling switch and
// temporary pause, permits zero matched rows, and observes notification
// failure before current snapshot synchronization.
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
	// The cleared global windows coexist with retained manual and temporary state;
	// a complete-record event exposes that combined eligibility after
	// this state update.
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue clear rate limit failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return nil
}

// ClearAntigravityQuotaScopes removes the quota-scope Extra key for the
// nondeleted ID using the context Ent client when present. The separate event
// uses r.sql and failure is observed; no matching row returns
// ErrProviderNotFound, and immediate snapshot synchronization is omitted.
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
	// Scope cleanup uses a context client when supplied but sends this event
	// through r.sql; no immediate snapshot callback accompanies that
	// best-effort notification.
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue clear quota scopes failed: provider=%d err=%v", id, err)
	}
	return nil
}

// ClearModelRateLimits removes the model-limit Extra key for the nondeleted
// ID using the context Ent client when available. No matching row returns
// ErrProviderNotFound; its separate r.sql event is best-effort, followed by
// immediate current snapshot synchronization.
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
	// Model-key cleanup may participate through the context client while the
	// notification uses r.sql; its error is observed before the following
	// full snapshot reload.
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue clear model rate limit failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return nil
}

// UpdateSessionWindow writes status and only the nonnil supplied window
// timestamps through Ent. Nil start or end keeps that column unchanged; an
// event is attempted only when either timestamp is supplied, and event
// failures are observed after the update.
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
	// A status-only write emits no event here; supplied timestamps can change
	// scheduling windows, so those observations request the complete
	// provider-change notification.
	if start != nil || end != nil {
		if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
			r.observe("[SchedulerOutbox] enqueue session window update failed: provider=%d err=%v", id, err)
		}
	}
	return nil
}

// UpdateSessionWindowEnd replaces the stored end through Ent and then
// attempts a scheduling event. Zero matched rows succeed, event failures are
// observed after the write, and this path relies on later propagation rather
// than immediate snapshot synchronization.
func (r *ProviderStore) UpdateSessionWindowEnd(ctx context.Context, id int64, end time.Time) error {
	_, err := r.client.Provider.Update().
		Where(dbprovider.IDEQ(id)).
		SetSessionWindowEnd(end).
		Save(ctx)
	if err != nil {
		return err
	}
	// The new end is written before its event, leaving outbox propagation to
	// reload the provider rather than publishing this timestamp as an
	// isolated cache patch.
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue session window end update failed: provider=%d err=%v", id, err)
	}
	return nil
}

// SetSchedulable writes the manual scheduling switch and then attempts its
// event. Zero-row updates succeed and notification failure is observed;
// disabling synchronizes the current snapshot promptly, while enabling relies
// on the outbox path.
func (r *ProviderStore) SetSchedulable(ctx context.Context, id int64, schedulable bool) error {
	_, err := r.client.Provider.Update().
		Where(dbprovider.IDEQ(id)).
		SetSchedulable(schedulable).
		Save(ctx)
	if err != nil {
		return err
	}
	// Send the switch change before choosing immediate publication for disabling;
	// enabling leaves the event consumer to rebuild availability from
	// the current record.
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue schedulable change failed: provider=%d err=%v", id, err)
	}
	if !schedulable {
		r.afterChange(ctx, id)
	}
	return nil
}

// AutoPauseExpiredProviders disables nondeleted, currently schedulable rows
// with auto-pause enabled and expiry at or before now. It returns the number
// of IDs read from RETURNING; scan or iteration errors fail the call even
// though the state statement may already have run.
//
// The state operation precedes a separate bulk-event attempt through r.sql.
// Only a nonempty affected set is published, and an event error is observed
// after expiration changes. The supplied executor determines transaction
// ownership, and the path relies on the bulk event rather than immediate
// snapshot synchronization.
func (r *ProviderStore) AutoPauseExpiredProviders(ctx context.Context, now time.Time) (int64, error) {
	// Select affected IDs from the expiration update itself, keeping the later
	// bulk payload tied to rows disabled by this scan rather than a separate
	// candidate read.
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
		// Close expiration results on every return, including scan failures, before
		// the borrowed executor is reused by another maintenance operation.
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
		// Send only returned IDs so the bulk consumer rebuilds affected membership
		// buckets after expiry disabled those providers, observing event failure
		// after the write.
		payload := map[string]any{"provider_ids": providerIDs}
		if err := r.enqueue(ctx, r.sql, ProviderBulkChanged, nil, nil, payload); err != nil {
			r.observe("[SchedulerOutbox] enqueue auto pause provider changes failed: err=%v", err)
		}
	}
	return int64(len(providerIDs)), nil
}

// RevertProxyFallback restores proxy_id from a nonnull fallback origin and
// clears that origin for a nondeleted provider. No affected row returns
// ErrProviderNotInFallback, covering missing rows and providers whose
// fallback was already restored.
//
// The state update precedes its separate best-effort event. RowsAffected
// errors are ignored, and any returned zero count produces that domain error;
// callers therefore do not receive the driver's count failure separately.
// A matching restoration observes notification errors and relies on later
// event propagation for snapshots. The executor retains transaction
// ownership, including failure after an event database error.
func (r *ProviderStore) RevertProxyFallback(ctx context.Context, providerID int64) error {
	res, err := r.sql.ExecContext(ctx, `
		UPDATE providers SET proxy_id=proxy_fallback_origin_id, proxy_fallback_origin_id=NULL, updated_at=NOW()
		WHERE id=$1 AND proxy_fallback_origin_id IS NOT NULL AND deleted_at IS NULL`, providerID)
	if err != nil {
		return err
	}
	// Ignore a count error and branch on the returned count; zero follows the
	// no-match domain-error path even if the state statement itself already succeeded.
	n, _ := res.RowsAffected()
	if n == 0 {
		return acctcore.ErrProviderNotInFallback
	}
	// Observe publication failure after clearing the origin; this method performs
	// no compensation, and the executor's owner still decides whether
	// restoration commits.
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &providerID, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] revert fallback enqueue failed: provider=%d err=%v", providerID, err)
	}
	return nil
}
