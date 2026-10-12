package billing

import (
	"context"
)

// Billing multipliers and RPM overrides share one user/group row but have
// independent unset values. A nil override maps to SQL NULL, and
// synchronization can replace one kind while retaining the other, so
// administrators can control charging and admission separately.

// UserGroupRateEntry combines stored overrides and display data for one group
// member.
type UserGroupRateEntry struct {
	UserID     int64  `json:"user_id"`     // override user record identifier
	UserName   string `json:"user_name"`   // override user display name
	UserEmail  string `json:"user_email"`  // override user email address
	UserNotes  string `json:"user_notes"`  // administrator user notes text
	UserStatus string `json:"user_status"` // stored user status name
	// RateMultiplier and RPMOverride use nil for independent unset SQL values.
	// Clearing one leaves the other available, and deleting the row clears both
	// user-specific settings.
	// RateMultiplier is the stored billing override. Nil represents an unset SQL
	// value, so GroupRateResolver uses the supplied group default.
	RateMultiplier *float64 `json:"rate_multiplier,omitempty"` // user billing rate multiplier
	// RPMOverride uses nil for an unset or cleared override. Scheduler admission
	// distinguishes that inherited group setting from a supplied zero, which
	// disables the group ceiling.
	RPMOverride *int `json:"rpm_override,omitempty"` // requests per minute
}

// GroupRateMultiplierInput supplies a user multiplier in a group replacement
// request.
type GroupRateMultiplierInput struct {
	UserID         int64   `json:"user_id"`         // override user record identifier
	RateMultiplier float64 `json:"rate_multiplier"` // replacement billing rate multiplier
}

// GroupRPMOverrideInput supplies a user RPM override in a group replacement
// request.
type GroupRPMOverrideInput struct {
	UserID int64 `json:"user_id"` // override user record identifier
	// RPMOverride uses nil for an unset or cleared override. Scheduler admission
	// distinguishes that inherited group setting from a supplied zero, which
	// disables the group ceiling.
	RPMOverride *int `json:"rpm_override"` // requests per minute
}

// UserGroupRateRepository reads and writes the independent billing and RPM
// overrides for user/group pairs.
type UserGroupRateRepository interface {
	// GetByUserID returns configured group multipliers indexed by group ID for
	// the user.
	GetByUserID(ctx context.Context, userID int64) (map[int64]float64, error)

	// GetByUserAndGroup returns the stored user multiplier for this group. A nil
	// result with no error means the row or rate is absent, allowing the resolver
	// to select its group default.
	GetByUserAndGroup(ctx context.Context, userID, groupID int64) (*float64, error)

	// GetRPMOverrideByUserAndGroup returns the stored user RPM override for this
	// group. Nil inherits the group setting, while a present zero disables that
	// group ceiling; repository errors remain distinguishable from absence.
	GetRPMOverrideByUserAndGroup(ctx context.Context, userID, groupID int64) (*int, error)

	// GetByGroupID returns stored rate and RPM rows with non-deleted user display
	// data.
	GetByGroupID(ctx context.Context, groupID int64) ([]UserGroupRateEntry, error)

	// SyncUserGroupRates updates supplied group rates and retains RPM overrides.
	// Nil values clear named rates; an empty map clears every user rate, while
	// omitted groups in a nonempty map retain their rates. Rows with both values
	// unset can be removed.
	SyncUserGroupRates(ctx context.Context, userID int64, rates map[int64]*float64) error

	// SyncGroupRateMultipliers replaces the group's user rates while retaining
	// RPM overrides. Omitted users have their rate cleared, supplied users
	// receive the given multiplier, and rows with neither override set can be
	// removed.
	SyncGroupRateMultipliers(ctx context.Context, groupID int64, entries []GroupRateMultiplierInput) error

	// SyncGroupRPMOverrides replaces the group's user RPM overrides while
	// retaining billing rates. Omitted users and supplied nil values clear RPM,
	// while supplied zero remains an explicit override; rows with neither value
	// set can be removed.
	SyncGroupRPMOverrides(ctx context.Context, groupID int64, entries []GroupRPMOverrideInput) error

	// ClearGroupRPMOverrides clears every group RPM override while retaining
	// billing rates. Rows that then have neither rate nor RPM can be deleted, so
	// clearing this setting may remove otherwise empty rows.
	ClearGroupRPMOverrides(ctx context.Context, groupID int64) error

	// DeleteByGroupID deletes both override values in every row for the group.
	DeleteByGroupID(ctx context.Context, groupID int64) error

	// DeleteByUserID deletes both override values in every row for the user.
	DeleteByUserID(ctx context.Context, userID int64) error
}
