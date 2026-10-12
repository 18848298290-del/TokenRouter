package dto

import (
	"time"

	billinghttpapi "github.com/TokenFlux/TokenRouter/internal/billing/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/identity"
)

type User[K any] struct {
	PreferredLocale *string `json:"preferred_locale"`
	ID              int64   `json:"id"`
	Email           string  `json:"email"`
	Username        string  `json:"username"`
	Role            string  `json:"role"`
	Balance         float64 `json:"balance"`
	FrozenBalance   float64 `json:"frozen_balance"`
	Concurrency     int     `json:"concurrency"`
	Status          string  `json:"status"`
	AllowedGroups   []int64 `json:"allowed_groups"`
	// DisabledPublicGroups lists public groups denied to this user by administrators.
	DisabledPublicGroups []int64    `json:"disabled_public_groups"`
	LastActiveAt         *time.Time `json:"last_active_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
	DeletedAt            *time.Time `json:"deleted_at,omitempty"`

	// Low-balance notification settings.
	BalanceNotifyEnabled       bool               `json:"balance_notify_enabled"`
	BalanceNotifyThresholdType string             `json:"balance_notify_threshold_type"`
	BalanceNotifyThreshold     *float64           `json:"balance_notify_threshold"`
	BalanceNotifyExtraEmails   []NotifyEmailEntry `json:"balance_notify_extra_emails"`
	TotalRecharged             float64            `json:"total_recharged"`

	// RPMLimit is the user-level requests-per-minute limit; zero means unlimited.
	// It applies when the selected group has no RPM limit.
	RPMLimit int `json:"rpm_limit"`
	// APIKeyLimit caps the number of API keys the user can create; zero means unlimited.
	APIKeyLimit int `json:"api_key_limit"`

	APIKeys       []K                               `json:"api_keys,omitempty"`
	Subscriptions []billinghttpapi.UserSubscription `json:"subscriptions,omitempty"`
}

// AdminUser is the administrator response, including internal user fields.
// User-facing endpoints must not expose administrator notes.
type AdminUser[K any] struct {
	User[K]

	Notes      string     `json:"notes"`
	LastUsedAt *time.Time `json:"last_used_at"`
	// GroupRates maps group IDs to user-specific rate multipliers.
	// map[groupID]rateMultiplier
	GroupRates map[int64]float64 `json:"group_rates,omitempty"`
}

// AuthResponse contains authentication tokens and user data.
type AuthResponse[K any] struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token,omitempty"`
	ExpiresIn    int      `json:"expires_in,omitempty"`
	TokenType    string   `json:"token_type"`
	User         *User[K] `json:"user"`
}

func UserFromIdentityShallow[K any](u *identity.User) *User[K] {
	if u == nil {
		return nil
	}
	return &User[K]{
		PreferredLocale:            u.PreferredLocale,
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
		BalanceNotifyEnabled:       u.BalanceNotifyEnabled,
		BalanceNotifyThresholdType: u.BalanceNotifyThresholdType,
		BalanceNotifyThreshold:     u.BalanceNotifyThreshold,
		BalanceNotifyExtraEmails:   NotifyEmailEntriesFromIdentity(u.BalanceNotifyExtraEmails),
		TotalRecharged:             u.TotalRecharged,
		RPMLimit:                   u.RPMLimit,
		APIKeyLimit:                u.APIKeyLimit,
		DeletedAt:                  u.DeletedAt,
	}
}

// UserFromIdentity converts the user, subscriptions, and caller-supplied key data to a response.
func UserFromIdentity[K any](u *identity.User, keys []K) *User[K] {
	out := UserFromIdentityShallow[K](u)
	if out == nil {
		return nil
	}
	out.APIKeys = keys
	if len(u.Subscriptions) > 0 {
		out.Subscriptions = make([]billinghttpapi.UserSubscription, 0, len(u.Subscriptions))
		for i := range u.Subscriptions {
			out.Subscriptions = append(out.Subscriptions, *billinghttpapi.UserSubscriptionFromService(&u.Subscriptions[i]))
		}
	}
	return out
}

func AdminUserFromIdentity[K any](u *identity.User, keys []K) *AdminUser[K] {
	base := UserFromIdentity(u, keys)
	if base == nil {
		return nil
	}
	return &AdminUser[K]{User: *base, Notes: u.Notes, LastUsedAt: u.LastUsedAt, GroupRates: u.GroupRates}
}
