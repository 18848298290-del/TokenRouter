package provider

import (
	"context"
	"log/slog"
	"maps"
	"net/http"
	"time"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// Grok quota observations can update both display snapshots and scheduling
// windows. Exhausted headers and successful recovery bypass ordinary snapshot
// throttling, while quiet observations reserve the configured write interval.
// The request record receives the snapshot before its best-effort Extra write,
// so callers retain the observation even when storage fails.
//
// Rate limiting installs the local scheduling block before persistence and can
// also share a bounded model window with OAuth providers on the same team.
// Existing longer limit and temporary-suspension deadlines remain relevant to
// that local block. Recovery delegates to the store's observed-timestamp compare,
// and pool-mode snapshot handling leaves default provider limit installation to
// request recovery rather than forcing it from exhausted headers.

// grokQuotaSnapshotExtraKey names the stored usage observation consumed by
// provider quota display and health recovery.
const grokQuotaSnapshotExtraKey = "grok_usage_snapshot"

// GrokHealthStore writes quota snapshots and provider health; conditional
// limit operations depend on
// the concrete store.
type GrokHealthStore interface {
	providercore.GrokRateLimitWriter // provider-wide deadline write interface
	// UpdateExtra merges quota and scheduler observation values for the provider.
	// The caller supplies a snapshot object and derived keys, and treats returned
	// write errors as best effort while retaining its request-local observation.
	UpdateExtra(context.Context, int64, map[string]any) error
	// SetTempUnschedulable writes the temporary scheduling deadline and
	// display reason.
	// The caller preserves any observed later deadline before invoking this method
	// and can discard persistence errors after installing local runtime state.
	SetTempUnschedulable(context.Context, int64, time.Time, string) error
}

// GrokHealth applies provider health rules using application-owned throttling
// and runtime state.
// @project-doc docs/interfaces/grok_upstream.md#grok_account_contract
type GrokHealth struct {
	// NormalizeModel is required when ObserveError receives a nonblank first Models
	// entry. It converts that attempt name before model health and quota
	// classification.
	NormalizeModel func(*providercore.Record, string) string
	// Store optionally persists snapshot and temporary state. Rate-limit
	// helpers can
	// use optional extension/recovery interfaces implemented by the concrete store.
	Store GrokHealthStore
	// Throttle optionally limits ordinary snapshot attempts. Critical observations
	// still call Allow but can proceed when its interval decision rejects an
	// ordinary write.
	Throttle *providercore.WriteThrottle
	// Runtime optionally installs immediate scheduling blocks before
	// persistent health
	// writes. Its nil receiver skips local state while store operations can
	// proceed.
	Runtime *providercore.RuntimeBlockState
	// Health optionally supplies explicit, model and temporary error policies. Its
	// required subservices need configuration for whichever observation
	// branches are reached.
	Health *UpstreamHealth
	// ModelTransient optionally records API-key transient provider/model failures.
	// A missing value skips that recording when the special transient branch
	// is selected.
	ModelTransient *providercore.ModelTransientState
}

// ProviderStateContext creates a five-second state-write budget retaining
// caller values.
// It detaches cancellation and deadline from a nonnil ctx, or uses Background
// for nil input. Callers invoke the returned cancel after their state operations
// to release the timer.
func ProviderStateContext(ctx context.Context) (context.Context, context.CancelFunc) {
	// Health classification can finish after request cancellation. Retain its
	// values while giving accepted state writes a separate cancellation budget.
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, 5*time.Second)
}

// StoreSnapshot retains quota data and applies eligible limit or recovery handling.
// Nil service, invalid provider ID or nil snapshot is ignored. The caller owns
// mutable access to record and snapshot: exhausted reset normalization can edit
// snapshot windows, and the exact snapshot pointer is installed in record.Extra.
//
// Ordinary observations can be skipped by Throttle, while 429, active exhaustion
// and successful recovery proceed through that gate. Extra persistence is best
// effort. Active windows install limits only when requested and outside pool
// mode; otherwise successful recovery can request observed-timestamp cleanup.
// The in-memory snapshot therefore remains available even when a write fails.
func (s *GrokHealth) StoreSnapshot(ctx context.Context, value *providercore.Record, snapshot *grok.QuotaSnapshot, installRateLimit bool, teamModel string) {
	if s == nil || value == nil || value.ID <= 0 || snapshot == nil {
		return
	}
	providerID := value.ID
	now := time.Now()
	resetAt, hasActiveLimit := providercore.GrokRateLimitResetAtForProvider(value, snapshot, now)
	if hasActiveLimit {
		providercore.NormalizeGrokExhaustedWindowResets(snapshot, resetAt, now)
	}
	recovery := providercore.IsSuccessfulGrokRateLimitRecovery(value, snapshot)
	critical := snapshot.StatusCode == http.StatusTooManyRequests || hasActiveLimit || recovery
	// Critical exhaustion and recovery must reach health handling even inside a
	// reserved ordinary interval; Allow still maintains the throttle
	// observation state.
	if s.Throttle != nil {
		allowed := s.Throttle.Allow(providerID, now)
		if !critical && !allowed {
			return
		}
	}

	updates := map[string]any{
		grokQuotaSnapshotExtraKey: snapshot,
	}
	// The scheduler reads derived grok_sched_* utilization and reset fields. Copy
	// them beside the snapshot so one observation supplies both display and
	// pause inputs.
	maps.Copy(updates, providercore.BuildGrokSchedulerExtraUpdates(snapshot))
	stateCtx := ctx
	if hasActiveLimit {
		var cancel context.CancelFunc
		stateCtx, cancel = ProviderStateContext(ctx)
		defer cancel()
	}
	// Keep the observation on the borrowed request record before the
	// best-effort write.
	// Callers serialize record/snapshot mutation because this retains the same
	// snapshot pointer.
	if value.Extra == nil {
		value.Extra = map[string]any{}
	}
	value.Extra[grokQuotaSnapshotExtraKey] = snapshot
	if s.Store != nil {
		_ = s.Store.UpdateExtra(stateCtx, providerID, updates)
	}
	// Pool mode still records headers, but default header exhaustion does not
	// install
	// its provider window here. Recovery remains eligible in the alternate branch,
	// including when limit installation was disabled for this observation.
	if installRateLimit && hasActiveLimit && !value.IsPoolMode() {
		s.RateLimit(stateCtx, value, resetAt, teamModel)
	} else if recovery {
		providercore.ClearGrokRateLimitAfterRecovery(stateCtx, s.Store, providercore.CloneRecord(value), slog.Warn)
	}
}

// RateLimit installs local and persistent scheduling limits for one provider.
// Nil service or record is ignored. The reset is normalized against current
// time and any later provider reset, and the local block also respects a later
// temporary suspension before the persistent write is attempted.
//
// Persistence delegates to the core's detached budget and extension-only
// capability when available. A nonempty teamModel can additionally update the
// bounded OAuth team/model window; missing team identity leaves that team path
// empty. Local blocking and persistent writes are separate effects, so a store
// failure does not undo the runtime block.
func (s *GrokHealth) RateLimit(ctx context.Context, value *providercore.Record, resetAt time.Time, teamModel string) {
	if s == nil || value == nil {
		return
	}
	now := time.Now()
	resetAt = providercore.NormalizeGrokRateLimitResetAt(value, resetAt, now)

	runtimeUntil := resetAt
	if value.TempUnschedulableUntil != nil && value.TempUnschedulableUntil.After(runtimeUntil) {
		runtimeUntil = *value.TempUnschedulableUntil
	}
	// Install the local block before the independent store attempt so scheduling
	// reacts to the observed window even when persistence later fails.
	s.Runtime.BlockProviderScheduling(value, runtimeUntil, "429")
	providercore.PersistGrokRateLimit(ctx, s.Store, providercore.CloneRecord(value), resetAt, slog.Warn)

	// Team/model state applies to OAuth providers with a usable team identity.
	// Bound its deadline separately so a provider reset cannot create an oversized
	// shared-team cooldown for the supplied model.
	if teamModel != "" {
		providercore.MarkGrokTeamModelRateLimit(value, teamModel, providercore.ResolveGrokTeamRateLimitUntil(resetAt, now))
	}
}

// TempUnschedule installs a temporary provider block and attempts its
// persistent deadline.
// Nil service or record is ignored. It preserves a later observed temporary
// deadline, installs runtime state first, then uses a detached state context for
// the optional store write. Storage errors are discarded.
//
// An observed later window determines both runtime and storage deadlines, so
// a shorter error cooldown cannot make the provider eligible early. Runtime
// installation and storage are independent effects: a write error leaves the
// already-installed local window active.
func (s *GrokHealth) TempUnschedule(ctx context.Context, value *providercore.Record, cooldown time.Duration, reason string) {
	if s == nil || value == nil {
		return
	}
	until := time.Now().Add(cooldown)
	if value.TempUnschedulableUntil != nil && value.TempUnschedulableUntil.After(until) {
		until = *value.TempUnschedulableUntil
	}
	// Keep immediate selection state available before the detached persistent
	// write;
	// ignoring that write error leaves this runtime protection in effect.
	s.Runtime.BlockProviderScheduling(value, until, reason)
	if s.Store != nil {
		stateCtx, cancel := ProviderStateContext(ctx)
		defer cancel()
		_ = s.Store.SetTempUnschedulable(stateCtx, value.ID, until, reason)
	}
}

// ObserveResponse records parsed quota headers or checks successful OAuth
// limit recovery.
// Parsed snapshots are stamped with model plan evidence before StoreSnapshot
// runs. A headerless successful observation can still request timestamp-conditional
// recovery using a cloned provider record, leaving snapshot parsing optional.
func (s *GrokHealth) ObserveResponse(ctx context.Context, value *providercore.Record, headers http.Header, status int, model string) {
	snapshot := grok.ParseQuotaObservation(headers, status, time.Now())
	if snapshot != nil {
		providercore.StampGrokQuotaPlan(providercore.CloneRecord(value), snapshot, model, grok.ResolveGrokTextResponsesModelID, grok.ApplyGrok45ResponsesPlanSignal)
		s.StoreSnapshot(ctx, value, snapshot, true, model)
		return
	}
	if providercore.IsSuccessfulGrokRateLimitRecovery(providercore.CloneRecord(value), &grok.QuotaSnapshot{StatusCode: status}) {
		providercore.ClearGrokRateLimitAfterRecovery(ctx, s.Store, providercore.CloneRecord(value), slog.Warn)
	}
}
