package billing

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

// Team member spending limits apply to calendar days, Monday-starting
// weeks, and months in the configured timezone. Missing or older starts
// reset the corresponding usage in a returned snapshot, while admission
// checks the selected member's positive limits in daily, weekly, monthly
// order.

// Member-quota errors identify the first exhausted period or missing
// membership so request admission can report the specific rejected
// team allowance.
var (
	// ErrTeamMemberDailyExceeded reports an exhausted daily member allowance.
	ErrTeamMemberDailyExceeded = apperror.TooManyRequests("TEAM_MEMBER_DAILY_LIMIT_EXCEEDED", "The team member daily limit has been reached")
	// ErrTeamMemberMonthlyExceeded reports an exhausted monthly member allowance.
	ErrTeamMemberMonthlyExceeded = apperror.TooManyRequests("TEAM_MEMBER_MONTHLY_LIMIT_EXCEEDED", "The team member monthly limit has been reached")
	// ErrTeamMemberWeeklyExceeded reports an exhausted weekly member allowance.
	ErrTeamMemberWeeklyExceeded = apperror.TooManyRequests("TEAM_MEMBER_WEEKLY_LIMIT_EXCEEDED", "The team member weekly limit has been reached")
	// ErrTeamMembershipRequired reports absent required team membership.
	ErrTeamMembershipRequired = apperror.Forbidden("TEAM_MEMBERSHIP_REQUIRED", "Team membership is required")
)

// MemberQuotaSnapshot retains calendar spending ceilings, totals, and
// window starts for one team member. Callers choose members subject to
// quota checks; the snapshot carries no role or membership-activation
// decision.
type MemberQuotaSnapshot struct {
	// DailyLimitUSD, WeeklyLimitUSD, and MonthlyLimitUSD cap DailyUsageUSD,
	// WeeklyUsageUSD, and MonthlyUsageUSD respectively. A nonpositive limit
	// leaves its period unlimited. CheckMemberQuotaSnapshot compares the
	// supplied totals; callers normalize window dates before checking
	// current-period spending.
	// DailyLimitUSD: USD daily ceiling.
	// WeeklyLimitUSD: USD weekly ceiling.
	// MonthlyLimitUSD: USD monthly ceiling.
	DailyLimitUSD, WeeklyLimitUSD, MonthlyLimitUSD float64
	// DailyUsageUSD: USD daily usage.
	// WeeklyUsageUSD: USD weekly usage.
	// MonthlyUsageUSD: USD monthly usage.
	DailyUsageUSD, WeeklyUsageUSD, MonthlyUsageUSD float64
	// DailyWindowStart, WeeklyWindowStart, and MonthlyWindowStart date
	// DailyUsageUSD, WeeklyUsageUSD, and MonthlyUsageUSD respectively.
	// Normalization replaces missing or older starts and clears matching
	// usage; starts at or after the current boundary keep their shared
	// time pointers.
	// DailyWindowStart: daily calendar origin.
	// WeeklyWindowStart: weekly calendar origin.
	// MonthlyWindowStart: monthly calendar origin.
	DailyWindowStart, WeeklyWindowStart, MonthlyWindowStart *time.Time
}

// CheckMemberQuotaSnapshot returns the first exhausted positive limit,
// checking daily, weekly, then monthly usage. Nonpositive limits are
// unlimited; callers normalize windows beforehand when the check needs
// current calendar-period usage.
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

// NormalizeMemberQuotaWindows returns a snapshot with missing or older
// calendar starts advanced and their matching usage cleared. Starts at
// or after the current boundary remain unchanged, including their shared
// time pointers. Copy a retained time before modifying it independently
// of the input snapshot. Callers persist the returned reset values when
// their operation requires storage updates.
func NormalizeMemberQuotaWindows(member MemberQuotaSnapshot, now time.Time, calendar timezone.Calendar) MemberQuotaSnapshot {
	// Use calendar boundaries for all three periods so daylight-saving clock
	// changes do not shift a daily reset away from local midnight.
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
