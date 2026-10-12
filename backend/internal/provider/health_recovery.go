package provider

import (
	"context"
	"encoding/json"
	"time"
)

// RecoveryStore supplies independent writes for clearing provider health state.
type RecoveryStore interface {
	GetByID(context.Context, int64) (*Record, error)
	ClearError(context.Context, int64) error
	ClearRateLimit(context.Context, int64) error
	ClearAntigravityQuotaScopes(context.Context, int64) error
	ClearModelRateLimits(context.Context, int64) error
	ClearTempUnschedulable(context.Context, int64) error
}

// RecoveryOptions supplies token invalidation, counter reset, and scheduling-block cleanup callbacks.
type RecoveryOptions struct {
	Now                  func() time.Time
	Warn                 func(string, ...any)
	InvalidateToken      func(context.Context, *Record) error
	ResetCounter         func(context.Context, int64)
	ClearSchedulingBlock func(int64)
}

// RecoveryService applies recovery rules for manual resets, successful tests, and quota windows.
type RecoveryService struct {
	providerRepo     RecoveryStore
	tempUnschedCache TempUnschedCache
	options          RecoveryOptions
}

// ProviderRecoveryOptions controls optional token invalidation during recovery.
type ProviderRecoveryOptions struct {
	InvalidateToken bool
}

func NewRecoveryService(store RecoveryStore, cache TempUnschedCache, options RecoveryOptions) *RecoveryService {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Warn == nil {
		options.Warn = func(string, ...any) {}
	}
	return &RecoveryService{providerRepo: store, tempUnschedCache: cache, options: options}
}

func (s *RecoveryService) resetCounter(ctx context.Context, id int64) {
	if s.options.ResetCounter != nil {
		s.options.ResetCounter(ctx, id)
	}
}

func (s *RecoveryService) clearSchedulingBlock(id int64) {
	if s.options.ClearSchedulingBlock != nil {
		s.options.ClearSchedulingBlock(id)
	}
}

// ClearRateLimit clears provider, quota-scope, and model limits, then temporary pauses and cached blocks.
func (s *RecoveryService) ClearRateLimit(ctx context.Context, providerID int64) error {
	if err := s.providerRepo.ClearRateLimit(ctx, providerID); err != nil {
		return err
	}
	if err := s.providerRepo.ClearAntigravityQuotaScopes(ctx, providerID); err != nil {
		return err
	}
	if err := s.providerRepo.ClearModelRateLimits(ctx, providerID); err != nil {
		return err
	}
	// Clear temporary pauses too so a quota reset can restore scheduling.
	if err := s.providerRepo.ClearTempUnschedulable(ctx, providerID); err != nil {
		return err
	}
	if s.tempUnschedCache != nil {
		if err := s.tempUnschedCache.DeleteTempUnsched(ctx, providerID); err != nil {
			s.options.Warn("temp_unsched_cache_delete_failed", "provider_id", providerID, "error", err)
		}
	}
	s.resetCounter(ctx, providerID)
	s.clearSchedulingBlock(providerID)
	return nil
}

// RecoverProviderState clears an error and recoverable runtime limits found on the provider.
func (s *RecoveryService) RecoverProviderState(ctx context.Context, providerID int64, options ProviderRecoveryOptions) (*SuccessfulTestRecovery, error) {
	provider, err := s.providerRepo.GetByID(ctx, providerID)
	if err != nil {
		return nil, err
	}

	result := &SuccessfulTestRecovery{}
	if provider.Status == StatusError {
		if err := s.providerRepo.ClearError(ctx, providerID); err != nil {
			return nil, err
		}
		result.ClearedError = true
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
	if result.ClearedError || result.ClearedRateLimit {
		s.resetCounter(ctx, providerID)
		if result.ClearedError && !result.ClearedRateLimit {
			s.clearSchedulingBlock(providerID)
		}
	}

	return result, nil
}

// RecoverProviderAfterSuccessfulTest clears recoverable error, rate-limit, overload,
// temporary-pause, and model-limit state after a successful probe.
func (s *RecoveryService) RecoverProviderAfterSuccessfulTest(ctx context.Context, providerID int64) (*SuccessfulTestRecovery, error) {
	return s.RecoverProviderState(ctx, providerID, ProviderRecoveryOptions{})
}

func (s *RecoveryService) ClearTempUnschedulable(ctx context.Context, providerID int64) error {
	if err := s.providerRepo.ClearTempUnschedulable(ctx, providerID); err != nil {
		return err
	}
	if s.tempUnschedCache != nil {
		if err := s.tempUnschedCache.DeleteTempUnsched(ctx, providerID); err != nil {
			s.options.Warn("temp_unsched_cache_delete_failed", "provider_id", providerID, "error", err)
		}
	}
	if err := s.providerRepo.ClearModelRateLimits(ctx, providerID); err != nil {
		s.options.Warn("clear_model_rate_limits_on_temp_unsched_reset_failed", "provider_id", providerID, "error", err)
	}
	s.clearSchedulingBlock(providerID)
	return nil
}

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
	default:
		return true
	}
}

func (s *RecoveryService) GetTempUnschedStatus(ctx context.Context, providerID int64) (*TempUnschedState, error) {
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
			if parsed.UntilUnix == 0 {
				parsed.UntilUnix = state.UntilUnix
			}
			state = &parsed
		} else {
			state.ErrorMessage = provider.TempUnschedulableReason
		}
	}

	if s.tempUnschedCache != nil {
		if err := s.tempUnschedCache.SetTempUnsched(ctx, providerID, state); err != nil {
			s.options.Warn("temp_unsched_cache_set_failed", "provider_id", providerID, "error", err)
		}
	}

	return state, nil
}
