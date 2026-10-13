//go:build integration

package routing_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	routingpostgres "github.com/TokenFlux/TokenRouter/internal/routing/postgres"
)

// Probe workers claim a group through a PostgreSQL lease before storing the
// round's result and next schedule. Two concurrent claim calls count this
// fixture's ownership once, then an advanced supplied clock makes the expired
// lease available to a recovery owner.
//
// A trigger rejects the schedule write after the result insertion. Counts and
// owner queries distinguish a complete transaction rollback from an error with
// partial state. Removing the trigger permits the same save to release the
// lease and record one result, while the summary query checks that the saved
// success contributes one observation.

// TestGroupProbeLeaseAndAtomicResult races two lease claims and later injects a
// schedule-write failure for the claimed group. Exactly one initial claim must
// match, recovery must reclaim it after expiry, and result storage must roll back
// or release the lease together with the schedule.
func TestGroupProbeLeaseAndAtomicResult(t *testing.T) {
	ctx := context.Background()
	client, integrationDB := routingDatabase(t)
	suffix := time.Now().UnixNano()
	group, err := client.Group.Create().SetName(fmt.Sprintf("test-probe-%d", suffix)).
		SetAllowedProtocols(capability.DefaultGroupClientProtocols(routing.PlatformOpenAI)).
		SetProtocolFallbacks(capability.DefaultProtocolFallbacks(routing.PlatformOpenAI)).
		SetAvailabilityProbeConfig(routing.GroupAvailabilityProbeConfig{Enabled: true, IntervalMinutes: 5, ModelID: "gpt-test", TimeoutSeconds: 10}).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Group.DeleteOneID(group.ID).Exec(context.Background())) })
	repo := routingpostgres.NewGroupAvailabilityProbeRepository(integrationDB)
	now := time.Now().UTC()
	// claimResult carries one concurrent claim's rows and error to the test.
	type claimResult struct {
		// groups and err are published together after ClaimDue returns, allowing
		// the receiver to check each call before counting ownership of the fixture group.
		groups []routing.GroupAvailabilityProbeDueGroup // groups returned by claim
		err    error                                    // claim query error result
	}
	// Give each claimant one buffered result slot so both database calls can
	// finish independently before the test receives and counts their claimed groups.
	claimed := make(chan claimResult, 2)
	for i := range 2 {
		go func(i int) {
			v, e := repo.ClaimDue(ctx, now, now.Add(time.Minute), fmt.Sprintf("runner-%d", i), 5)
			claimed <- claimResult{v, e}
		}(i)
	}
	matches := 0
	for range 2 {
		r := <-claimed
		require.NoError(t, r.err)
		for _, v := range r.groups {
			if v.GroupID == group.ID {
				matches++
			}
		}
	}
	require.Equal(t, 1, matches)
	// Advance the supplied clock beyond the one-minute lease. ClaimDue can then
	// recover this group without waiting for wall-clock expiry in the test.
	later := now.Add(2 * time.Minute)
	recovered, err := repo.ClaimDue(ctx, later, later.Add(time.Minute), "recovered", 5)
	require.NoError(t, err)
	require.Contains(t, func() []int64 {
		ids := make([]int64, len(recovered))
		for i := range recovered {
			ids[i] = recovered[i].GroupID
		}
		return ids
	}(), group.ID)

	// Reject this group's state write after its result insertion. The subsequent
	// SQL checks require that the result disappears and the recovery owner's lease survives.
	functionName := fmt.Sprintf("test_probe_result_fail_%d", suffix)
	triggerName := fmt.Sprintf("test_probe_result_trigger_%d", suffix)
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.group_id = %d THEN RAISE EXCEPTION 'test forced schedule failure'; END IF; RETURN NEW; END $$`, functionName, group.ID))
	require.NoError(t, err)
	// Dispose of the trigger before its function, using IF EXISTS because the
	// successful-save phase drops the trigger early to permit its state update.
	t.Cleanup(func() {
		_, e := integrationDB.ExecContext(context.Background(), fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON group_availability_probe_states", triggerName))
		require.NoError(t, e)
		_, e = integrationDB.ExecContext(context.Background(), fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", functionName))
		require.NoError(t, e)
	})
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT OR UPDATE ON group_availability_probe_states FOR EACH ROW EXECUTE FUNCTION %s()", triggerName, functionName))
	require.NoError(t, err)
	result := &routing.GroupAvailabilityProbeResult{GroupID: group.ID, ModelID: "gpt-test", Status: routing.GroupAvailabilityProbeStatusSuccess, Success: true, LatencyMs: 15, StartedAt: now, FinishedAt: now.Add(15 * time.Millisecond)}
	next := later.Add(5 * time.Minute)
	require.ErrorContains(t, repo.SaveResultAndScheduleNext(ctx, result, next), "test forced schedule failure")
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM group_availability_probe_results WHERE group_id=$1", group.ID).Scan(&count))
	require.Zero(t, count)
	var owner string
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT locked_by FROM group_availability_probe_states WHERE group_id=$1", group.ID).Scan(&owner))
	require.Equal(t, "recovered", owner)
	// Release the injected failure before reusing the same result. A successful
	// save must now publish the next schedule and clear both lease columns together.
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf("DROP TRIGGER %s ON group_availability_probe_states", triggerName))
	require.NoError(t, err)
	require.NoError(t, repo.SaveResultAndScheduleNext(ctx, result, next))
	var scheduled time.Time
	var released bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT next_run_at, locked_by IS NULL AND locked_until IS NULL FROM group_availability_probe_states WHERE group_id=$1", group.ID).Scan(&scheduled, &released))
	require.True(t, released)
	// next carries the nanosecond fraction inherited from now. PostgreSQL stores
	// next_run_at at microsecond precision, so allow that storage loss on readback.
	require.WithinDuration(t, next, scheduled, time.Microsecond)
	summaries, err := repo.GetSummaryByGroupIDs(ctx, []int64{group.ID}, 1, 120, "Asia/Shanghai", later)
	require.NoError(t, err)
	require.EqualValues(t, 1, summaries[group.ID].SuccessCount)
	require.EqualValues(t, 1, summaries[group.ID].TotalCount)
}
