package billing

import (
	"context"
	"fmt"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// group_rate_admin.go validates and stores user-group billing multipliers and
// RPM overrides.
// User group overrides carry separate billing multipliers and
// request-per-minute limits in the same stored rows. Rate updates and RPM
// updates use distinct repository operations. After a successful RPM mutation,
// an optional invalidation callback removes group authentication snapshots so
// later reloads can read the changed limits.

// GroupRateAdmin validates user-group override updates and invokes optional
// authentication invalidation after successful RPM mutations.
type GroupRateAdmin struct {
	// userGroupRateRepo may be nil when override management is unavailable. Every
	// method then returns before validation, persistence or cache invalidation.
	userGroupRateRepo UserGroupRateRepository // group override persistence operations
	// authCacheInvalidator is optional. Successful RPM writes invoke it after
	// persistence; rate writes and whole-row deletion do not invoke this
	// callback.
	authCacheInvalidator interface {
		// InvalidateAuthCacheByGroupID removes authentication snapshots containing
		// this group's user overrides. GroupRateAdmin calls it after successful RPM
		// persistence so later authentication reloads can read the changed limits.
		InvalidateAuthCacheByGroupID(context.Context, int64)
	} // group authentication snapshot invalidator
}

// NewGroupRateAdmin binds an override repository and optional group
// authentication invalidator. A nil repository makes its read and mutation
// methods return empty or successful results; invalidation is used after
// successful RPM updates.
func NewGroupRateAdmin(repo UserGroupRateRepository, invalidator interface {
	// InvalidateAuthCacheByGroupID removes authentication snapshots containing
	// this group's user overrides. GroupRateAdmin calls it after successful RPM
	// persistence so later authentication reloads can read the changed limits.
	InvalidateAuthCacheByGroupID(context.Context, int64)
},
) *GroupRateAdmin {
	return &GroupRateAdmin{userGroupRateRepo: repo, authCacheInvalidator: invalidator}
}

// GetGroupRateMultipliers returns the repository rows for the group, including
// rate and RPM values. A nil repository produces nil entries and no error;
// stored row filtering and ordering belong to the repository.
func (s *GroupRateAdmin) GetGroupRateMultipliers(ctx context.Context, groupID int64) ([]UserGroupRateEntry, error) {
	if s.userGroupRateRepo == nil {
		return nil, nil
	}
	return s.userGroupRateRepo.GetByGroupID(ctx, groupID)
}

// ClearGroupRateMultipliers deletes the group override rows through
// DeleteByGroupID. Those rows contain both rates and RPM overrides, so both
// values are removed; a nil repository makes the operation a successful no-op.
func (s *GroupRateAdmin) ClearGroupRateMultipliers(ctx context.Context, groupID int64) error {
	if s.userGroupRateRepo == nil {
		return nil
	}
	return s.userGroupRateRepo.DeleteByGroupID(ctx, groupID)
}

// BatchSetGroupRateMultipliers replaces the group rates using the supplied
// user entries. Values at or below zero are rejected before writing;
// repository synchronization retains RPM overrides and clears rates for
// omitted users. A nil repository skips validation and writing. Repository
// errors can follow partial writes, so callers reread stored state when a
// failed replacement's outcome matters.
func (s *GroupRateAdmin) BatchSetGroupRateMultipliers(ctx context.Context, groupID int64, entries []GroupRateMultiplierInput) error {
	if s.userGroupRateRepo == nil {
		return nil
	}
	for _, e := range entries {
		if e.RateMultiplier <= 0 {
			return fmt.Errorf("rate_multiplier must be > 0 (user_id=%d)", e.UserID)
		}
	}
	return s.userGroupRateRepo.SyncGroupRateMultipliers(ctx, groupID, entries)
}

// ClearGroupRPMOverrides removes group RPM overrides and invokes the optional
// authentication invalidator after success. The repository retains rates; a
// nil repository makes this a successful no-op. Repository errors stop
// invalidation and can follow partial writes, so callers reread stored state
// when they need the outcome of a failed clear.
func (s *GroupRateAdmin) ClearGroupRPMOverrides(ctx context.Context, groupID int64) error {
	if s.userGroupRateRepo == nil {
		return nil
	}
	if err := s.userGroupRateRepo.ClearGroupRPMOverrides(ctx, groupID); err != nil {
		return err
	}
	// Persist the RPM changes before invalidating authentication snapshots, so
	// subsequent cache reloads read the updated overrides from the repository.
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByGroupID(ctx, groupID)
	}
	return nil
}

// BatchSetGroupRPMOverrides replaces group RPM overrides and invokes the
// optional authentication invalidator after success. Negative supplied values
// are rejected; nil overrides and omitted users clear RPM while the repository
// retains rates. A nil repository skips validation and writing.
//
// PostgreSQL storage narrows values to int32, so callers keep them representable
// in that type. Repository errors can follow partial writes and stop
// invalidation; callers reread stored state when a failed replacement's outcome
// matters.
func (s *GroupRateAdmin) BatchSetGroupRPMOverrides(ctx context.Context, groupID int64, entries []GroupRPMOverrideInput) error {
	if s.userGroupRateRepo == nil {
		return nil
	}
	for _, e := range entries {
		if e.RPMOverride != nil && *e.RPMOverride < 0 {
			return infraerrors.BadRequest("INVALID_RPM_OVERRIDE", fmt.Sprintf("rpm_override must be >= 0 (user_id=%d)", e.UserID))
		}
	}
	if err := s.userGroupRateRepo.SyncGroupRPMOverrides(ctx, groupID, entries); err != nil {
		return err
	}
	// Persist the RPM changes before invalidating authentication snapshots, so
	// subsequent cache reloads read the updated overrides from the repository.
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByGroupID(ctx, groupID)
	}
	return nil
}
