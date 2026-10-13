package ops

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestShouldFallbackOpsPreagg combines automatic and raw modes with coverage,
// ordinary, wrapped, and absent errors. It protects the broad error-based retry
// decision and nil-filter handling.
func TestShouldFallbackOpsPreagg(t *testing.T) {
	preaggErr := ErrOpsPreaggregatedNotPopulated
	otherErr := errors.New("some other error")

	autoFilter := &OpsDashboardFilter{QueryMode: OpsQueryModeAuto}
	rawFilter := &OpsDashboardFilter{QueryMode: OpsQueryModeRaw}

	tests := []struct {
		name   string              // subtest scenario display name
		filter *OpsDashboardFilter // query mode input filter
		err    error               // repository query failure input
		want   bool                // expected raw retry decision
	}{
		{"auto mode + preagg error => fallback", autoFilter, preaggErr, true},
		{"auto mode + other error => fallback", autoFilter, otherErr, true},
		{"auto mode + nil error => no fallback", autoFilter, nil, false},
		{"raw mode + preagg error => no fallback", rawFilter, preaggErr, false},
		{"nil filter => no fallback", nil, preaggErr, false},
		{"wrapped preagg error => fallback", autoFilter, errors.Join(preaggErr, otherErr), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ShouldFallbackOpsPreagg(tc.filter, tc.err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestCloneOpsFilterWithMode supplies nil and populated dashboard filters to
// the mode-copy helper. It protects mode isolation and equality of the retained
// start-time, platform, and group values.
func TestCloneOpsFilterWithMode(t *testing.T) {
	t.Run("nil filter returns nil", func(t *testing.T) {
		require.Nil(t, CloneOpsFilterWithMode(nil, OpsQueryModeRaw))
	})

	t.Run("cloned filter has new mode", func(t *testing.T) {
		groupID := int64(42)
		original := &OpsDashboardFilter{
			StartTime: time.Now(),
			EndTime:   time.Now().Add(time.Hour),
			Platform:  "anthropic",
			GroupID:   &groupID,
			QueryMode: OpsQueryModeAuto,
		}

		cloned := CloneOpsFilterWithMode(original, OpsQueryModeRaw)
		require.Equal(t, OpsQueryModeRaw, cloned.QueryMode)
		require.Equal(t, OpsQueryModeAuto, original.QueryMode, "original should not be modified")
		require.Equal(t, original.Platform, cloned.Platform)
		require.Equal(t, original.StartTime, cloned.StartTime)
		require.Equal(t, original.GroupID, cloned.GroupID)
	})
}
