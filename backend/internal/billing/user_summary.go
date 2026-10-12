package billing

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/identity/contact"
)

// UserSummary contains user details needed by entitlement queries.
type UserSummary struct {
	ID                         int64
	Email                      string
	Username                   string
	Role                       string
	Balance                    float64
	FrozenBalance              float64
	Concurrency                int
	Status                     string
	AllowedGroups              []int64
	DisabledPublicGroups       []int64
	LastActiveAt               *time.Time
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
	BalanceNotifyEnabled       bool
	BalanceNotifyThresholdType string
	BalanceNotifyThreshold     *float64
	BalanceNotifyExtraEmails   []NotifyEmailSummary
	TotalRecharged             float64
	RPMLimit                   int
	APIKeyLimit                int
	DeletedAt                  *time.Time
}

// NotifyEmailSummary is a contact email record.
type NotifyEmailSummary = contact.Entry

// ParseNotifyEmails accepts JSON arrays of email strings or contact records.
func ParseNotifyEmails(raw string) []NotifyEmailSummary { return contact.ParseNotifyEmails(raw) }
