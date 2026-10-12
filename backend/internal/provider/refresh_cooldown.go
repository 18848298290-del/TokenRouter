package provider

import (
	"time"
)

// RefreshCooldownVersion captures credentials and the observed cooldown for conditional cleanup after refresh.
type RefreshCooldownVersion struct {
	CredentialVersion
	ParentProviderID *int64
	QuotaDimension   string
	Until            *time.Time
	Reason           string
}

func ObserveRefreshCooldown(value *Record) RefreshCooldownVersion {
	if value == nil {
		return RefreshCooldownVersion{}
	}
	return RefreshCooldownVersion{
		CredentialVersion: FailureVersion(value).CredentialVersion,
		ParentProviderID:  clonePointer(value.ParentProviderID), QuotaDimension: value.QuotaDimension,
		Until: clonePointer(value.TempUnschedulableUntil), Reason: value.TempUnschedulableReason,
	}
}
