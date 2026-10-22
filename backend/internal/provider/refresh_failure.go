package provider

import (
	"context"
	"time"
)

// refresh_failure.go captures attempted provider state and defines conditional
// refresh-failure writes.
// A failed exchange can make credentials unusable or leave a provider eligible
// for another attempt after a cooldown. FailureVersion captures the attempted
// identity and scheduling switch so storage can reject late failures after an
// administrator changes that row. The action and deadline are carried separately
// from the comparison state.

// Refresh failure actions select durable credential rejection or a temporary
// scheduling cooldown after background attempts fail.
const (
	// RefreshFailurePermanent marks the matching provider errored and
	// unschedulable.
	RefreshFailurePermanent RefreshFailureKind = iota
	// RefreshFailureCooldown extends the matching provider's temporary pause.
	RefreshFailureCooldown
)

// RefreshFailureVersion combines the attempted authentication identity with its
// observed scheduling switch.
type RefreshFailureVersion struct {
	CredentialVersion      // observed credential comparison state
	Schedulable       bool // observed scheduling enablement switch
}

// RefreshFailureKind identifies the persistent action selected for a failed
// exchange.
type RefreshFailureKind uint8

// RefreshFailure supplies the health action and diagnostic reason for
// conditional persistence.
type RefreshFailure struct {
	Kind    RefreshFailureKind // chosen failure persistence action
	Message string             // stored failure diagnostic reason
	// Until is the cooldown deadline when Kind is RefreshFailureCooldown;
	// permanent failures ignore this value and update persistent health instead.
	Until time.Time
}

// RefreshFailureWriter conditionally records failure against the attempted
// identity and scheduling state.
type RefreshFailureWriter interface {
	// ApplyOAuthRefreshFailure reports whether the expected identity and scheduling
	// state match, even when an existing cooldown is already long enough. A false
	// result tells RefreshAttempts to skip runtime blocking and stale-result cleanup.
	ApplyOAuthRefreshFailure(context.Context, RefreshFailureVersion, RefreshFailure) (bool, error)
}

// FailureVersion captures credential identity and scheduling state for a
// conditional failure write. ObserveCredentialVersion supplies detached
// supported values; nil input returns a zero failure version, including its
// disabled scheduling switch.
func FailureVersion(value *Record) RefreshFailureVersion {
	if value == nil {
		return RefreshFailureVersion{}
	}
	return RefreshFailureVersion{
		CredentialVersion: ObserveCredentialVersion(value),
		Schedulable:       value.Schedulable,
	}
}
