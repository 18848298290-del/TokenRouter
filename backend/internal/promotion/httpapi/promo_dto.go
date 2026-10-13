package httpapi

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	identitydto "github.com/TokenFlux/TokenRouter/internal/identity/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
)

// Promo responses expose the grant controls separately from usage receipts,
// whose amount records the value at redemption. Conversion allocates response
// containers while retaining optional time pointers; recipient conversion
// reuses identity's shallow account mapping and copies notification recipient
// entries.

// PromoCode is the administrator response for a registration balance
// promotion.
type PromoCode struct {
	ID          int64   `json:"id"`           // promotion row identifier
	Code        string  `json:"code"`         // registration promotion text
	BonusAmount float64 `json:"bonus_amount"` // USD
	// MaxUses and UsedCount describe total grant capacity. A positive maximum
	// prevents further redemption when the count reaches it; zero maximum leaves
	// total usage unlimited.
	MaxUses   int    `json:"max_uses"`   // positive total-use ceiling
	UsedCount int    `json:"used_count"` // recorded promotion grant count
	Status    string `json:"status"`     // saved availability status
	// ExpiresAt retains the service record's time pointer. Nil serializes as
	// null; callers copy the time before changing the response independently.
	// optional eligibility deadline
	ExpiresAt *time.Time `json:"expires_at"`
	Notes     string     `json:"notes"`      // administrator promotion note
	CreatedAt time.Time  `json:"created_at"` // promotion creation timestamp
	// latest promotion mutation timestamp
	UpdatedAt time.Time `json:"updated_at"`
}

// PromoCodeUsage is the administrator response for one recorded registration
// grant.
type PromoCodeUsage struct {
	ID int64 `json:"id"` // grant receipt identifier
	// granting promotion identifier
	PromoCodeID int64 `json:"promo_code_id"`
	// credited account identifier
	UserID      int64     `json:"user_id"`
	BonusAmount float64   `json:"bonus_amount"` // USD
	UsedAt      time.Time `json:"used_at"`      // recorded grant timestamp

	// User is populated only when the service receipt contains loaded recipient
	// details. Nil omits the field, and its shallow account pointers remain
	// shared.
	// optional loaded recipient response
	User *identitydto.User[struct{}] `json:"user,omitempty"`
}

// PromoCodeFromService allocates a response record for a service promotion or
// returns nil for nil input.
//
// Scalar fields are copied, while ExpiresAt retains the service value's time
// pointer.
func PromoCodeFromService(pc *promotion.PromoCode) *PromoCode {
	if pc == nil {
		return nil
	}
	return &PromoCode{
		ID:          pc.ID,
		Code:        pc.Code,
		BonusAmount: pc.BonusAmount,
		MaxUses:     pc.MaxUses,
		UsedCount:   pc.UsedCount,
		Status:      pc.Status,
		ExpiresAt:   pc.ExpiresAt,
		Notes:       pc.Notes,
		CreatedAt:   pc.CreatedAt,
		UpdatedAt:   pc.UpdatedAt,
	}
}

// PromoCodeUsageFromService allocates a receipt response or returns nil for
// nil input.
//
// It converts an optional loaded recipient through promoUserDTO; the service
// receipt's amount remains the recorded grant value.
func PromoCodeUsageFromService(u *promotion.PromoCodeUsage) *PromoCodeUsage {
	if u == nil {
		return nil
	}
	return &PromoCodeUsage{
		ID:          u.ID,
		PromoCodeID: u.PromoCodeID,
		UserID:      u.UserID,
		BonusAmount: u.BonusAmount,
		UsedAt:      u.UsedAt,
		User:        promoUserDTO(u.User),
	}
}

// promoUserDTO maps optional recipient data through identity's shallow
// account response converter.
//
// Nil input returns nil. Account time and threshold pointers and group slices
// remain shared; notification recipient entries are copied into DTO storage.
func promoUserDTO(u *promotion.UserView) *identitydto.User[struct{}] {
	if u == nil {
		return nil
	}
	return identitydto.UserFromIdentityShallow[struct{}](&identity.User{
		ID:                         u.ID,
		Email:                      u.Email,
		Username:                   u.Username,
		Role:                       u.Role,
		Balance:                    u.Balance,
		FrozenBalance:              u.FrozenBalance,
		Concurrency:                u.Concurrency,
		Status:                     u.Status,
		AllowedGroups:              u.AllowedGroups,
		DisabledPublicGroups:       u.DisabledPublicGroups,
		LastActiveAt:               u.LastActiveAt,
		CreatedAt:                  u.CreatedAt,
		UpdatedAt:                  u.UpdatedAt,
		DeletedAt:                  u.DeletedAt,
		BalanceNotifyEnabled:       u.BalanceNotifyEnabled,
		BalanceNotifyThresholdType: u.BalanceNotifyThresholdType,
		BalanceNotifyThreshold:     u.BalanceNotifyThreshold,
		BalanceNotifyExtraEmails:   u.BalanceNotifyExtraEmails,
		TotalRecharged:             u.TotalRecharged,
		RPMLimit:                   u.RPMLimit,
		APIKeyLimit:                u.APIKeyLimit,
	})
}
