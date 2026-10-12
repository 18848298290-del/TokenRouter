package provider

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
)

// RefreshTokenOperation exchanges credentials for the direct refresh path.
type RefreshTokenOperation interface {
	Refresh(context.Context, *Record) (map[string]any, error)
}
type ProviderRefreshAttemptGate interface {
	RefreshAttemptGate
	AcquireRate(context.Context) (func(), error)
}

// GrokRefreshMutationWriter conditionally records Grok failures against the attempted credentials and proxy.
type GrokRefreshMutationWriter interface {
	SetGrokOAuthRefreshErrorIfCredentialsUnchanged(context.Context, int64, map[string]any, *int64, string) (bool, error)
	SetGrokOAuthRefreshTempUnschedulableIfCredentialsUnchanged(context.Context, int64, map[string]any, *int64, time.Time, string) (bool, error)
}

// RefreshAttempts runs one provider's attempts, budgets, retries, failure handling, and post-refresh actions
// using the injected API.
type RefreshAttempts struct {
	API                               *OAuthRefreshAPI
	Tuning                            *RefreshTuning
	Policy                            BackgroundRefreshPolicy
	AttemptTimeout                    time.Duration
	Now                               func() time.Time
	Info, Warn, Error                 func(string, ...any)
	NonRetryable, SharedProviderError func(error) bool
	AmbiguousEntitlement              func(*Record, error) bool
	Persist                           func(context.Context, *Record, map[string]any) error
	FailureWriter                     RefreshFailureWriter
	GrokMutation                      GrokRefreshMutationWriter
	Invalidate                        func(context.Context, *Record) error
	PrepareFailure                    func(*Record) func(time.Time, string)
	ClearRefreshRequest               func(context.Context, *Record, string)
	PostActions, SyncCleanup          func(context.Context, *Record)
}

type rateLimitedRefreshExecutor struct {
	OAuthRefreshExecutor
	acquireRate func(context.Context) (func(), error)
}

func (s RefreshAttempts) Run(
	ctx context.Context,
	provider *Record,
	refresher RefreshTokenOperation,
	executor OAuthRefreshExecutor,
	refreshWindow time.Duration,
	gate RefreshAttemptGate,
) error {
	var lastErr error
	var failureProvider *Record
	maxRetries := s.Tuning.Retries()

	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		releaseAttempt := func() {}
		var acquireRate func(context.Context) (func(), error)
		if gate != nil {
			if providerGate, ok := gate.(ProviderRefreshAttemptGate); ok {
				var err error
				releaseAttempt, err = providerGate.Acquire(ctx)
				if err != nil {
					return err
				}
				acquireRate = providerGate.AcquireRate
			} else {
				// Legacy gates limit exchange rate; acquire immediately before calling upstream Refresh.
				acquireRate = gate.Acquire
			}
		}
		attemptCtx, cancelAttempt := context.WithTimeout(ctx, s.AttemptTimeout)
		failureProvider = nil
		var newCredentials map[string]any
		var err error
		shortCircuit := false
		credentialsPersisted := false

		// Prefer the coordinated API for distributed locking and database rereads.
		if s.API != nil && executor != nil {
			actualExecutor := executor
			if acquireRate != nil {
				actualExecutor = &rateLimitedRefreshExecutor{
					OAuthRefreshExecutor: executor,
					acquireRate:          acquireRate,
				}
			}
			result, refreshErr := s.API.RefreshWithAttemptSnapshot(attemptCtx, provider, actualExecutor, refreshWindow)
			if result != nil && result.Provider != nil {
				provider = result.Provider
				failureProvider = CloneRecord(provider)
			}
			if refreshErr != nil {
				err = refreshErr
			} else if result.LockHeld {
				// Let the caller policy classify lock contention.
				err = s.Policy.HandleLockHeld()
				shortCircuit = true
			} else if !result.Refreshed {
				// Let the caller policy classify an exchange skipped after a reread.
				err = s.Policy.HandleAlreadyRefreshed()
				shortCircuit = true
			} else {
				credentialsPersisted = result.NewCredentials != nil
				_ = result.NewCredentials // The API has already assigned _token_version and persisted credentials.
			}
		} else {
			// Call the refresher directly when no coordinator is configured.
			failureProvider = CloneRecord(provider)
			releaseRate := func() {}
			if acquireRate != nil {
				releaseRate, err = acquireRate(attemptCtx)
			}
			if err == nil {
				newCredentials, err = refresher.Refresh(attemptCtx, provider)
			}
			if releaseRate != nil {
				releaseRate()
			}
			attemptTimedOut := errors.Is(attemptCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
			if err == nil && newCredentials != nil && !attemptTimedOut {
				newCredentials["_token_version"] = s.Now().UnixMilli()
				if saveErr := s.Persist(attemptCtx, provider, newCredentials); saveErr != nil {
					err = fmt.Errorf("failed to save credentials: %w", saveErr)
				} else {
					credentialsPersisted = true
				}
			}
		}
		attemptTimedOut := errors.Is(attemptCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
		cancelAttempt()
		releaseAttempt()
		persistedAfterAttemptDeadline := attemptTimedOut && credentialsPersisted && err == nil
		if attemptTimedOut && !persistedAfterAttemptDeadline && !IsProviderScopedTerminalRefreshError(err) {
			cause := err
			if cause == nil {
				cause = context.DeadlineExceeded
			}
			err = &RefreshAttemptTimeoutError{Cause: cause}
			shortCircuit = false
			if credentialsPersisted {
				s.SyncCleanup(ctx, provider)
			}
		}
		if shortCircuit {
			return err
		}

		if err == nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				if credentialsPersisted {
					s.SyncCleanup(ctx, provider)
				}
				return ctxErr
			}
			if persistedAfterAttemptDeadline {
				// The credentials are committed; a deadline reached during bounded cleanup must not turn success into retry or
				// cooldown evidence.
				s.SyncCleanup(ctx, provider)
				return nil
			}
			s.PostActions(ctx, provider)
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			if credentialsPersisted {
				s.SyncCleanup(ctx, provider)
			}
			return ctxErr
		}
		if errors.Is(err, ErrRefreshSkipped) {
			return ErrRefreshSkipped
		}
		if IsProviderScopedTerminalRefreshError(err) {
			return err
		}
		if _, ok := errors.AsType[*RefreshStateUnavailableError](err); ok {
			return &ProviderCycleContainmentRefreshError{Cause: err}
		}
		if s.AmbiguousEntitlement(provider, err) {
			// Grok token-endpoint 403 errors have ambiguous ownership; contain this platform for the cycle without
			// changing provider health.
			return &ProviderCycleContainmentRefreshError{Cause: err}
		}

		// OAuth client or scope failures may affect the whole platform; return a platform error without marking
		// individual providers invalid.
		if s.SharedProviderError(err) {
			return &ProviderConfigurationRefreshError{Cause: err}
		}

		// Conditionally record permanent failure for a nonretryable provider error.
		if s.NonRetryable(err) {
			errorMsg := "Token refresh failed (non-retryable): " + logredact.RedactText(err.Error())
			isGrokOAuth := provider.IsGrokOAuth()
			publishFailure := s.PrepareFailure(failureProvider)
			persistentlyBlocked := false
			var setErr error
			if isGrokOAuth {
				conditionalRepo := s.GrokMutation
				ok := conditionalRepo != nil
				if !ok {
					return &ProviderConfigurationRefreshError{
						Cause: errors.New("grok OAuth conditional refresh mutation repository is not configured"),
					}
				}
				persistentlyBlocked, setErr = conditionalRepo.SetGrokOAuthRefreshErrorIfCredentialsUnchanged(
					ctx,
					provider.ID,
					provider.Credentials,
					provider.ProxyID,
					errorMsg,
				)
				if setErr == nil && !persistentlyBlocked {
					s.Info("token_refresh.grok_error_status_skipped_stale_credentials", "provider_id", provider.ID)
					return ErrRefreshSkipped
				}
			} else {
				if failureProvider == nil {
					return err
				}
				writer := s.FailureWriter
				ok := writer != nil
				if !ok {
					return &ProviderConfigurationRefreshError{Cause: errors.New("OAuth conditional refresh failure writer is not configured")}
				}
				persistentlyBlocked, setErr = writer.ApplyOAuthRefreshFailure(ctx, FailureVersion(failureProvider), RefreshFailure{Kind: RefreshFailurePermanent, Message: errorMsg})
				if setErr == nil && !persistentlyBlocked {
					s.Info("token_refresh.error_status_skipped_stale_credentials", "provider_id", provider.ID)
					return ErrRefreshSkipped
				}
			}
			if setErr != nil {
				s.Error("token_refresh.set_error_status_failed",
					"provider_id", provider.ID,
					"error", setErr,
				)
				if isGrokOAuth {
					return &ProviderCycleContainmentRefreshError{
						Cause: fmt.Errorf("failed to conditionally persist Grok OAuth refresh failure: %w", setErr),
					}
				}
			} else if persistentlyBlocked {
				s.ClearRefreshRequest(ctx, failureProvider, "non_retryable")
				publishFailure(time.Time{}, "token_refresh_non_retryable")
			}
			cacheInvalidationFailed := false
			if provider.Type == ProviderTypeOAuth && (!isGrokOAuth || persistentlyBlocked) {
				if s.Invalidate == nil {
					cacheInvalidationFailed = true
				} else if invalidateErr := s.Invalidate(ctx, provider); invalidateErr != nil {
					cacheInvalidationFailed = true
					s.Warn("token_refresh.invalidate_failed_token_cache_failed",
						"provider_id", provider.ID,
						"error", logredact.RedactText(invalidateErr.Error()),
					)
				}
			}
			return &ProviderPermanentRefreshError{
				Cause:                   err,
				PersistentlyBlocked:     persistentlyBlocked,
				CacheInvalidationFailed: cacheInvalidationFailed,
			}
		}

		lastErr = err
		s.Warn("token_refresh.retry_attempt_failed",
			"provider_id", provider.ID,
			"attempt", attempt,
			"max_retries", maxRetries,
			"error", logredact.RedactText(err.Error()),
		)

		// Back off before another attempt, allowing the cycle context to cancel the wait.
		if attempt < maxRetries {
			backoff := s.Tuning.RetryBackoff(provider.ID, attempt)
			if backoff > 0 {
				timer := time.NewTimer(backoff)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Exhausted retryable errors trigger a temporary scheduling block.
	s.Warn("token_refresh.retry_exhausted",
		"provider_id", provider.ID,
		"platform", provider.Platform,
		"max_retries", maxRetries,
		"error", logredact.RedactText(lastErr.Error()),
	)

	// Keep status active during the ten-minute cooldown so a later refresh cycle can try again.
	until := s.Now().Add(TokenRefreshTempUnschedDuration)
	reason := "token refresh retry exhausted"
	if lastErr != nil {
		reason += ": " + logredact.RedactText(lastErr.Error())
	}
	publishFailure := s.PrepareFailure(failureProvider)
	if provider.IsGrokOAuth() {
		conditionalRepo := s.GrokMutation
		ok := conditionalRepo != nil
		if !ok {
			return &ProviderConfigurationRefreshError{
				Cause: errors.New("grok OAuth conditional refresh mutation repository is not configured"),
			}
		}
		applied, setErr := conditionalRepo.SetGrokOAuthRefreshTempUnschedulableIfCredentialsUnchanged(
			ctx,
			provider.ID,
			provider.Credentials,
			provider.ProxyID,
			until,
			reason,
		)
		if setErr != nil {
			s.Warn("token_refresh.set_temp_unschedulable_failed",
				"provider_id", provider.ID,
				"error", setErr,
			)
			return &ProviderCycleContainmentRefreshError{
				Cause: fmt.Errorf("failed to conditionally persist Grok OAuth refresh cooldown: %w", setErr),
			}
		} else if !applied {
			s.Info("token_refresh.grok_temp_unschedulable_skipped_stale_credentials", "provider_id", provider.ID)
			return ErrRefreshSkipped
		}
		publishFailure(until, "token_refresh_retry_exhausted")
		s.Info("token_refresh.temp_unschedulable_set",
			"provider_id", provider.ID,
			"until", until.Format(time.RFC3339),
		)
		return lastErr
	}

	// Queue or reread failures have no attempted identity to compare; leave provider health unchanged.
	if failureProvider == nil {
		return lastErr
	}

	writer := s.FailureWriter
	ok := writer != nil
	if !ok {
		return &ProviderConfigurationRefreshError{Cause: errors.New("OAuth conditional refresh failure writer is not configured")}
	}
	applied, setErr := writer.ApplyOAuthRefreshFailure(ctx, FailureVersion(failureProvider), RefreshFailure{Kind: RefreshFailureCooldown, Message: reason, Until: until})
	if setErr == nil && !applied {
		s.Info("token_refresh.temp_unschedulable_skipped_stale_credentials", "provider_id", provider.ID)
		return ErrRefreshSkipped
	}
	if setErr != nil {
		s.Warn("token_refresh.set_temp_unschedulable_failed",
			"provider_id", provider.ID,
			"error", setErr,
		)
	} else {
		publishFailure(until, "token_refresh_retry_exhausted")
		s.Info("token_refresh.temp_unschedulable_set",
			"provider_id", provider.ID,
			"until", until.Format(time.RFC3339),
		)
	}

	return lastErr
}

func (e *rateLimitedRefreshExecutor) Refresh(ctx context.Context, provider *Record) (map[string]any, error) {
	if e == nil || e.OAuthRefreshExecutor == nil {
		return nil, errors.New("OAuth refresh executor is not configured")
	}
	release := func() {}
	if e.acquireRate != nil {
		var err error
		release, err = e.acquireRate(ctx)
		if err != nil {
			return nil, err
		}
	}
	defer release()
	return e.OAuthRefreshExecutor.Refresh(ctx, provider)
}
