//go:build integration

package postgres

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/audit"
	auditpg "github.com/TokenFlux/TokenRouter/internal/audit/postgres"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Cleanup cancellation and manual backfill requests can occur after earlier
// database work has completed. The scenarios retain real repositories while
// intercepting selected calls, creating a controlled interleaving between a
// stale read and a live-state update or between a committed deletion and the
// next cancellation check. The assertions inspect persisted state and rebuilt
// usage totals after those interleavings.
//
// A first-read barrier holds the previously read analytics state until the test
// releases it. A cleanup adapter cancels after a nonempty deletion, and another
// adapter reports aggregate repair completion through a buffered channel. Audit
// clearing uses an inserted trigger failure to check that deletion and its
// trace roll back together, then exercises a successful clear in the same
// scenario.

// pausedState pauses its first completed progress read to expose a concurrent
// state update.
type pausedState struct {
	*AggregationStore // underlying analytics progress operations
	// reads numbers completed delegated reads; only the first closes entered
	// and waits for the test to close release before returning its saved result.
	reads atomic.Int32 // completed progress read counter
	// entered and release bracket the first read after its database query.
	// The test updates live progress between those signals, then releases the
	// already-read value to the backfill request.
	// entered signals the pause; release ends that pause
	entered, release chan struct{}
}

// canceledCleanup cancels a real cleanup task after a successful nonempty
// deletion batch.
type canceledCleanup struct {
	usage.UsageCleanupRepository // real task persistence and deletion
	// taskID and operator identify the task and canceling actor used after
	// each successful nonempty deletion. CreateTask records the ID before
	// returning to its service caller.
	// taskID identifies the task; operator identifies its canceler
	taskID, operator int64
	// observed and once publish cancellation observation. Repeated canceled
	// status reads share that channel, and once prevents any later read from
	// attempting to close it again.
	observed chan struct{} // cancellation observation completion signal
	once     sync.Once     // single observation signal publication
}

// repairStore reports real analytics repair outcomes to the lifecycle
// scenario's waiting test.
type repairStore struct {
	*AggregationStore // real analytics range repair
	// done receives every delegated repair result. The test supplies buffering
	// and drains the result so repair completion can continue returning.
	done chan error // observed aggregate repair result
}

// TestAuditClearTraceFailureRollsBack rejects the audit-clear trace through a
// database trigger. The original audit row survives that failed clear, while a
// subsequent successful clear leaves one trace with the observed deletion
// count.
func TestAuditClearTraceFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	repo := auditpg.NewAuditLogRepository(integrationDB)
	s := audit.NewAuditLogService(repo, nil)
	defer s.Stop()
	_, err := integrationDB.ExecContext(ctx, "TRUNCATE audit_logs")
	require.NoError(t, err)
	require.NoError(t, repo.Insert(ctx, &audit.AuditLog{Action: "test-original", CreatedAt: time.Now()}))
	// Reject the trace insert after audit clearing has performed its deletion,
	// forcing the transaction path to demonstrate restoration of the earlier row.
	_, err = integrationDB.ExecContext(ctx, `CREATE FUNCTION test_reject_clear_trace() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='admin.audit_log.clear' THEN RAISE EXCEPTION 'test trace failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER test_reject_clear_trace BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION test_reject_clear_trace()`)
	require.NoError(t, err)
	// Remove the injected database objects and residual traces after this
	// scenario, keeping its failure trigger from affecting later audit operations.
	t.Cleanup(func() {
		_, e := integrationDB.ExecContext(ctx, `DROP TRIGGER test_reject_clear_trace ON audit_logs; DROP FUNCTION test_reject_clear_trace(); TRUNCATE audit_logs`)
		require.NoError(t, e)
	})
	_, err = s.ClearAll(ctx, &audit.AuditLog{})
	require.Error(t, err)
	count, err := repo.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
	_, err = integrationDB.ExecContext(ctx, "DROP TRIGGER test_reject_clear_trace ON audit_logs")
	require.NoError(t, err)
	// Removing the injected rejection exercises the success phase in the same
	// fixture; the trace action and deleted_rows then describe the actual cleared
	// row.
	deleted, err := s.ClearAll(ctx, &audit.AuditLog{})
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)
	var action string
	var deletedRows int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT action,(extra->>'deleted_rows')::bigint FROM audit_logs").Scan(&action, &deletedRows))
	require.Equal(t, audit.AuditActionAuditLogClear, action)
	require.Equal(t, int64(1), deletedRows)
	// The cleanup callback registered above drops the named trigger
	// unconditionally. Recreate it after the success phase so teardown can execute
	// its full statement.
	_, err = integrationDB.ExecContext(ctx, `CREATE TRIGGER test_reject_clear_trace BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION test_reject_clear_trace()`)
	require.NoError(t, err)
}

// TestManualBackfillPreservesConcurrentState updates live progress while a
// manual request holds an earlier state read. Live fields and the new manual
// request survive; progress from a superseded worker is rejected.
func TestManualBackfillPreservesConcurrentState(t *testing.T) {
	ctx := context.Background()
	base := NewAggregationStoreWithSQL(integrationDB, timezone.NewCalendar(time.Local))
	saved, err := base.GetUsageAnalyticsAggregationState(ctx)
	require.NoError(t, err)
	// Restore saved progress after this interleaving because the state singleton
	// is shared with later integration scenarios that read aggregation coverage.
	t.Cleanup(func() { require.NoError(t, base.SaveUsageAnalyticsAggregationState(ctx, saved)) })
	old := time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC)
	next := old.Add(time.Hour)
	require.NoError(t, base.SaveUsageAnalyticsAggregationState(ctx, &usage.UsageAnalyticsAggregationState{LiveWatermark: old, Phase: "idle"}))
	r := &pausedState{AggregationStore: base, entered: make(chan struct{}), release: make(chan struct{})}
	s := usage.NewDashboardAggregationService(r, nil, &usage.Options{DashboardAgg: usage.DashboardAggregationConfig{BackfillEnabled: true}})
	s.SetPreAggregationSettings(nil)
	defer s.Stop()
	done := make(chan error, 1)
	// Begin the manual request asynchronously, then wait for its saved first read
	// before committing live progress and releasing that stale result.
	go func() { done <- s.TriggerBackfill(old.Add(-time.Hour), old) }()
	<-r.entered
	_, err = base.ApplyUsageAnalyticsState(ctx, usage.AnalyticsStateChange{Kind: usage.AnalyticsLiveSuccess, State: usage.UsageAnalyticsAggregationState{LiveWatermark: next, Phase: "idle"}})
	require.NoError(t, err)
	close(r.release)
	require.NoError(t, <-done)
	got, err := base.GetUsageAnalyticsAggregationState(ctx)
	require.NoError(t, err)
	require.True(t, got.LiveWatermark.Equal(next))
	require.NotNil(t, got.ManualBackfillStart)
	oldTarget, oldCursor := got.ManualBackfillStart, got.ManualBackfillCursor
	newTarget, newCursor := old.Add(-4*time.Hour), next
	_, err = base.ApplyUsageAnalyticsState(ctx, usage.AnalyticsStateChange{Kind: usage.AnalyticsManualRequest, State: usage.UsageAnalyticsAggregationState{ManualBackfillStart: &newTarget, ManualBackfillCursor: &newCursor}})
	require.NoError(t, err)
	// The worker still holds the earlier target and cursor. Compare that pair
	// against the newly persisted request so an old completion cannot clear it.
	_, err = base.ApplyUsageAnalyticsState(ctx, usage.AnalyticsStateChange{Kind: usage.AnalyticsManualProgress, State: usage.UsageAnalyticsAggregationState{Phase: "idle"}, ExpectedManualStart: oldTarget, ExpectedManualCursor: oldCursor})
	require.ErrorIs(t, err, usage.ErrAnalyticsRequestSuperseded)
	got, err = base.GetUsageAnalyticsAggregationState(ctx)
	require.NoError(t, err)
	require.True(t, got.ManualBackfillStart.Equal(newTarget))
	require.True(t, got.LiveWatermark.Equal(next))
	// A later live completion owns its watermark and outcome fields. The
	// assertions keep the newer manual target and cursor through that update.
	_, err = base.ApplyUsageAnalyticsState(ctx, usage.AnalyticsStateChange{Kind: usage.AnalyticsLiveSuccess, State: usage.UsageAnalyticsAggregationState{LiveWatermark: next.Add(time.Hour), Phase: "idle"}})
	require.NoError(t, err)
	got, err = base.GetUsageAnalyticsAggregationState(ctx)
	require.NoError(t, err)
	require.True(t, got.ManualBackfillStart.Equal(newTarget))
	require.True(t, got.ManualBackfillCursor.Equal(newCursor))
}

// TestCanceledPartialCleanupRepairsCommittedData cancels after one of two usage
// rows is deleted. Independent analytics repair matches the surviving raw
// count, the task remains canceled, and the user's balance remains unchanged.
func TestCanceledPartialCleanupRepairsCommittedData(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	u := mustCreateUser(t, client, &identity.User{Email: "test-cancel@test.local", Balance: 7})
	key := mustCreateApiKey(t, client, &apikey.APIKey{UserID: u.ID, Key: "sk-test-cancel", Name: "k"})
	provider := mustCreateProvider(t, client, &providercore.Record{Name: "test-cancel"})
	repo := NewUsageLogRepositoryWithSQL(client, integrationDB, timezone.NewCalendar(time.Local))
	defer repo.StopUsageBatchers()
	now := time.Now().UTC().Add(-72 * time.Hour)
	for range 2 {
		_, e := repo.Create(ctx, &usage.UsageLog{UserID: u.ID, APIKeyID: key.ID, ProviderID: provider.ID, Model: "lifecycle-test", TotalCost: 1, ActualCost: 1, CreatedAt: now})
		require.NoError(t, e)
	}
	base := NewAggregationStoreWithSQL(integrationDB, timezone.NewCalendar(time.Local))
	require.NoError(t, base.AggregateUsageAnalyticsRange(ctx, now.Add(-time.Hour), now.Add(time.Hour)))
	ar := &repairStore{AggregationStore: base, done: make(chan error, 1)}
	agg := usage.NewDashboardAggregationService(ar, nil, nil)
	agg.SetPreAggregationSettings(nil)
	agg.Start()
	defer agg.Stop()
	cr := &canceledCleanup{UsageCleanupRepository: NewUsageCleanupRepository(client, integrationDB), operator: u.ID, observed: make(chan struct{})}
	cleanup := usage.NewUsageCleanupService(cr, nil, agg, &usage.Options{UsageCleanup: usage.UsageCleanupConfig{Enabled: true, BatchSize: 1}})
	task, e := cleanup.CreateTask(ctx, usage.UsageCleanupFilters{StartTime: now.Add(-time.Hour), EndTime: now.Add(time.Hour), UserID: &u.ID}, u.ID)
	require.NoError(t, e)
	// Wait for the worker's persisted-cancellation poll so stopping cleanup
	// follows the committed partial batch reaching its next status check.
	select {
	case <-cr.observed:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel not observed")
	}
	// Stop cleanup after observing cancellation, then await repair separately;
	// stopping the canceled worker must leave the already-submitted aggregate
	// repair able to finish.
	cleanup.Stop()
	select {
	case e := <-ar.done:
		require.NoError(t, e)
	case <-time.After(5 * time.Second):
		t.Fatal("aggregate repair did not finish independently of canceled cleanup")
	}
	var raw, total int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_logs WHERE user_id=$1", u.ID).Scan(&raw))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COALESCE(SUM(total_requests),0) FROM usage_analytics_hourly WHERE user_id=$1", u.ID).Scan(&total))
	require.Equal(t, int64(1), raw)
	require.Equal(t, raw, total)
	status, e := cr.UsageCleanupRepository.GetTaskStatus(ctx, task.ID)
	require.NoError(t, e)
	require.Equal(t, usage.UsageCleanupStatusCanceled, status)
	balance, e := client.User.Get(ctx, u.ID)
	require.NoError(t, e)
	require.Equal(t, float64(7), balance.Balance, "deleting analytics must not refund money")
}

// GetUsageAnalyticsAggregationState delegates the query, then holds its first
// returned state and error at the test barrier. Later completed reads return
// immediately. The test must close release even when the held read fails, since
// the channel wait does not select on ctx cancellation.
//
// Closing entered lets the test persist a live change after this query has
// captured its state. Waiting for release then returns that earlier result to
// the manual request, reproducing the stale-read interleaving needed by the
// state-ownership assertion.
func (r *pausedState) GetUsageAnalyticsAggregationState(ctx context.Context) (*usage.UsageAnalyticsAggregationState, error) {
	v, e := r.AggregationStore.GetUsageAnalyticsAggregationState(ctx)
	// Choose one completed read with an atomic increment so concurrent callers
	// cannot both publish and wait at the fixture barrier.
	if r.reads.Add(1) == 1 {
		// Publish that the first query has captured its result before blocking; the
		// test performs its concurrent state update during this pause.
		close(r.entered)
		<-r.release
	}
	return v, e
}

// CreateTask delegates task creation and records the supplied task's resulting
// ID before returning its error. The fixture supplies a nonnil task; later
// deletion calls use the recorded ID for cancellation.
func (r *canceledCleanup) CreateTask(ctx context.Context, t *usage.UsageCleanupTask) error {
	e := r.UsageCleanupRepository.CreateTask(ctx, t)
	r.taskID = t.ID
	return e
}

// DeleteUsageLogsBatch delegates the real raw deletion and then cancels the
// fixture task after every successful nonempty batch. Empty or failed batches
// return the deletion result directly. A cancellation failure replaces the nil
// deletion error while retaining the count already returned by deletion.
//
// This ordering creates committed partial work before the worker's next status
// check. The lifecycle scenario can then verify repair of surviving data and
// the retained canceled status, rather than testing cancellation before any
// batch has made progress.
func (r *canceledCleanup) DeleteUsageLogsBatch(ctx context.Context, f usage.UsageCleanupFilters, n int) (int64, error) {
	d, e := r.UsageCleanupRepository.DeleteUsageLogsBatch(ctx, f, n)
	if e == nil && d > 0 {
		// Deletion has already returned a positive count. Persist cancellation
		// afterwards so the next worker status poll encounters partial progress that
		// still needs repair.
		_, e = r.CancelTask(ctx, r.taskID, r.operator)
	}
	return d, e
}

// GetTaskStatus delegates the stored-state read and publishes the first
// observed canceled value by closing observed. sync.Once permits repeated
// canceled reads, and the original status and error remain the caller's result.
func (r *canceledCleanup) GetTaskStatus(ctx context.Context, id int64) (string, error) {
	v, e := r.UsageCleanupRepository.GetTaskStatus(ctx, id)
	if v == usage.UsageCleanupStatusCanceled {
		// Repeated worker polls can observe the same cancellation. Publish one
		// completion signal so the waiting test can stop cleanup after that poll.
		r.once.Do(func() { close(r.observed) })
	}
	return v, e
}

// RecomputeUsageAnalyticsRange delegates real repair and sends its error to
// done before returning that error. The test supplies a buffered channel and
// receives the result; additional undrained repairs can block this helper.
func (r *repairStore) RecomputeUsageAnalyticsRange(ctx context.Context, start, end time.Time) error {
	e := r.AggregationStore.RecomputeUsageAnalyticsRange(ctx, start, end)
	// Expose the completed database repair before returning to the runtime,
	// allowing the test to await work submitted by the canceled cleanup task.
	r.done <- e
	return e
}
