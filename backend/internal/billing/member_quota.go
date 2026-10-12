package billing

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

var (
	ErrTeamMemberDailyExceeded   = apperror.TooManyRequests("TEAM_MEMBER_DAILY_LIMIT_EXCEEDED", "The team member daily limit has been reached")
	ErrTeamMemberMonthlyExceeded = apperror.TooManyRequests("TEAM_MEMBER_MONTHLY_LIMIT_EXCEEDED", "The team member monthly limit has been reached")
	ErrTeamMemberWeeklyExceeded  = apperror.TooManyRequests("TEAM_MEMBER_WEEKLY_LIMIT_EXCEEDED", "The team member weekly limit has been reached")
	ErrTeamMembershipRequired    = apperror.Forbidden("TEAM_MEMBERSHIP_REQUIRED", "Team membership is required")
)

// MemberQuotaSnapshot contains member spending windows. The caller decides whether the member's role requires
// quota checks.
type MemberQuotaSnapshot struct {
	DailyLimitUSD, WeeklyLimitUSD, MonthlyLimitUSD          float64
	DailyUsageUSD, WeeklyUsageUSD, MonthlyUsageUSD          float64
	DailyWindowStart, WeeklyWindowStart, MonthlyWindowStart *time.Time
}

// CheckMemberQuotaSnapshot checks daily, weekly, then monthly limits. Nonpositive limits are unlimited; windows
// are not modified.
func CheckMemberQuotaSnapshot(member MemberQuotaSnapshot) error {
	if member.DailyLimitUSD > 0 && member.DailyUsageUSD >= member.DailyLimitUSD {
		return ErrTeamMemberDailyExceeded
	}
	if member.WeeklyLimitUSD > 0 && member.WeeklyUsageUSD >= member.WeeklyLimitUSD {
		return ErrTeamMemberWeeklyExceeded
	}
	if member.MonthlyLimitUSD > 0 && member.MonthlyUsageUSD >= member.MonthlyLimitUSD {
		return ErrTeamMemberMonthlyExceeded
	}
	return nil
}

// NormalizeMemberQuotaWindows returns a copy with expired calendar windows reset. The caller persists usage and
// resets in a transaction.
func NormalizeMemberQuotaWindows(member MemberQuotaSnapshot, now time.Time, calendar timezone.Calendar) MemberQuotaSnapshot {
	daily, weekly, monthly := calendar.StartOfDay(now), calendar.StartOfWeek(now), calendar.StartOfMonth(now)
	if member.DailyWindowStart == nil || member.DailyWindowStart.Before(daily) {
		member.DailyUsageUSD = 0
		member.DailyWindowStart = &daily
	}
	if member.WeeklyWindowStart == nil || member.WeeklyWindowStart.Before(weekly) {
		member.WeeklyUsageUSD = 0
		member.WeeklyWindowStart = &weekly
	}
	if member.MonthlyWindowStart == nil || member.MonthlyWindowStart.Before(monthly) {
		member.MonthlyUsageUSD = 0
		member.MonthlyWindowStart = &monthly
	}
	return member
}
