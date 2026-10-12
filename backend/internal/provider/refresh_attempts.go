package provider

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
)

// Background exchange attempts can stop before upstream work, fail permanently,
// exhaust retries, or commit credentials before cleanup reaches a deadline.
// Those outcomes need different health and accounting decisions: a reported
// successful rotation stays successful when only its inner deadline expires,
// individual credential rejection does not
// establish a shared platform fault, and uncertain platform state stops the
// cycle.
//
// Capacity admission precedes the attempt context, while rate admission happens
// at the actual exchange after coordinated locking and rereading. The attempted
// provider snapshot supports conditional failure writes, and a successful match
// precedes runtime failure publication or request-flag cleanup. Retryable
// failures use capped backoff and eventually request a temporary cooldown.
// Post-commit cleanup receives its own context through the injected callback,
// whose I/O behavior determines actual completion timing.

// RefreshTokenOperation supplies credential exchange for background work using
// the direct refresh path.
type RefreshTokenOperation interface {
	// Refresh exchanges credentials using the supplied attempt context and
	// provider. The direct path adds token-version metadata to a nonnil returned
	// map, so implementations give the caller mutation ownership; actual I/O
	// cancellation depends on the supplied implementation.
	Refresh(context.Context, *Record) (map[string]any, error)
}

// ProviderRefreshAttemptGate separates occupied attempt capacity from rate
// admission at actual exchange.
type ProviderRefreshAttemptGate interface {
	RefreshAttemptGate // occupied attempt capacity admission
	// AcquireRate obtains rate permission immediately before upstream exchange
	// and returns a callable release. Capacity has already been obtained for this
	// attempt; failure skips exchange while its caller releases that capacity.
	AcquireRate(context.Context) (func(), error)
}

// GrokRefreshMutationWriter conditionally records failure against attempted Grok
// credentials and proxy selection.
type GrokRefreshMutationWriter interface {
	// SetGrokOAuthRefreshErrorIfCredentialsUnchanged records permanent rejection
	// when an active Grok OAuth row matches the attempted credentials and proxy.
	// False reports a predicate miss, preventing RefreshAttempts from publishing
	// a runtime block or clearing a request flag.
	SetGrokOAuthRefreshErrorIfCredentialsUnchanged(context.Context, int64, map[string]any, *int64, string) (bool, error)
	// SetGrokOAuthRefreshTempUnschedulableIfCredentialsUnchanged extends a pause
	// for an active Grok OAuth row matching attempted credentials and proxy.
	// False also reports an equal or longer existing cooldown, while storage
	// errors cause RefreshAttempts to contain the platform outcome.
	SetGrokOAuthRefreshTempUnschedulableIfCredentialsUnchanged(context.Context, int64, map[string]any, *int64, time.Time, string) (bool, error)
}

// RefreshAttempts binds admission, exchange budgets and identity-checked failure
// handling for one provider.
type RefreshAttempts struct {
	// API selects coordinated execution when the caller also supplies an executor.
	// Otherwise Run uses its direct refresher and Persist callback instead.
	API *OAuthRefreshAPI
	// shared retry and backoff settings
	Tuning *RefreshTuning
	// unperformed exchange statistics policy
	Policy BackgroundRefreshPolicy
	// AttemptTimeout is supplied as a positive context budget by assembly; it
	// begins after capacity admission and does not force noncooperative I/O to
	// return.
	AttemptTimeout time.Duration
	// direct version and cooldown clock
	Now func() time.Time
	// reached attempt outcome loggers
	Info, Warn, Error func(string, ...any)
	// reached failure classification callbacks
	NonRetryable, SharedProviderError func(error) bool
	// reached Grok entitlement classifier
	AmbiguousEntitlement func(*Record, error) bool
	// Persist is required on a direct successful nonnil map result. Its
	// implementation needs to honor the passed context, including parent
	// cancellation that may precede this call.
	Persist func(context.Context, *Record, map[string]any) error
	// conditional non-Grok failure writer
	FailureWriter RefreshFailureWriter
	// conditional Grok health mutation writer
	GrokMutation GrokRefreshMutationWriter
	// optional failed-token invalidation callback
	Invalidate func(context.Context, *Record) error
	// PrepareFailure accepts an attempted snapshot, including nil, and returns a
	// callable publisher. Capturing it before the conditional write lets the
	// observer reject publication after an interleaved runtime clear.
	PrepareFailure func(*Record) func(time.Time, string)
	// matched one-time flag cleanup callback
	ClearRefreshRequest func(context.Context, *Record, string)
	// PostActions handles ordinary success, and SyncCleanup handles a detected
	// successful rotation when an attempt deadline or outer cancellation is
	// observed. Both are required when their respective result paths are reached.
	PostActions, SyncCleanup func(context.Context, *Record)
}

// rateLimitedRefreshExecutor defers rate admission until coordinated exchange
// actually reaches upstream work.
type rateLimitedRefreshExecutor struct {
	OAuthRefreshExecutor // borrowed platform exchange implementation
	// acquireRate is optional; successful admission returns a callable release
	// that runs after delegated exchange, including when it returns an error.
	acquireRate func(context.Context) (func(), error)
}

// Run attempts background refresh for one provider and returns its classified
// outcome after success, skip, permanent rejection or retry exhaustion. Callers
// provide a nonnil context and provider, plus every callback reached by their
// configured path; positive AttemptTimeout supplies a cooperative attempt
// deadline.
//
// Capacity is obtained before that deadline begins, while rate admission wraps
// the actual exchange. Coordinated execution captures current attempted state
// and owns conditional success persistence. The direct path copies its failure
// observation but mutates the returned credential map's version field; Persist
// needs to honor the passed cancellation context, since direct parent
// cancellation is not separately checked before that callback.
//
// The persistence marker uses a nonnil coordinated result map or a nil direct
// persistence error. A permissive shadow executor can return that map despite
// skipping storage; production OpenAI refresh eligibility excludes shadows.
// An otherwise successful detected rotation receives SyncCleanup when only its
// inner deadline expires. Outer cancellation returns its error after cleanup
// when the success marker was set. Shared configuration, uncertain state and
// ambiguous Grok entitlement stop the platform; individual permanent failures
// conditionally change health before runtime publication, and comparison
// conflicts become skips. Retryable failures use total-attempt counting and
// context-aware backoff, then request a matching cooldown while retaining the
// last exchange error.
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
				// A gate exposing only Acquire supplies
				// exchange-rate admission. Delay that call
				// until upstream work so contention or
				// fresh credentials use no rate slot.
				acquireRate = gate.Acquire
			}
		}
		attemptCtx, cancelAttempt := context.WithTimeout(ctx, s.AttemptTimeout)
		failureProvider = nil
		var newCredentials map[string]any
		var err error
		shortCircuit := false
		credentialsPersisted := false

		// Coordinated locking and rereads own success persistence when both components
		// exist; wrapping Refresh admits rate only when an actual exchange is required.
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
				// Contention performed no exchange. Apply
				// its accounting policy before retry
				// classification so this skip supplies no
				// provider failure evidence.
				err = s.Policy.HandleLockHeld()
				shortCircuit = true
			} else if !result.Refreshed {
				// The reread made exchange unnecessary.
				// Apply the configured accounting policy
				// before retry classification so this skip
				// supplies no failure evidence.
				err = s.Policy.HandleAlreadyRefreshed()
				shortCircuit = true
			} else {
				credentialsPersisted = result.NewCredentials != nil
				// Successful nonshadow maps were versioned
				// and saved by the coordinator. The
				// production refresher excludes shadows,
				// whose generic coordinator result can
				// contain a map even though that branch
				// skipped persistence.
				_ = result.NewCredentials
			}
		} else {
			// Missing coordinated components select direct
			// exchange. Keep a copied failure observation
			// while the returned map is versioned in place
			// before Persist.
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
				// The direct path owns this returned map
				// and versions it in place. Persist
				// receives the attempt context so its
				// implementation decides whether parent
				// cancellation prevents a write already
				// reached here.
				newCredentials["_token_version"] = s.Now().UnixMilli()
				if saveErr := s.Persist(attemptCtx, provider, newCredentials); saveErr != nil {
					err = fmt.Errorf("failed to save credentials: %w", saveErr)
				} else {
					credentialsPersisted = true
				}
			}
		}
		// Read inner-deadline state before canceling its context, then release capacity
		// before outcome handling so outer cancellation keeps its separate
		// classification.
		attemptTimedOut := errors.Is(attemptCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
		cancelAttempt()
		releaseAttempt()
		persistedAfterAttemptDeadline := attemptTimedOut && credentialsPersisted && err == nil
		// Count an expired uncommitted attempt while retaining terminal platform and
		// committed-success outcomes; timing cannot replace their more specific
		// classification.
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
				// A successful persistence marker remains set when the deadline is
				// observed. Finish success cleanup so this timing cannot trigger
				// another exchange or cooldown.
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
			// The Grok classifier found insufficient
			// credential-specific entitlement evidence.
			// Contain the cycle so ambiguous rejection
			// cannot permanently change this provider.
			return &ProviderCycleContainmentRefreshError{Cause: err}
		}

		// Client or scope rejection affects shared platform configuration. Classify it
		// before individual health writes so this outcome cannot reject their
		// credentials.
		if s.SharedProviderError(err) {
			return &ProviderConfigurationRefreshError{Cause: err}
		}

		// Prepare publication before conditional health persistence, then publish only
		// after a match so replaced credentials cannot receive this failure block.
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
				// Matched permanent persistence authorizes
				// request-flag cleanup and runtime
				// publication; conflicts and write failures
				// cannot install this credential block.
				s.ClearRefreshRequest(ctx, failureProvider, "non_retryable")
				publishFailure(time.Time{}, "token_refresh_non_retryable")
			}
			// Keep token invalidation outcome separate from durable blocking so Grok
			// reconciliation can report a partially completed permanent-failure action.
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

		// Retry after releasing the prior capacity grant, within the cycle budget.
		// Cancellation stops the wait before another exchange can begin.
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

	// Keep the last exchange error while attempting an identity-checked pause, so
	// retry exhaustion can defer scheduling without requiring permanent
	// reauthorization.
	s.Warn("token_refresh.retry_exhausted",
		"provider_id", provider.ID,
		"platform", provider.Platform,
		"max_retries", maxRetries,
		"error", logredact.RedactText(lastErr.Error()),
	)

	// Retain persistent status during this temporary pause so another candidate
	// cycle can retry after its deadline instead of requiring provider
	// reauthorization.
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
		// The conditional pause matched before this publication. A competing
		// credential identity therefore cannot inherit this captured failure
		// notice.
		publishFailure(until, "token_refresh_retry_exhausted")
		s.Info("token_refresh.temp_unschedulable_set",
			"provider_id", provider.ID,
			"until", until.Format(time.RFC3339),
		)
		return lastErr
	}

	// Failures before snapshot capture cannot authorize a conditional health write.
	// Return the outcome while leaving the unidentified provider state unchanged.
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
		// The conditional pause matched before this publication. A competing
		// credential identity therefore cannot inherit this captured failure
		// notice.
		publishFailure(until, "token_refresh_retry_exhausted")
		s.Info("token_refresh.temp_unschedulable_set",
			"provider_id", provider.ID,
			"until", until.Format(time.RFC3339),
		)
	}

	return lastErr
}

// Refresh obtains rate admission before delegating to the borrowed platform
// executor. A nil wrapper or executor returns a configuration error; a reached
// acquireRate callback returns a callable release that is deferred through
// exchange success or error.
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
