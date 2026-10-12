package provider

import (
	"context"
	"errors"
	"fmt"
)

// Refresh failures determine whether one credential can recover or whether work
// for its entire platform should stop for the cycle. Typed errors retain private
// causes for classification while returning fixed public messages, and permanent
// failures separately report durable blocking and cache invalidation outcomes.

// ErrRefreshSkipped identifies an unperformed exchange so refresh policies and
// worker statistics can classify the operation consistently.
var ErrRefreshSkipped = fmt.Errorf("refresh skipped")

// ProviderConfigurationRefreshError stops platform work after a shared OAuth
// configuration rejection.
type ProviderConfigurationRefreshError struct {
	Cause error `json:"-"` // private diagnostic cause
}

// ProviderCycleContainmentRefreshError stops further platform work after an
// uncertain refresh outcome.
type ProviderCycleContainmentRefreshError struct {
	Cause error `json:"-"` // private diagnostic cause
}

// ProviderPermanentRefreshError reports rejected credentials and the attempted
// containment outcomes.
type ProviderPermanentRefreshError struct {
	Cause error `json:"-"` // rejected exchange cause
	// PersistentlyBlocked reports a matching permanent-failure write;
	// reconciliation counts this result as blocked even if token-cache
	// invalidation later fails.
	PersistentlyBlocked bool `json:"-"`
	// CacheInvalidationFailed marks a missing or failing invalidator; Grok
	// reconciliation uses it to report a partially completed blocking action.
	CacheInvalidationFailed bool `json:"-"`
}

// RefreshAttemptTimeoutError distinguishes an attempt deadline from cancellation
// of its outer cycle.
type RefreshAttemptTimeoutError struct {
	// Cause carries the attempt failure; when absent, Unwrap supplies
	// context.DeadlineExceeded so callers can still classify the timeout.
	Cause error `json:"-"`
}

// IsProviderScopedTerminalRefreshError reports a configuration or containment
// error anywhere in the error chain. RefreshAttempts uses this
// classification to keep such outcomes from becoming deadline retries or
// provider cooldown evidence.
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

// Error returns the fixed public message for an expired refresh attempt.
func (e *RefreshAttemptTimeoutError) Error() string {
	return "OAuth refresh attempt timed out"
}

// Unwrap returns the underlying attempt failure, selecting
// context.DeadlineExceeded when the receiver or Cause is nil. That default lets
// errors.Is recognize a timeout even when no cause was recorded.
func (e *RefreshAttemptTimeoutError) Unwrap() error {
	if e == nil || e.Cause == nil {
		return context.DeadlineExceeded
	}
	return e.Cause
}

// Error returns a fixed message that excludes the private configuration cause.
func (e *ProviderConfigurationRefreshError) Error() string {
	return "provider OAuth configuration rejected"
}

// Error returns a fixed message for containment of the platform cycle.
func (e *ProviderCycleContainmentRefreshError) Error() string {
	return "provider OAuth failure contained for this cycle"
}

// Unwrap exposes the private cause for refresh classification and diagnostic
// error chaining. A nil receiver yields nil, allowing callers to inspect an
// absent wrapper safely.
func (e *ProviderCycleContainmentRefreshError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Error returns a fixed message for permanently rejected OAuth credentials.
func (e *ProviderPermanentRefreshError) Error() string {
	return "provider OAuth credentials permanently rejected"
}

// Unwrap exposes the credential rejection retained for refresh error
// classification. A nil receiver yields nil; the fixed Error message remains
// separate from this diagnostic cause.
func (e *ProviderPermanentRefreshError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Unwrap exposes the shared configuration failure for classification through
// wrapped errors. A nil receiver yields nil, and callers obtain the public
// message through Error separately.
func (e *ProviderConfigurationRefreshError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
