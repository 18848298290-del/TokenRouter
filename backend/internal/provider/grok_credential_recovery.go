package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	grokCredentialMutationTimeout     = 5 * time.Second
	grokCredentialMutationConfirmWait = 250 * time.Millisecond
	grokCredentialCacheCleanupTimeout = 500 * time.Millisecond
)

var ErrGrokCredentialStateUpdateFailed = errors.New("grok oauth provider state update failed")

// GrokCredentialMutation describes a classified credential-state update and the snapshot it applies to.
type GrokCredentialMutation struct {
	Permanent     bool
	Transient     bool
	VerifyMissing bool
	VerifyProxy   bool
	Reason        string
	Snapshot      *CredentialMutationSnapshot `json:"-"`
}

// GrokCredentialStateWriter conditionally writes state when the credential snapshot still matches.
type GrokCredentialStateWriter interface {
	SetGrokCredentialErrorIfMatch(context.Context, int64, CredentialMutationSnapshot, string) (bool, error)
	SetGrokCredentialTempUnschedulableIfMatch(context.Context, int64, CredentialMutationSnapshot, time.Time, string) (bool, error)
}

// GrokCredentialRecovery shares provider locks and runtime blocks across request paths.
// Persistent writes, commit verification, and cache invalidation have separate time budgets.
type GrokCredentialRecovery struct {
	Read       func(context.Context, int64) (*Record, error)
	State      GrokCredentialStateWriter
	Invalidate func(context.Context, *Record) error
	Runtime    *RuntimeBlockState
	Warn       func(string, ...any)
	locks      sync.Map
}

// @project-doc docs/interfaces/grok_upstream.md#grok_account_contract
func (s *GrokCredentialRecovery) Apply(ctx context.Context, value *Record, class GrokCredentialMutation) (string, error) {
	if s == nil || value == nil || ctx == nil || ctx.Err() != nil {
		if ctx != nil {
			return "", ctx.Err()
		}
		return "", context.Canceled
	}
	mutationMu := s.mutationLock(value.ID)
	if err := mutationMu.Lock(ctx); err != nil {
		return "", err
	}
	defer mutationMu.Unlock()
	stateRepo, hasConditionalStateRepo := s.State, s.State != nil
	snapshot := GrokCredentialMutationSnapshot(CloneRecord(value))
	if class.Snapshot != nil {
		snapshot = *class.Snapshot
	}
	if token, err := s.validateCurrent(ctx, value.ID, snapshot, class); err != nil || token != "" {
		return token, err
	}

	if class.Permanent {
		if token, ok := s.concurrentlyRefreshedToken(ctx, value.ID, snapshot); ok {
			return token, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		rollbackRuntime := s.blockRuntime(value, time.Time{}, class.Reason)
		keepRuntimeBlock := false
		runtimeRollbackDone := false
		defer func() {
			if !keepRuntimeBlock && !runtimeRollbackDone {
				rollbackRuntime()
			}
		}()
		if s.Read == nil {
			keepRuntimeBlock = true
			return "", fmt.Errorf("%w: provider repository is not configured", ErrGrokCredentialStateUpdateFailed)
		}
		if !hasConditionalStateRepo {
			keepRuntimeBlock = true
			return "", fmt.Errorf("%w: conditional provider repository is not configured", ErrGrokCredentialStateUpdateFailed)
		}
		stateCtx, cancel := context.WithTimeout(ctx, grokCredentialMutationTimeout)
		if err := ctx.Err(); err != nil {
			cancel()
			return "", err
		}
		updated, err := stateRepo.SetGrokCredentialErrorIfMatch(stateCtx, value.ID, snapshot, class.Reason)
		requestErr := ctx.Err()
		cancel()
		if err != nil {
			s.warn("grok_credential_failure.set_error_failed", "provider_id", value.ID, "reason", class.Reason, "error", err)
			if requestErr != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				if s.mutationCommitted(value.ID, class, time.Time{}) {
					updated = true
				} else if requestErr != nil {
					return "", requestErr
				} else {
					keepRuntimeBlock = true
					return "", fmt.Errorf("%w: permanent state commit could not be confirmed: %v", ErrGrokCredentialStateUpdateFailed, err)
				}
			} else {
				keepRuntimeBlock = true
				return "", fmt.Errorf("%w: persist permanent state: %v", ErrGrokCredentialStateUpdateFailed, err)
			}
		}
		if !updated {
			rollbackRuntime()
			runtimeRollbackDone = true
			return s.resolveCASMiss(ctx, value.ID, snapshot)
		}
		// Keep the runtime block after the persistent error state has committed.
		keepRuntimeBlock = true
		if s.Invalidate == nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", fmt.Errorf("%w: token provider is not configured", ErrGrokCredentialStateUpdateFailed)
		}
		invalidateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grokCredentialCacheCleanupTimeout)
		err = s.Invalidate(invalidateCtx, CloneRecord(value))
		cancel()
		if err != nil {
			s.warn("grok_credential_failure.invalidate_token_failed", "provider_id", value.ID, "reason", class.Reason, "error", err)
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", fmt.Errorf("%w: invalidate cached credential: %v", ErrGrokCredentialStateUpdateFailed, err)
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", nil
	}

	if class.Transient {
		until := time.Now().Add(TokenRefreshTempUnschedDuration)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		rollbackRuntime := s.blockRuntime(value, until, class.Reason)
		keepRuntimeBlock := false
		runtimeRollbackDone := false
		defer func() {
			if !keepRuntimeBlock && !runtimeRollbackDone {
				rollbackRuntime()
			}
		}()
		stateCtx, cancel := context.WithTimeout(ctx, grokCredentialMutationTimeout)
		if s.Read == nil {
			cancel()
			keepRuntimeBlock = true
			return "", fmt.Errorf("%w: provider repository is not configured", ErrGrokCredentialStateUpdateFailed)
		}
		if !hasConditionalStateRepo {
			cancel()
			keepRuntimeBlock = true
			return "", fmt.Errorf("%w: conditional provider repository is not configured", ErrGrokCredentialStateUpdateFailed)
		}
		if err := ctx.Err(); err != nil {
			cancel()
			return "", err
		}
		updated, err := stateRepo.SetGrokCredentialTempUnschedulableIfMatch(stateCtx, value.ID, snapshot, until, class.Reason)
		requestErr := ctx.Err()
		cancel()
		if err != nil {
			s.warn("grok_credential_failure.set_temp_unschedulable_failed", "provider_id", value.ID, "reason", class.Reason, "error", err)
			if requestErr != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				if s.mutationCommitted(value.ID, class, until) {
					updated = true
				} else if requestErr != nil {
					return "", requestErr
				} else {
					keepRuntimeBlock = true
					return "", fmt.Errorf("%w: transient state commit could not be confirmed: %v", ErrGrokCredentialStateUpdateFailed, err)
				}
			} else {
				keepRuntimeBlock = true
				return "", fmt.Errorf("%w: persist transient state: %v", ErrGrokCredentialStateUpdateFailed, err)
			}
		}
		if !updated {
			rollbackRuntime()
			runtimeRollbackDone = true
			return s.resolveCASMiss(ctx, value.ID, snapshot)
		}
		// Keep the runtime block after the temporary scheduling pause has committed.
		keepRuntimeBlock = true
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", nil
	}

	return "", nil
}

func (s *GrokCredentialRecovery) validateCurrent(
	ctx context.Context,
	providerID int64,
	snapshot CredentialMutationSnapshot,
	class GrokCredentialMutation,
) (string, error) {
	if s == nil || s.Read == nil || providerID <= 0 || ctx == nil || ctx.Err() != nil {
		if ctx != nil {
			return "", ctx.Err()
		}
		return "", context.Canceled
	}
	checkCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	latest, err := s.Read(checkCtx, providerID)
	if err != nil {
		if errors.Is(err, ErrProviderNotFound) {
			return "", ErrRefreshProviderStateChanged
		}
		return "", fmt.Errorf("%w: %v", ErrRefreshProviderRereadFailed, err)
	}
	if latest == nil || !latest.IsGrokOAuth() || !latest.IsSchedulable() || s.blocked(latest) {
		return "", ErrRefreshProviderStateChanged
	}
	latestSnapshot := GrokCredentialMutationSnapshot(latest)
	if latestSnapshot.CredentialsJSON != snapshot.CredentialsJSON ||
		!GrokCredentialProxyIDsEqual(latestSnapshot.ProxyID, snapshot.ProxyID) {
		if token, ok := s.concurrentlyRefreshedToken(ctx, providerID, snapshot); ok {
			return token, nil
		}
		return "", ErrRefreshProviderStateChanged
	}

	// The loaded proxy object is outside the provider-row CAS comparison.
	// Recheck it so recovery of the same proxy ID takes precedence over a stale missing-proxy failure.
	if class.VerifyProxy {
		if latest.ProxyID == nil || latest.Proxy != nil {
			return "", ErrRefreshProviderStateChanged
		}
	} else if latest.ProxyID != nil && latest.Proxy == nil {
		return "", ErrRefreshProviderStateChanged
	}

	if class.VerifyMissing {
		expiresAt := latest.GetCredentialAsTime("expires_at")
		credentialsStillMissing := strings.TrimSpace(latest.GetGrokAccessToken()) == "" ||
			strings.TrimSpace(latest.GetGrokRefreshToken()) == "" || expiresAt == nil || !time.Now().Before(*expiresAt)
		if !credentialsStillMissing {
			return "", ErrRefreshProviderStateChanged
		}
	}
	return "", nil
}

func (s *GrokCredentialRecovery) mutationLock(providerID int64) *RefreshLock {
	actual, _ := s.locks.LoadOrStore(providerID, NewRefreshLock())
	mu, ok := actual.(*RefreshLock)
	if !ok {
		mu = NewRefreshLock()
		s.locks.Store(providerID, mu)
	}
	return mu
}

func (s *GrokCredentialRecovery) mutationCommitted(providerID int64, class GrokCredentialMutation, until time.Time) bool {
	if s == nil || s.Read == nil || providerID <= 0 {
		return false
	}
	confirmCtx, cancel := context.WithTimeout(context.Background(), grokCredentialMutationConfirmWait)
	defer cancel()
	latest, err := s.Read(confirmCtx, providerID)
	if err != nil || latest == nil {
		return false
	}
	if class.Permanent {
		return latest.Status == StatusError && !latest.Schedulable && latest.ErrorMessage == class.Reason
	}
	if class.Transient {
		return latest.TempUnschedulableUntil != nil && !latest.TempUnschedulableUntil.Before(until) &&
			latest.TempUnschedulableReason == class.Reason
	}
	return false
}

func (s *GrokCredentialRecovery) resolveCASMiss(ctx context.Context, providerID int64, snapshot CredentialMutationSnapshot) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if token, ok := s.concurrentlyRefreshedToken(ctx, providerID, snapshot); ok {
		return token, nil
	}
	return "", ErrRefreshProviderStateChanged
}

func (s *GrokCredentialRecovery) blockRuntime(value *Record, until time.Time, reason string) func() {
	if s == nil || value == nil {
		return func() {}
	}
	return s.Runtime.BlockRollback(value.ID, until, reason)
}

func (s *GrokCredentialRecovery) concurrentlyRefreshedToken(ctx context.Context, providerID int64, baseline CredentialMutationSnapshot) (string, bool) {
	if s == nil || s.Read == nil || providerID <= 0 || ctx == nil || ctx.Err() != nil {
		return "", false
	}
	checkCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	latest, err := s.Read(checkCtx, providerID)
	if err != nil || latest == nil {
		return "", false
	}
	latestSnapshot := GrokCredentialMutationSnapshot(latest)
	if !GrokCredentialProxyIDsEqual(latestSnapshot.ProxyID, baseline.ProxyID) ||
		latestSnapshot.CredentialsJSON == baseline.CredentialsJSON || !latest.IsSchedulable() ||
		(latest.ProxyID != nil && latest.Proxy == nil) || s.blocked(latest) {
		return "", false
	}
	latestToken := strings.TrimSpace(latest.GetGrokAccessToken())
	if latestToken == "" || strings.TrimSpace(latest.GetGrokRefreshToken()) == "" {
		return "", false
	}
	expiresAt := latest.GetCredentialAsTime("expires_at")
	if expiresAt == nil || !time.Now().Before(*expiresAt) {
		return "", false
	}
	return latestToken, true
}

func (s *GrokCredentialRecovery) blocked(value *Record) bool {
	if s == nil || value == nil || (value.Platform != PlatformOpenAI && value.Platform != PlatformGrok) {
		return false
	}
	return s.Runtime.Blocked(value.ID, func() string { return RefreshCredentialIdentity(value) })
}

func (s *GrokCredentialRecovery) warn(message string, values ...any) {
	if s.Warn != nil {
		s.Warn(message, values...)
	}
}
