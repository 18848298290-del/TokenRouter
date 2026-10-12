package promotion

import (
	"context"
	"math"
	"time"
)

// Affiliate rebate settings.
const (
	AffiliateRebateRateDefault          = 20.0
	AffiliateRebateRateMin              = 0.0
	AffiliateRebateRateMax              = 100.0
	AffiliateEnabledDefault             = false // Invite rebates default to disabled
	AffiliateRebateFreezeHoursDefault   = 0     // Zero disables freezing
	AffiliateRebateFreezeHoursMax       = 720   // 30 days in hours
	AffiliateRebateDurationDaysDefault  = 0     // Zero means no expiration
	AffiliateRebateDurationDaysMax      = 3650  // Approximately 10 years
	AffiliateRebatePerInviteeCapDefault = 0.0   // Reasoning point cap; zero means unlimited
	AdminRechargeRebateEnabledDefault   = false // Admin recharges default to producing no rebate
)

type SettingsReader interface {
	IsAffiliateEnabled(context.Context) bool
	GetAffiliateRebateRatePercent(context.Context) float64
	GetAffiliateRebateFreezeHours(context.Context) int
	GetAffiliateRebateDurationDays(context.Context) int
	GetAffiliateRebatePerInviteeCap(context.Context) float64
}
type (
	AuthCacheInvalidator interface{ InvalidateAuthCacheByUserID(context.Context, int64) }
	BalanceCache         interface {
		InvalidateUserBalance(context.Context, int64) error
	}
)

type Runtime struct {
	Background func(string, func())
	Now        func() time.Time
	Warn       func(int64, error)
}

func ClampRebateRate(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return AffiliateRebateRateDefault
	}
	if value < AffiliateRebateRateMin {
		return AffiliateRebateRateMin
	}
	if value > AffiliateRebateRateMax {
		return AffiliateRebateRateMax
	}
	return value
}
