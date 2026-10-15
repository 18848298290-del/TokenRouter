package promotion

import (
	"context"
	"math"
	"time"
)

// options.go defines rebate policy defaults and the dependencies used by
// promotion services.
// Invite rebates use percentages of purchased reasoning points, with zero
// duration and zero lifetime cap leaving those limits open. Settings readers
// supply live policy, while cache invalidators and Runtime callbacks let
// services use application clocks and cleanup facilities.

// Affiliate policy defaults and bounds govern percentage selection, freeze
// periods, and the limits applied to each invitee.
const (
	// percentage when rate is unusable.
	AffiliateRebateRateDefault = 20.0
	// minimum accepted rebate percentage.
	AffiliateRebateRateMin = 0.0
	// maximum accepted rebate percentage.
	AffiliateRebateRateMax = 100.0
	// missing policy disables rebates.
	AffiliateEnabledDefault = false
	// zero credits available quota.
	AffiliateRebateFreezeHoursDefault = 0
	// maximum freeze hours accepted.
	AffiliateRebateFreezeHoursMax = 720
	// zero leaves duration unlimited.
	AffiliateRebateDurationDaysDefault = 0
	// maximum eligible lifetime days.
	AffiliateRebateDurationDaysMax = 3650
	// zero leaves invitee cap unlimited.
	AffiliateRebatePerInviteeCapDefault = 0.0
	// missing policy skips admin rebates.
	AdminRechargeRebateEnabledDefault = false
)

// SettingsReader supplies the rebate policy consulted during inviter binding
// and accrual.
type SettingsReader interface {
	// IsAffiliateEnabled reports whether inviter binding and new rebate accrual
	// are enabled.
	//
	// AffiliateService uses its disabled default when the settings reader is
	// absent; implementations interpret the supplied context.
	IsAffiliateEnabled(context.Context) bool
	// GetAffiliateRebateRatePercent returns the global percentage used when an
	// inviter has no usable custom rate.
	//
	// Implementations supply a finite bounded value; zero disables the resulting
	// grant.
	GetAffiliateRebateRatePercent(context.Context) float64
	// GetAffiliateRebateFreezeHours returns the freeze period in hours for newly
	// accrued rebates.
	//
	// Positive values direct newly granted quota into frozen funds; zero credits
	// available quota immediately.
	GetAffiliateRebateFreezeHours(context.Context) int
	// GetAffiliateRebateDurationDays returns the eligible lifetime measured in
	// calendar days from invitee profile creation.
	//
	// Positive values stop later purchase accrual; zero leaves this configured
	// duration limit open.
	GetAffiliateRebateDurationDays(context.Context) int
	// GetAffiliateRebatePerInviteeCap returns the lifetime reasoning-point cap
	// for one inviter and invitee pair.
	//
	// Positive values limit accumulated purchase grants; zero leaves the pair's
	// cumulative rebate amount unlimited.
	GetAffiliateRebatePerInviteeCap(context.Context) float64
}
type (
	// AuthCacheInvalidator requests refreshed API Key authentication data after
	// promotion credits.
	AuthCacheInvalidator interface {
		// InvalidateAuthCacheByUserID requests eviction of authentication data for
		// the credited user's API Keys.
		//
		// The interface returns no error; services cannot infer successful cache
		// publication from its completion.
		InvalidateAuthCacheByUserID(context.Context, int64)
	}
	// BalanceCache removes cached user balances after promotion fund changes.
	BalanceCache interface {
		// InvalidateUserBalance requests removal of the cached amount for one user.
		//
		// Errors describe a failed attempt; callers decide whether to observe them
		// after persistent funds have changed.
		InvalidateUserBalance(context.Context, int64) error
	}
)

// Runtime supplies promotion clocks, background dispatch, and cache failure
// observation.
type Runtime struct {
	// Background receives optional cache cleanup work. Nil makes
	// PromoService.background run it synchronously; a supplied runner defines
	// admission and execution timing.
	Background func(string, func()) // named callback dispatch
	// Now supplies eligibility timestamps. Service constructors substitute
	// time.Now when absent; callers coordinate concurrent access to injected
	// clock state.
	Now func() time.Time // current promotion clock
	// Warn observes returned affiliate balance-cache errors when supplied. Nil
	// leaves those errors unreported by AffiliateService while its transfer
	// result remains successful.
	Warn func(int64, error) // affiliate invalidation observer
}

// ClampRebateRate returns a percentage bounded between zero and one hundred.
//
// NaN and either infinity select the default percentage; finite out-of-range
// values select the nearest accepted endpoint.
func ClampRebateRate(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		// An unusable numeric rate selects the policy default so callers receive a
		// finite percentage for grant calculation.
		return AffiliateRebateRateDefault
	}
	if value < AffiliateRebateRateMin {
		// A zero percentage disables new grants for the earning inviter.
		return AffiliateRebateRateMin
	}
	if value > AffiliateRebateRateMax {
		// The upper percentage uses all purchased points before grant rounding.
		return AffiliateRebateRateMax
	}
	return value
}
