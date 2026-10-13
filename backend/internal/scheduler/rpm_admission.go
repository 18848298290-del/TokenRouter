package scheduler

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// User RPM admission first resolves the group allowance, including an
// authentication-snapshot override, then checks the user-wide count. A
// nonpositive supplied override disables group counting but leaves a positive
// user allowance active, whereas a missing override permits repository lookup
// and group defaults.
//
// Both checks increment before comparing with the allowance, and rejected
// requests remain counted. A group rejection returns before user-wide
// incrementing, while a failed group counter can still lead to user rejection.
// Observation and override lookup errors have separate recovery: lookup
// failure uses the group value, counter failure admits past that check. This
// ordering keeps the two allowance decisions distinguishable through their
// error codes.

// RPM admission errors identify whether the group allowance or total user
// allowance rejected the request after counting.
var (
	ErrGroupRPMExceeded = apperror.TooManyRequests("GROUP_RPM_EXCEEDED", "group requests-per-minute limit exceeded")
	ErrUserRPMExceeded  = apperror.TooManyRequests("USER_RPM_EXCEEDED", "user requests-per-minute limit exceeded")
)

// RPMUser holds authenticated user limits and the optional captured group
// allowance override.
type RPMUser struct {
	ID int64 // admission counter identity
	// RPMLimit sets the user's allowance across groups. Nonpositive values
	// skip total counting; a successful positive-limit increment remains
	// recorded even when it rejects the request.
	RPMLimit int // requests per minute
	// UserGroupRPMOverride takes precedence over repository lookup when
	// present. A nonpositive pointed value disables group counting; nil
	// leaves lookup and the group default available.
	UserGroupRPMOverride *int // captured group allowance override
}

// RPMGroup holds the request group identity and its shared RPM allowance.
type RPMGroup struct {
	ID int64 // admission counter identity
	// RPMLimit supplies the shared group allowance when no user-group
	// override was found. A nonpositive value skips group counting while
	// the user's allowance can still apply.
	RPMLimit int // requests per minute
}

// RPMOverrides supplies an optional persisted allowance for a user and group
// pair.
type RPMOverrides interface {
	// GetRPMOverrideByUserAndGroup returns the optional user-group
	// allowance override. Nil means the group default applies, while a
	// supplied nonpositive value disables group counting; lookup errors
	// are handled by the admission consumer.
	GetRPMOverrideByUserAndGroup(context.Context, int64, int64) (*int, error)
}

// RPMAdmission applies group and user RPM allowances after caller funding
// admission succeeds.
type RPMAdmission struct {
	cache       UserRPMCache // optional user RPM counter storage
	overrides   RPMOverrides // optional persisted allowance reader
	diagnostics Diagnostics  // admission observation failure sink
}

// NewRPMAdmission binds user counters, optional override lookup, and admission
// diagnostics.
func NewRPMAdmission(cache UserRPMCache, overrides RPMOverrides, diagnostics Diagnostics) *RPMAdmission {
	return &RPMAdmission{cache: cache, overrides: overrides, diagnostics: diagnostics}
}

// Check increments and checks the effective group allowance before the user
// allowance. Nil service, cache, or user permits the request without counting;
// callers perform funding checks before invoking it.
//
// The authenticated user-group override wins over repository lookup, and
// supplied nonpositive overrides skip that group increment. A group rejection
// returns before total counting. Counter errors are logged and skip that
// check, while later successful user counting can still reject. Each
// successful increment remains recorded even when its allowance rejects the
// request, and lookup errors recover to the group default rather than
// disabling it.
func (s *RPMAdmission) Check(ctx context.Context, user *RPMUser, group *RPMGroup) error {
	if s == nil || s.cache == nil || user == nil {
		return nil
	}

	// Group admission precedes total user counting, so a group rejection
	// does not consume an additional user-wide counter increment.
	if group != nil {
		// Use captured authentication allowance when supplied. Query
		// only missing overrides, and retain the group default if
		// lookup cannot produce an override.
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
			// A supplied nonpositive override disables this group
			// counter. Leave the later user-wide gate reachable so
			// its positive allowance still applies to admitted
			// requests.
			if *override > 0 {
				count, incErr := s.cache.IncrementUserGroupRPM(ctx, user.ID, group.ID)
				if incErr != nil {
					s.diagnostics.printf(
						"service.billing_cache",
						"Warning: rpm increment (override) failed for user=%d group=%d: %v",
						user.ID, group.ID, incErr,
					)
					// The group increment is unconfirmed.
					// Continue to the independent user
					// gate so its successful count can
					// still reject an over-limit request.
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
				// Group counter storage failed after choosing
				// the default allowance. Continue to user
				// counting rather than treating the
				// unavailable group observation as rejection.
			} else if count > group.RPMLimit {
				return ErrGroupRPMExceeded
			}
		}
	}

	// Only admitted group decisions reach total user counting. Its
	// successful increment includes this request even when the returned
	// count exceeds the user allowance.
	if user.RPMLimit > 0 {
		count, err := s.cache.IncrementUserRPM(ctx, user.ID)
		if err != nil {
			s.diagnostics.printf(
				"service.billing_cache",
				"Warning: rpm increment (user) failed for user=%d: %v",
				user.ID, err,
			)
			// The total counter result is unconfirmed and no later
			// RPM gate remains. Permit this request after
			// reporting the storage failure through diagnostics.
			return nil
		}
		if count > user.RPMLimit {
			return ErrUserRPMExceeded
		}
	}

	return nil
}
