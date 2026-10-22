package provider

import (
	"time"
)

// refresh_cooldown.go captures credential identity and temporary pause fields
// for conditional refresh cleanup.
// Successful refresh may clear a temporary pause that was observed before the
// exchange. Capturing its deadline and reason alongside authentication identity
// lets storage leave a newer administrator pause in place, even when the
// provider ID still matches.

// RefreshCooldownVersion carries the observed identity and temporary window
// required for conditional recovery.
type RefreshCooldownVersion struct {
	CredentialVersion // observed credential comparison state
	// ParentProviderID copies the observed parent value so later changes to
	// that pointer cannot change this cooldown comparison's captured parent.
	ParentProviderID *int64
	QuotaDimension   string // observed provider quota dimension
	// Until and Reason identify the exact pause observed before cleanup.
	// Storage compares both alongside authentication identity, and a changed
	// window makes post actions suppress publication of their stale record.
	Until  *time.Time // detached observed pause deadline
	Reason string     // observed temporary pause reason
}

// ObserveRefreshCooldown captures identity, parent, quota dimension and the
// current temporary pause for conditional cleanup. Nil input yields a zero
// version; copied pointers and supported credential containers detach the
// observation from later updates.
func ObserveRefreshCooldown(value *Record) RefreshCooldownVersion {
	if value == nil {
		return RefreshCooldownVersion{}
	}
	return RefreshCooldownVersion{
		CredentialVersion: ObserveCredentialVersion(value),
		ParentProviderID:  clonePointer(value.ParentProviderID), QuotaDimension: value.QuotaDimension,
		Until: clonePointer(value.TempUnschedulableUntil), Reason: value.TempUnschedulableReason,
	}
}
