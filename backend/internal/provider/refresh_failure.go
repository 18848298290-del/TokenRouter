package provider

import (
	"context"
	"time"
)

const (
	RefreshFailurePermanent RefreshFailureKind = iota
	RefreshFailureCooldown
)

// RefreshFailureVersion captures the attempted identity and scheduling state for conditional failure writes.
type RefreshFailureVersion struct {
	CredentialVersion
	Schedulable bool
}
type RefreshFailureKind uint8

type RefreshFailure struct {
	Kind    RefreshFailureKind
	Message string
	Until   time.Time
}

// RefreshFailureWriter reports whether the expected identity and state match; retaining a longer existing
// cooldown still counts as a match.
// A mismatch must not trigger an in-memory block, success invalidation, or another exchange.
type RefreshFailureWriter interface {
	ApplyOAuthRefreshFailure(context.Context, RefreshFailureVersion, RefreshFailure) (bool, error)
}

func FailureVersion(value *Record) RefreshFailureVersion {
	if value == nil {
		return RefreshFailureVersion{}
	}
	credentials := CloneValues(value.Credentials)
	if credentials == nil {
		credentials = map[string]any{}
	}
	return RefreshFailureVersion{CredentialVersion: CredentialVersion{ID: value.ID, Platform: value.Platform, Type: value.Type, Status: value.Status, Credentials: credentials, ProxyID: clonePointer(value.ProxyID)}, Schedulable: value.Schedulable}
}
