package provider

import (
	"context"
	"fmt"
	"time"
)

// Antigravity health handling records exhausted INTERNAL 500 retry rounds
// separately from model and AI Credits cooldowns. One and two counted rounds
// choose temporary pauses; later rounds write error status, while successful
// responses can reset the shared counter before another exhausted round.
//
// Model writes precede local cache publication, and a missing publisher skips
// local model-limit mutation entirely. AI Credits clearing instead deletes the
// borrowed local limit before persistence and retains that deletion even if
// storage fails, with no publication from the clear method. Model-key
// expansion can produce several independent writes and returns success when
// any succeeds, so partial accepted limits remain. Callers synchronize
// Record.Extra and retained callback dependencies while these operations run;
// diagnostics report failures without rolling back earlier successful work.

// Antigravity health constants select exhausted-round escalation durations and
// the synthetic AI Credits model-limit entry used by existing scheduling
// expiry checks.
const (
	// Exhausted INTERNAL 500 round counts choose temporary pauses before the
	// threshold switches later failures to persistent error status.
	Internal500PenaltyTier1Duration  = 30 * time.Minute // First round: pause for 30 minutes.
	Internal500PenaltyTier2Duration  = 2 * time.Hour    // Second round: pause for two hours.
	Internal500PenaltyTier3Threshold = 3                // Third and later rounds: set error status.

	// CreditsExhaustedKey identifies exhausted AI Credits in model_rate_limits.
	// It uses the same storage and expiry checks as model rate limits.
	CreditsExhaustedKey      = "AICredits"
	CreditsExhaustedDuration = 5 * time.Hour
)

// AntigravityHealthStore persists provider error, temporary pause, and
// model-limit observations.
type AntigravityHealthStore interface {
	// SetError records the supplied provider error for later scheduling and
	// recovery.
	SetError(context.Context, int64, string) error
	// SetTempUnschedulable stores a provider pause until the supplied observation
	// deadline.
	SetTempUnschedulable(context.Context, int64, time.Time, string) error
	// SetModelRateLimit stores the supplied model key and reset observation.
	// Health callers treat nil error as accepted storage before local publication,
	// with optional source strings left to the store implementation.
	SetModelRateLimit(context.Context, int64, string, time.Time, ...string) error
	// UpdateExtra stores the supplied provider metadata patch through its
	// implementation.
	UpdateExtra(context.Context, int64, map[string]any) error
}

// AntigravityHealth binds persistent health writes, exhausted-round counters,
// and snapshot publication.
type AntigravityHealth struct {
	// Store is required for reached persistence and remains usable through
	// callback completion. Individual writes can succeed independently of local
	// mutation or publication, so later failure does not imply rollback.
	Store AntigravityHealthStore
	// Counter optionally records exhausted retry rounds. Missing counter skips
	// increment/reset work, and its implementation owns concurrent count updates.
	Counter Internal500CounterCache
	// Publish optionally publishes a borrowed mutated record. Its absence
	// also disables local model-limit mutation in
	// UpdateProviderModelRateLimitInCache.
	Publish func(context.Context, *Record) error
	// ModelKeys is required by model expansion and returns the keys attempted
	// independently. Empty expansion reports failure before any storage write.
	ModelKeys func(string) []string
	// Error, Warn, and Info are required on their reached penalty paths.
	// Error reports counter/write failure, Warn reports stronger accepted
	// penalties, and Info reports accepted first-round scheduling pauses.
	// Error: penalty failure diagnostics; Warn: stronger penalty diagnostics;
	// Info: first-round pause diagnostics.
	Error, Warn, Info func(string, ...any)
	// Logf is required by reached model/credits diagnostics. Publication and
	// persistence failures are recorded while previously accepted work remains
	// in its durable or caller-owned local state.
	Logf func(string, ...any)
}

// Internal500CounterCache records exhausted retry-round counts separately from
// scheduling state.
type Internal500CounterCache interface {
	// IncrementInternal500Count atomically increments and returns the row round
	// count.
	// Failure prevents penalty selection here, while accepted counts receive
	// no rollback after a later scheduling failure. Production Redis retries can
	// repeat server increments after an uncertain reply.
	IncrementInternal500Count(ctx context.Context, providerID int64) (int64, error)
	// ResetInternal500Count clears the row round count after response success.
	// Health handling tolerates its error with diagnostics, so implementations
	// report failure without implying that provider scheduling state changed.
	ResetInternal500Count(ctx context.Context, providerID int64) error
}

// ApplyInternal500Penalty persists a penalty for a stable nonnil record and
// supplied round count. Store and reached Error/Warn/Info callbacks are
// required; count below one produces no write.
//
// Exactly one and two choose their thirty-minute and two-hour pauses from the
// current clock. At least three sets persistent error status, whose later
// recovery is owned elsewhere rather than a time-based pause here. Storage
// failure is logged and stops that branch; accepted writes only emit
// diagnostics and do not mutate or publish the supplied record. Prefix is
// unused in this operation.
func (s *AntigravityHealth) ApplyInternal500Penalty(
	ctx context.Context, prefix string, provider *Record, count int64,
) {
	// Write the selected health state before its acceptance diagnostic. A failed
	// write emits only the failure diagnostic and leaves this borrowed record
	// untouched, so logs distinguish rejected persistence from accepted penalties.
	switch {
	case count >= int64(Internal500PenaltyTier3Threshold):
		reason := fmt.Sprintf("INTERNAL 500 consecutive failures: %d rounds", count)
		if err := s.Store.SetError(ctx, provider.ID, reason); err != nil {
			s.Error("internal500_set_error_failed", "provider_id", provider.ID, "error", err)
			return
		}
		s.Warn("internal500_provider_disabled",
			"provider_id", provider.ID, "provider_name", provider.Name, "consecutive_count", count)
	case count == 2:
		until := time.Now().Add(Internal500PenaltyTier2Duration)
		reason := fmt.Sprintf("INTERNAL 500 x%d (temp unsched %v)", count, Internal500PenaltyTier2Duration)
		if err := s.Store.SetTempUnschedulable(ctx, provider.ID, until, reason); err != nil {
			s.Error("internal500_temp_unsched_failed", "provider_id", provider.ID, "error", err)
			return
		}
		s.Warn("internal500_temp_unschedulable",
			"provider_id", provider.ID, "provider_name", provider.Name,
			"duration", Internal500PenaltyTier2Duration, "consecutive_count", count)
	case count == 1:
		until := time.Now().Add(Internal500PenaltyTier1Duration)
		reason := fmt.Sprintf("INTERNAL 500 x%d (temp unsched %v)", count, Internal500PenaltyTier1Duration)
		if err := s.Store.SetTempUnschedulable(ctx, provider.ID, until, reason); err != nil {
			s.Error("internal500_temp_unsched_failed", "provider_id", provider.ID, "error", err)
			return
		}
		s.Info("internal500_temp_unschedulable",
			"provider_id", provider.ID, "provider_name", provider.Name,
			"duration", Internal500PenaltyTier1Duration, "consecutive_count", count)
	}
}

// HandleInternal500RetryExhausted increments the shared counter before penalty
// selection. Nil Counter skips work; otherwise a nonnil stable provider and
// reached Error are required, and increment failure leaves scheduling state
// unchanged.
//
// The count is recorded independently of the subsequent health write, so a
// penalty storage failure does not undo the counted round. Redis command
// retries can repeat a server increment after an uncertain response; one
// interface invocation need not equal one stored increment.
// ApplyInternal500Penalty then chooses its tier using that returned count.
// Counter implementation owns atomic increment; this method owns sequencing
// and diagnostic handling while preserving earlier counted work.
func (s *AntigravityHealth) HandleInternal500RetryExhausted(
	ctx context.Context, prefix string, provider *Record,
) {
	if s.Counter == nil {
		return
	}
	// Count the exhausted round before selecting its penalty so later state
	// write failure still leaves that round observed by the shared counter.
	count, err := s.Counter.IncrementInternal500Count(ctx, provider.ID)
	if err != nil {
		s.Error("internal500_counter_increment_failed",
			"prefix", prefix, "provider_id", provider.ID, "error", err)
		return
	}
	s.ApplyInternal500Penalty(ctx, prefix, provider, count)
}

// ResetInternal500Counter best-effort clears the selected row counter after
// success. Nil Counter skips work, and a reset failure is logged through
// required Error without changing health state or propagating an error result.
func (s *AntigravityHealth) ResetInternal500Counter(
	ctx context.Context, prefix string, providerID int64,
) {
	if s.Counter == nil {
		return
	}
	// Diagnose a failed count reset without changing provider health or returning
	// an error, keeping this success-path bookkeeping separate from scheduling.
	if err := s.Counter.ResetInternal500Count(ctx, providerID); err != nil {
		s.Error("internal500_counter_reset_failed",
			"prefix", prefix, "provider_id", providerID, "error", err)
	}
}

// SetCreditsExhausted stores a five-hour synthetic AI Credits model limit for
// a stable record. Nil or zero ID skips work; reached Store and Logf are
// required.
//
// Durable failure is logged and prevents local publication. Success delegates
// local mutation to UpdateProviderModelRateLimitInCache, which only changes
// Extra when Publish is configured. Publication failure leaves the durable
// marker and already changed local record in place. This method reports those
// observations through diagnostics and performs no rollback of accepted
// storage.
func (s *AntigravityHealth) SetCreditsExhausted(ctx context.Context, provider *Record) {
	if provider == nil || provider.ID == 0 {
		return
	}
	// Store the synthetic model limit before local mutation/publication so a
	// rejected durable write cannot advertise an accepted credits cooldown.
	resetAt := time.Now().Add(CreditsExhaustedDuration)
	if err := s.Store.SetModelRateLimit(ctx, provider.ID, CreditsExhaustedKey, resetAt); err != nil {
		s.Logf("set credits exhausted failed: provider=%d err=%v", provider.ID, err)
		return
	}
	s.UpdateProviderModelRateLimitInCache(ctx, provider, CreditsExhaustedKey, resetAt)
	s.Logf("credits_exhausted_marked provider=%d reset_at=%s",
		provider.ID, resetAt.UTC().Format(time.RFC3339))
}

// ClearCreditsExhausted removes a synthetic AI Credits entry from a stable
// caller-owned record. Nil/zero ID, absent Extra, differently typed limits, or
// no marker skips work.
//
// The borrowed nested limits map is deleted locally before Store.UpdateExtra.
// Storage error is logged and retains the local deletion, so the supplied
// record can differ from durable state afterward. Other model entries stay in
// that map, and this method performs no snapshot publication. Callers
// synchronize shared Extra mutation and provide reached Store and Logf.
func (s *AntigravityHealth) ClearCreditsExhausted(ctx context.Context, provider *Record) {
	if provider == nil || provider.ID == 0 || provider.Extra == nil {
		return
	}
	rawLimits, ok := provider.Extra["model_rate_limits"].(map[string]any)
	if !ok {
		return
	}
	if _, exists := rawLimits[CreditsExhaustedKey]; !exists {
		return
	}
	// Delete from the borrowed local map before persisting its replacement.
	// Storage failure retains this deletion and no publication follows here,
	// allowing the caller to observe local/durable divergence after failure.
	delete(rawLimits, CreditsExhaustedKey)
	provider.Extra["model_rate_limits"] = rawLimits
	if err := s.Store.UpdateExtra(ctx, provider.ID, map[string]any{
		"model_rate_limits": rawLimits,
	}); err != nil {
		s.Logf("clear credits exhausted failed: provider=%d err=%v", provider.ID, err)
	}
}

// SetModelRateLimitByModelName writes the supplied model text directly as a
// storage key. Nil repository or exactly empty model returns false; reached
// logf is required, failure returns false, and accepted storage returns true
// after its smart-retry diagnostic.
func SetModelRateLimitByModelName(ctx context.Context, repo AntigravityHealthStore, providerID int64, modelName, prefix string, statusCode int, resetAt time.Time, afterSmartRetry bool, logf func(string, ...any)) bool {
	if repo == nil || modelName == "" {
		return false
	}
	// Persist exact model text before reporting success so this key remains
	// associated with the upstream observation instead of a separate scope label.
	if err := repo.SetModelRateLimit(ctx, providerID, modelName, resetAt); err != nil {
		logf("%s status=%d model_rate_limit_failed model=%s error=%v", prefix, statusCode, modelName, err)
		return false
	}
	// Truncate diagnostic duration to seconds; the stored reset keeps precision.
	if afterSmartRetry {
		logf("%s status=%d model_rate_limited_after_smart_retry model=%s provider=%d reset_in=%v", prefix, statusCode, modelName, providerID, time.Until(resetAt).Truncate(time.Second))
	} else {
		logf("%s status=%d model_rate_limited model=%s provider=%d reset_in=%v", prefix, statusCode, modelName, providerID, time.Until(resetAt).Truncate(time.Second))
	}
	return true
}

// SetAntigravityModelRateLimits expands model text through required ModelKeys
// and attempts each key independently. Nil record/repository or no keys
// returns false; the stable mutable record and reached Logf are caller
// obligations.
//
// Each accepted durable write can update the local limit and publish it before
// the next key is attempted. Failure of another key retains earlier writes,
// and the result is true when any key succeeded rather than only when all did.
// Missing Publish skips local mutation but still permits durable success.
// Callers synchronize Extra and retain dependencies throughout this sequence.
func (s *AntigravityHealth) SetAntigravityModelRateLimits(ctx context.Context, repo AntigravityHealthStore, provider *Record, modelName, prefix string, statusCode int, resetAt time.Time, afterSmartRetry bool) bool {
	if provider == nil || repo == nil {
		return false
	}
	keys := s.ModelKeys(modelName)
	if len(keys) == 0 {
		return false
	}

	success := false
	for _, key := range keys {
		if SetModelRateLimitByModelName(ctx, repo, provider.ID, key, prefix, statusCode, resetAt, afterSmartRetry, s.Logf) {
			s.UpdateProviderModelRateLimitInCache(ctx, provider, key, resetAt)
			success = true
		}
	}
	return success
}

// UpdateProviderModelRateLimitInCache mutates a stable caller-owned
// model-limit map and publishes the record. Nil Publish, nil record, or empty
// key returns before any local allocation or mutation.
//
// The chosen key receives current UTC observation time and supplied reset text
// before publication. A missing or differently typed limits map is replaced
// with a new map, while existing unrelated entries remain. Publish receives
// the borrowed record; its failure is diagnosed through Logf and leaves the
// local changed values in place. Callers synchronize record sharing and retain
// it through the callback.
func (s *AntigravityHealth) UpdateProviderModelRateLimitInCache(ctx context.Context, provider *Record, modelKey string, resetAt time.Time) {
	if s.Publish == nil || provider == nil || modelKey == "" {
		return
	}

	// Prepare the local model-limit entry before publishing this borrowed
	// record. An existing incompatible limits representation is replaced so
	// scheduling consumers can read the resulting typed map.
	if provider.Extra == nil {
		provider.Extra = make(map[string]any)
	}

	limits, _ := provider.Extra["model_rate_limits"].(map[string]any)
	if limits == nil {
		limits = make(map[string]any)
		provider.Extra["model_rate_limits"] = limits
	}

	limits[modelKey] = map[string]any{
		"rate_limited_at":     time.Now().UTC().Format(time.RFC3339),
		"rate_limit_reset_at": resetAt.UTC().Format(time.RFC3339),
	}

	// Publish after local mutation so the consumer sees the selected reset.
	// Failure leaves those caller-owned values in place and is reported separately
	// from the durable write performed by the surrounding operation.
	if err := s.Publish(ctx, provider); err != nil {
		s.Logf("[antigravity-Forward] cache_update_failed provider=%d model=%s err=%v", provider.ID, modelKey, err)
	}
}
