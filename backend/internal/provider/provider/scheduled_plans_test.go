package provider

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestScheduledCronLifecycle exercises repeated Start calls and stopping before
// startup. Registration must stay deferred, occur at most once for an active
// instance, and remain absent after an earlier stop.
func TestScheduledCronLifecycle(t *testing.T) {
	for _, stopFirst := range []bool{true, false} {
		schedule, ok := NewScheduledCron(time.UTC).(*ScheduledCron)
		require.True(t, ok)
		require.Empty(t, schedule.cron.Entries())
		if stopFirst {
			require.NoError(t, schedule.Stop(context.Background()))
		}
		require.NoError(t, schedule.Start(context.Background(), func() {}))
		require.NoError(t, schedule.Start(context.Background(), func() {}))
		if stopFirst {
			require.Empty(t, schedule.cron.Entries())
		} else {
			require.Len(t, schedule.cron.Entries(), 1)
		}
		require.NoError(t, schedule.Stop(context.Background()))
	}
}

// TestScheduledCronRejectsInvalidExpression supplies text that cannot form a
// five-field cron schedule. NextScheduledTestRun must return an error so
// invalid plans cannot acquire a next execution time.
func TestScheduledCronRejectsInvalidExpression(t *testing.T) {
	_, err := NextScheduledTestRun("not a cron", time.Now())
	require.Error(t, err)
}
