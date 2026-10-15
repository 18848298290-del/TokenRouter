package provider

import (
	"context"
	"encoding/json"
	"time"
)

// health_recovery.go clears recoverable provider health state and reconstructs
// temporary-pause status for display.
// Provider recovery clears error status, global windows, platform scopes, model
// limits and temporary pauses through ordered store calls. A later primary
// failure can leave earlier successful production writes in place, while error
// cleanup retains the manual scheduling switch. Generic ID-based recovery and
// conditional managed refresh have separate entry points.
//
// Auxiliary cache deletion, invalidation and runtime cleanup can fail or run
// after primary state already changed. Full recovery can reset a counter twice,
// requiring idempotent callbacks. Status display first accepts a future cached
// pause, then reads the column and its mixed structured/plain reason. A JSON
// reason can replace the initial column deadline; only its zero deadline
// inherits the column value, and omitted fields retain Go zero values.

// RecoveryStore supplies usable provider reads and separate primary
// health-cleanup calls for recovery commands.
type RecoveryStore interface {
	// GetByID returns a usable provider on success so recovery can inspect its
	// state. Missing providers and read failures return errors; the caller chooses
	// subsequent primary cleanup from the returned observation.
	GetByID(context.Context, int64) (*Record, error)
	// ClearError attempts active status and empty error text for the provider. The
	// production writer retains the manual scheduling switch and health windows,
	// so subsequent recovery steps remain separate decisions.
	ClearError(context.Context, int64) error
	// ClearRateLimit attempts provider rate-limit and overload column cleanup.
	// Scoped Extra limits and temporary pauses remain separate; errors stop later
	// primary recovery while earlier successful calls can remain applied.
	ClearRateLimit(context.Context, int64) error
	// ClearAntigravityQuotaScopes attempts quota-scope removal for the selected
	// provider. Generic recovery treats it as a primary step, returning its error
	// before later model and temporary cleanup.
	ClearAntigravityQuotaScopes(context.Context, int64) error
	// ClearModelRateLimits attempts removal of model-limit data for the selected
	// provider. Full recovery returns its error, while temporary-only cleanup logs
	// that failure and continues its runtime phase.
	ClearModelRateLimits(context.Context, int64) error
	// ClearTempUnschedulable attempts removal of the persistent deadline and
	// reason for the provider. Cache deletion follows separately, and the
	// production writer can return success even when no row matches.
	ClearTempUnschedulable(context.Context, int64) error
}

// RecoveryOptions supplies clock, diagnostics and auxiliary cleanup callbacks
// for synchronous provider recovery.
type RecoveryOptions struct {
	// Now supplies temporary-status comparison time; NewRecoveryService selects
	// time.Now when this clock callback is absent.
	Now func() time.Time
	// Warn observes auxiliary failures while primary recovery may continue;
	// NewRecoveryService selects a no-op when this callback is absent.
	Warn func(string, ...any)
	// InvalidateToken optionally runs after requested error recovery. Its failure
	// is logged and leaves the earlier primary clear in place.
	InvalidateToken func(context.Context, *Record) error
	// ResetCounter can run twice when RecoverProviderState reaches full limit
	// cleanup. Supply an idempotent callback; absent callbacks skip this auxiliary
	// operation.
	ResetCounter func(context.Context, int64)
	// ClearSchedulingBlock optionally removes runtime state after the selected
	// primary clear succeeds; temporary-only recovery can reach it after logged
	// model/cache failures.
	ClearSchedulingBlock func(int64)
}

// RecoveryService retains primary and auxiliary dependencies for health cleanup
// and temporary-status display.
type RecoveryService struct {
	// providerRepo is borrowed during synchronous recovery calls; its successful
	// reads supply usable records, and the application owns its resource lifetime.
	providerRepo RecoveryStore
	// tempUnschedCache is optional and separate from persistent health. Its read
	// failures propagate, while deletion and repopulation failures are logged by
	// their commands.
	tempUnschedCache TempUnschedCache
	options          RecoveryOptions // normalized recovery callbacks
}

// ProviderRecoveryOptions selects auxiliary work after a primary provider error
// clear.
type ProviderRecoveryOptions struct {
	// InvalidateToken requests the configured invalidator only when error cleanup
	// succeeds; RecoverProviderState imposes no provider-type filter on this
	// requested operation.
	InvalidateToken bool
}

// NewRecoveryService retains the store and optional cache, replacing missing
// clock and logger callbacks with defaults. Construction performs no cleanup;
// supply a usable primary store and keep dependencies available while
// synchronous recovery runs.
func NewRecoveryService(store RecoveryStore, cache TempUnschedCache, options RecoveryOptions) *RecoveryService {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Warn == nil {
		options.Warn = func(string, ...any) {}
	}
	return &RecoveryService{providerRepo: store, tempUnschedCache: cache, options: options}
}

// resetCounter invokes optional counter cleanup for the selected provider.
// A missing callback skips cleanup; a configured callback receives ctx
// synchronously and owns cancellation handling and diagnostics.
func (s *RecoveryService) resetCounter(ctx context.Context, id int64) {
	if s.options.ResetCounter != nil {
		s.options.ResetCounter(ctx, id)
	}
}

// clearSchedulingBlock invokes optional runtime cleanup for the selected
// provider. A configured callback runs synchronously with no context argument,
// so it needs to return before the recovery command can complete.
func (s *RecoveryService) clearSchedulingBlock(id int64) {
	if s.options.ClearSchedulingBlock != nil {
		s.options.ClearSchedulingBlock(id)
	}
}

// ClearRateLimit clears global windows, Antigravity scopes, model limits and
// temporary pause in that order. Supply a usable service/store. A primary error
// stops subsequent calls after earlier successful production writes may already
// be applied.
//
// After primary cleanup, cache deletion is best effort, then counter and
// runtime callbacks run. Nil confirms completed primary calls even when cache
// deletion failed. The core compares no credential identity and opens no
// encompassing transaction; the supplied store determines any wider transaction
// participation across these ID-based calls.
func (s *RecoveryService) ClearRateLimit(ctx context.Context, providerID int64) error {
	// Complete primary calls in their fixed order before auxiliary cleanup. A
	// later failure stops the sequence while earlier successful production writes
	// remain observable through subsequent reads.
	if err := s.providerRepo.ClearRateLimit(ctx, providerID); err != nil {
		return err
	}
	if err := s.providerRepo.ClearAntigravityQuotaScopes(ctx, providerID); err != nil {
		return err
	}
	if err := s.providerRepo.ClearModelRateLimits(ctx, providerID); err != nil {
		return err
	}
	if err := s.providerRepo.ClearTempUnschedulable(ctx, providerID); err != nil {
		return err
	}
	// Cache deletion follows primary cleanup and has its own failure outcome. Log
	// incomplete shared-cache removal and continue this command's remaining
	// auxiliary operations.
	if s.tempUnschedCache != nil {
		if err := s.tempUnschedCache.DeleteTempUnsched(ctx, providerID); err != nil {
			s.options.Warn("temp_unsched_cache_delete_failed", "provider_id", providerID, "error", err)
		}
	}
	s.resetCounter(ctx, providerID)
	s.clearSchedulingBlock(providerID)
	return nil
}

// RecoverProviderState reads a usable provider and clears its observed error
// and recorded runtime health. Error cleanup comes first, with requested
// optional invalidation afterward; window pointers and retained scoped values
// select full limit cleanup.
//
// A later primary failure returns nil and error after earlier successful
// production writes can remain applied. Invalidation and cache failures are
// logged and can leave auxiliary cleanup incomplete despite primary success
// flags. Combined recovery can reset counters twice, while cleared flags select
// runtime cleanup. The manual scheduling switch remains stored unchanged, and
// these calls impose no observed-identity condition.
func (s *RecoveryService) RecoverProviderState(ctx context.Context, providerID int64, options ProviderRecoveryOptions) (*SuccessfulTestRecovery, error) {
	provider, err := s.providerRepo.GetByID(ctx, providerID)
	if err != nil {
		return nil, err
	}

	result := &SuccessfulTestRecovery{}
	// Clear the observed error before optional invalidation and later windows. A
	// later nil recovery result with an error does not roll this earlier
	// successful production call back.
	if provider.Status == StatusError {
		if err := s.providerRepo.ClearError(ctx, providerID); err != nil {
			return nil, err
		}
		result.ClearedError = true
		// Token cleanup is auxiliary after the error clear. Log a failed invalidation
		// and continue window recovery while retaining the completed error-clear flag.
		if options.InvalidateToken && s.options.InvalidateToken != nil {
			if invalidateErr := s.options.InvalidateToken(ctx, provider); invalidateErr != nil {
				s.options.Warn("recover_provider_state_invalidate_token_failed", "provider_id", providerID, "error", invalidateErr)
			}
		}
	}

	if hasRecoverableRuntimeState(provider) {
		if err := s.ClearRateLimit(ctx, providerID); err != nil {
			return nil, err
		}
		result.ClearedRateLimit = true
	}
	// Full window cleanup has already reset counter/runtime state. This completion
	// phase can repeat counter cleanup, while error-only recovery still needs its
	// runtime block removed.
	if result.ClearedError || result.ClearedRateLimit {
		s.resetCounter(ctx, providerID)
		if result.ClearedError && !result.ClearedRateLimit {
			s.clearSchedulingBlock(providerID)
		}
	}

	return result, nil
}

// RecoverProviderAfterSuccessfulTest delegates recovery with token invalidation
// unrequested. Call after accepting a successful probe; this wrapper does not
// inspect that probe and returns the core's primary cleanup flags or failure.
func (s *RecoveryService) RecoverProviderAfterSuccessfulTest(ctx context.Context, providerID int64) (*SuccessfulTestRecovery, error) {
	return s.RecoverProviderState(ctx, providerID, ProviderRecoveryOptions{})
}

// ClearTempUnschedulable clears the primary pause before cache/model cleanup
// and runtime removal. Supply a usable service/store. A primary pause error
// returns immediately after that unsuccessful call.
//
// Cache deletion and model cleanup errors are logged and leave this method's
// result nil, so runtime cleanup still runs after those failures. Earlier
// successful primary storage retains its own outcome. This command skips
// counter reset; full rate-limit recovery owns its separate counter phase.
// Store implementation determines transaction participation across the calls.
func (s *RecoveryService) ClearTempUnschedulable(ctx context.Context, providerID int64) error {
	// Clear the persistent pause before auxiliary work, so a primary failure keeps
	// cache, model and runtime cleanup from running on this command.
	if err := s.providerRepo.ClearTempUnschedulable(ctx, providerID); err != nil {
		return err
	}
	// Cache deletion follows primary cleanup and has its own failure outcome. Log
	// incomplete shared-cache removal and continue this command's remaining
	// auxiliary operations.
	if s.tempUnschedCache != nil {
		if err := s.tempUnschedCache.DeleteTempUnsched(ctx, providerID); err != nil {
			s.options.Warn("temp_unsched_cache_delete_failed", "provider_id", providerID, "error", err)
		}
	}
	// The primary temporary pause has already cleared here. Model cleanup is
	// auxiliary on this command, so log its failure and still reach runtime-block
	// removal.
	if err := s.providerRepo.ClearModelRateLimits(ctx, providerID); err != nil {
		s.options.Warn("clear_model_rate_limits_on_temp_unsched_reset_failed", "provider_id", providerID, "error", err)
	}
	s.clearSchedulingBlock(providerID)
	return nil
}

// hasRecoverableRuntimeState reports recorded window pointers or scoped Extra
// values selecting full cleanup. Nil records fail; expired pointers still
// qualify, and supported empty collections represent absent recorded scope
// state.
func hasRecoverableRuntimeState(provider *Record) bool {
	if provider == nil {
		return false
	}
	if provider.RateLimitedAt != nil || provider.RateLimitResetAt != nil || provider.OverloadUntil != nil || provider.TempUnschedulableUntil != nil {
		return true
	}
	if len(provider.Extra) == 0 {
		return false
	}
	return hasNonEmptyMapValue(provider.Extra, "model_rate_limits") ||
		hasNonEmptyMapValue(provider.Extra, "antigravity_quota_scopes")
}

// hasNonEmptyMapValue tests one Extra value for recoverable scope presence.
// Missing or nil interfaces fail; map[string]any, map[string]string and []any
// use length, while other present representations select cleanup, including
// unsupported typed-nil values.
func hasNonEmptyMapValue(extra map[string]any, key string) bool {
	raw, ok := extra[key]
	if !ok || raw == nil {
		return false
	}
	switch typed := raw.(type) {
	case map[string]any:
		return len(typed) > 0
	case map[string]string:
		return len(typed) > 0
	case []any:
		return len(typed) > 0
	// An unsupported present representation still selects cleanup, letting
	// malformed retained scope data reach the same removal command used for
	// ordinary recorded collections.
	default:
		return true
	}
}

// GetTempUnschedStatus returns a future cached pause or reconstructs display
// state from a future database column. Cache read errors return before database
// fallback. Supply a usable service/store; comparisons use Unix-second
// precision.
//
// A JSON reason replaces the initial state, retaining any nonzero encoded
// deadline even when it differs from the column. Zero inherits the checked
// column deadline, and omitted fields retain Go zero values. Unparseable
// reasons become diagnostic text. A cache hit returns its pointer directly with
// cache-defined ownership; database reconstruction allocates state. Optional
// recaching failure is logged and leaves the reconstructed response available.
func (s *RecoveryService) GetTempUnschedStatus(ctx context.Context, providerID int64) (*TempUnschedState, error) {
	// Compare cache and column deadlines at Unix-second precision; a database
	// deadline within this current second is therefore considered elapsed for
	// status display.
	now := s.options.Now().Unix()
	if s.tempUnschedCache != nil {
		state, err := s.tempUnschedCache.GetTempUnsched(ctx, providerID)
		if err != nil {
			return nil, err
		}
		if state != nil && state.UntilUnix > now {
			return state, nil
		}
	}

	// Missing or expired shared state permits a persistent fallback. Check the
	// column deadline before decoding its reason so retained diagnostics alone
	// cannot reconstruct an elapsed pause.
	provider, err := s.providerRepo.GetByID(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if provider.TempUnschedulableUntil == nil {
		return nil, nil
	}
	if provider.TempUnschedulableUntil.Unix() <= now {
		return nil, nil
	}

	state := &TempUnschedState{
		UntilUnix: provider.TempUnschedulableUntil.Unix(),
	}

	if provider.TempUnschedulableReason != "" {
		var parsed TempUnschedState
		if err := json.Unmarshal([]byte(provider.TempUnschedulableReason), &parsed); err == nil {
			// Fill an omitted or zero reason deadline from the checked column. A nonzero
			// encoded deadline remains authoritative for this returned state, while
			// other omitted fields keep Go zero values.
			if parsed.UntilUnix == 0 {
				parsed.UntilUnix = state.UntilUnix
			}
			state = &parsed
		} else {
			// Retain unparseable stored text with the column deadline so older plain
			// reasons remain displayable through the structured status response.
			state.ErrorMessage = provider.TempUnschedulableReason
		}
	}

	// Repopulate cache after constructing display state. Cache failure is logged
	// separately and leaves the reconstructed response available independently of
	// whether shared storage accepted it.
	if s.tempUnschedCache != nil {
		if err := s.tempUnschedCache.SetTempUnsched(ctx, providerID, state); err != nil {
			s.options.Warn("temp_unsched_cache_set_failed", "provider_id", providerID, "error", err)
		}
	}

	return state, nil
}
