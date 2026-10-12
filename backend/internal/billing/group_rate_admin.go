package billing

import (
	"context"
	"fmt"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// GroupRateAdmin validates and writes user-specific rates and RPM overrides. RPM updates invalidate authentication
// caches.
type GroupRateAdmin struct {
	userGroupRateRepo    UserGroupRateRepository
	authCacheInvalidator interface{ InvalidateAuthCacheByGroupID(context.Context, int64) }
}

func NewGroupRateAdmin(repo UserGroupRateRepository, invalidator interface{ InvalidateAuthCacheByGroupID(context.Context, int64) }) *GroupRateAdmin {
	return &GroupRateAdmin{userGroupRateRepo: repo, authCacheInvalidator: invalidator}
}

func (s *GroupRateAdmin) GetGroupRateMultipliers(ctx context.Context, groupID int64) ([]UserGroupRateEntry, error) {
	if s.userGroupRateRepo == nil {
		return nil, nil
	}
	return s.userGroupRateRepo.GetByGroupID(ctx, groupID)
}

func (s *GroupRateAdmin) ClearGroupRateMultipliers(ctx context.Context, groupID int64) error {
	if s.userGroupRateRepo == nil {
		return nil
	}
	return s.userGroupRateRepo.DeleteByGroupID(ctx, groupID)
}

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

func (s *GroupRateAdmin) ClearGroupRPMOverrides(ctx context.Context, groupID int64) error {
	if s.userGroupRateRepo == nil {
		return nil
	}
	if err := s.userGroupRateRepo.ClearGroupRPMOverrides(ctx, groupID); err != nil {
		return err
	}
	// Authentication snapshots contain RPM overrides and need invalidation after changes.
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByGroupID(ctx, groupID)
	}
	return nil
}

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
	// Authentication snapshots contain RPM overrides and need invalidation after changes.
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByGroupID(ctx, groupID)
	}
	return nil
}
