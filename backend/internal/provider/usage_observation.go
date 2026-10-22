package provider

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// usage_observation.go captures usage observation versions and constructs
// cache identities for conditional health updates.
// Usage observations can complete after credentials, shadow ownership or health
// windows have changed. Captured versions supply identity and old-window
// comparisons to conditional writers, while cache identities select observations
// belonging to the current provider credentials and quota dimension.

// Observation errors distinguish a superseded provider identity from missing
// conditional storage support so callers can leave older results unapplied.
var (
	ErrUsageObservationChanged       = errors.New("provider identity changed during usage query")
	ErrUsageObservationWriterMissing = errors.New("usage observation conditional writer is not configured")
)

// UsageObservationVersion records identity and observed health windows for
// conditional usage updates. Its pointer fields and credential data are captured
// by ObserveUsageVersion before asynchronous work can outlive that observation.
type UsageObservationVersion struct {
	CredentialVersion // captured credential and provider identity
	// ParentProviderID and QuotaDimension identify the observed shadow quota owner.
	// Conditional writers compare this identity so a query for one parent or
	// dimension cannot publish for another.
	ParentProviderID *int64 // observed shadow parent ID
	QuotaDimension   string // observed quota dimension name
	// RateLimitedAt, RateLimitResetAt and OverloadUntil retain windows read before
	// querying. Conditional recovery compares them to keep later health
	// observations from being cleared by an earlier completed query.
	RateLimitedAt, RateLimitResetAt, OverloadUntil *time.Time // observed health window timestamps
}

// UsageExtraWriter conditionally stores usage fields for a captured provider
// identity.
type UsageExtraWriter interface {
	// UpdateUsageExtraIfUnchanged applies nonempty fields while the captured
	// identity still matches storage. Empty effective patches can succeed before
	// comparison; false reports a changed or absent record, and persistence
	// failures return an error.
	UpdateUsageExtraIfUnchanged(context.Context, UsageObservationVersion, map[string]any) (bool, error)
}

// UsageObservationWriter adds observed health-window changes to conditional
// usage snapshot writes.
type UsageObservationWriter interface {
	UsageExtraWriter // conditional usage snapshot writer
	// SetUsageRateLimitIfUnchanged installs a reset for the captured identity.
	// False reports a changed or absent record, so an older query cannot attribute
	// its quota rejection to replacement credentials.
	SetUsageRateLimitIfUnchanged(context.Context, UsageObservationVersion, time.Time) (bool, error)
	// ClearUsageRateLimitIfUnchanged clears the captured rate-limit and overload
	// windows while identity and those windows still match. False leaves a later
	// health observation available to scheduling and its own recovery path.
	ClearUsageRateLimitIfUnchanged(context.Context, UsageObservationVersion) (bool, error)
}

// ObserveUsageVersion captures credential identity, shadow parent, quota
// dimension and health timestamps from the supplied record. Nil input returns an
// empty version; captured pointers and supported credential collections are
// copied.
func ObserveUsageVersion(value *Record) UsageObservationVersion {
	if value == nil {
		return UsageObservationVersion{}
	}
	return UsageObservationVersion{
		CredentialVersion: ObserveCredentialVersion(value),
		ParentProviderID:  clonePointer(value.ParentProviderID), QuotaDimension: value.QuotaDimension,
		RateLimitedAt: clonePointer(value.RateLimitedAt), RateLimitResetAt: clonePointer(value.RateLimitResetAt), OverloadUntil: clonePointer(value.OverloadUntil),
	}
}

// UsageCacheIdentity returns the credential digest, status, parent ID and quota
// dimension used to separate cached usage observations. Nil input returns empty,
// and an absent parent contributes zero.
func UsageCacheIdentity(value *Record) string {
	if value == nil {
		return ""
	}
	parent := int64(0)
	if value.ParentProviderID != nil {
		parent = *value.ParentProviderID
	}
	return fmt.Sprintf("%s/%s/%d/%s", RefreshCredentialIdentity(value), value.Status, parent, value.QuotaDimension)
}
