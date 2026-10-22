//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/testutil/postgrescontainer"
)

// group_probe_integration_test.go checks leased configurations and availability history in PostgreSQL.
// The latest probe can lie outside the availability window, and equal start
// times need a deterministic winner. PostgreSQL fixtures separate recent counts
// from latest status and include a group with no history to exercise the
// unknown result.

// TestGroupProbeLatestResults inserts tied recent probes, an older probe, and
// an empty group. It checks ID tie resolution, window totals, an older latest
// status, and an absent latest timestamp.
func TestGroupProbeLatestResults(t *testing.T) {
	db := postgrescontainer.New(t)
	ctx := context.Background()
	ids := make([]int64, 3)
	for i, name := range []string{"probe-recent", "probe-old", "probe-empty"} {
		require.NoError(t, db.QueryRow("INSERT INTO groups(name) VALUES($1) RETURNING id", name).Scan(&ids[i]))
	}
	// Use whole seconds so PostgreSQL timestamp storage keeps the equality
	// assertion exact.
	now := time.Now().UTC().Truncate(time.Second)
	for _, row := range []struct {
		group  int64  // owning fixture group
		status string // stored probe outcome
		// start and finish describe the observation separately: ties on start use
		// insertion ID for latest selection, and the returned timestamp comes from
		// finish.
		start  time.Time // latest ordering timestamp
		finish time.Time // returned completion timestamp
	}{
		{ids[0], "failed", now.Add(-time.Hour), now.Add(-time.Hour)},
		{ids[0], "success", now.Add(-time.Hour), now.Add(-time.Hour + time.Minute)},
		{ids[1], "failed", now.Add(-48 * time.Hour), now.Add(-48*time.Hour + time.Minute)},
	} {
		_, err := db.Exec(`INSERT INTO group_availability_probe_results(group_id,model_id,status,success,started_at,finished_at)
			VALUES($1,'test',$2,$3,$4,$5)`, row.group, row.status, row.status == "success", row.start, row.finish)
		require.NoError(t, err)
	}
	store := NewGroupAvailabilityProbeRepository(db)
	got, err := store.GetSummaryByGroupIDs(ctx, ids, 1, 60, "UTC", now)
	require.NoError(t, err)
	require.Equal(t, "success", got[ids[0]].LastStatus)
	require.True(t, now.Add(-time.Hour+time.Minute).Equal(*got[ids[0]].LastCheckedAt))
	require.EqualValues(t, 2, got[ids[0]].TotalCount)
	require.EqualValues(t, 1, got[ids[0]].SuccessCount)
	require.Equal(t, "failed", got[ids[1]].LastStatus)
	require.Zero(t, got[ids[1]].TotalCount)
	require.Nil(t, got[ids[2]].LastCheckedAt)
}

// TestGroupProbeClaimRetainsProtocol leases old and protocol-specific configurations from JSON storage.
// Claimed settings keep the explicit entry while records saved before protocol selection remain automatic.
func TestGroupProbeClaimRetainsProtocol(t *testing.T) {
	db := postgrescontainer.New(t)
	for index, config := range []string{
		`{"enabled":true,"model_id":"alias","prompt":"hi"}`,
		`{"enabled":true,"model_id":"alias","prompt":"hi","protocol":"openai_chat_completions"}`,
	} {
		_, err := db.Exec(`INSERT INTO groups(name, availability_probe_config) VALUES($1, $2::jsonb)`, fmt.Sprintf("probe-%d", index), config)
		require.NoError(t, err)
	}
	now := time.Now().UTC()
	claimed, err := NewGroupAvailabilityProbeRepository(db).ClaimDue(t.Context(), now, now.Add(time.Minute), "test", 10)
	require.NoError(t, err)
	require.Len(t, claimed, 2)
	require.Empty(t, claimed[0].Config.Protocol)
	require.EqualValues(t, "openai_chat_completions", claimed[1].Config.Protocol)
}
