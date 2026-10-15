package provider

import (
	"context"
	"errors"
	"sync"
	"time"
)

// refresh_contracts.go defines OAuth refresh dependencies and constructs the
// shared refresh coordinator.
// OAuth refresh reads the current provider before exchange and checks separate
// storage capabilities before saving the result. Executors decide platform
// eligibility and expiry, the optional cache supplies distributed locking, and
// platform policy attaches Grok-specific failure information. Construction
// supplies defaults and leaves exchange work to coordinator entry points.

// RefreshRepository loads persisted provider state for refresh eligibility and
// identity checks.
type RefreshRepository interface {
	// GetByID loads the selected provider or returns a storage error for the
	// coordinator. A nil record with nil error is also treated as missing current
	// state before exchange.
	GetByID(context.Context, int64) (*Record, error)
}

// OAuthRefreshExecutor supplies platform eligibility, expiry decisions,
// credential exchange and lock identity.
type OAuthRefreshExecutor interface {
	// CanRefresh reports whether the current provider can use this platform's
	// refresh exchange.
	CanRefresh(*Record) bool
	// NeedsRefresh reports whether the current provider needs exchange within the
	// supplied expiry window. The coordinator calls it after lock acquisition and
	// rereading, allowing an earlier refresh to make this operation unnecessary.
	NeedsRefresh(*Record, time.Duration) bool
	// Refresh exchanges the current provider's credentials using the supplied
	// cancellation context. Nil credentials with nil error can represent completed
	// work; the coordinator discards a response observed after cancellation before
	// starting persistence.
	Refresh(context.Context, *Record) (map[string]any, error)
	// CacheKey identifies the lock shared with this provider's other refresh entry
	// points. The coordinator derives it from the selected record before rereading,
	// so callers use consistent keys across background, request and administrative
	// work.
	CacheKey(*Record) string
}

// RefreshCache provides distributed lock acquisition, release and cached
// access-token invalidation.
type RefreshCache interface {
	// AcquireRefreshLock tries to reserve the key for the supplied lease duration.
	// The coordinator treats false as contention and retains local protection on
	// error. The Redis adapter can retry SETNX after a lost reply, so false or
	// error does not prove that an earlier command left the key untouched.
	// This interface supplies no renewal operation.
	AcquireRefreshLock(context.Context, string, time.Duration) (bool, error)
	// ReleaseRefreshLock attempts key deletion using a separate cleanup deadline
	// context. The Redis adapter issues an unconditional DEL and can retry it;
	// an error may follow an earlier applied delete. The coordinator logs errors
	// and releases its local lock after this logical call returns. Redis socket
	// I/O follows client timeouts when ContextTimeoutEnabled is false, while pool
	// acquisition and retry backoff still receive the supplied context.
	ReleaseRefreshLock(context.Context, string) error
	// DeleteAccessToken attempts cached-token deletion for the selected refresh
	// key. Grok success requests deletion after conditional persistence because
	// rotation can make the earlier token unusable; Redis retries mean an error
	// does not prove that every earlier delete left the token untouched.
	DeleteAccessToken(context.Context, string) error
}

// RefreshPlatformPolicy supplies platform-specific eligibility and error
// classification callbacks.
type RefreshPlatformPolicy struct {
	Eligibility         func(*Record) error // request-side Grok eligibility check
	MissingRefreshToken func() error        // missing Grok credential error
	// attempted credential error wrapper
	SnapshotError      func(error, *Record) error
	ConfigurationError func(error) error // shared configuration error wrapper
	ContainmentError   func(error) error // uncertain Grok persistence wrapper
}

// RefreshOptions supplies the clock, lock budget, logging, and platform policy.
type RefreshOptions struct {
	LockTTL  time.Duration         // distributed refresh lease duration
	Now      func() time.Time      // credential version stamp clock
	Warn     func(string, ...any)  // degraded operation logging callback
	Info     func(string, ...any)  // recovered race logging callback
	Error    func(string, ...any)  // failed persistence logging callback
	Platform RefreshPlatformPolicy // platform eligibility and error callbacks
}

// OAuthRefreshResult reports exchange completion, lock contention and the
// applicable provider state.
type OAuthRefreshResult struct {
	// Refreshed marks a completed exchange, including a nil-credential result;
	// callers use NewCredentials separately to determine whether credentials
	// were supplied for persistence or further handling.
	Refreshed bool
	// NewCredentials holds the versioned exchange map when supplied. Supported
	// containers are detached from executor values, while unsupported values
	// can remain shared under CloneValues.
	NewCredentials map[string]any `json:"-"`
	// Provider holds current state on a skip or success and the captured
	// attempted identity on an exchange failure retained for conditional writes.
	Provider *Record `json:"-"`
	// LockHeld reports an unsuccessful distributed acquisition result before the
	// provider reread. Redis retries can return false after an earlier command
	// installed the lease; callers apply their token or statistics skip policy.
	LockHeld bool
}

// OAuthRefreshAPI shares refresh locking and shutdown registration across
// credential exchange entry points.
type OAuthRefreshAPI struct {
	activity     refreshActivity   // registered work and shutdown state
	providerRepo RefreshRepository // current provider and conditional writers
	tokenCache   RefreshCache      // optional distributed refresh cache
	lockTTL      time.Duration     // captured distributed lease duration
	localLocks   sync.Map          // cache-key process lock table
	options      RefreshOptions    // defaulted clock and callbacks
}

// NewOAuthRefreshAPI captures the provider reader and optional distributed cache
// for coordinated exchanges. Nonpositive LockTTL selects sixty seconds, nil Now
// selects time.Now, and nil logging callbacks become no-ops. Missing platform
// callbacks become error suppliers or pass-through wrappers, and construction
// starts no background work.
func NewOAuthRefreshAPI(repo RefreshRepository, cache RefreshCache, options RefreshOptions) *OAuthRefreshAPI {
	if options.LockTTL <= 0 {
		options.LockTTL = defaultRefreshLockTTL
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	noop := func(string, ...any) {}
	if options.Warn == nil {
		options.Warn = noop
	}
	if options.Info == nil {
		options.Info = noop
	}
	if options.Error == nil {
		options.Error = noop
	}
	if options.Platform.Eligibility == nil {
		options.Platform.Eligibility = func(*Record) error { return errors.New("oauth refresh request eligibility policy is not configured") }
	}
	if options.Platform.MissingRefreshToken == nil {
		options.Platform.MissingRefreshToken = func() error { return errors.New("refresh token is missing") }
	}
	if options.Platform.SnapshotError == nil {
		options.Platform.SnapshotError = func(err error, _ *Record) error { return err }
	}
	if options.Platform.ConfigurationError == nil {
		options.Platform.ConfigurationError = func(err error) error { return err }
	}
	if options.Platform.ContainmentError == nil {
		options.Platform.ContainmentError = func(err error) error { return err }
	}
	return &OAuthRefreshAPI{providerRepo: repo, tokenCache: cache, lockTTL: options.LockTTL, options: options}
}

// LockLease reports the captured distributed lease and whether a cache was
// supplied. A nil coordinator returns zero and false; background assembly uses
// the pair to bound its attempt timeout.
func (api *OAuthRefreshAPI) LockLease() (time.Duration, bool) {
	if api == nil {
		return 0, false
	}
	return api.lockTTL, api.tokenCache != nil
}
