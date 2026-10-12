package provider

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	ErrUsageObservationChanged       = errors.New("provider identity changed during usage query")
	ErrUsageObservationWriterMissing = errors.New("usage observation conditional writer is not configured")
)

// UsageObservationVersion captures provider identity and health windows for conditional observation writes.
type UsageObservationVersion struct {
	CredentialVersion
	ParentProviderID                               *int64
	QuotaDimension                                 string
	RateLimitedAt, RateLimitResetAt, OverloadUntil *time.Time
}

// UsageExtraWriter conditionally writes a usage snapshot for the observed provider version.
type UsageExtraWriter interface {
	UpdateUsageExtraIfUnchanged(context.Context, UsageObservationVersion, map[string]any) (bool, error)
}

// UsageObservationWriter conditionally writes snapshots and changes rate limits
// for the observed provider version.
type UsageObservationWriter interface {
	UsageExtraWriter
	SetUsageRateLimitIfUnchanged(context.Context, UsageObservationVersion, time.Time) (bool, error)
	ClearUsageRateLimitIfUnchanged(context.Context, UsageObservationVersion) (bool, error)
}

func ObserveUsageVersion(value *Record) UsageObservationVersion {
	if value == nil {
		return UsageObservationVersion{}
	}
	return UsageObservationVersion{
		CredentialVersion: FailureVersion(value).CredentialVersion,
		ParentProviderID:  clonePointer(value.ParentProviderID), QuotaDimension: value.QuotaDimension,
		RateLimitedAt: clonePointer(value.RateLimitedAt), RateLimitResetAt: clonePointer(value.RateLimitResetAt), OverloadUntil: clonePointer(value.OverloadUntil),
	}
}

// UsageCacheIdentity identifies cached usage by credentials, status, parent, and quota dimension.
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
