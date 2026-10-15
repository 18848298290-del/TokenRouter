package dto

import (
	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// notify_email.go converts notification recipients between identity records
// and user JSON values.
// Additional notification recipients expose separate delivery and verification
// flags in user JSON. Conversion allocates a result slice while retaining nil
// input as nil, so DTO callers can distinguish an absent collection from an
// allocated empty collection.

// NotifyEmailEntry represents a saved notification recipient and its delivery
// and verification flags.
type NotifyEmailEntry struct {
	Email    string `json:"email"`    // recipient email address
	Disabled bool   `json:"disabled"` // recipient delivery disabled
	Verified bool   `json:"verified"` // recipient verification complete
}

// NotifyEmailEntriesFromIdentity copies identity recipients into an
// independently allocated DTO slice. Nil input returns nil; empty allocated
// input produces an empty allocated result, and scalar email and flag values
// are copied.
func NotifyEmailEntriesFromIdentity(entries []identity.NotifyEmailEntry) []NotifyEmailEntry {
	if entries == nil {
		return nil
	}
	result := make([]NotifyEmailEntry, len(entries))
	for i, e := range entries {
		result[i] = NotifyEmailEntry{
			Email:    e.Email,
			Disabled: e.Disabled,
			Verified: e.Verified,
		}
	}
	return result
}

// NotifyEmailEntriesToIdentity copies DTO recipients into an independently
// allocated identity slice. Nil input returns nil; empty allocated input
// produces an empty allocated result, and scalar email and flag values are
// copied.
func NotifyEmailEntriesToIdentity(entries []NotifyEmailEntry) []identity.NotifyEmailEntry {
	if entries == nil {
		return nil
	}
	result := make([]identity.NotifyEmailEntry, len(entries))
	for i, e := range entries {
		result[i] = identity.NotifyEmailEntry{
			Email:    e.Email,
			Disabled: e.Disabled,
			Verified: e.Verified,
		}
	}
	return result
}
