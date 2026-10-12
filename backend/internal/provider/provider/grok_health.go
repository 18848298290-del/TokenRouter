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

const grokQuotaSnapshotExtraKey = "grok_usage_snapshot"

// GrokHealthStore writes quota snapshots and provider health; conditional limit operations depend on
// the concrete store.
type GrokHealthStore interface {
	providercore.GrokRateLimitWriter
	UpdateExtra(context.Context, int64, map[string]any) error
	SetTempUnschedulable(context.Context, int64, time.Time, string) error
}

// GrokHealth applies provider health rules using application-owned throttling and runtime state.
// @project-doc docs/interfaces/grok_upstream.md#grok_account_contract
type GrokHealth struct {
	NormalizeModel func(*providercore.Record, string) string
	Store          GrokHealthStore
	Throttle       *providercore.WriteThrottle
	Runtime        *providercore.RuntimeBlockState
	Health         *UpstreamHealth
	ModelTransient *providercore.ModelTransientState
}

// ProviderStateContext gives state writes a five-second budget detached from caller cancellation.
// The caller must invoke the returned cancel function.
func ProviderStateContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, 5*time.Second)
}

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
	if s.Throttle != nil {
		allowed := s.Throttle.Allow(providerID, now)
		if !critical && !allowed {
			return
		}
	}

	updates := map[string]any{
		grokQuotaSnapshotExtraKey: snapshot,
	}
	// The scheduler reads grok_sched_* fields to enforce configured automatic-pause thresholds.
	maps.Copy(updates, providercore.BuildGrokSchedulerExtraUpdates(snapshot))
	stateCtx := ctx
	if hasActiveLimit {
		var cancel context.CancelFunc
		stateCtx, cancel = ProviderStateContext(ctx)
		defer cancel()
	}
	// This mutates the request-local record. Callers must not share it across goroutines.
	if value.Extra == nil {
		value.Extra = map[string]any{}
	}
	value.Extra[grokQuotaSnapshotExtraKey] = snapshot
	if s.Store != nil {
		_ = s.Store.UpdateExtra(stateCtx, providerID, updates)
	}
	// Pool mode saves quota observations while the upstream pool handles provider switching.
	// Other providers can install limits from exhausted windows in either successful or failed responses.
	if installRateLimit && hasActiveLimit && !value.IsPoolMode() {
		s.RateLimit(stateCtx, value, resetAt, teamModel)
	} else if recovery {
		providercore.ClearGrokRateLimitAfterRecovery(stateCtx, s.Store, providercore.CloneRecord(value), slog.Warn)
	}
}

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
	s.Runtime.BlockProviderScheduling(value, runtimeUntil, "429")
	providercore.PersistGrokRateLimit(ctx, s.Store, providercore.CloneRecord(value), resetAt, slog.Warn)

	// Share the model cooldown with OAuth providers on the same xAI team.
	// An empty model skips this team-level update.
	if teamModel != "" {
		providercore.MarkGrokTeamModelRateLimit(value, teamModel, providercore.ResolveGrokTeamRateLimitUntil(resetAt, now))
	}
}

func (s *GrokHealth) TempUnschedule(ctx context.Context, value *providercore.Record, cooldown time.Duration, reason string) {
	if s == nil || value == nil {
		return
	}
	until := time.Now().Add(cooldown)
	if value.TempUnschedulableUntil != nil && value.TempUnschedulableUntil.After(until) {
		until = *value.TempUnschedulableUntil
	}
	s.Runtime.BlockProviderScheduling(value, until, reason)
	if s.Store != nil {
		stateCtx, cancel := ProviderStateContext(ctx)
		defer cancel()
		_ = s.Store.SetTempUnschedulable(stateCtx, value.ID, until, reason)
	}
}

// ObserveResponse stamps and saves quota headers, or checks recovery when quota headers are absent.
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
