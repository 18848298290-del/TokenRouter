package provider

import (
	"context"
	"errors"
	"time"

	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// qoder_request_refresh.go recovers Qoder sessions after request failures
// through identity-checked refresh coordination. A Qoder request failure can
// race with another worker rotating the same provider credentials. The refresh
// adapter hashes the identity used by the failed request, asks the shared
// coordinator to exchange only that matching identity, and invalidates cached
// sessions when a resulting record is accepted.
//
// Lock contention follows a stricter return path: storage is reread
// immediately and then polled for a changed credential hash within the core
// wait budget. Non-contention results can return the coordinator's record, a
// fresh storage read, or the supplied record after read failure. These paths
// distinguish observed rotation from best-effort recovery rather than
// promising that every successful return contains different credentials.

// QoderRequestRefresh binds failed-request credential exchange to storage and
// cached-session invalidation.
type QoderRequestRefresh struct {
	// Store is required and supplies provider rereads after coordinated refresh.
	// Lock contention polls it for a hash different from the failed identity.
	Store provider.RefreshRepository
	// Tokens optionally invalidates cached sessions after an accepted record or
	// rotation. Missing invalidation support leaves credential refresh results
	// usable.
	Tokens *QoderTokenProvider
	// Coordinator is required to claim and persist refresh work. Missing
	// coordination
	// returns a configuration error before attempting the failed-identity exchange.
	Coordinator *provider.OAuthRefreshAPI
	// NewRefresher optionally constructs the exchanger used for this attempt. Nil
	// selects a refresher built from Transport and Profiles; a nil result is
	// rejected.
	NewRefresher func() *QoderTokenRefresher
	// Transport and Profiles supply the default refresher when NewRefresher is
	// absent. A custom factory owns its own choice of these request dependencies.
	Transport QoderTransport              // default refresh HTTP transport
	Profiles  *egressprovider.TLSProfiles // default TLS profile resolver
}

// qoderFailedRefreshExecutor checks failed identity before delegating a
// coordinated site exchange.
type qoderFailedRefreshExecutor struct {
	*QoderTokenRefresher        // delegated site exchange adapter
	failedCredentials    string // failed request identity hash
}

// RefreshProviderSession refreshes credentials still matching a failed Qoder
// request.
// It requires a record, repository, coordinator and a nonnil refresher result.
// The executor compares failed identity after the coordinator rereads storage,
// so credentials already rotated by another worker skip another exchange.
//
// A held refresh lock enters storage polling and returns only an observed
// changed hash. Other results prefer the coordinator record, then a successful
// reread, and finally the supplied record when that read fails. Optional session
// invalidation follows the selected result; this best-effort path can return
// unchanged credentials and does not turn a storage reread failure into an error.
func (s *QoderRequestRefresh) RefreshProviderSession(ctx context.Context, value *provider.Record) (*provider.Record, error) {
	if s == nil {
		return nil, errors.New("qoder gateway service is not configured")
	}
	if value == nil {
		return nil, errors.New("provider is nil")
	}
	if s.Store == nil {
		return nil, errors.New("qoder provider repository is not configured")
	}
	refresherFactory := s.NewRefresher
	if refresherFactory == nil {
		refresherFactory = func() *QoderTokenRefresher {
			return NewQoderTokenRefresher(QoderRefreshOptions{Transport: s.Transport, Profiles: s.Profiles})
		}
	}
	refresher := refresherFactory()
	if refresher == nil {
		return nil, errors.New("qoder token refresher is nil")
	}
	refreshAPI := s.Coordinator
	if refreshAPI == nil {
		return nil, errors.New("qoder refresh API is not configured")
	}
	failedCredentialsHash := provider.QoderRefreshCredentialsHash(value.Credentials)
	executor := qoderFailedRefreshExecutor{
		QoderTokenRefresher: refresher,
		failedCredentials:   failedCredentialsHash,
	}
	result, err := refreshAPI.RefreshIfNeeded(ctx, value, executor, 15*time.Minute)
	if err != nil {
		return nil, err
	}

	// A held lock can belong to another identity exchange. Wait for a stored hash
	// change before returning so this path cannot report the failed identity
	// as rotated.
	if result != nil && result.LockHeld {
		return s.waitForQoderLockedRefresh(ctx, value, failedCredentialsHash)
	}

	// A coordinator result is preferred over a later read because it already
	// reflects that attempt. Session invalidation is optional and follows the
	// chosen record.
	if result != nil && result.Provider != nil {
		if s.Tokens != nil {
			s.Tokens.InvalidateProvider(result.Provider)
		}
		return result.Provider, nil
	}
	// If no coordinator record is available, a successful reread supplies current
	// credentials. Read failures remain best effort and leave the final
	// input-record path.
	if s.Store != nil {
		if fresh, err := s.Store.GetByID(ctx, value.ID); err == nil && fresh != nil {
			if s.Tokens != nil {
				s.Tokens.InvalidateProvider(fresh)
			}
			return fresh, nil
		}
	}
	// No usable reread remains. Invalidate by ID only when an exchange may have
	// occurred, then return the supplied record so the caller retains this
	// attempt identity.
	if s.Tokens != nil && (result == nil || result.Refreshed) {
		s.Tokens.Invalidate(value.ID)
	}
	return value, nil
}

// waitForQoderLockedRefresh polls for a credential hash different from the
// failed one.
// It reads immediately, then uses the core's three-second budget and 100ms
// interval. Read errors are retried within that budget; timeout returns an
// ErrQoderRefreshInProgress wrapper. A changed record invalidates optional sessions
// before being returned.
func (s *QoderRequestRefresh) waitForQoderLockedRefresh(ctx context.Context, value *provider.Record, failedCredentialsHash string) (*provider.Record, error) {
	if s == nil || s.Store == nil || value == nil {
		return nil, provider.ErrQoderRefreshInProgress
	}
	var result *provider.Record
	err := provider.WaitForQoderRefresh(ctx, func(readCtx context.Context) (bool, error) {
		fresh, err := s.Store.GetByID(readCtx, value.ID)
		if err != nil {
			return false, err
		}
		if fresh == nil {
			return false, nil
		}
		// Compare the same authentication fields used before coordination; only a
		// changed hash permits lock-wait recovery and publishes that record to
		// session invalidation.
		if provider.QoderRefreshCredentialsHash(fresh.Credentials) != failedCredentialsHash {
			if s.Tokens != nil {
				s.Tokens.InvalidateProvider(fresh)
			}
			result = fresh
			return true, nil
		}
		return false, nil
	})
	return result, err
}

// NeedsRefresh reports whether current Qoder credentials still match the
// failed identity.
// A nil delegated refresher returns false. The core requires a refresh token,
// uses the supplied failed hash when nonempty, and otherwise applies its normal
// expiry-based eligibility.
func (e qoderFailedRefreshExecutor) NeedsRefresh(value *provider.Record, ttl time.Duration) bool {
	if e.QoderTokenRefresher == nil {
		return false
	}
	return provider.NeedsRefreshQoderAfterFailure(value, e.failedCredentials, ttl)
}
