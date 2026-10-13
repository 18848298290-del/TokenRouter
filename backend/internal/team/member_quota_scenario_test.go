package team_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/team"
)

// Team Key admission passes ordinary-member spending snapshots to billing for
// positive daily, weekly and monthly quota checks. Owners bypass that
// snapshot check, and zero limits leave their represented periods unlimited
// before any request-specific spending is added.

// TestCheckTeamMemberLimitSnapshot submits owner and ordinary-member quota
// values through the API Key admission helper. It observes unlimited/owner
// success, daily/weekly/monthly exhaustion errors and success when all
// represented usage stays below limits.
func TestCheckTeamMemberLimitSnapshot(t *testing.T) {
	tests := []struct {
		name string // descriptive quota scenario name
		// member points to the role/limit/usage observation passed to Key
		// admission. The check reads it synchronously and leaves the supplied
		// fixture value unchanged.
		member *team.TeamMembership // supplied membership quota snapshot
		// want selects the asserted rejection sentinel when nonnil. Nil selects
		// successful admission for unlimited, owner and below-limit cases in this
		// table.
		want error // expected quota rejection sentinel
	}{
		{name: "unlimited", member: &team.TeamMembership{Role: team.TeamRoleMember}},
		{name: "owner_ignores_limits", member: &team.TeamMembership{Role: team.TeamRoleOwner, DailyLimitUSD: 1, DailyUsageUSD: 1}},
		{name: "daily", member: &team.TeamMembership{Role: team.TeamRoleMember, DailyLimitUSD: 1, DailyUsageUSD: 1}, want: team.ErrTeamMemberDailyExceeded},
		{name: "weekly", member: &team.TeamMembership{Role: team.TeamRoleMember, WeeklyLimitUSD: 2, WeeklyUsageUSD: 3}, want: team.ErrTeamMemberWeeklyExceeded},
		{name: "monthly", member: &team.TeamMembership{Role: team.TeamRoleMember, MonthlyLimitUSD: 4, MonthlyUsageUSD: 4}, want: team.ErrTeamMemberMonthlyExceeded},
		{name: "below_limits", member: &team.TeamMembership{Role: team.TeamRoleMember, DailyLimitUSD: 2, DailyUsageUSD: 1, WeeklyLimitUSD: 5, WeeklyUsageUSD: 4, MonthlyLimitUSD: 10, MonthlyUsageUSD: 9}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := apikey.KeyCheckTeamMemberLimitSnapshot(tt.member)
			if tt.want == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.want)
		})
	}
}
