package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// A selected refresh token can become stale while another worker or
// administrator changes the provider. Local and optional distributed locks
// serialize exchange attempts before a current-row reread; the attempted
// identity is captured so a late result can be compared against the
// authentication state it used. Background skips and marked request failures
// remain distinct outcomes.
//
// Successful exchanges receive a version stamp on copied supported credential
// containers. Non-Grok storage compares provider identity, status, credentials
// and proxy, while Grok uses its credentials-and-proxy writer followed by
// durable readback. Comparison conflicts trigger a current-state reread after
// one exchange, and rejected tokens can recover through a changed nonempty
// stored refresh token. Shutdown registration tracks the entire lock lifetime, with
// cancellation checks before persistence and separate cleanup deadline contexts
// after an acquired lease or committed Grok rotation.

// Refresh lease and cleanup durations govern distributed acquisition and the
// separate deadline contexts used after cancellation or persistence.
const (
	// defaultRefreshLockTTL is the default distributed refresh lease duration.
	defaultRefreshLockTTL = 60 * time.Second
	// defaultRefreshLockReleaseTimeout sets the cache-release context deadline.
	defaultRefreshLockReleaseTimeout = 2 * time.Second
	// defaultRefreshPostPersistCleanupTimeout sets the Grok cleanup deadline.
	defaultRefreshPostPersistCleanupTimeout = 2 * time.Second
)

// Refresh errors distinguish unavailable current state, changed request
// eligibility and failed credential persistence so callers select recovery or
// containment appropriately.
var (
	// ErrRefreshProviderRereadFailed reports request-side reread or ID failure.
	ErrRefreshProviderRereadFailed = errors.New("oauth refresh provider reread failed")
	// ErrRefreshProviderStateChanged reports a changed or missing refresh target.
	ErrRefreshProviderStateChanged = errors.New("oauth refresh provider state changed")
	// ErrRefreshCredentialPersist reports missing conditional storage or write
	// failure.
	ErrRefreshCredentialPersist = errors.New("oauth refresh credential persistence failed")
)

// GrokRefreshSuccessWriter saves rotated Grok credentials against the attempted
// credentials and proxy.
type GrokRefreshSuccessWriter interface {
	// UpdateGrokOAuthCredentialsIfUnchanged atomically writes credentials and
	// their scheduler outbox event when the Grok OAuth row still matches its
	// attempted credentials and proxy. A false result reports a comparison miss;
	// callers reread current state and discard their exchange result.
	//
	// Production compares ID, Grok platform, OAuth kind, complete JSONB
	// credentials and nullable proxy selection in one update statement. Its
	// outbox failure rolls back that statement. Status is not part of this
	// comparison, and the coordinator performs durable readback and reached
	// request-side eligibility checks after success.
	UpdateGrokOAuthCredentialsIfUnchanged(
		ctx context.Context,
		id int64,
		expectedCredentials map[string]any,
		expectedProxyID *int64,
		credentials map[string]any,
	) (bool, error)
}

// oauthRefreshRequestPathKey distinguishes request eligibility handling in the
// shared refresh context.
type oauthRefreshRequestPathKey struct{}

// RefreshLock serializes one refresh owner using a cancellable channel token.
type RefreshLock struct {
	// token remains open for the lock lifetime; one successful send owns the
	// lock until Unlock consumes it, and callers release each grant once.
	token chan struct{}
}

// RefreshStateUnavailableError retains the underlying background reread failure
// for platform containment.
type RefreshStateUnavailableError struct {
	err error // underlying provider reread failure
}

// WithRefreshRequestPath marks a nonnil context so the shared coordinator
// applies request eligibility rules. The returned child retains parent
// cancellation and values, while unmarked calls use background skip handling.
func WithRefreshRequestPath(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauthRefreshRequestPathKey{}, true)
}

// isOAuthRefreshRequestPath reports the request-path marker stored in the
// supplied context.
func isOAuthRefreshRequestPath(ctx context.Context) bool {
	requestPath, _ := ctx.Value(oauthRefreshRequestPathKey{}).(bool)
	return requestPath
}

// Error returns a fixed public message for unavailable current provider state.
func (e *RefreshStateUnavailableError) Error() string {
	return "OAuth refresh provider state is unavailable"
}

// Unwrap exposes the underlying reread error for background containment
// classification. A nil receiver yields nil, while Error supplies the fixed
// public message separately from this diagnostic cause.
func (e *RefreshStateUnavailableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// NewRefreshLock creates an open one-token channel for cancellable refresh
// ownership.
func NewRefreshLock() *RefreshLock {
	return &RefreshLock{token: make(chan struct{}, 1)}
}

// Lock waits for the token channel to accept this owner or for ctx cancellation.
// Callers use an initialized lock and a nonnil context, then invoke Unlock
// exactly once after successful acquisition.
//
// Sending the token and receiving cancellation compete in one select, so
// available capacity can win even when ctx is already canceled. The coordinator
// therefore rechecks its stop and context state after acquisition before
// exchange. A canceled wait returns the context error without owning a token;
// the channel stays open for later owners and supplies no FIFO ordering promise.
func (m *RefreshLock) Lock(ctx context.Context) error {
	// Admission and cancellation can be ready together; the caller's stop/context
	// recheck after this select prevents a granted waiter from starting stopped
	// work.
	select {
	case m.token <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Unlock returns one successfully acquired refresh token to waiting callers.
// Invoke it exactly once per successful Lock; an extra invocation can consume
// another owner's token or wait indefinitely.
func (m *RefreshLock) Unlock() {
	// Consume the owner's token to release capacity while keeping this channel
	// open for the lock retained in the coordinator's shared key table.
	<-m.token
}

// getLocalLock returns the lock stored for the exact cache key through sync.Map
// publication. The table expects RefreshLock pointers; an unexpected stored type
// is replaced with a newly constructed lock.
func (api *OAuthRefreshAPI) getLocalLock(cacheKey string) *RefreshLock {
	actual, _ := api.localLocks.LoadOrStore(cacheKey, NewRefreshLock())
	mu, ok := actual.(*RefreshLock)
	// Restore the expected table entry type when an unexpected value is found.
	// Ordinary publication stores only lock pointers shared by this cache key.
	if !ok {
		mu = NewRefreshLock()
		api.localLocks.Store(cacheKey, mu)
	}
	return mu
}

// RefreshIfNeeded coordinates current-row eligibility, exchange and conditional
// credential persistence for the selected provider. Reached registration needs a
// nonnil context, and callers keep the selected record stable during this call.
// Marked request calls reject lost eligibility, while ordinary
// calls can return a current provider without exchanging. Exchange errors can
// carry the attempted provider snapshot; successful nil credentials still set
// Refreshed.
func (api *OAuthRefreshAPI) RefreshIfNeeded(ctx context.Context, value *Record, executor OAuthRefreshExecutor, window time.Duration) (*OAuthRefreshResult, error) {
	return api.refreshIfNeeded(ctx, value, executor, window, false)
}

// RefreshWithAttemptSnapshot coordinates refresh while retaining a captured
// attempted provider on cancellation after snapshot creation for unmarked
// background work. It uses RefreshIfNeeded's caller and eligibility rules;
// marked request cancellation and failures before capture can return a nil
// result.
func (api *OAuthRefreshAPI) RefreshWithAttemptSnapshot(ctx context.Context, value *Record, executor OAuthRefreshExecutor, window time.Duration) (*OAuthRefreshResult, error) {
	return api.refreshIfNeeded(ctx, value, executor, window, true)
}

// refreshIfNeeded runs one locked, current-state exchange and handles its
// identity conflict, recovery or persisted result. It rejects missing
// dependencies, requires a nonnil registration context, and derives its lock key
// from the selected provider before rereading.
//
// Background work skips inactive or unrefreshable current records; marked
// request work reports state errors and applies Grok eligibility and credential
// snapshots. The pre-exchange record is copied for comparison, with
// CloneValues's supported-container limits. Cancellation observed before
// persistence discards returned credentials, retaining that attempted copy only
// for the requested unmarked background result.
//
// Nonnil exchange maps receive a version stamp, then conditional writers
// decide whether their attempted identity still matches. A conflict rereads
// current state after the single exchange; reread failures and reached request
// eligibility failures are still reported. Grok persistence is followed by cache
// deletion and durable readback; uncertain persistence or readback is contained.
// A nil credential map still yields Refreshed=true, and credential shadows skip
// durable persistence. Locks and activity registration remain owned until result
// handling and deferred cleanup complete.
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
	// Register completion before deferred lock release so lease cleanup finishes
	// before shutdown observes this refresh operation as drained.
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

	// Reread under local ownership so expiry and credentials reflect a prior
	// completed exchange; the selected input cannot safely supply the token
	// alone.
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
	// Background maintenance leaves inactive rows untouched, while request callers
	// need a failure signal when their selected credentials lose active
	// eligibility.
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

	// A prior owner may have extended expiry before this lock was obtained.
	// Return its current state as an unperformed exchange for the caller's skip
	// policy.
	if !executor.NeedsRefresh(freshProvider, refreshWindow) {
		return &OAuthRefreshResult{
			Provider: freshProvider,
		}, nil
	}

	// Capture comparison values before the executor can return or mutate its
	// working record, keeping supported nested credentials detached for later
	// conditional success and failure handling.
	attemptedProvider := snapshotRefreshRecord(freshProvider)
	if err := ctx.Err(); err != nil {
		if retainCancelledAttempt && !requestPath {
			return &OAuthRefreshResult{Provider: attemptedProvider}, err
		}
		return nil, err
	}
	newCredentials, refreshErr := executor.Refresh(ctx, freshProvider)
	if ctxErr := ctx.Err(); ctxErr != nil {
		// An executor can ignore cancellation and return credentials after shutdown.
		// Discard that observed late response before beginning any credential write.
		if retainCancelledAttempt && !requestPath {
			return &OAuthRefreshResult{Provider: attemptedProvider}, ctxErr
		}
		return nil, ctxErr
	}
	if refreshErr != nil {
		// A consumed token can already have a replacement in storage. Compare a
		// second read before classifying rejection so this call can reuse changed
		// credentials after one exchange rather than invalidating their provider.
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
		// Return the captured attempted identity with the exchange error so failure
		// persistence can reject a newer administrator credential or proxy state.
		result := &OAuthRefreshResult{Provider: attemptedProvider}
		if requestPath && attemptedProvider.Platform == PlatformGrok {
			return result, api.options.Platform.SnapshotError(refreshErr, attemptedProvider)
		}
		return result, refreshErr
	}

	// A supplied map needs a version stamp before conditional persistence. Nil
	// maps skip storage but can still represent a successfully completed
	// exchange.
	if newCredentials != nil {
		// Copy supported credential containers before adding a version stamp so
		// caller metadata does not mutate the executor's nested map observations.
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
				// An exchange may have consumed the input
				// refresh token before this uncertain
				// local write. Contain the outcome so
				// another attempt does not reuse that
				// token as if persistence had safely
				// failed before rotation.
				return nil, api.options.Platform.ContainmentError(fmt.Errorf("OAuth refresh succeeded but credential persistence failed: %w", updateErr))
			}
			// Discard rotated values when the comparison loses to a newer identity;
			// reread that row rather than exchanging its refresh token a second time.
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
			// Readback carries current administrative
			// and scheduling fields after the
			// credential statement, which compares
			// credentials and proxy rather than every
			// later field in the exchanged working
			// record.
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
				// The administrator's matching-row update
				// won this comparison. Return its current
				// record without reexchange, rejecting
				// lost request eligibility when reached.
				if requestPath && (!current.IsActive() || !executor.CanRefresh(current)) {
					return nil, fmt.Errorf("%w: provider changed during refresh", ErrRefreshProviderStateChanged)
				}
				return &OAuthRefreshResult{Provider: current}, nil
			}
			freshProvider.Credentials = CloneValues(newCredentials)
		} else {
			// A shadow inherits parent authentication. Leave its durable credentials
			// untouched while reporting the exchanged map from a permissive executor;
			// the production OpenAI refresher excludes this target before exchange.
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

// releaseRefreshLock attempts cache lease deletion with a separate two-second
// deadline context, retaining parent values while ignoring parent cancellation.
// Errors are logged; production Redis socket I/O uses client timeouts when
// ContextTimeoutEnabled is false, so this context alone gives no wall-clock
// completion bound. Pool acquisition and retry backoff still receive the supplied
// context and can observe its cancellation.
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

// loadGrokDurableProviderAfterPersist attempts cached-token deletion before
// reading committed Grok provider state. A separate two-second deadline context
// ignores parent cancellation, and deletion errors are logged before the reread
// still proceeds.
//
// The reread keeps administrative and scheduling fields current after rotation
// rather than constructing a result from the exchanged map alone. Both
// operations share this cleanup context, so deletion can consume the available
// deadline before storage reads. Production Redis socket I/O uses configured
// client timeouts when context timeouts are disabled; the returned provider or
// read error therefore describes actual completed work, not a guaranteed
// two-second wall-clock bound.
func (api *OAuthRefreshAPI) loadGrokDurableProviderAfterPersist(parent context.Context, cacheKey string, providerID int64) (*Record, error) {
	cleanupParent := context.Background()
	if parent != nil {
		cleanupParent = context.WithoutCancel(parent)
	}
	ctx, cancel := context.WithTimeout(cleanupParent, defaultRefreshPostPersistCleanupTimeout)
	defer cancel()

	// Rotation can make the previously cached access token unusable. Attempt
	// deletion after persistence, then read current state even if deletion failed;
	// both operations receive the separate cleanup deadline context.
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

// IsInvalidGrantError reports whether error text contains a recognized
// rejected-token marker.
func IsInvalidGrantError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "invalid_grant") ||
		strings.Contains(msg, "refresh_token_reused") ||
		strings.Contains(msg, "refresh token has already been used")
}

// tryRecoverFromRefreshRace rereads current state and accepts a changed nonempty
// refresh token after rejection. Callers supply a nonnil usedProvider whose
// refresh_token value represents the token used for that attempt.
//
// Read errors, missing records or empty tokens return false and leave the
// exchange failure with its caller. The comparison uses GetCredential string
// values and does not recheck other provider identity or health fields. The
// owning coordinator performs reached Grok request eligibility after recovery,
// while ordinary background recovery returns the reread record as an unrefreshed
// result with no second exchange.
func (api *OAuthRefreshAPI) tryRecoverFromRefreshRace(ctx context.Context, usedProvider *Record) (*Record, bool) {
	if api.providerRepo == nil {
		return nil, false
	}
	reReadProvider, err := api.providerRepo.GetByID(ctx, usedProvider.ID)
	// Missing current-token evidence cannot recover the rejected exchange.
	// Retain its original failure with the caller rather than replacing it with
	// a secondary read error or assuming the selected record is current.
	if err != nil || reReadProvider == nil {
		return nil, false
	}
	usedRT := usedProvider.GetCredential("refresh_token")
	currentRT := reReadProvider.GetCredential("refresh_token")
	if usedRT == "" || currentRT == "" {
		return nil, false
	}
	// Changed nonempty stored token text supplies recovery evidence after this
	// rejected attempt; the caller decides whether the reread remains
	// request-eligible.
	if usedRT != currentRT {
		return reReadProvider, true
	}
	return nil, false
}

// MergeCredentials fills absent newCreds keys from oldCreds, mutating and
// returning the destination map. A nil destination is allocated, while every
// present key, including one holding nil, retains its supplied value.
//
// The merge lets refreshed token fields take precedence while keeping omitted
// identity or platform metadata from the older map. Values are assigned as
// supplied, so nested mutable data inherited from oldCreds remains shared rather
// than copied. Callers synchronize destination writes and keep shared values
// stable, using CloneValues separately when supported-container detachment is
// required.
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

// snapshotRefreshRecord uses CloneRecord to capture a stable pre-exchange
// record, converting nil credentials to an empty object. Nil input remains nil;
// copied supported containers and pointers detach observations, while
// unsupported mutable values still require owner stability.
func snapshotRefreshRecord(value *Record) *Record {
	copy := CloneRecord(value)
	// Normalize absent credentials to an empty object before JSON comparison so
	// a constructed nil map can match the empty object persisted for that row.
	if copy != nil && copy.Credentials == nil {
		copy.Credentials = map[string]any{}
	}
	return copy
}
