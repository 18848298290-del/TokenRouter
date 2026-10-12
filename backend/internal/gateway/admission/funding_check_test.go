package admission

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

type fundingCheckFixture func(context.Context, billing.CheckInput) error

type rpmCheckFixture func(context.Context, *scheduler.RPMUser, *scheduler.RPMGroup) error

func (f fundingCheckFixture) Check(ctx context.Context, input billing.CheckInput) error {
	return f(ctx, input)
}

func (f rpmCheckFixture) Check(ctx context.Context, user *scheduler.RPMUser, group *scheduler.RPMGroup) error {
	return f(ctx, user, group)
}

// TestFundingAdmissionPreservesOrderAndWaitBoundary checks that RPM is counted once and post-wait checks revalidate funds alone.
func TestFundingAdmissionPreservesOrderAndWaitBoundary(t *testing.T) {
	for _, mode := range []string{"rejected", "accepted", "after_wait"} {
		t.Run(mode, func(t *testing.T) {
			var events []string
			denied := errors.New("funding denied")
			funds := fundingCheckFixture(func(_ context.Context, input billing.CheckInput) error {
				events = append(events, "funds")
				require.Equal(t, int64(11), input.Payer.ID)
				if mode == "rejected" {
					return denied
				}
				return nil
			})
			rpm := rpmCheckFixture(func(_ context.Context, user *scheduler.RPMUser, group *scheduler.RPMGroup) error {
				events = append(events, "rpm")
				require.Equal(t, int64(11), user.ID)
				require.Equal(t, int64(22), group.ID)
				return nil
			})
			checker := NewFundingAdmission(funds, rpm)
			input := billing.CheckInput{Payer: &billing.UserSummary{ID: 11}}
			if mode == "after_wait" {
				require.NoError(t, checker.CheckFunding(context.Background(), input))
				require.Equal(t, []string{"funds"}, events)
				return
			}
			err := checker.Check(context.Background(), input, &scheduler.RPMUser{ID: 11}, &scheduler.RPMGroup{ID: 22})
			if mode == "rejected" {
				require.ErrorIs(t, err, denied)
				require.Equal(t, []string{"funds"}, events)
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{"funds", "rpm"}, events)
		})
	}
}
