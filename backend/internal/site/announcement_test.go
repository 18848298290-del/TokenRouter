package site

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAnnouncementTargeting_Matches_EmptyMatchesAll supplies no targeting groups with different user observations.
// It verifies that an absent expression reaches users regardless of balance
// and whether an active plan set is supplied.
func TestAnnouncementTargeting_Matches_EmptyMatchesAll(t *testing.T) {
	var targeting AnnouncementTargeting
	require.True(t, targeting.Matches(0, nil))
	require.True(t, targeting.Matches(123.45, map[int64]struct{}{1: {}}))
}

// TestAnnouncementTargeting_NormalizeAndValidate_RejectsEmptyConditionGroup supplies a configured group with no conditions.
// It verifies that incomplete targeting is rejected instead of being stored
// as an alternative that could grant unconditional visibility.
func TestAnnouncementTargeting_NormalizeAndValidate_RejectsEmptyConditionGroup(t *testing.T) {
	targeting := AnnouncementTargeting{
		AnyOf: []AnnouncementConditionGroup{
			{AllOf: nil},
		},
	}
	_, err := targeting.NormalizeAndValidate()
	require.Error(t, err)
	require.ErrorIs(t, err, ErrAnnouncementInvalidTarget)
}

// TestAnnouncementTargeting_NormalizeAndValidate_RejectsInvalidCondition supplies an unsupported balance operator.
// It verifies the invalid-target error reaches the caller, preventing storage
// of a condition the runtime matcher cannot evaluate as a supported comparison.
func TestAnnouncementTargeting_NormalizeAndValidate_RejectsInvalidCondition(t *testing.T) {
	targeting := AnnouncementTargeting{
		AnyOf: []AnnouncementConditionGroup{
			{
				AllOf: []AnnouncementCondition{
					{Type: "balance", Operator: "between", Value: 10},
				},
			},
		},
	}
	_, err := targeting.NormalizeAndValidate()
	require.Error(t, err)
	require.ErrorIs(t, err, ErrAnnouncementInvalidTarget)
}

// TestAnnouncementTargeting_Matches_AndOrSemantics combines a low-balance alternative with balance-and-plan requirements.
// It verifies that groups are alternatives, that every condition in a group
// is required, and that the numeric threshold's equality behavior is respected.
func TestAnnouncementTargeting_Matches_AndOrSemantics(t *testing.T) {
	targeting := AnnouncementTargeting{
		AnyOf: []AnnouncementConditionGroup{
			{
				AllOf: []AnnouncementCondition{
					{Type: AnnouncementConditionTypeBalance, Operator: AnnouncementOperatorGTE, Value: 100},
					{Type: AnnouncementConditionTypeSubscription, Operator: AnnouncementOperatorIn, PlanIDs: []int64{10}},
				},
			},
			{
				AllOf: []AnnouncementCondition{
					{Type: AnnouncementConditionTypeBalance, Operator: AnnouncementOperatorLT, Value: 5},
				},
			},
		},
	}

	// The second group matches balances below 5.
	require.True(t, targeting.Matches(4.99, nil))
	require.False(t, targeting.Matches(5, nil))

	// The first group requires balance >=100 and an active subscription to plan 10.
	require.False(t, targeting.Matches(100, map[int64]struct{}{}))
	require.False(t, targeting.Matches(99.9, map[int64]struct{}{10: {}}))
	require.True(t, targeting.Matches(100, map[int64]struct{}{10: {}}))
}
