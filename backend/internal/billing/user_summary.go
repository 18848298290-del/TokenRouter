package billing

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/identity/contact"
)

// Entitlement queries need balances, request limits and alert preferences from
// a user snapshot. This representation carries those values across billing
// services and persistence adapters, while notification address parsing
// accepts both saved record arrays and historical string arrays.

// UserSummary carries user funds, access limits and notification preferences
// for billing consumers.
type UserSummary struct {
	ID                         int64      // billing user record identifier
	Email                      string     // primary user email address
	Username                   string     // user display name text
	Role                       string     // stored user role name
	Balance                    float64    // current user balance amount
	FrozenBalance              float64    // frozen user balance amount
	Concurrency                int        // user concurrent request limit
	Status                     string     // stored user status name
	AllowedGroups              []int64    // explicitly permitted group identifiers
	DisabledPublicGroups       []int64    // administrator-denied public group identifiers
	LastActiveAt               *time.Time // latest observed activity time
	CreatedAt                  time.Time  // user record creation time
	UpdatedAt                  time.Time  // user record update time
	BalanceNotifyEnabled       bool       // user balance alert switch
	BalanceNotifyThresholdType string     // fixed amount or percentage
	// BalanceNotifyThreshold overrides the global amount when present. Balance
	// notifications interpret it using BalanceNotifyThresholdType and
	// TotalRecharged; nil selects the global threshold.
	BalanceNotifyThreshold   *float64             // user balance alert threshold
	BalanceNotifyExtraEmails []NotifyEmailSummary // saved additional alert recipients
	TotalRecharged           float64              // cumulative credited balance amount
	// RPMLimit supplies the total user request ceiling after scheduler group
	// admission. Positive values apply independently of group ceilings, while
	// zero disables the user-wide check.
	RPMLimit int // requests per minute
	// APIKeyLimit supplies the user API key creation ceiling. A zero value leaves
	// creation unlimited in the API key service.
	APIKeyLimit int        // API key creation limit
	DeletedAt   *time.Time // user soft-deletion timestamp
}

// NotifyEmailSummary is the saved contact address and its delivery flags.
type NotifyEmailSummary = contact.Entry

// ParseNotifyEmails delegates saved recipient decoding to the identity contact
// parser. Record arrays retain their flags; historical strings are trimmed
// into unverified records, and empty or undecodable input yields no
// recipients.
func ParseNotifyEmails(raw string) []NotifyEmailSummary { return contact.ParseNotifyEmails(raw) }
