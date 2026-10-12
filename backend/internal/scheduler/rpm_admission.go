package scheduler

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// RPM rejection errors distinguish the group allowance from the total user allowance.
var (
	ErrGroupRPMExceeded = apperror.TooManyRequests("GROUP_RPM_EXCEEDED", "group requests-per-minute limit exceeded")
	ErrUserRPMExceeded  = apperror.TooManyRequests("USER_RPM_EXCEEDED", "user requests-per-minute limit exceeded")
)

// RPMUser contains user RPM settings; a nil group override permits a repository lookup.
type RPMUser struct {
	ID                   int64
	RPMLimit             int
	UserGroupRPMOverride *int
}
type RPMGroup struct {
	ID       int64
	RPMLimit int
}
type RPMOverrides interface {
	GetRPMOverrideByUserAndGroup(context.Context, int64, int64) (*int, error)
}

// RPMAdmission checks user and group RPM after the caller has passed funding checks.
type RPMAdmission struct {
	cache       UserRPMCache
	overrides   RPMOverrides
	diagnostics Diagnostics
}

func NewRPMAdmission(cache UserRPMCache, overrides RPMOverrides, diagnostics Diagnostics) *RPMAdmission {
	return &RPMAdmission{cache: cache, overrides: overrides, diagnostics: diagnostics}
}

// Check increments and checks the user's group count, then the total user count, rejecting either exceeded limit.
// A supplied group override replaces the group limit; nonpositive overrides skip the group check.
// A positive user limit still applies after group admission.
// Counter errors are logged and skip that limit check; a later successful check can still reject the request.
func (s *RPMAdmission) Check(ctx context.Context, user *RPMUser, group *RPMGroup) error {
	if s == nil || s.cache == nil || user == nil {
		return nil
	}

	// The user override takes precedence over the group's configured limit.
	if group != nil {
		// Use the authentication snapshot's override when available, otherwise query the repository.
		var override *int
		if user.UserGroupRPMOverride != nil {
			override = user.UserGroupRPMOverride
		} else if s.overrides != nil {
			dbOverride, err := s.overrides.GetRPMOverrideByUserAndGroup(ctx, user.ID, group.ID)
			if err != nil {
				s.diagnostics.printf(
					"service.billing_cache",
					"Warning: rpm override lookup failed for user=%d group=%d: %v",
					user.ID, group.ID, err,
				)
			} else {
				override = dbOverride
			}
		}

		if override != nil {
			// A nonpositive override skips group counting; the total user limit still applies.
			if *override > 0 {
				count, incErr := s.cache.IncrementUserGroupRPM(ctx, user.ID, group.ID)
				if incErr != nil {
					s.diagnostics.printf(
						"service.billing_cache",
						"Warning: rpm increment (override) failed for user=%d group=%d: %v",
						user.ID, group.ID, incErr,
					)
					// Skip this failed check and continue to the user limit.
				} else if count > *override {
					return ErrGroupRPMExceeded
				}
			}
			// The override replaces the group limit; total user counting follows below.
		} else if group.RPMLimit > 0 {
			// With no override, apply the group limit.
			count, err := s.cache.IncrementUserGroupRPM(ctx, user.ID, group.ID)
			if err != nil {
				s.diagnostics.printf(
					"service.billing_cache",
					"Warning: rpm increment (group) failed for user=%d group=%d: %v",
					user.ID, group.ID, err,
				)
				// Skip this failed check and continue to the user limit.
			} else if count > group.RPMLimit {
				return ErrGroupRPMExceeded
			}
		}
	}

	// Count total user RPM after group admission.
	if user.RPMLimit > 0 {
		count, err := s.cache.IncrementUserRPM(ctx, user.ID)
		if err != nil {
			s.diagnostics.printf(
				"service.billing_cache",
				"Warning: rpm increment (user) failed for user=%d: %v",
				user.ID, err,
			)
			return nil // Allow admission when the total user counter fails.
		}
		if count > user.RPMLimit {
			return ErrUserRPMExceeded
		}
	}

	return nil
}
