package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	defaultRefreshLockTTL                   = 60 * time.Second
	defaultRefreshLockReleaseTimeout        = 2 * time.Second
	defaultRefreshPostPersistCleanupTimeout = 2 * time.Second
)

var (
	ErrRefreshProviderRereadFailed = errors.New("oauth refresh provider reread failed")
	ErrRefreshProviderStateChanged = errors.New("oauth refresh provider state changed")
	ErrRefreshCredentialPersist    = errors.New("oauth refresh credential persistence failed")
)

// GrokRefreshSuccessWriter persists rotated Grok credentials.
// Implementations compare the complete attempted credentials and proxy, then atomically commit credentials and
// scheduler invalidation.
type GrokRefreshSuccessWriter interface {
	UpdateGrokOAuthCredentialsIfUnchanged(
		ctx context.Context,
		id int64,
		expectedCredentials map[string]any,
		expectedProxyID *int64,
		credentials map[string]any,
	) (bool, error)
}

type oauthRefreshRequestPathKey struct{}

type RefreshLock struct {
	token chan struct{}
}

type RefreshStateUnavailableError struct {
	err error
}

func WithRefreshRequestPath(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauthRefreshRequestPathKey{}, true)
}

func isOAuthRefreshRequestPath(ctx context.Context) bool {
	requestPath, _ := ctx.Value(oauthRefreshRequestPathKey{}).(bool)
	return requestPath
}

func (e *RefreshStateUnavailableError) Error() string {
	return "OAuth refresh provider state is unavailable"
}

func (e *RefreshStateUnavailableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func NewRefreshLock() *RefreshLock {
	return &RefreshLock{token: make(chan struct{}, 1)}
}

func (m *RefreshLock) Lock(ctx context.Context) error {
	select {
	case m.token <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *RefreshLock) Unlock() {
	<-m.token
}

// getLocalLock returns the process-local lock for cacheKey.
func (api *OAuthRefreshAPI) getLocalLock(cacheKey string) *RefreshLock {
	actual, _ := api.localLocks.LoadOrStore(cacheKey, NewRefreshLock())
	mu, ok := actual.(*RefreshLock)
	if !ok {
		mu = NewRefreshLock()
		api.localLocks.Store(cacheKey, mu)
	}
	return mu
}

// RefreshIfNeeded refreshes eligible OAuth credentials using local and available distributed locking.
//
// The coordinated refresh proceeds as follows:
//  1. Acquire refresh locks; Redis errors leave local locking in effect.
//  2. Reload the provider to avoid exchanging a stale refresh token.
//  3. Recheck whether refresh is needed.
//  4. Execute the platform-specific credential exchange.
//  5. Assign _token_version and conditionally persist credentials.
//  6. Release the locks.
//
// Cancellation returns an error and discards any exchange result not yet persisted.
func (api *OAuthRefreshAPI) RefreshIfNeeded(ctx context.Context, value *Record, executor OAuthRefreshExecutor, window time.Duration) (*OAuthRefreshResult, error) {
	return api.refreshIfNeeded(ctx, value, executor, window, false)
}

// RefreshWithAttemptSnapshot retains the attempted identity for periodic failure handling, including cancelled
// attempts.
func (api *OAuthRefreshAPI) RefreshWithAttemptSnapshot(ctx context.Context, value *Record, executor OAuthRefreshExecutor, window time.Duration) (*OAuthRefreshResult, error) {
	return api.refreshIfNeeded(ctx, value, executor, window, true)
}

func (api *OAuthRefreshAPI) refreshIfNeeded(
	ctx context.Context,
	provider *Record,
	executor OAuthRefreshExecutor,
	refreshWindow time.Duration,
	retainCancelledAttempt bool,
) (*OAuthRefreshResult, error) {
	if api == nil || api.providerRepo == nil {
		return nil, errors.New("oauth refresh provider repository is not configured")
	}
	if provider == nil {
		return nil, errors.New("oauth refresh provider is nil")
	}
	if executor == nil {
		return nil, errors.New("oauth refresh executor is nil")
	}
	ctx, finish, err := api.beginRefresh(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	requestPath := isOAuthRefreshRequestPath(ctx)
	cacheKey := executor.CacheKey(provider)

	release, held, err := api.acquireRefreshLock(ctx, provider.ID, cacheKey)
	if err != nil {
		return nil, err
	}
	if held {
		return &OAuthRefreshResult{LockHeld: true}, nil
	}
	defer release()

	// Reload the latest refresh token while holding the refresh lock.
	freshProvider, err := api.providerRepo.GetByID(ctx, provider.ID)
	if err != nil {
		if requestPath {
			return nil, fmt.Errorf("%w: %v", ErrRefreshProviderRereadFailed, err)
		}
		return nil, &RefreshStateUnavailableError{err: err}
	}
	if freshProvider == nil {
		if requestPath {
			return nil, fmt.Errorf("%w: provider not found", ErrRefreshProviderStateChanged)
		}
		return nil, &RefreshStateUnavailableError{err: fmt.Errorf("provider not found")}
	}
	if freshProvider.ID != provider.ID {
		return nil, fmt.Errorf("%w: provider identity mismatch", ErrRefreshProviderRereadFailed)
	}
	// Request-path state changes are errors; background refresh skips inactive providers. Administrative refresh
	// uses a separate entry point.
	if !freshProvider.IsActive() {
		if requestPath {
			return nil, fmt.Errorf("%w: provider is not active", ErrRefreshProviderStateChanged)
		}
		return &OAuthRefreshResult{Provider: freshProvider}, nil
	}
	if requestPath && freshProvider.Platform == PlatformGrok {
		if eligibilityErr := api.options.Platform.Eligibility(freshProvider); eligibilityErr != nil {
			return nil, api.options.Platform.SnapshotError(eligibilityErr, freshProvider)
		}
	}
	if !executor.CanRefresh(freshProvider) {
		if requestPath && freshProvider.IsGrokOAuth() && strings.TrimSpace(freshProvider.GetGrokRefreshToken()) == "" {
			return nil, api.options.Platform.SnapshotError(api.options.Platform.MissingRefreshToken(), freshProvider)
		}
		if requestPath {
			return nil, fmt.Errorf("%w: provider is no longer refreshable", ErrRefreshProviderStateChanged)
		}
		return &OAuthRefreshResult{Provider: freshProvider}, nil
	}

	// Another refresh path may already have extended the token lifetime.
	if !executor.NeedsRefresh(freshProvider, refreshWindow) {
		return &OAuthRefreshResult{
			Provider: freshProvider,
		}, nil
	}

	// Capture the attempted identity before invoking the platform exchange.
	attemptedProvider := snapshotRefreshRecord(freshProvider)
	if err := ctx.Err(); err != nil {
		if retainCancelledAttempt && !requestPath {
			return &OAuthRefreshResult{Provider: attemptedProvider}, err
		}
		return nil, err
	}
	newCredentials, refreshErr := executor.Refresh(ctx, freshProvider)
	if ctxErr := ctx.Err(); ctxErr != nil {
		// Discard credentials returned after the attempt or cycle was cancelled, even if upstream ignored cancellation.
		if retainCancelledAttempt && !requestPath {
			return &OAuthRefreshResult{Provider: attemptedProvider}, ctxErr
		}
		return nil, ctxErr
	}
	if refreshErr != nil {
		// A rejected refresh token may have been consumed by another worker.
		// Recover by rereading the database and accepting a changed refresh token.
		if IsInvalidGrantError(refreshErr) {
			if recoveredProvider, recovered := api.tryRecoverFromRefreshRace(ctx, freshProvider); recovered {
				if requestPath && recoveredProvider.Platform == PlatformGrok {
					if eligibilityErr := api.options.Platform.Eligibility(recoveredProvider); eligibilityErr != nil {
						return nil, api.options.Platform.SnapshotError(eligibilityErr, recoveredProvider)
					}
				}
				api.options.Info("oauth_refresh_race_recovered",
					"provider_id", freshProvider.ID,
					"platform", freshProvider.Platform,
				)
				return &OAuthRefreshResult{
					Provider: recoveredProvider,
				}, nil
			}
		}
		// Return the attempted snapshot so later failure writes can compare against that identity.
		result := &OAuthRefreshResult{Provider: attemptedProvider}
		if requestPath && attemptedProvider.Platform == PlatformGrok {
			return result, api.options.Platform.SnapshotError(refreshErr, attemptedProvider)
		}
		return result, refreshErr
	}

	// Version and conditionally persist the returned credentials.
	if newCredentials != nil {
		// Clone the executor-owned map before adding version metadata.
		cloned := CloneValues(newCredentials)
		cloned["_token_version"] = api.options.Now().UnixMilli()
		newCredentials = cloned

		if freshProvider.IsGrokOAuth() {
			conditionalRepo, ok := api.providerRepo.(GrokRefreshSuccessWriter)
			if !ok {
				return nil, api.options.Platform.ConfigurationError(fmt.Errorf("grok OAuth refresh success CAS repository is not configured"))
			}
			applied, updateErr := conditionalRepo.UpdateGrokOAuthCredentialsIfUnchanged(
				ctx,
				freshProvider.ID,
				attemptedProvider.Credentials,
				attemptedProvider.ProxyID,
				newCredentials,
			)
			if updateErr != nil {
				api.options.Error("oauth_refresh_update_failed",
					"provider_id", freshProvider.ID,
					"platform", freshProvider.Platform,
					"error", updateErr,
				)
				// Upstream may have consumed the refresh token; retrying after an uncertain local commit can produce
				// invalid_grant.
				return nil, api.options.Platform.ContainmentError(fmt.Errorf("OAuth refresh succeeded but credential persistence failed: %w", updateErr))
			}
			if !applied {
				currentProvider, readErr := api.providerRepo.GetByID(ctx, freshProvider.ID)
				if readErr != nil || currentProvider == nil {
					if readErr == nil {
						readErr = fmt.Errorf("provider not found after Grok OAuth success CAS miss")
					}
					return nil, api.options.Platform.ContainmentError(fmt.Errorf("grok OAuth success CAS lost and current state is unavailable: %w", readErr))
				}
				api.options.Info("oauth_refresh_success_cas_skipped_stale_credentials",
					"provider_id", freshProvider.ID,
					"platform", freshProvider.Platform,
				)
				return &OAuthRefreshResult{Provider: currentProvider}, nil
			}
			durableProvider, readErr := api.loadGrokDurableProviderAfterPersist(ctx, cacheKey, freshProvider.ID)
			if readErr != nil || durableProvider == nil {
				if readErr == nil {
					readErr = fmt.Errorf("provider not found after Grok OAuth success CAS")
				}
				return nil, api.options.Platform.ContainmentError(fmt.Errorf("grok OAuth success persisted but durable provider state is unavailable: %w", readErr))
			}
			// Use the durable record so concurrent administrative and scheduling fields remain current.
			freshProvider = durableProvider
		} else if !freshProvider.IsCredentialShadow() {
			writer, ok := api.providerRepo.(CredentialRefreshWriter)
			if !ok {
				return nil, fmt.Errorf("%w: conditional credential writer is not configured", ErrRefreshCredentialPersist)
			}
			applied, updateErr := writer.UpdateOAuthCredentialsIfUnchanged(ctx, CredentialVersion{ID: attemptedProvider.ID, Platform: attemptedProvider.Platform, Type: attemptedProvider.Type, Status: attemptedProvider.Status, ProxyID: attemptedProvider.ProxyID, Credentials: attemptedProvider.Credentials}, newCredentials)
			if updateErr != nil {
				api.options.Error("oauth_refresh_update_failed", "provider_id", freshProvider.ID, "error", updateErr)
				return nil, fmt.Errorf("%w: %v", ErrRefreshCredentialPersist, updateErr)
			}
			if !applied {
				current, readErr := api.providerRepo.GetByID(ctx, freshProvider.ID)
				if readErr != nil {
					return nil, &RefreshStateUnavailableError{err: readErr}
				}
				if current == nil || current.ID != attemptedProvider.ID {
					return nil, fmt.Errorf("%w: provider not found after credential comparison", ErrRefreshProviderStateChanged)
				}
				// Discard this exchange after a concurrent update and return the current provider state.
				if requestPath && (!current.IsActive() || !executor.CanRefresh(current)) {
					return nil, fmt.Errorf("%w: provider changed during refresh", ErrRefreshProviderStateChanged)
				}
				return &OAuthRefreshResult{Provider: current}, nil
			}
			freshProvider.Credentials = CloneValues(newCredentials)
		} else {
			api.options.Warn("skip persisting credentials to spark shadow provider", "provider_id", freshProvider.ID, "parent_id", *freshProvider.ParentProviderID)
		}
	}

	if requestPath && freshProvider.Platform == PlatformGrok {
		if eligibilityErr := api.options.Platform.Eligibility(freshProvider); eligibilityErr != nil {
			return nil, api.options.Platform.SnapshotError(eligibilityErr, freshProvider)
		}
	}

	return &OAuthRefreshResult{
		Refreshed:      true,
		NewCredentials: newCredentials,
		Provider:       freshProvider,
	}, nil
}

func (api *OAuthRefreshAPI) releaseRefreshLock(parent context.Context, cacheKey string) {
	cleanupParent := context.Background()
	if parent != nil {
		cleanupParent = context.WithoutCancel(parent)
	}
	ctx, cancel := context.WithTimeout(cleanupParent, defaultRefreshLockReleaseTimeout)
	defer cancel()
	if err := api.tokenCache.ReleaseRefreshLock(ctx, cacheKey); err != nil {
		api.options.Warn("oauth_refresh_lock_release_failed", "cache_key", cacheKey, "error", err)
	}
}

func (api *OAuthRefreshAPI) loadGrokDurableProviderAfterPersist(parent context.Context, cacheKey string, providerID int64) (*Record, error) {
	cleanupParent := context.Background()
	if parent != nil {
		cleanupParent = context.WithoutCancel(parent)
	}
	ctx, cancel := context.WithTimeout(cleanupParent, defaultRefreshPostPersistCleanupTimeout)
	defer cancel()

	// Rotation may revoke the cached access token; delete it with a separate cleanup budget after commit.
	if api.tokenCache != nil {
		if err := api.tokenCache.DeleteAccessToken(ctx, cacheKey); err != nil {
			api.options.Warn("oauth_refresh_post_persist_cache_delete_failed",
				"provider_id", providerID,
				"cache_key", cacheKey,
				"error", err,
			)
		}
	}

	return api.providerRepo.GetByID(ctx, providerID)
}

// IsInvalidGrantError recognizes errors indicating an invalid or consumed refresh token.
func IsInvalidGrantError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "invalid_grant") ||
		strings.Contains(msg, "refresh_token_reused") ||
		strings.Contains(msg, "refresh token has already been used")
}

// tryRecoverFromRefreshRace rereads credentials after a rejected refresh token.
// A changed, nonempty refresh token is accepted as recovery from a concurrent update.
func (api *OAuthRefreshAPI) tryRecoverFromRefreshRace(ctx context.Context, usedProvider *Record) (*Record, bool) {
	if api.providerRepo == nil {
		return nil, false
	}
	reReadProvider, err := api.providerRepo.GetByID(ctx, usedProvider.ID)
	if err != nil || reReadProvider == nil {
		return nil, false
	}
	usedRT := usedProvider.GetCredential("refresh_token")
	currentRT := reReadProvider.GetCredential("refresh_token")
	if usedRT == "" || currentRT == "" {
		return nil, false
	}
	// A different stored refresh token makes the rejected token obsolete.
	if usedRT != currentRT {
		return reReadProvider, true
	}
	return nil, false
}

// MergeCredentials fills missing newCreds keys from oldCreds, mutating and returning newCreds.
func MergeCredentials(oldCreds, newCreds map[string]any) map[string]any {
	if newCreds == nil {
		newCreds = make(map[string]any)
	}
	for k, v := range oldCreds {
		if _, exists := newCreds[k]; !exists {
			newCreds[k] = v
		}
	}
	return newCreds
}

// snapshotRefreshRecord copies the pre-exchange identity and represents nil credentials as an empty object.
func snapshotRefreshRecord(value *Record) *Record {
	copy := CloneRecord(value)
	if copy != nil && copy.Credentials == nil {
		copy.Credentials = map[string]any{}
	}
	return copy
}
