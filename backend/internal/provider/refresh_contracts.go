package provider

import (
	"context"
	"errors"
	"sync"
	"time"
)

// RefreshRepository reads persisted provider state; conditional write interfaces are checked separately.
type RefreshRepository interface {
	GetByID(context.Context, int64) (*Record, error)
}

// OAuthRefreshExecutor checks eligibility and exchanges credentials for one platform.
type OAuthRefreshExecutor interface {
	CanRefresh(*Record) bool
	NeedsRefresh(*Record, time.Duration) bool
	Refresh(context.Context, *Record) (map[string]any, error)
	CacheKey(*Record) string
}

// RefreshCache provides distributed refresh locks and access-token invalidation.
type RefreshCache interface {
	AcquireRefreshLock(context.Context, string, time.Duration) (bool, error)
	ReleaseRefreshLock(context.Context, string) error
	DeleteAccessToken(context.Context, string) error
}

// RefreshPlatformPolicy supplies platform-specific eligibility and error classification callbacks.
type RefreshPlatformPolicy struct {
	Eligibility         func(*Record) error
	MissingRefreshToken func() error
	SnapshotError       func(error, *Record) error
	ConfigurationError  func(error) error
	ContainmentError    func(error) error
}

// RefreshOptions supplies the clock, lock budget, logging, and platform policy.
type RefreshOptions struct {
	LockTTL  time.Duration
	Now      func() time.Time
	Warn     func(string, ...any)
	Info     func(string, ...any)
	Error    func(string, ...any)
	Platform RefreshPlatformPolicy
}

// OAuthRefreshResult holds one exchange's result; provider credentials are excluded from JSON.
type OAuthRefreshResult struct {
	Refreshed      bool
	NewCredentials map[string]any `json:"-"`
	Provider       *Record        `json:"-"`
	LockHeld       bool
}

// OAuthRefreshAPI owns the process-local lock table. Construction starts no background work.
type OAuthRefreshAPI struct {
	activity     refreshActivity
	providerRepo RefreshRepository
	tokenCache   RefreshCache
	lockTTL      time.Duration
	localLocks   sync.Map
	options      RefreshOptions
}

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

// LockLease reports the configured lock TTL and whether distributed locking is configured.
func (api *OAuthRefreshAPI) LockLease() (time.Duration, bool) {
	if api == nil {
		return 0, false
	}
	return api.lockTTL, api.tokenCache != nil
}
