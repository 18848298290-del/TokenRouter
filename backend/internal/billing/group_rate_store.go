package billing

import (
	"context"
)

// UserGroupRateEntry contains a user's rate and RPM overrides for a group.
// Nil RateMultiplier and RPMOverride values mean unset (SQL NULL).
type UserGroupRateEntry struct {
	UserID         int64    `json:"user_id"`
	UserName       string   `json:"user_name"`
	UserEmail      string   `json:"user_email"`
	UserNotes      string   `json:"user_notes"`
	UserStatus     string   `json:"user_status"`
	RateMultiplier *float64 `json:"rate_multiplier,omitempty"`
	RPMOverride    *int     `json:"rpm_override,omitempty"`
}

// GroupRateMultiplierInput supplies one user-specific rate in a group batch update.
type GroupRateMultiplierInput struct {
	UserID         int64   `json:"user_id"`
	RateMultiplier float64 `json:"rate_multiplier"`
}

// GroupRPMOverrideInput supplies one user-specific RPM override in a group batch update.
// A nil RPMOverride clears the override.
type GroupRPMOverrideInput struct {
	UserID      int64 `json:"user_id"`
	RPMOverride *int  `json:"rpm_override"`
}

// UserGroupRateRepository stores user-specific group billing multipliers and RPM limits.
// Configured overrides take precedence over group defaults.
type UserGroupRateRepository interface {
	// GetByUserID returns the user's non-NULL group multipliers.
	GetByUserID(ctx context.Context, userID int64) (map[int64]float64, error)

	// GetByUserAndGroup returns the user's multiplier for a group, or nil when unset.
	GetByUserAndGroup(ctx context.Context, userID, groupID int64) (*float64, error)

	// GetRPMOverrideByUserAndGroup returns the user's RPM override for a group, or nil when unset.
	GetRPMOverrideByUserAndGroup(ctx context.Context, userID, groupID int64) (*int, error)

	// GetByGroupID returns stored user override rows for the group.
	GetByGroupID(ctx context.Context, groupID int64) ([]UserGroupRateEntry, error)

	// SyncUserGroupRates updates supplied rates, clearing nil entries. An empty map clears all rates; RPM overrides
	// remain.
	SyncUserGroupRates(ctx context.Context, userID int64, rates map[int64]*float64) error

	// SyncGroupRateMultipliers replaces the group's user-specific rates, retaining RPM overrides.
	SyncGroupRateMultipliers(ctx context.Context, groupID int64, entries []GroupRateMultiplierInput) error

	// SyncGroupRPMOverrides replaces the group's user-specific RPM overrides, retaining rates.
	// Omitted users and nil RPMOverride values clear the corresponding overrides.
	SyncGroupRPMOverrides(ctx context.Context, groupID int64, entries []GroupRPMOverrideInput) error

	// ClearGroupRPMOverrides clears every RPM override in the group.
	ClearGroupRPMOverrides(ctx context.Context, groupID int64) error

	// DeleteByGroupID deletes all user overrides for a group.
	DeleteByGroupID(ctx context.Context, groupID int64) error

	// DeleteByUserID deletes all group overrides for a user.
	DeleteByUserID(ctx context.Context, userID int64) error
}
