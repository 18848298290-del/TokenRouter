package provider

import (
	"context"
	"errors"
	"fmt"
)

// ErrRefreshSkipped reports lock contention or an already-refreshed provider; callers classify it according to their skip policy.
var ErrRefreshSkipped = fmt.Errorf("refresh skipped")

type ProviderConfigurationRefreshError struct {
	Cause error `json:"-"`
}

type ProviderCycleContainmentRefreshError struct {
	Cause error `json:"-"`
}

type ProviderPermanentRefreshError struct {
	Cause                   error `json:"-"`
	PersistentlyBlocked     bool  `json:"-"`
	CacheInvalidationFailed bool  `json:"-"`
}

type RefreshAttemptTimeoutError struct {
	Cause error `json:"-"`
}

func IsProviderScopedTerminalRefreshError(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := errors.AsType[*ProviderCycleContainmentRefreshError](err); ok {
		return true
	}
	var configurationErr *ProviderConfigurationRefreshError
	return errors.As(err, &configurationErr)
}

func (e *RefreshAttemptTimeoutError) Error() string {
	return "OAuth refresh attempt timed out"
}

func (e *RefreshAttemptTimeoutError) Unwrap() error {
	if e == nil || e.Cause == nil {
		return context.DeadlineExceeded
	}
	return e.Cause
}

func (e *ProviderConfigurationRefreshError) Error() string {
	return "provider OAuth configuration rejected"
}

func (e *ProviderCycleContainmentRefreshError) Error() string {
	return "provider OAuth failure contained for this cycle"
}

func (e *ProviderCycleContainmentRefreshError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *ProviderPermanentRefreshError) Error() string {
	return "provider OAuth credentials permanently rejected"
}

func (e *ProviderPermanentRefreshError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *ProviderConfigurationRefreshError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
