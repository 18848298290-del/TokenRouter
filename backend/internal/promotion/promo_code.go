package promotion

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/identity/contact"
)

const (
	PromoCodeStatusActive   = "active"
	PromoCodeStatusDisabled = "disabled"
)

// PromoCode describes a registration promotion and its usage limits.
type PromoCode struct {
	ID          int64
	Code        string
	BonusAmount float64
	MaxUses     int
	UsedCount   int
	Status      string
	ExpiresAt   *time.Time
	Notes       string
	CreatedAt   time.Time
	UpdatedAt   time.Time

	// Loaded usage records.
	UsageRecords []PromoCodeUsage
}

// PromoCodeUsage records a user's promotion credit.
type PromoCodeUsage struct {
	ID          int64
	PromoCodeID int64
	UserID      int64
	BonusAmount float64
	UsedAt      time.Time

	// Loaded code and user details.
	PromoCode *PromoCode
	User      *UserView
}

// CreatePromoCodeInput contains the fields for a new promotion code.
type CreatePromoCodeInput struct {
	Code        string
	BonusAmount float64
	MaxUses     int
	ExpiresAt   *time.Time
	Notes       string
}

// UpdatePromoCodeInput contains optional edits to a promotion code.
type UpdatePromoCodeInput struct {
	Code        *string
	BonusAmount *float64
	MaxUses     *int
	Status      *string
	ExpiresAt   *time.Time
	Notes       *string
}

// UserView contains user details included in promotion usage records.
type UserView struct {
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
	DeletedAt                  *time.Time
	BalanceNotifyEnabled       bool
	BalanceNotifyThresholdType string
	BalanceNotifyThreshold     *float64
	BalanceNotifyExtraEmails   []contact.Entry
	TotalRecharged             float64
	RPMLimit                   int
	APIKeyLimit                int
}

func (p *PromoCode) CanUseAt(now time.Time) bool {
	if p.Status != PromoCodeStatusActive {
		return false
	}
	if p.ExpiresAt != nil && now.After(*p.ExpiresAt) {
		return false
	}
	if p.MaxUses > 0 && p.UsedCount >= p.MaxUses {
		return false
	}
	return true
}

// IsExpired reports whether the code expired before the current time.
func (p *PromoCode) IsExpired() bool { return p.IsExpiredAt(time.Now()) }

func (p *PromoCode) IsExpiredAt(now time.Time) bool {
	return p.ExpiresAt != nil && now.After(*p.ExpiresAt)
}
