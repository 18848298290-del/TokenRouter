package usage

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/settings"
	p "github.com/TokenFlux/TokenRouter/internal/settings/preaggregation"
)

// Aggregation progress can be updated while manual backfill is reading its
// target, and shutdown can cancel a repair that has already entered storage.
// Controlled read and repair channels reproduce those orderings, while
// range-capturing fixtures expose which hours, dates and persistence
// operations a round attempted.
//
// Historical cases examine saved cursors and phase transitions after bounded
// work, failed daily rebuilding or a query deadline. Runtime-setting cases
// inspect dispatch and schedule admission separately from storage contents.
// The complete-bucket-named repair case checks which method received the
// supplied range; it does not inspect rebuilt database buckets. Fixture
// configuration and observations are serialized except for the explicitly
// synchronized regression helpers.

// SettingKeyPreAggregationSettings names the persisted pre-aggregation JSON
// key used by runtime configuration fixture reads.
const SettingKeyPreAggregationSettings = p.SettingKeyPreAggregationSettings

// blockingAggregation holds one repair query and records whether its runtime
// context was canceled.
type blockingAggregation struct {
	DashboardAggregationRepository // unused dashboard repository method slots
	// entered and release separate one repair's invocation from its completion.
	// Configure both before dispatch; canceled records which context result
	// the selected wait returned.
	entered  chan struct{} // first blocked operation entry signal
	release  chan struct{} // explicit blocked operation release signal
	canceled atomic.Bool   // atomic context cancellation observation flag
}

// stateRepo blocks the first copied state read so manual-request and
// live-progress writes can interleave. Its current state is protected by mu,
// while reads counts invocations atomically.
type stateRepo struct {
	UsageAnalyticsAggregationRepository // unused analytics repository method slots
	// mu protects state during copying, replacement and field-specific merges.
	// Copies retain timestamp pointers, so callers leave those pointed-to values
	// stable while concurrent top-level updates are applied.
	mu    sync.Mutex                     // fixture progress state access mutex
	state UsageAnalyticsAggregationState // configured shallow aggregation progress state
	reads atomic.Int32                   // atomic state read invocation count
	// entered closes after the first read copies state, and release controls
	// when that old snapshot returns. Later reads bypass this gate while live
	// progress can advance.
	entered chan struct{} // first blocked operation entry signal
	release chan struct{} // explicit blocked operation release signal
}

// dashboardAggregationRepoTestStub captures requested ranges and retention
// calls for serial aggregation tests. Configure its outcomes before dispatch
// and inspect captured fields after the tested round completes.
type dashboardAggregationRepoTestStub struct {
	aggregateCalls       int                    // captured dashboard aggregation invocation count
	aggregateRanges      []aggregationRangeCall // captured dashboard aggregation ranges
	recomputeCalls       int                    // captured dashboard repair invocation count
	cleanupUsageCalls    int                    // recorded original row cleanup attempts
	cleanupDedupCalls    int                    // recorded dedup cleanup attempts
	ensurePartitionCalls int                    // recorded partition preparation attempts
	lastStart            time.Time              // latest dashboard write start
	lastEnd              time.Time              // latest dashboard write end
	watermark            time.Time              // configured dashboard progress instant
	aggregateErr         error                  // injected dashboard write failure
	cleanupAggregatesErr error                  // injected rollup cleanup failure
	cleanupUsageErr      error                  // injected original row cleanup failure
	cleanupDedupErr      error                  // injected dedup cleanup failure
	ensurePartitionErr   error                  // injected partition preparation failure
	// aggregateStarted optionally receives a nonblocking invocation signal.
	// A channel whose send is not ready drops that notification, and reception
	// can precede completion of the admitted round.
	aggregateStarted chan struct{} // optional dashboard write notification channel
}

// aggregationRangeCall records one start and end pair passed to an
// aggregation fixture.
type aggregationRangeCall struct {
	start time.Time // captured range beginning instant
	end   time.Time // captured range ending instant
}

// usageAnalyticsAggregationRepoTestStub captures analytics stage ranges and
// stores shallow progress copies. These test cases use its state and callback
// configuration serially; synchronized concurrent-state tests use stateRepo.
type usageAnalyticsAggregationRepoTestStub struct {
	state          UsageAnalyticsAggregationState                    // configured shallow aggregation progress state
	oldest         *time.Time                                        // configured retained source beginning instant
	aggregateCalls []aggregationRangeCall                            // captured full analytics write ranges
	hourlyCalls    []aggregationRangeCall                            // captured analytics hourly write ranges
	dailyCalls     []aggregationRangeCall                            // captured analytics daily write ranges
	recomputeCalls []aggregationRangeCall                            // captured analytics repair request ranges
	hourlyCallback func(context.Context, time.Time, time.Time) error // optional hourly outcome injection callback
	dailyCallback  func(context.Context, time.Time, time.Time) error // optional daily outcome injection callback
}

type (
	// PreAggregationUsageSettings names runtime usage settings used by
	// aggregation configuration fixtures.
	PreAggregationUsageSettings = p.PreAggregationUsageSettings
	// PreAggregationOpsSettings names runtime Ops settings included in
	// aggregation configuration fixtures.
	PreAggregationOpsSettings = p.PreAggregationOpsSettings
	// runtimeSettingRepoStub stores configuration text under a mutex for runtime
	// setting tests.
	runtimeSettingRepoStub struct {
		mu     sync.Mutex        // fixture setting map access mutex
		values map[string]string // configured persisted setting text map
	}
)

// RecomputeRange announces one repair query and blocks until its selected
// release or context-cancellation case. The fixture is configured before its
// single invocation, and cancellation is recorded atomically.
//
// The shutdown test can therefore observe whether runtime cancellation
// reaches an already-entered repository operation. A release result succeeds,
// while a canceled result returns ctx.Err. The select decides which ready
// branch supplies the result; the fixture does not impose priority when both
// signals are ready.
func (r *blockingAggregation) RecomputeRange(ctx context.Context, a, b time.Time) error {
	// Publish entry before choosing release or cancellation so the test starts
	// shutdown while this repair is already registered and executing in storage.
	close(r.entered)
	select {
	case <-ctx.Done():
		r.canceled.Store(true)
		return ctx.Err()
	case <-r.release:
		return nil
	}
}

// TestRegressionAggregationStopCancelsWork dispatches a repair blocked inside
// storage, then calls Stop. It requires shutdown completion within the
// observation timeout and a recorded canceled repository context.
func TestRegressionAggregationStopCancelsWork(t *testing.T) {
	r := &blockingAggregation{entered: make(chan struct{}), release: make(chan struct{})}
	s := NewDashboardAggregationService(r, nil, nil)
	s.runtimeStarted = true
	if e := s.TriggerRecomputeRange(time.Now().Add(-time.Hour), time.Now()); e != nil {
		t.Fatal(e)
	}
	<-r.entered
	done := make(chan struct{})
	// Start shutdown after repository entry so the runtime must cancel and wait
	// for accepted repair work, rather than reject a dispatch that never entered storage.
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Error("Stop did not cancel context-aware in-flight aggregation")
	}
	close(r.release)
	<-done
	if !r.canceled.Load() {
		t.Error("aggregation context was never canceled")
	}
}

// GetUsageAnalyticsAggregationState copies current progress under mu, then
// holds the first read before returning it. The returned state is shallow, so
// its timestamp pointers retain their stored references.
//
// The first invocation closes entered only after copying, and release delays
// publication of that old snapshot. Another field-specific update can
// therefore advance live progress while the manual caller still holds its
// earlier observation. Callers configure the two channels before use and
// invoke this controlled first-read path once.
func (r *stateRepo) GetUsageAnalyticsAggregationState(context.Context) (*UsageAnalyticsAggregationState, error) {
	r.mu.Lock()
	v := r.state
	r.mu.Unlock()
	// Hold the first copied snapshot outside mu so another update can publish live
	// progress while its reader still carries the earlier state for manual-request persistence.
	if r.reads.Add(1) == 1 {
		close(r.entered)
		<-r.release
	}
	return &v, nil
}

// SaveUsageAnalyticsAggregationState stores the supplied state by value under
// mu. Callers supply a nonnil pointer, and retained timestamp pointers remain
// shared with the fixture's shallow state representation.
func (r *stateRepo) SaveUsageAnalyticsAggregationState(_ context.Context, v *UsageAnalyticsAggregationState) error {
	r.mu.Lock()
	r.state = *v
	r.mu.Unlock()
	return nil
}

// TestRegressionManualBackfillPreservesLiveProgress holds a manual request's
// first state read while live progress advances. It requires successful
// request creation and retention of the later live watermark.
func TestRegressionManualBackfillPreservesLiveProgress(t *testing.T) {
	old := time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC)
	next := old.Add(time.Hour)
	r := &stateRepo{state: UsageAnalyticsAggregationState{LiveWatermark: old}, entered: make(chan struct{}), release: make(chan struct{})}
	s := NewDashboardAggregationService(&dashboardAggregationRepoTestStub{}, nil, &Options{DashboardAgg: DashboardAggregationConfig{BackfillEnabled: true}})
	s.analyticsRepo = r
	done := make(chan error, 1)
	// Start manual persistence through the controlled read, allowing a later
	// live watermark to be published before the older manual observation returns.
	go func() { done <- s.TriggerBackfill(old.Add(-time.Hour), old) }()
	// The first snapshot is held after copying state. Publish a newer live watermark
	// before releasing that stale read so manual persistence must use current owned fields.
	<-r.entered
	s.markAnalyticsLiveSuccess(context.Background(), next, old, time.Now())
	close(r.release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	r.mu.Lock()
	got := r.state.LiveWatermark
	r.mu.Unlock()
	if !got.Equal(next) {
		t.Errorf("live watermark regressed: want %v got %v", next, got)
	}
}

// ApplyUsageAnalyticsState merges the change against current state under mu
// before replacing it. This lets the regression apply field-specific manual
// updates to the latest live watermark rather than its blocked read copy.
func (r *stateRepo) ApplyUsageAnalyticsState(_ context.Context, c AnalyticsStateChange) (*UsageAnalyticsAggregationState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, e := ApplyAnalyticsStateChange(r.state, c)
	if e != nil {
		return nil, e
	}
	// Merge against the latest guarded state before publication. A blocked earlier
	// read can then supply a manual change while this fixture keeps current live progress.
	r.state = v
	return &v, nil
}

// AggregateRange captures dashboard range calls and optionally announces an
// attempted write. The notification is nonblocking, and the configured
// aggregation error is returned after its serial fixture fields are recorded.
func (s *dashboardAggregationRepoTestStub) AggregateRange(ctx context.Context, start, end time.Time) error {
	s.aggregateCalls++
	s.aggregateRanges = append(s.aggregateRanges, aggregationRangeCall{start: start, end: end})
	s.lastStart = start
	s.lastEnd = end
	if s.aggregateStarted != nil {
		select {
		case s.aggregateStarted <- struct{}{}:
		default:
		}
	}
	return s.aggregateErr
}

// AggregateUsageAnalyticsRange records the full analytics range attempted by
// a serial test round.
func (s *usageAnalyticsAggregationRepoTestStub) AggregateUsageAnalyticsRange(_ context.Context, start, end time.Time) error {
	s.aggregateCalls = append(s.aggregateCalls, aggregationRangeCall{start: start, end: end})
	return nil
}

// AggregateUsageAnalyticsHourlyRange records the attempted hour before
// calling its optional injected callback. A missing callback succeeds, while
// configured delays or failures control the historical budget and retry
// cases.
func (s *usageAnalyticsAggregationRepoTestStub) AggregateUsageAnalyticsHourlyRange(ctx context.Context, start, end time.Time) error {
	s.hourlyCalls = append(s.hourlyCalls, aggregationRangeCall{start: start, end: end})
	if s.hourlyCallback != nil {
		return s.hourlyCallback(ctx, start, end)
	}
	return nil
}

// RebuildUsageAnalyticsDailyRange records the attempted daily range before
// its optional callback runs. A missing callback succeeds, and injected
// failure lets tests inspect cursor publication after the daily stage.
func (s *usageAnalyticsAggregationRepoTestStub) RebuildUsageAnalyticsDailyRange(ctx context.Context, start, end time.Time) error {
	s.dailyCalls = append(s.dailyCalls, aggregationRangeCall{start: start, end: end})
	if s.dailyCallback != nil {
		return s.dailyCallback(ctx, start, end)
	}
	return nil
}

// RecomputeUsageAnalyticsRange records the supplied repair range for
// analytics dispatch assertions.
func (s *usageAnalyticsAggregationRepoTestStub) RecomputeUsageAnalyticsRange(_ context.Context, start, end time.Time) error {
	s.recomputeCalls = append(s.recomputeCalls, aggregationRangeCall{start: start, end: end})
	return nil
}

// GetUsageAnalyticsAggregationState returns a shallow state copy for the
// serial fixture. Timestamp pointers retain their references, while later
// top-level field assignments update only the returned copy until persistence
// runs.
func (s *usageAnalyticsAggregationRepoTestStub) GetUsageAnalyticsAggregationState(context.Context) (*UsageAnalyticsAggregationState, error) {
	state := s.state
	return &state, nil
}

// SaveUsageAnalyticsAggregationState stores a shallow copy of a supplied
// nonnil state. Nil input succeeds with the current fixture state retained,
// and callers serialize access in these range and budget tests.
func (s *usageAnalyticsAggregationRepoTestStub) SaveUsageAnalyticsAggregationState(_ context.Context, state *UsageAnalyticsAggregationState) error {
	if state != nil {
		s.state = *state
	}
	return nil
}

// GetOldestUsageLogTime returns the configured retained-source starting
// instant for backfill fixtures.
func (s *usageAnalyticsAggregationRepoTestStub) GetOldestUsageLogTime(context.Context) (*time.Time, error) {
	return s.oldest, nil
}

// CleanupUsageAnalytics acknowledges unused analytics retention calls in the
// backfill fixtures.
func (s *usageAnalyticsAggregationRepoTestStub) CleanupUsageAnalytics(context.Context, time.Time, time.Time) error {
	return nil
}

// RecomputeRange counts a repair invocation before delegating to the
// range-capturing fixture write.
func (s *dashboardAggregationRepoTestStub) RecomputeRange(ctx context.Context, start, end time.Time) error {
	s.recomputeCalls++
	return s.AggregateRange(ctx, start, end)
}

// GetAggregationWatermark returns the configured dashboard progress instant
// for scheduled-round fixtures.
func (s *dashboardAggregationRepoTestStub) GetAggregationWatermark(ctx context.Context) (time.Time, error) {
	return s.watermark, nil
}

// UpdateAggregationWatermark acknowledges progress publication calls in the
// scheduled-round fixture.
func (s *dashboardAggregationRepoTestStub) UpdateAggregationWatermark(ctx context.Context, aggregatedAt time.Time) error {
	return nil
}

// CleanupAggregates returns the configured rollup retention outcome for the
// tested round.
func (s *dashboardAggregationRepoTestStub) CleanupAggregates(ctx context.Context, hourlyCutoff, dailyCutoff time.Time) error {
	return s.cleanupAggregatesErr
}

// CleanupUsageLogs records one original-row retention attempt and returns its
// configured outcome.
func (s *dashboardAggregationRepoTestStub) CleanupUsageLogs(ctx context.Context, cutoff time.Time) error {
	s.cleanupUsageCalls++
	return s.cleanupUsageErr
}

// CleanupUsageBillingDedup records one deduplication retention attempt and
// returns its configured outcome.
func (s *dashboardAggregationRepoTestStub) CleanupUsageBillingDedup(ctx context.Context, cutoff time.Time) error {
	s.cleanupDedupCalls++
	return s.cleanupDedupErr
}

// EnsureUsageLogsPartitions records one partition preparation attempt and
// returns its configured outcome.
func (s *dashboardAggregationRepoTestStub) EnsureUsageLogsPartitions(ctx context.Context, now time.Time) error {
	s.ensurePartitionCalls++
	return s.ensurePartitionErr
}

// TestDashboardAggregationService_RunScheduledAggregation_EpochUsesLiveLookback
// supplies epoch progress and a two-minute lookback. It requires one
// aggregate call with a start near the captured end minus that lookback.
func TestDashboardAggregationService_RunScheduledAggregation_EpochUsesLiveLookback(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{watermark: time.Unix(0, 0).UTC()}
	svc := &DashboardAggregationService{
		repo: repo,
		cfg: DashboardAggregationConfig{
			Enabled:         true,
			IntervalSeconds: 60,
			LookbackSeconds: 120,
			Retention: DashboardAggregationRetentionConfig{
				UsageLogsDays: 1,
				HourlyDays:    1,
				DailyDays:     1,
			},
		},
	}

	svc.runScheduledAggregation()

	require.Equal(t, 1, repo.aggregateCalls)
	require.False(t, repo.lastEnd.IsZero())
	require.WithinDuration(t, repo.lastEnd.Add(-120*time.Second), repo.lastStart, time.Second)
}

// TestDashboardAggregationServiceRuntimeDisabledStopsWrites supplies disabled
// runtime usage settings under enabled deployment configuration. It requires
// the scheduled attempt to make zero dashboard aggregation calls.
func TestDashboardAggregationServiceRuntimeDisabledStopsWrites(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{watermark: time.Unix(0, 0).UTC()}
	settingRepo := newRuntimeSettingRepoStub()
	settingRepo.values[SettingKeyPreAggregationSettings] = `{"usage":{"enabled":false,"interval_seconds":60},"ops":{"enabled":true}}`
	cfg := &Options{DashboardAgg: DashboardAggregationConfig{Enabled: true, IntervalSeconds: 60}}
	settings := NewPreAggregationSettingsService(settingRepo, cfg)
	svc := NewDashboardAggregationService(repo, nil, cfg)
	svc.SetPreAggregationSettings(settings)

	svc.runScheduledAggregation()

	require.Zero(t, repo.aggregateCalls)
}

// TestDashboardAggregationServiceRuntimeEnableTriggersImmediately starts an
// initially disabled runtime, then enables usage settings. It requires a
// range-write notification before the observation timeout expires.
func TestDashboardAggregationServiceRuntimeEnableTriggersImmediately(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{
		watermark:        time.Unix(0, 0).UTC(),
		aggregateStarted: make(chan struct{}, 1),
	}
	settingRepo := newRuntimeSettingRepoStub()
	settingRepo.values[SettingKeyPreAggregationSettings] = `{"usage":{"enabled":false,"interval_seconds":60},"ops":{"enabled":true}}`
	cfg := &Options{
		DashboardAgg: DashboardAggregationConfig{
			Enabled: true, IntervalSeconds: 60, LookbackSeconds: 120,
			Retention: DashboardAggregationRetentionConfig{HourlyDays: 1, DailyDays: 1},
		},
	}
	settings := NewPreAggregationSettingsService(settingRepo, cfg)
	svc := NewDashboardAggregationService(repo, nil, cfg)
	svc.SetPreAggregationSettings(settings)
	svc.Start()
	t.Cleanup(svc.Stop)

	_, err := settings.Update(context.Background(), PreAggregationSettings{
		Usage: PreAggregationUsageSettings{Enabled: true, IntervalSeconds: 60},
		Ops:   PreAggregationOpsSettings{Enabled: true},
	})
	require.NoError(t, err)
	select {
	case <-repo.aggregateStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("开启预聚合后未立即触发任务")
	}
}

// TestDashboardAggregationServiceRunningJobPreservesWakeup sets an occupied
// local running flag beside a cleared schedule timestamp. It requires no
// aggregate call and the immediate-run timestamp to remain cleared.
func TestDashboardAggregationServiceRunningJobPreservesWakeup(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{}
	svc := &DashboardAggregationService{
		repo: repo,
		cfg: DashboardAggregationConfig{
			Enabled:         true,
			IntervalSeconds: 3600,
		},
	}
	atomic.StoreInt32(&svc.running, 1)
	svc.lastScheduledAt.Store(0)

	svc.runScheduledAggregation()

	require.Zero(t, svc.lastScheduledAt.Load())
	require.Zero(t, repo.aggregateCalls)
}

// TestDashboardAggregationServiceRecomputeRunsWhileRuntimeDisabled starts
// with runtime usage disabled and requests repair. It requires successful
// dispatch and a repository repair notification despite the scheduled-work
// setting.
func TestDashboardAggregationServiceRecomputeRunsWhileRuntimeDisabled(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{aggregateStarted: make(chan struct{}, 1)}
	settingRepo := newRuntimeSettingRepoStub()
	settingRepo.values[SettingKeyPreAggregationSettings] = `{"usage":{"enabled":false,"interval_seconds":60},"ops":{"enabled":true}}`
	cfg := &Options{DashboardAgg: DashboardAggregationConfig{Enabled: true, IntervalSeconds: 60}}
	settings := NewPreAggregationSettingsService(settingRepo, cfg)
	svc := NewDashboardAggregationService(repo, nil, cfg)
	svc.SetPreAggregationSettings(settings)
	svc.Start()
	t.Cleanup(svc.Stop)

	end := time.Now().UTC()
	require.NoError(t, svc.TriggerRecomputeRange(end.Add(-time.Hour), end))
	select {
	case <-repo.aggregateStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("运行时关闭后内部一致性重算未执行")
	}
}

// TestDashboardAggregationService_CleanupRetentionFailure_DoesNotRecord
// injects a rollup retention cleanup failure. It requires an absent
// successful-cleanup timestamp while original-row and billing-deduplication
// cleanup calls are still recorded.
func TestDashboardAggregationService_CleanupRetentionFailure_DoesNotRecord(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{cleanupAggregatesErr: errors.New("清理失败")}
	svc := &DashboardAggregationService{
		repo: repo,
		cfg: DashboardAggregationConfig{
			Retention: DashboardAggregationRetentionConfig{
				UsageLogsDays: 1,
				HourlyDays:    1,
				DailyDays:     1,
			},
		},
	}

	svc.maybeCleanupRetention(context.Background(), time.Now().UTC())

	require.Nil(t, svc.lastRetentionCleanup.Load())
	require.Equal(t, 1, repo.cleanupUsageCalls)
	require.Equal(t, 1, repo.cleanupDedupCalls)
}

// TestDashboardAggregationService_CleanupDedupFailure_DoesNotRecord injects a
// billing deduplication retention failure. It requires an absent
// successful-cleanup timestamp and exactly one captured deduplication cleanup
// attempt for this round.
func TestDashboardAggregationService_CleanupDedupFailure_DoesNotRecord(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{cleanupDedupErr: errors.New("dedup cleanup failed")}
	svc := &DashboardAggregationService{
		repo: repo,
		cfg: DashboardAggregationConfig{
			Retention: DashboardAggregationRetentionConfig{
				UsageLogsDays: 1,
				HourlyDays:    1,
				DailyDays:     1,
			},
		},
	}

	svc.maybeCleanupRetention(context.Background(), time.Now().UTC())

	require.Nil(t, svc.lastRetentionCleanup.Load())
	require.Equal(t, 1, repo.cleanupDedupCalls)
}

// TestDashboardAggregationService_PartitionFailure_DoesNotAggregate injects a
// partition preparation error. Its assertions require one partition check and
// one aggregation call, so the tested behavior continues range aggregation.
func TestDashboardAggregationService_PartitionFailure_DoesNotAggregate(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{ensurePartitionErr: errors.New("partition failed")}
	svc := &DashboardAggregationService{
		repo: repo,
		cfg: DashboardAggregationConfig{
			Enabled:         true,
			IntervalSeconds: 60,
			LookbackSeconds: 120,
			Retention: DashboardAggregationRetentionConfig{
				UsageLogsDays:         1,
				UsageBillingDedupDays: 2,
				HourlyDays:            1,
				DailyDays:             1,
			},
		},
	}

	svc.runScheduledAggregation()

	require.Equal(t, 1, repo.ensurePartitionCalls)
	require.Equal(t, 1, repo.aggregateCalls)
}

// TestDashboardAggregationService_TriggerBackfill_TooLarge requests more than
// the configured one-day range. It requires the oversized-backfill sentinel
// and no dashboard range write during request validation.
func TestDashboardAggregationService_TriggerBackfill_TooLarge(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{}
	svc := &DashboardAggregationService{
		repo: repo,
		cfg: DashboardAggregationConfig{
			BackfillEnabled: true,
			BackfillMaxDays: 1,
		},
	}

	start := time.Now().AddDate(0, 0, -3)
	end := time.Now()
	err := svc.TriggerBackfill(start, end)
	require.ErrorIs(t, err, ErrDashboardBackfillTooLarge)
	require.Equal(t, 0, repo.aggregateCalls)
}

// TestDashboardAggregationServiceTriggerBackfillPersistsManualRange supplies
// an intrahour request beside automatic progress. It checks UTC-hour manual
// fields, retained automatic cursor, backfill phase and cleared historical
// admission timestamp.
func TestDashboardAggregationServiceTriggerBackfillPersistsManualRange(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 35, 0, 0, time.UTC)
	automaticCursor := now.AddDate(0, 0, -10).Truncate(time.Hour)
	analyticsRepo := &usageAnalyticsAggregationRepoTestStub{
		state: UsageAnalyticsAggregationState{BackfillCursor: timePointer(automaticCursor)},
	}
	svc := &DashboardAggregationService{
		repo:          &dashboardAggregationRepoTestStub{},
		analyticsRepo: analyticsRepo,
		cfg: DashboardAggregationConfig{
			BackfillEnabled: true,
			BackfillMaxDays: 31,
		},
	}
	svc.lastBackfillAt.Store(now.Add(-30 * time.Second).UnixNano())

	require.NoError(t, svc.TriggerBackfill(now.AddDate(0, 0, -7), now))

	require.Equal(t, now.AddDate(0, 0, -7).Truncate(time.Hour), *analyticsRepo.state.ManualBackfillStart)
	require.Equal(t, now.Truncate(time.Hour), *analyticsRepo.state.ManualBackfillCursor)
	require.Equal(t, automaticCursor, *analyticsRepo.state.BackfillCursor)
	require.Equal(t, "backfill", analyticsRepo.state.Phase)
	require.Zero(t, svc.lastBackfillAt.Load())
}

// TestDashboardAggregationServiceManualBackfillStopsAtRequestedStart supplies
// a two-hour manual request with older retained history. It checks two chunk
// calls, the final chunk's exact range, cleared manual fields and coverage at
// the requested target.
func TestDashboardAggregationServiceManualBackfillStopsAtRequestedStart(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	target := now.Add(-2 * time.Hour)
	oldest := now.Add(-10 * time.Hour)
	analyticsRepo := &usageAnalyticsAggregationRepoTestStub{
		state: UsageAnalyticsAggregationState{
			CoverageStart:        timePointer(now),
			BackfillCursor:       timePointer(now),
			SourceOldestAt:       timePointer(oldest),
			ManualBackfillStart:  timePointer(target),
			ManualBackfillCursor: timePointer(now),
		},
		oldest: timePointer(oldest),
	}
	dashboardRepo := &dashboardAggregationRepoTestStub{}
	svc := &DashboardAggregationService{repo: dashboardRepo, analyticsRepo: analyticsRepo}

	svc.runHistoricalBackfill(context.Background(), now)

	require.Len(t, analyticsRepo.aggregateCalls, 2)
	require.Equal(t, target, analyticsRepo.aggregateCalls[1].start)
	require.Equal(t, target.Add(time.Hour), analyticsRepo.aggregateCalls[1].end)
	require.Nil(t, analyticsRepo.state.ManualBackfillStart)
	require.Nil(t, analyticsRepo.state.ManualBackfillCursor)
	require.Equal(t, target, *analyticsRepo.state.CoverageStart)
	require.Equal(t, target, *analyticsRepo.state.BackfillCursor)
	require.Equal(t, "idle", analyticsRepo.state.Phase)
	require.Equal(t, 2, dashboardRepo.aggregateCalls)
}

// TestDashboardAggregationServiceManualBackfillResumesFromSavedCursor
// requests twenty-six hours and invokes two rounds. It checks the
// twenty-four-hour first-round cursor and the remaining two chunks before
// manual fields clear.
func TestDashboardAggregationServiceManualBackfillResumesFromSavedCursor(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	target := now.Add(-26 * time.Hour)
	analyticsRepo := &usageAnalyticsAggregationRepoTestStub{
		state: UsageAnalyticsAggregationState{
			CoverageStart:        timePointer(now),
			BackfillCursor:       timePointer(now),
			ManualBackfillStart:  timePointer(target),
			ManualBackfillCursor: timePointer(now),
		},
	}
	svc := &DashboardAggregationService{
		repo:          &dashboardAggregationRepoTestStub{},
		analyticsRepo: analyticsRepo,
	}

	svc.runHistoricalBackfill(context.Background(), now)

	require.Len(t, analyticsRepo.aggregateCalls, dashboardAggregationBackfillMaxHours)
	require.Equal(t, now.Add(-24*time.Hour), *analyticsRepo.state.ManualBackfillCursor)
	require.Equal(t, target, *analyticsRepo.state.ManualBackfillStart)
	require.Equal(t, "backfill", analyticsRepo.state.Phase)

	svc.lastBackfillAt.Store(0)
	svc.runHistoricalBackfill(context.Background(), now.Add(time.Minute))

	require.Len(t, analyticsRepo.aggregateCalls, 26)
	require.Equal(t, target, analyticsRepo.aggregateCalls[25].start)
	require.Equal(t, target.Add(time.Hour), analyticsRepo.aggregateCalls[25].end)
	require.Nil(t, analyticsRepo.state.ManualBackfillStart)
	require.Nil(t, analyticsRepo.state.ManualBackfillCursor)
}

// TestDashboardAggregationServiceLiveRebuildsTouchedClosedUTCDays advances a
// live lookback across UTC midnight. It checks daily rebuild ranges while
// yesterday is touched, then no daily call after the start reaches today.
func TestDashboardAggregationServiceLiveRebuildsTouchedClosedUTCDays(t *testing.T) {
	analyticsRepo := &usageAnalyticsAggregationRepoTestStub{}
	svc := &DashboardAggregationService{
		repo:          &dashboardAggregationRepoTestStub{},
		analyticsRepo: analyticsRepo,
	}
	start := time.Date(2026, 8, 3, 23, 58, 0, 0, time.UTC)
	end := time.Date(2026, 8, 4, 0, 2, 0, 0, time.UTC)

	require.NoError(t, svc.aggregateLiveRange(context.Background(), start, end))
	require.Equal(t, []aggregationRangeCall{{start: start, end: end}}, analyticsRepo.hourlyCalls)
	require.Equal(t, []aggregationRangeCall{{
		start: truncateToDayUTC(start),
		end:   truncateToDayUTC(end),
	}}, analyticsRepo.dailyCalls)

	analyticsRepo.dailyCalls = nil
	// Keep yesterday's bucket eligible for refresh while the next lookback still
	// crosses midnight, allowing another round to absorb its late records.
	require.NoError(t, svc.aggregateLiveRange(context.Background(), start.Add(time.Minute), end.Add(time.Minute)))
	require.Equal(t, []aggregationRangeCall{{
		start: truncateToDayUTC(start),
		end:   truncateToDayUTC(end),
	}}, analyticsRepo.dailyCalls)

	analyticsRepo.dailyCalls = nil
	// Once the lookback lies within today, yesterday's bucket needs no further refresh.
	require.NoError(t, svc.aggregateLiveRange(context.Background(), start.Add(2*time.Minute), end.Add(2*time.Minute)))
	require.Empty(t, analyticsRepo.dailyCalls)
}

// TestDashboardAggregationServiceAutomaticBackfillRebuildsCompletedDay
// supplies one complete retained UTC date. It checks twenty-four hourly
// calls, one daily range and an idle cursor at the oldest hour.
func TestDashboardAggregationServiceAutomaticBackfillRebuildsCompletedDay(t *testing.T) {
	cursor := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	oldest := cursor.Add(-24 * time.Hour)
	analyticsRepo := &usageAnalyticsAggregationRepoTestStub{
		state: UsageAnalyticsAggregationState{
			CoverageStart:  timePointer(cursor),
			BackfillCursor: timePointer(cursor),
			SourceOldestAt: timePointer(oldest),
		},
	}
	svc := &DashboardAggregationService{
		repo:          &dashboardAggregationRepoTestStub{},
		analyticsRepo: analyticsRepo,
	}

	svc.runHistoricalBackfill(context.Background(), cursor)

	require.Len(t, analyticsRepo.hourlyCalls, 24)
	require.Equal(t, []aggregationRangeCall{{start: oldest, end: cursor}}, analyticsRepo.dailyCalls)
	require.Equal(t, oldest, *analyticsRepo.state.BackfillCursor)
	require.Equal(t, "idle", analyticsRepo.state.Phase)
}

// TestDashboardAggregationServiceAutomaticBackfillRebuildsOldestPartialDay
// supplies three retained hours within one date. It checks three hourly
// calls, a full-date daily rebuild request and the cursor at oldest source
// time.
func TestDashboardAggregationServiceAutomaticBackfillRebuildsOldestPartialDay(t *testing.T) {
	cursor := time.Date(2026, 8, 2, 8, 0, 0, 0, time.UTC)
	oldest := time.Date(2026, 8, 2, 5, 0, 0, 0, time.UTC)
	analyticsRepo := &usageAnalyticsAggregationRepoTestStub{
		state: UsageAnalyticsAggregationState{
			CoverageStart:  timePointer(cursor),
			BackfillCursor: timePointer(cursor),
			SourceOldestAt: timePointer(oldest),
		},
	}
	svc := &DashboardAggregationService{
		repo:          &dashboardAggregationRepoTestStub{},
		analyticsRepo: analyticsRepo,
	}

	svc.runHistoricalBackfill(context.Background(), cursor)

	require.Len(t, analyticsRepo.hourlyCalls, 3)
	dayStart := truncateToDayUTC(oldest)
	require.Equal(t, []aggregationRangeCall{{start: dayStart, end: dayStart.AddDate(0, 0, 1)}}, analyticsRepo.dailyCalls)
	require.Equal(t, oldest, *analyticsRepo.state.BackfillCursor)
}

// TestDashboardAggregationServiceDailyFailureDoesNotAdvanceCursor fails daily
// rebuilding after its hourly write, then retries successfully. It checks
// retained cursor and error phase first, followed by repeated calls and
// completed idle progress.
func TestDashboardAggregationServiceDailyFailureDoesNotAdvanceCursor(t *testing.T) {
	oldest := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	cursor := oldest.Add(time.Hour)
	dailyErr := errors.New("日表写入失败")
	analyticsRepo := &usageAnalyticsAggregationRepoTestStub{
		state: UsageAnalyticsAggregationState{
			CoverageStart:  timePointer(cursor),
			BackfillCursor: timePointer(cursor),
			SourceOldestAt: timePointer(oldest),
		},
		dailyCallback: func(context.Context, time.Time, time.Time) error {
			return dailyErr
		},
	}
	svc := &DashboardAggregationService{
		repo:          &dashboardAggregationRepoTestStub{},
		analyticsRepo: analyticsRepo,
	}

	svc.runHistoricalBackfill(context.Background(), cursor)
	require.Equal(t, cursor, *analyticsRepo.state.BackfillCursor)
	require.Equal(t, "error", analyticsRepo.state.Phase)
	require.Equal(t, dailyErr.Error(), analyticsRepo.state.LastError)

	analyticsRepo.dailyCallback = nil
	svc.lastBackfillAt.Store(0)
	svc.runHistoricalBackfill(context.Background(), cursor.Add(time.Minute))
	require.Len(t, analyticsRepo.hourlyCalls, 2)
	require.Len(t, analyticsRepo.dailyCalls, 2)
	require.Equal(t, oldest, *analyticsRepo.state.BackfillCursor)
	require.Equal(t, "idle", analyticsRepo.state.Phase)
}

// TestDashboardAggregationServiceBudgetExhaustionKeepsBackfill succeeds one
// hour before later work waits for its deadline. It requires backfill phase,
// cleared prior error observations and one hour of saved progress.
func TestDashboardAggregationServiceBudgetExhaustionKeepsBackfill(t *testing.T) {
	cursor := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	oldest := cursor.Add(-4 * time.Hour)
	call := 0
	analyticsRepo := &usageAnalyticsAggregationRepoTestStub{
		state: UsageAnalyticsAggregationState{
			CoverageStart:  timePointer(cursor),
			BackfillCursor: timePointer(cursor),
			SourceOldestAt: timePointer(oldest),
			LastError:      "旧错误",
			LastErrorAt:    timePointer(cursor.Add(-time.Minute)),
		},
		hourlyCallback: func(ctx context.Context, _ time.Time, _ time.Time) error {
			call++
			if call == 1 {
				// Complete one hour with measurable elapsed work before later calls wait
				// for deadline, letting the test inspect saved progress after a budget pause.
				time.Sleep(5 * time.Millisecond)
				return nil
			}
			<-ctx.Done()
			return errors.New("pq: canceling statement due to user request")
		},
	}
	svc := &DashboardAggregationService{
		repo:           &dashboardAggregationRepoTestStub{},
		analyticsRepo:  analyticsRepo,
		backfillBudget: 30 * time.Millisecond,
	}

	svc.runHistoricalBackfill(context.Background(), cursor)

	require.Equal(t, "backfill", analyticsRepo.state.Phase)
	require.Empty(t, analyticsRepo.state.LastError)
	require.Nil(t, analyticsRepo.state.LastErrorAt)
	require.Equal(t, cursor.Add(-time.Hour), *analyticsRepo.state.BackfillCursor)
}

// TestDashboardAggregationServiceFirstChunkTimeoutIsError blocks the first
// hour through its short deadline. It requires error phase, the budget
// diagnostic and an unchanged cursor with a recorded error instant.
func TestDashboardAggregationServiceFirstChunkTimeoutIsError(t *testing.T) {
	cursor := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	oldest := cursor.Add(-2 * time.Hour)
	analyticsRepo := &usageAnalyticsAggregationRepoTestStub{
		state: UsageAnalyticsAggregationState{
			CoverageStart:  timePointer(cursor),
			BackfillCursor: timePointer(cursor),
			SourceOldestAt: timePointer(oldest),
		},
		hourlyCallback: func(ctx context.Context, _ time.Time, _ time.Time) error {
			<-ctx.Done()
			return errors.New("pq: canceling statement due to user request")
		},
	}
	svc := &DashboardAggregationService{
		repo:           &dashboardAggregationRepoTestStub{},
		analyticsRepo:  analyticsRepo,
		backfillBudget: 20 * time.Millisecond,
	}

	svc.runHistoricalBackfill(context.Background(), cursor)

	require.Equal(t, "error", analyticsRepo.state.Phase)
	require.Contains(t, analyticsRepo.state.LastError, "backfill for one hour exceeded the 20ms work budget")
	require.NotNil(t, analyticsRepo.state.LastErrorAt)
	require.Equal(t, cursor, *analyticsRepo.state.BackfillCursor)
}

// TestShouldStartBackfillChunkUsesPreviousDuration exercises first-chunk
// admission and later remaining-time comparisons. It requires strict
// greater-than admission for later chunks and rejection when remaining time
// is exhausted.
func TestShouldStartBackfillChunkUsesPreviousDuration(t *testing.T) {
	require.True(t, shouldStartBackfillChunk(0, time.Millisecond, time.Second))
	require.True(t, shouldStartBackfillChunk(1, 11*time.Millisecond, 10*time.Millisecond))
	require.False(t, shouldStartBackfillChunk(1, 10*time.Millisecond, 10*time.Millisecond))
	require.False(t, shouldStartBackfillChunk(1, 0, time.Millisecond))
}

// TestDashboardAggregationServiceRecomputeUsesCompleteAnalyticsBuckets
// supplies intrahour repair bounds. It requires the analytics recompute
// method to receive those bounds and the generic analytics aggregate method
// to remain unused.
func TestDashboardAggregationServiceRecomputeUsesCompleteAnalyticsBuckets(t *testing.T) {
	analyticsRepo := &usageAnalyticsAggregationRepoTestStub{}
	svc := &DashboardAggregationService{
		repo:          &dashboardAggregationRepoTestStub{},
		analyticsRepo: analyticsRepo,
	}
	start := time.Date(2026, 8, 4, 9, 15, 0, 0, time.UTC)
	end := time.Date(2026, 8, 4, 10, 30, 0, 0, time.UTC)

	require.NoError(t, svc.recomputeRange(context.Background(), start, end))

	require.Empty(t, analyticsRepo.aggregateCalls)
	require.Equal(t, []aggregationRangeCall{{start: start, end: end}}, analyticsRepo.recomputeCalls)
}

// ApplyUsageAnalyticsState merges a field-specific change into the serial
// fixture's current state. Merge errors retain that state; a successful
// returned shallow value becomes the configured result for later reads.
func (s *usageAnalyticsAggregationRepoTestStub) ApplyUsageAnalyticsState(_ context.Context, c AnalyticsStateChange) (*UsageAnalyticsAggregationState, error) {
	v, e := ApplyAnalyticsStateChange(s.state, c)
	if e != nil {
		return nil, e
	}
	s.state = v
	return &v, nil
}

// newRuntimeSettingRepoStub constructs an empty runtime setting map for
// aggregation fixtures.
func newRuntimeSettingRepoStub() *runtimeSettingRepoStub {
	return &runtimeSettingRepoStub{values: map[string]string{}}
}

// GetValue returns stored setting text under the fixture mutex or
// ErrSettingNotFound. This missing-key result lets the runtime settings
// service apply its configured deployment defaults in the tested service.
func (s *runtimeSettingRepoStub) GetValue(_ context.Context, k string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[k]
	if !ok {
		return "", settings.ErrSettingNotFound
	}
	return v, nil
}

// Set stores supplied setting text under the fixture mutex for runtime
// updates.
func (s *runtimeSettingRepoStub) Set(_ context.Context, k, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[k] = v
	return nil
}

// NewPreAggregationSettingsService converts usage Options into the owning
// runtime settings service inputs. Nil options stay nil, while supplied
// deployment settings provide usage enablement, interval and manual-backfill
// limits to the fixture.
func NewPreAggregationSettingsService(repo p.Repository, cfg *Options) *p.PreAggregationSettingsService {
	var options *p.Options
	if cfg != nil {
		options = &p.Options{Usage: p.UsageOptions{Enabled: cfg.DashboardAgg.Enabled, IntervalSeconds: cfg.DashboardAgg.IntervalSeconds, BackfillEnabled: cfg.DashboardAgg.BackfillEnabled, BackfillMaxDays: cfg.DashboardAgg.BackfillMaxDays}}
	}
	return p.NewPreAggregationSettingsService(repo, options)
}
