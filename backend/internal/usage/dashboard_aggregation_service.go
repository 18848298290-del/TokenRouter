package usage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// dashboard_aggregation_service.go schedules usage aggregation, advances
// historical coverage, and repairs buckets after deletions.
// Live aggregation revisits the recent usage window while historical coverage
// advances backward through completed UTC hours. Manual backfill has its own
// target and cursor, and state updates use field-specific changes so a later
// administrator request or live watermark can survive another flow's progress
// write.
//
// Scheduled rounds acquire local admission before consuming their wakeup and
// then acquire the application's singleton lease. Historical chunks have a
// round budget and a maximum hour count; daily rebuilding finishes before
// automatic coverage crosses a completed date. Deletion repair uses the same
// local running flag with a separate runtime task. Shutdown closes task
// admission, cancels runtime contexts and waits for both dispatched work and
// timing-wheel callbacks.

// Aggregation constants bound scheduled rounds, historical chunk work,
// retention frequency and singleton lease lifetime.
const (
	defaultDashboardAggregationTimeout         = 2 * time.Minute  // scheduled and state operation timeout
	defaultDashboardAggregationBackfillTimeout = 30 * time.Minute // legacy backfill and repair timeout
	dashboardAggregationRetentionInterval      = 6 * time.Hour    // successful retention cleanup retry interval
	dashboardAggregationSchedulerTick          = 30 * time.Second // runtime schedule eligibility check interval
	dashboardAggregationBackfillBudget         = 10 * time.Second // historical round work time budget
	dashboardAggregationBackfillMaxHours       = 24               // historical round maximum hour count

	dashboardAggregationLeaderLockKey = "dashboard:aggregation:leader" // distributed aggregation lease identifier
	// The lease outlasts the scheduled timeout so another instance is not admitted
	// while this round still owns its ordinary context-bounded aggregation work.
	dashboardAggregationLeaderLockTTL = 5 * time.Minute // distributed aggregation lease lifetime
)

// Aggregation errors distinguish disabled or oversized manual requests from a
// locally occupied execution slot.
var (
	// ErrDashboardBackfillDisabled reports that deployment configuration rejects
	// manual historical backfill.
	ErrDashboardBackfillDisabled = errors.New("dashboard aggregation backfill is disabled")
	// ErrDashboardBackfillTooLarge reports that the requested elapsed range
	// exceeds its configured maximum.
	ErrDashboardBackfillTooLarge = errors.New("backfill time range is too large")
	// errDashboardAggregationRunning reports an occupied local aggregation slot
	// to repair retry handling.
	errDashboardAggregationRunning = errors.New("aggregation job is already running")
)

// DashboardAggregationRepository supplies dashboard rollup writes, progress
// reads and retention operations.
type DashboardAggregationRepository interface {
	// AggregateRange refreshes dashboard rollups affected by the supplied range
	// using ctx. Scheduled and historical work call it before their
	// corresponding multidimensional analytics writes, so callers treat errors
	// as incomplete round work.
	AggregateRange(ctx context.Context, start, end time.Time) error
	// RecomputeRange repairs dashboard rollups and derived active-user data
	// affected by the supplied range. Cleanup repair calls it after original
	// usage deletion; its implementation determines affected buckets before
	// rebuilding from retained rows.
	RecomputeRange(ctx context.Context, start, end time.Time) error
	// GetAggregationWatermark returns dashboard progress used to choose the next
	// live lookback. The scheduler treats unavailable or epoch progress as a
	// recent-window start, leaving older retained data to budgeted historical
	// coverage work.
	GetAggregationWatermark(ctx context.Context) (time.Time, error)
	// UpdateAggregationWatermark records the live or legacy-backfill end after
	// successful aggregation. The service logs a failed watermark write and
	// keeps the completed data work available for a later overlapping round.
	UpdateAggregationWatermark(ctx context.Context, aggregatedAt time.Time) error
	// CleanupAggregates removes expired dashboard hourly and daily data under
	// their separate cutoff instants. The retention round records its
	// last-success timestamp only after this and the other selected cleanup
	// calls succeed.
	CleanupAggregates(ctx context.Context, hourlyCutoff, dailyCutoff time.Time) error
	// CleanupUsageLogs removes original usage data older than the supplied
	// cutoff under ctx. The PostgreSQL implementation drops expired partitions
	// where available and otherwise commits bounded deletion batches until no
	// full batch remains.
	CleanupUsageLogs(ctx context.Context, cutoff time.Time) error
	// CleanupUsageBillingDedup archives expired billing deduplication records
	// through the application-bound billing operation. The aggregation service
	// passes its retention cutoff and includes failures in the retention round's
	// success decision.
	CleanupUsageBillingDedup(ctx context.Context, cutoff time.Time) error
	// EnsureUsageLogsPartitions prepares usage partitions around the supplied
	// instant when storage is partitioned. The service logs preparation errors
	// and still attempts the range write, which determines whether aggregation
	// can proceed.
	EnsureUsageLogsPartitions(ctx context.Context, now time.Time) error
}

// DashboardAggregationService coordinates live rollups, bounded historical
// coverage and asynchronous deletion repair.
type DashboardAggregationService struct {
	reporter func(string, string, ...any) // optional aggregation diagnostic reporter
	// runtimeMu protects runtimeStarted, runtimeStopped and background work
	// registration. Start and StopContext change admission under this lock
	// before dispatch or wait operations can observe those states.
	runtimeMu      sync.Mutex     // runtime task admission lock
	runtimeStarted bool           // runtime task admission start flag
	runtimeStopped bool           // permanent runtime shutdown flag
	runtimeWG      sync.WaitGroup // accepted background task completion count
	// runCtx and runCancel belong to the aggregation runtime. Scheduled,
	// historical and repair contexts derive from runCtx so shutdown can cancel
	// their context-aware work and retry waits.
	runCtx    context.Context    // aggregation runtime cancellation context
	runCancel context.CancelFunc // aggregation runtime cancellation function
	stopOnce  sync.Once          // shutdown sequence invocation guard
	// stopDone closes after timing-wheel callbacks and registered runtime tasks
	// return. StopContext callers wait on this shared completion signal or their
	// own context deadline.
	stopDone chan struct{}                  // shutdown completion publication channel
	repo     DashboardAggregationRepository // dashboard rollup and retention store
	// analyticsRepo is discovered from repo when SetPreAggregationSettings
	// binds, including a nil settings reader. Absent analytics capability leaves
	// the service's range work on its dashboard repository.
	analyticsRepo UsageAnalyticsAggregationRepository // optional multidimensional aggregation store
	timingWheel   TimingWheel                         // recurring aggregation callback scheduler
	// cfg and settings supply deployment and runtime schedule choices. cfg
	// remains the enabled ceiling for scheduled work; a bound settings reader
	// can disable usage or replace its interval.
	cfg      DashboardAggregationConfig // deployment aggregation configuration
	settings AggregationSettings        // optional runtime aggregation settings
	// running admits scheduled, legacy backfill and repair work through
	// compare-and-swap. Historical chunks execute inside the admitted scheduled
	// round rather than claiming another local slot.
	running int32 // local aggregation execution flag
	// lastScheduledAt records admitted schedule time in Unix nanoseconds.
	// TriggerNow clears it, and the running flag is acquired before its due
	// check so an active job cannot consume that wakeup.
	lastScheduledAt atomic.Int64 // scheduled round admission timestamp
	// lastBackfillAt gates reverse-history rounds to one attempt per minute.
	// A successfully persisted manual request clears this timestamp before
	// requesting another round.
	lastBackfillAt       atomic.Int64 // historical round admission Unix nanoseconds
	lastRetentionCleanup atomic.Value // last successful retention cleanup instant
	// locker is bound before concurrent scheduled work. Nil uses local admission
	// alone; successful configured acquisition returns a release callback held
	// through that scheduled round.
	locker     SingletonLocker // optional distributed singleton lease function
	instanceID string          // singleton lease owner identifier
	// backfillBudget replaces the normal historical work budget when positive
	// for tests. Zero and negative overrides keep the ten-second production
	// round allowance.
	backfillBudget time.Duration // optional historical budget override
}

// NewDashboardAggregationService constructs an idle aggregation runtime with
// its own cancellable context. Bind repositories, settings and the singleton
// locker before Start; StopContext closes admission and waits within the
// caller's shutdown budget.
func NewDashboardAggregationService(repo DashboardAggregationRepository, timingWheel TimingWheel, cfg *Options) *DashboardAggregationService {
	var aggCfg DashboardAggregationConfig
	if cfg != nil {
		aggCfg = cfg.DashboardAgg
	}
	runCtx, runCancel := context.WithCancel(context.Background())
	return &DashboardAggregationService{
		reporter: usageReporter(cfg), runCtx: runCtx, runCancel: runCancel, stopDone: make(chan struct{}),
		repo:        repo,
		timingWheel: timingWheel,
		cfg:         aggCfg,
		instanceID:  uuid.NewString(),
	}
}

// SetPreAggregationSettings binds runtime settings and discovers analytics
// capabilities from repo before concurrent use. Enabling usage or changing
// its interval clears scheduled admission time and requests background work
// when the runtime accepts dispatch. Nil services return immediately; nil
// settings still permits discovery of the repository's analytics capability.
func (s *DashboardAggregationService) SetPreAggregationSettings(settings AggregationSettings) {
	if s == nil {
		return
	}
	s.settings = settings
	if analyticsRepo, ok := s.repo.(UsageAnalyticsAggregationRepository); ok {
		s.analyticsRepo = analyticsRepo
	}
	if settings != nil {
		settings.RegisterListener(func(previous, next PreAggregationSettings) {
			if (!previous.Usage.Enabled && next.Usage.Enabled) || previous.Usage.IntervalSeconds != next.Usage.IntervalSeconds {
				// Reset due-time admission before dispatch so the next accepted scheduled
				// round can attempt current settings immediately rather than wait for its
				// previous interval.
				s.lastScheduledAt.Store(0)
				s.runBackground(s.runScheduledAggregation)
			}
		})
	}
}

// SetSingletonLocker binds the scheduled-round lease function before
// concurrent aggregation use. Nil selects local-only admission; successful
// acquisition supplies a release callback whose lifetime covers the admitted
// scheduled round.
func (s *DashboardAggregationService) SetSingletonLocker(locker SingletonLocker) { s.locker = locker }

// Start opens runtime task admission once and registers scheduled work when
// deployment and dependencies permit it. Bind dependencies on a nonnil
// receiver and complete Start before beginning StopContext; recurring callback
// registration happens after runtimeMu is released.
//
// Admission opens under runtimeMu even when missing scheduling dependencies or
// disabled deployment prevent recurring work, allowing internal repair to be
// dispatched afterward. Background registration uses that same admission lock
// so StopContext can account for accepted tasks. A stopped runtime stays
// stopped, and repeated Start calls leave its existing admission state in place.
func (s *DashboardAggregationService) Start() {
	s.runtimeMu.Lock()
	if s.runtimeStarted || s.runtimeStopped {
		s.runtimeMu.Unlock()
		return
	}
	// Open background admission under runtimeMu before startup dispatch can occur.
	// StopContext takes the same lock to close admission before waiting for accepted tasks.
	s.runtimeStarted = true
	s.runtimeMu.Unlock()

	if s == nil || s.repo == nil || s.timingWheel == nil {
		return
	}
	if !s.cfg.Enabled {
		s.logMessage("service.dashboard_aggregation", "[DashboardAggregation] Aggregation job is disabled")
		return
	}

	if s.cfg.RecomputeDays > 0 && s.analyticsRepo == nil {
		s.runBackground(s.recomputeRecentDays)
	}

	s.timingWheel.ScheduleRecurring("dashboard:aggregation", dashboardAggregationSchedulerTick, func() {
		s.runScheduledAggregation()
	})
	s.runBackground(s.runScheduledAggregation)
	s.logMessage("service.dashboard_aggregation", "[DashboardAggregation] Aggregation job started (scheduler_tick=%v, lookback=%ds)", dashboardAggregationSchedulerTick, s.cfg.LookbackSeconds)
	if !s.cfg.BackfillEnabled {
		s.logMessage("service.dashboard_aggregation", "[DashboardAggregation] Automatic backfill is disabled; manually backfill historical data outside the retention window")
	}
}

// TriggerBackfill persists a manual target and cursor truncated to UTC hours,
// then requests immediate work. Deployment backfill settings, increasing bounds
// and the positive elapsed-range limit are checked first; the analytics
// capability must already be bound.
//
// The field-specific request update keeps live progress under its separate
// ownership. Successful persistence resets historical and scheduled admission
// times so an admitted round can read a durable target. Dispatch can be ignored
// before Start or after shutdown while the request remains stored. Inputs
// within one UTC hour can persist equal target and cursor values.
func (s *DashboardAggregationService) TriggerBackfill(start, end time.Time) error {
	if s == nil || s.repo == nil {
		return errors.New("aggregation service is not initialized")
	}
	if !s.cfg.BackfillEnabled {
		s.logMessage("service.dashboard_aggregation", "[DashboardAggregation] Backfill rejected: backfill_enabled=false")
		return ErrDashboardBackfillDisabled
	}
	if !end.After(start) {
		return errors.New("invalid backfill time range")
	}
	if s.cfg.BackfillMaxDays > 0 {
		// The manual range cap measures elapsed days, including DST duration differences.
		maxRange := time.Duration(s.cfg.BackfillMaxDays) * 24 * time.Hour
		if end.Sub(start) > maxRange {
			return ErrDashboardBackfillTooLarge
		}
	}
	if s.analyticsRepo == nil {
		return errors.New("analytics aggregation service is not initialized")
	}

	ctx, cancel := context.WithTimeout(s.operationContext(), defaultDashboardAggregationTimeout)
	defer cancel()
	state, err := s.readAnalyticsState(ctx)
	if err != nil {
		return err
	}
	// Hour-aligned requests match the buckets processed by manual backfill.
	requestedStart := start.UTC().Truncate(time.Hour)
	// Use an exclusive UTC-hour cursor, omitting any fractional end hour.
	requestedCursor := end.UTC().Truncate(time.Hour)
	// Persist hour-aligned target and cursor together before signaling work. The
	// field-specific request update keeps concurrent live progress owned by its separate flow.
	state.ManualBackfillStart = timePointer(requestedStart)
	state.ManualBackfillCursor = timePointer(requestedCursor)
	state.Phase = "backfill"
	if err := s.updateAnalyticsState(ctx, state, AnalyticsManualRequest); err != nil {
		return err
	}
	// Persist the target before resetting historical and scheduled admission times.
	// The next accepted round can then read this request under its ordinary lease and budget.
	s.lastBackfillAt.Store(0)
	s.TriggerNow()
	return nil
}

// TriggerRecomputeRange requests deletion repair for a strictly increasing
// range. Calls before Start or after shutdown can return nil while leaving the
// repair unstarted; nil acknowledges validation and the dispatch request.
//
// Admitted work derives each attempt's thirty-minute timeout from the runtime
// context. An occupied local slot permits up to three attempts, with a
// five-second wait after each occupied-slot result. Other failures are logged
// and end that task. Runtime cancellation interrupts retry waits and
// context-aware storage, while recomputeRange controls local write admission.
func (s *DashboardAggregationService) TriggerRecomputeRange(start, end time.Time) error {
	if s == nil || s.repo == nil {
		return errors.New("aggregation service is not initialized")
	}
	if !end.After(start) {
		return errors.New("invalid recalculation time range")
	}

	s.runBackground(func() {
		// Repair can collide with the local scheduled slot. Bound retries so an
		// occupied runtime does not retain this deletion-repair task indefinitely.
		const maxRetries = 3
		for range maxRetries {
			ctx, cancel := context.WithTimeout(s.operationContext(), defaultDashboardAggregationBackfillTimeout)
			err := s.recomputeRange(ctx, start, end)
			cancel()
			if err == nil {
				return
			}
			if !errors.Is(err, errDashboardAggregationRunning) {
				s.logMessage("service.dashboard_aggregation", "[INFO] [DashboardAggregation] Recalculation failed: %v", err)
				return
			}
			// Wait between occupied-slot retries under the runtime cancellation signal
			// so shutdown can release this registered repair task before another attempt starts.
			select {
			case <-s.operationContext().Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
		s.logMessage("service.dashboard_aggregation", "[DashboardAggregation] Recalculation abandoned: aggregation job remains busy")
	})
	return nil
}

// recomputeRecentDays requests legacy startup aggregation for the configured
// positive recent-day count. It uses a runtime-derived thirty-minute context
// and logs failures; disabled or nonpositive startup ranges return
// immediately.
func (s *DashboardAggregationService) recomputeRecentDays() {
	days := s.cfg.RecomputeDays
	if days <= 0 {
		return
	}
	now := time.Now().UTC()
	start := now.AddDate(0, 0, -days)

	ctx, cancel := context.WithTimeout(s.operationContext(), defaultDashboardAggregationBackfillTimeout)
	defer cancel()
	// Startup backfill reports range or admission failure and leaves later
	// scheduled rounds available to maintain live data under their own range.
	if err := s.backfillRange(ctx, start, now); err != nil {
		s.logMessage("service.dashboard_aggregation", "[INFO] [DashboardAggregation] Failed to start recalculation: %v", err)
		return
	}
}

// recomputeRange repairs dashboard and optional multidimensional rollups
// under one local admission slot. A competing job returns
// errDashboardAggregationRunning, and every admitted path releases the flag
// when it ends.
//
// Dashboard repair executes before multidimensional repair. The second error
// can therefore follow completed dashboard work; the service returns that
// error for the caller's retry or reporting decision. This path leaves
// aggregation watermarks unchanged and relies on the caller's context for
// stopping the repository operations.
func (s *DashboardAggregationService) recomputeRange(ctx context.Context, start, end time.Time) error {
	// Admit one local range-writing sequence and release its flag on every exit.
	// Competing scheduled or repair tasks leave this active sequence in control of progress.
	if !atomic.CompareAndSwapInt32(&s.running, 0, 1) {
		return errDashboardAggregationRunning
	}
	defer atomic.StoreInt32(&s.running, 0)

	jobStart := time.Now().UTC()
	// Dashboard repair finishes before analytics repair begins. A later
	// analytics error returns after that earlier work, so the two stages can
	// have different completion outcomes.
	if err := s.repo.RecomputeRange(ctx, start, end); err != nil {
		return err
	}
	if s.analyticsRepo != nil {
		if err := s.analyticsRepo.RecomputeUsageAnalyticsRange(ctx, start, end); err != nil {
			return err
		}
	}
	s.logMessage("service.dashboard_aggregation", "[DashboardAggregation] Recalculation completed (start=%s end=%s duration=%s)",
		start.UTC().Format(time.RFC3339),
		end.UTC().Format(time.RFC3339),
		time.Since(jobStart).String(),
	)
	return nil
}

// runScheduledAggregation performs an eligible live round and its bounded
// historical and retention work. It acquires the local running flag before
// consuming schedule time, then requires the configured singleton lease
// before writing rollups.
//
// An unavailable or unusable watermark selects the recent lookback; older
// coverage is handled by reverse backfill. Live range writes precede
// dashboard watermark publication and field-specific analytics success
// updates. Historical and retention work follow that result. Contexts derive
// from the runtime, and deferred releases end both local admission and the
// acquired lease on every return.
func (s *DashboardAggregationService) runScheduledAggregation() {
	if !s.usageEnabled(s.operationContext()) {
		return
	}
	// Admit one local range-writing sequence and release its flag on every exit.
	// Competing scheduled or repair tasks leave this active sequence in control of progress.
	if !atomic.CompareAndSwapInt32(&s.running, 0, 1) {
		return
	}
	defer atomic.StoreInt32(&s.running, 0)
	// Acquire execution before consuming the wakeup. TriggerNow can reset the
	// schedule while another job is active, and that new request must remain available.
	if !s.scheduledRunDue(time.Now()) {
		return
	}

	jobStart := time.Now().UTC()
	ctx, cancel := context.WithTimeout(s.operationContext(), defaultDashboardAggregationTimeout)
	defer cancel()

	// Acquire the singleton lease before rollup and watermark writes so scheduled
	// work from another instance cannot publish competing progress during this admitted round.
	release, ok := s.acquireSingleton(ctx)
	if !ok {
		return
	}
	defer release()

	now := time.Now().UTC()
	s.markAnalyticsRunStarted(ctx, now)
	last, err := s.repo.GetAggregationWatermark(ctx)
	if err != nil {
		s.logMessage("service.dashboard_aggregation", "[INFO] [DashboardAggregation] Failed to read watermark: %v", err)
		// An unreadable watermark leaves its range unknown. Use the epoch marker so
		// this round starts from recent lookback and budgeted backfill handles older history.
		last = time.Unix(0, 0).UTC()
	}

	lookback := time.Duration(s.cfg.LookbackSeconds) * time.Second
	epoch := time.Unix(0, 0).UTC()
	start := last.Add(-lookback)
	if !last.After(epoch) {
		// Uninitialized progress starts with recent live data. Older retained hours
		// advance through bounded reverse backfill, keeping the first live round limited.
		start = now.Add(-lookback)
	} else if start.After(now) {
		// A future stored watermark cannot supply a usable start. Restore recent
		// lookback so this round can refresh current data before historical work.
		start = now.Add(-lookback)
	}
	if err := s.aggregateLiveRange(ctx, start, now); err != nil {
		s.logMessage("service.dashboard_aggregation", "[INFO] [DashboardAggregation] Aggregation failed: %v", err)
		s.markAnalyticsRunFailed(ctx, now, jobStart, err)
		return
	}

	// Publish the watermark after live range writes. A failed progress write keeps
	// overlapping work eligible on the next round while completed rollup data remains stored.
	updateErr := s.repo.UpdateAggregationWatermark(ctx, now)
	if updateErr != nil {
		s.logMessage("service.dashboard_aggregation", "[INFO] [DashboardAggregation] Failed to update watermark: %v", updateErr)
	}
	slog.Debug("[DashboardAggregation] Aggregation completed",
		"start", start.Format(time.RFC3339),
		"end", now.Format(time.RFC3339),
		"duration", time.Since(jobStart).String(),
		"watermark_updated", updateErr == nil,
	)
	s.markAnalyticsLiveSuccess(ctx, now, start, jobStart)
	s.runHistoricalBackfill(ctx, now)

	s.maybeCleanupRetention(ctx, now)
}

// backfillRange aggregates a legacy range through UTC-day-sized windows under
// local admission. It rejects nonincreasing bounds and expands the requested
// start back to its UTC midnight before selecting the first window.
//
// Each window finishes before the cursor advances to the next, leaving
// earlier work completed if a later query fails. The end watermark is
// attempted after all windows succeed; its failure is logged rather than
// returned. Retention runs afterward using the requested end, and
// context-aware repository operations determine cancellation of this
// sequence.
func (s *DashboardAggregationService) backfillRange(ctx context.Context, start, end time.Time) error {
	// Admit one local range-writing sequence and release its flag on every exit.
	// Competing scheduled or repair tasks leave this active sequence in control of progress.
	if !atomic.CompareAndSwapInt32(&s.running, 0, 1) {
		return errDashboardAggregationRunning
	}
	defer atomic.StoreInt32(&s.running, 0)

	jobStart := time.Now().UTC()
	startUTC := start.UTC()
	endUTC := end.UTC()
	if !endUTC.After(startUTC) {
		return errors.New("invalid backfill time range")
	}

	// UTC-day windows begin at midnight, expanding an intraday requested start.
	cursor := truncateToDayUTC(startUTC)
	for cursor.Before(endUTC) {
		windowEnd := cursor.Add(24 * time.Hour)
		if windowEnd.After(endUTC) {
			windowEnd = endUTC
		}
		if err := s.aggregateRange(ctx, cursor, windowEnd); err != nil {
			return err
		}
		cursor = windowEnd
	}

	// Advance the legacy end watermark after every selected window has succeeded.
	// Failed publication is logged while a later round can repeat those completed windows.
	updateErr := s.repo.UpdateAggregationWatermark(ctx, endUTC)
	if updateErr != nil {
		s.logMessage("service.dashboard_aggregation", "[INFO] [DashboardAggregation] Failed to update watermark: %v", updateErr)
	}
	s.logMessage("service.dashboard_aggregation", "[DashboardAggregation] Backfill aggregation completed (start=%s end=%s duration=%s watermark_updated=%t)",
		startUTC.Format(time.RFC3339),
		endUTC.Format(time.RFC3339),
		time.Since(jobStart).String(),
		updateErr == nil,
	)

	s.maybeCleanupRetention(ctx, endUTC)
	return nil
}

// aggregateRange writes dashboard and multidimensional rollups for an
// increasing range. Empty or reversed bounds return successfully, and
// partition preparation is attempted before either range write.
//
// Partition errors are logged while the dashboard write still determines
// whether work can continue. A successful dashboard write precedes the
// analytics range write when that capability is bound. Errors return from the
// failed stage, so callers retain their prior cursor and can repeat the range
// to finish its remaining work.
func (s *DashboardAggregationService) aggregateRange(ctx context.Context, start, end time.Time) error {
	if !end.After(start) {
		return nil
	}
	// Partition preparation can fail while existing storage still accepts this
	// range. Log the preparation error and let the subsequent aggregation write decide progress.
	if err := s.repo.EnsureUsageLogsPartitions(ctx, end); err != nil {
		s.logMessage("service.dashboard_aggregation", "[INFO] [DashboardAggregation] Partition check failed: %v", err)
	}
	// Complete dashboard writes before the analytics stage. An analytics error can
	// leave this earlier work completed, so the caller advances its cursor only on full success.
	if err := s.repo.AggregateRange(ctx, start, end); err != nil {
		return err
	}
	if s.analyticsRepo != nil {
		return s.analyticsRepo.AggregateUsageAnalyticsRange(ctx, start, end)
	}
	return nil
}

// aggregateHourlyRange writes dashboard rollups and analytics hourly data for
// an increasing range. Empty or reversed bounds return successfully, and
// partition preparation is attempted before either write.
//
// Partition errors are logged while dashboard aggregation still runs. The
// analytics hourly write follows successful dashboard work; daily rebuilding
// is a separate caller-owned step. Returning an hourly-stage error keeps
// caller progress unadvanced, allowing later work to repeat the same range
// before marking coverage complete.
func (s *DashboardAggregationService) aggregateHourlyRange(ctx context.Context, start, end time.Time) error {
	if !end.After(start) {
		return nil
	}
	// Partition preparation can fail while existing storage still accepts this
	// range. Log the preparation error and let the subsequent aggregation write decide progress.
	if err := s.repo.EnsureUsageLogsPartitions(ctx, end); err != nil {
		s.logMessage("service.dashboard_aggregation", "[INFO] [DashboardAggregation] Partition check failed: %v", err)
	}
	// Complete dashboard writes before the analytics stage. An analytics error can
	// leave this earlier work completed, so the caller advances its cursor only on full success.
	if err := s.repo.AggregateRange(ctx, start, end); err != nil {
		return err
	}
	if s.analyticsRepo != nil {
		return s.analyticsRepo.AggregateUsageAnalyticsHourlyRange(ctx, start, end)
	}
	return nil
}

// aggregateLiveRange refreshes hourly data before rebuilding touched closed
// UTC dates. An hourly error ends the operation, and a missing analytics
// capability leaves dashboard work as its complete result.
//
// The start's UTC midnight and end's UTC midnight bound the daily rebuild.
// This refreshes yesterday repeatedly while the live lookback still crosses
// midnight, absorbing late rows into its closed-day bucket. When the lookback
// lies within the current UTC day, no closed date is rebuilt by this path.
func (s *DashboardAggregationService) aggregateLiveRange(ctx context.Context, start, end time.Time) error {
	if err := s.aggregateHourlyRange(ctx, start, end); err != nil || s.analyticsRepo == nil {
		return err
	}
	// Late records can still enter yesterday while live lookback crosses midnight.
	// Rebuild touched closed dates after hourly writes so daily reports receive those updates.
	dayStart := truncateToDayUTC(start)
	closedDayEnd := truncateToDayUTC(end)
	if !closedDayEnd.After(dayStart) {
		return nil
	}
	return s.analyticsRepo.RebuildUsageAnalyticsDailyRange(ctx, dayStart, closedDayEnd)
}

// aggregateAutomaticBackfillHour writes an automatic historical chunk and
// rebuilds a day when its boundary becomes complete. Hourly errors return
// before daily work, and missing analytics capabilities need no daily
// rebuild.
//
// Reaching UTC midnight marks a fully processed date; reaching oldestHour can
// finish a retained partial date. In either case, daily rebuilding finishes
// before this helper succeeds, allowing the caller to publish its cursor
// afterward. A daily error therefore keeps that hour eligible for a later
// retry.
func (s *DashboardAggregationService) aggregateAutomaticBackfillHour(ctx context.Context, start, end, oldestHour time.Time) error {
	if err := s.aggregateHourlyRange(ctx, start, end); err != nil || s.analyticsRepo == nil {
		return err
	}
	// Compare completed chunk boundaries at the stored UTC-hour bucket
	// precision.
	chunkStart := start.UTC().Truncate(time.Hour)
	if chunkStart.Hour() != 0 && !chunkStart.Equal(oldestHour.UTC().Truncate(time.Hour)) {
		return nil
	}
	dayStart := truncateToDayUTC(chunkStart)
	return s.analyticsRepo.RebuildUsageAnalyticsDailyRange(ctx, dayStart, dayStart.AddDate(0, 0, 1))
}

// maybeCleanupRetention attempts configured rollup, original-row and
// billing-dedup cleanup using cutoffs derived from now. A successful prior
// cleanup suppresses another attempt within six hours measured against the
// supplied instant.
//
// Each selected operation still runs after an earlier failure, so one call can
// finish some cleanup targets while leaving others unfinished. The shared
// lastRetentionCleanup timestamp advances only when all selected operations
// succeed. Failures are logged and retain the previous timestamp, allowing a
// later admitted round to retry the incomplete retention pass.
func (s *DashboardAggregationService) maybeCleanupRetention(ctx context.Context, now time.Time) {
	// Throttle from the last fully successful cleanup, allowing failed retention
	// operations to be attempted again on a later round instead of consuming the interval.
	lastAny := s.lastRetentionCleanup.Load()
	if lastAny != nil {
		if last, ok := lastAny.(time.Time); ok && now.Sub(last) < dashboardAggregationRetentionInterval {
			return
		}
	}

	hourlyCutoff := now.AddDate(0, 0, -s.cfg.Retention.HourlyDays)
	dailyCutoff := now.AddDate(0, 0, -s.cfg.Retention.DailyDays)
	usageCutoff := now.AddDate(0, 0, -s.cfg.Retention.UsageLogsDays)
	dedupCutoff := now.AddDate(0, 0, -s.cfg.Retention.UsageBillingDedupDays)

	// Run CleanupAggregates, optional CleanupUsageAnalytics, CleanupUsageLogs
	// and CleanupUsageBillingDedup even after an earlier failure. Their separate
	// outcomes jointly decide whether the shared success timestamp can advance.
	aggErr := s.repo.CleanupAggregates(ctx, hourlyCutoff, dailyCutoff)
	if aggErr != nil {
		s.logMessage("service.dashboard_aggregation", "[INFO] [DashboardAggregation] Aggregation retention cleanup failed: %v", aggErr)
	}
	if s.analyticsRepo != nil {
		if analyticsErr := s.analyticsRepo.CleanupUsageAnalytics(ctx, hourlyCutoff, dailyCutoff); analyticsErr != nil {
			s.logMessage("service.dashboard_aggregation", "[INFO] [DashboardAggregation] Analytics aggregation retention cleanup failed: %v", analyticsErr)
			aggErr = analyticsErr
		}
	}
	usageErr := s.repo.CleanupUsageLogs(ctx, usageCutoff)
	if usageErr != nil {
		s.logMessage("service.dashboard_aggregation", "[INFO] [DashboardAggregation] usage_logs retention cleanup failed: %v", usageErr)
	}
	dedupErr := s.repo.CleanupUsageBillingDedup(ctx, dedupCutoff)
	if dedupErr != nil {
		s.logMessage("service.dashboard_aggregation", "[INFO] [DashboardAggregation] usage_billing_dedup retention cleanup failed: %v", dedupErr)
	}
	// Publish the shared retention timestamp only after every selected cleanup
	// succeeds. Any failed operation leaves the next admitted round eligible to retry cleanup.
	if aggErr == nil && usageErr == nil && dedupErr == nil {
		s.lastRetentionCleanup.Store(now)
	}
}

// TriggerNow clears scheduled admission time and requests a background
// aggregation attempt. Nil services return immediately; dispatch is accepted
// only by a started runtime that has not begun shutdown.
func (s *DashboardAggregationService) TriggerNow() {
	if s == nil {
		return
	}
	// Reset due-time admission before dispatch so the next accepted scheduled
	// round can attempt current settings immediately rather than wait for its
	// previous interval.
	s.lastScheduledAt.Store(0)
	s.runBackground(s.runScheduledAggregation)
}

// RuntimeStatus returns analytics progress for the settings page, or
// unavailable when that capability is absent. Read failures report error,
// runtime disablement overrides the phase, and lag is clamped to nonnegative
// whole seconds. Returned progress pointers retain the state reader's storage,
// so callers treat those pointed-to timestamps as immutable.
func (s *DashboardAggregationService) RuntimeStatus(ctx context.Context) PreAggregationRuntimeStatus {
	status := PreAggregationRuntimeStatus{Phase: "unavailable"}
	if s == nil || s.analyticsRepo == nil {
		return status
	}
	state, err := s.readAnalyticsState(ctx)
	if err != nil {
		status.Phase = "error"
		status.LastError = err.Error()
		return status
	}
	status.Phase = state.Phase
	if !s.usageEnabled(ctx) {
		status.Phase = "disabled"
	}
	if !state.LiveWatermark.IsZero() && state.LiveWatermark.After(time.Unix(0, 0)) {
		watermark := state.LiveWatermark.UTC()
		status.LiveWatermark = &watermark
		// Future watermark values display zero lag; fractional seconds are discarded.
		status.LagSeconds = maxInt64(0, int64(time.Since(watermark).Seconds()))
	}
	status.CoverageStart = state.CoverageStart
	status.SourceOldestAt = state.SourceOldestAt
	status.LastRunAt = state.LastRunAt
	status.LastSuccessAt = state.LastSuccessAt
	status.LastErrorAt = state.LastErrorAt
	status.LastError = state.LastError
	status.LastDurationMS = state.LastDurationMS
	return status
}

// usageEnabled reports whether deployment and runtime settings permit
// scheduled usage work. Nil services or disabled deployment return false;
// eligible services without a settings reader use the deployment enablement
// alone.
func (s *DashboardAggregationService) usageEnabled(ctx context.Context) bool {
	if s == nil || !s.cfg.Enabled {
		return false
	}
	if s.settings == nil {
		return true
	}
	return s.settings.UsageEnabled(ctx)
}

// scheduledRunDue claims a due schedule timestamp using the runtime interval
// when one is bound. Nonpositive intervals select one minute, and a recent
// positive timestamp rejects another scheduled attempt.
//
// The atomic compare-and-swap publishes the new timestamp only if the value
// read for eligibility remains current. Callers acquire the running slot
// before this helper so an occupied round cannot consume a TriggerNow reset.
// The helper therefore coordinates timing admission separately from the
// runtime's task registration and distributed lease acquisition.
func (s *DashboardAggregationService) scheduledRunDue(now time.Time) bool {
	interval := time.Duration(s.cfg.IntervalSeconds) * time.Second
	if s.settings != nil {
		interval = time.Duration(s.settings.Resolve(s.operationContext()).Usage.IntervalSeconds) * time.Second
	}
	if interval <= 0 {
		interval = time.Minute
	}
	last := s.lastScheduledAt.Load()
	if last > 0 && now.Sub(time.Unix(0, last)) < interval {
		return false
	}
	// Eligibility used the observed timestamp. Publish only while it still matches
	// so a concurrent reset or schedule claim can make this attempted admission lose.
	return s.lastScheduledAt.CompareAndSwap(last, now.UnixNano())
}

// markAnalyticsRunStarted attempts a field-specific live-start update with
// the observed progress row. Missing analytics capabilities, failed reads and
// failed writes leave the aggregation round running with its data work as the
// primary result.
func (s *DashboardAggregationService) markAnalyticsRunStarted(ctx context.Context, now time.Time) {
	if s.analyticsRepo == nil {
		return
	}
	state, err := s.readAnalyticsState(ctx)
	// A failed state read leaves phase and last-run time as previously stored.
	// The admitted live round can still attempt its rollup writes afterward.
	if err != nil {
		return
	}
	state.Phase = "live"
	state.LastRunAt = timePointer(now)
	// This progress annotation is best effort. Leave the caller's data-work outcome
	// as the primary result if the state update cannot be persisted in this context.
	_ = s.updateAnalyticsState(ctx, state, AnalyticsRunStarted)
}

// markAnalyticsLiveSuccess attempts a live watermark and outcome update after
// live writes. Initial coverage and automatic cursor use the lookback's UTC
// hour, while field-specific persistence retains other flows' current manual
// request.
func (s *DashboardAggregationService) markAnalyticsLiveSuccess(ctx context.Context, now, start, jobStart time.Time) {
	if s.analyticsRepo == nil {
		return
	}
	state, err := s.readAnalyticsState(ctx)
	// A state-read failure can leave completed live SQL unreflected in analytics
	// status. Keep the data-work outcome and let a later round refresh this
	// progress annotation.
	if err != nil {
		return
	}
	state.LiveWatermark = now
	if state.CoverageStart == nil {
		// Initial coverage uses the enclosing UTC hour for later aggregate routing.
		coverage := start.UTC().Truncate(time.Hour)
		state.CoverageStart = &coverage
		state.BackfillCursor = &coverage
	}
	state.Phase = "idle"
	state.LastSuccessAt = timePointer(now)
	state.LastError = ""
	state.LastErrorAt = nil
	state.LastDurationMS = time.Since(jobStart).Milliseconds()
	// This progress annotation is best effort. Leave the caller's data-work outcome
	// as the primary result if the state update cannot be persisted in this context.
	_ = s.updateAnalyticsState(ctx, state, AnalyticsLiveSuccess)
}

// markAnalyticsRunFailed attempts a field-specific failed outcome with
// diagnostic text and elapsed duration. Missing analytics capabilities and
// state-read failures return immediately; update failure is ignored by this
// reporting path. Callers supply a nonnil runErr for its recorded diagnostic.
func (s *DashboardAggregationService) markAnalyticsRunFailed(ctx context.Context, now, jobStart time.Time, runErr error) {
	if s.analyticsRepo == nil {
		return
	}
	state, err := s.readAnalyticsState(ctx)
	// A failed state read leaves this error absent from persisted runtime
	// status. Return from the outcome annotation while its caller retains the
	// separate data-work failure decision.
	if err != nil {
		return
	}
	state.Phase = "error"
	state.LastError = runErr.Error()
	state.LastErrorAt = timePointer(now)
	state.LastDurationMS = time.Since(jobStart).Milliseconds()
	// This progress annotation is best effort. Leave the caller's data-work outcome
	// as the primary result if the state update cannot be persisted in this context.
	_ = s.updateAnalyticsState(ctx, state, AnalyticsRunFailed)
}

// runHistoricalBackfill advances automatic retained-history coverage unless a
// complete manual request takes precedence. It admits at most one historical
// round per minute and processes at most twenty-four reverse UTC-hour chunks
// within its work budget.
//
// An automatic chunk rebuilds any newly completed date before cursor
// publication. A deadline after completed chunks requests a pause with saved
// progress; a first-chunk deadline reports failure. Successful chunks publish
// field-specific coverage and cursor changes, and a persistence error ends
// the round before another hour begins.
func (s *DashboardAggregationService) runHistoricalBackfill(ctx context.Context, now time.Time) {
	if s.analyticsRepo == nil {
		return
	}
	// Bound historical rounds independently of live scheduling. Claim this minute's
	// attempt atomically so another invocation cannot advance the same saved cursor concurrently.
	last := s.lastBackfillAt.Load()
	if last > 0 && now.Sub(time.Unix(0, last)) < time.Minute {
		return
	}
	if !s.lastBackfillAt.CompareAndSwap(last, now.UnixNano()) {
		return
	}
	state, err := s.readAnalyticsState(ctx)
	// Without readable progress this round cannot choose a trustworthy reverse
	// cursor. Leave persisted coverage as supplied and let a later admitted
	// round retry the state read.
	if err != nil {
		return
	}
	if state.ManualBackfillStart != nil && state.ManualBackfillCursor != nil {
		s.runManualHistoricalBackfill(ctx, state)
		return
	}
	if state.SourceOldestAt == nil {
		oldest, oldestErr := s.analyticsRepo.GetOldestUsageLogTime(ctx)
		// An unreadable source beginning cannot anchor reverse coverage. Leave
		// historical progress for a later round while completed live work remains
		// available.
		if oldestErr != nil {
			return
		}
		state.SourceOldestAt = oldest
	}
	if state.SourceOldestAt == nil {
		return
	}
	// Retained-source coverage begins at its enclosing UTC-hour bucket.
	oldestHour := state.SourceOldestAt.UTC().Truncate(time.Hour)
	cursor := now.UTC().Truncate(time.Hour)
	if state.BackfillCursor != nil {
		cursor = state.BackfillCursor.UTC().Truncate(time.Hour)
	}
	if !cursor.After(oldestHour) {
		state.Phase = "idle"
		state.CoverageStart = timePointer(oldestHour)
		state.BackfillCursor = timePointer(oldestHour)
		// Phase or completed-target publication is best effort here. If persistence
		// fails, the stored request remains available for a later admitted round.
		_ = s.updateAnalyticsState(ctx, state, AnalyticsAutomaticProgress)
		return
	}

	started := time.Now()
	budget := s.resolveBackfillBudget()
	var previousIterationDuration time.Duration
	state.Phase = "backfill"
	// Phase or completed-target publication is best effort here. If persistence
	// fails, the stored request remains available for a later admitted round.
	_ = s.updateAnalyticsState(ctx, state, AnalyticsAutomaticProgress)
	for processed := range dashboardAggregationBackfillMaxHours {
		chunkStart := cursor.Add(-time.Hour)
		if chunkStart.Before(oldestHour) {
			chunkStart = oldestHour
		}
		remaining := budget - time.Since(started)
		if !shouldStartBackfillChunk(processed, remaining, previousIterationDuration) {
			s.pauseHistoricalBackfill(ctx, state, started)
			return
		}
		iterationStarted := time.Now()
		// Give this hour the remaining round budget so context-aware SQL cannot use
		// an independent full timeout after earlier chunks have consumed part of the allowance.
		chunkCtx, cancel := context.WithTimeout(ctx, remaining)
		chunkErr := s.aggregateAutomaticBackfillHour(chunkCtx, chunkStart, cursor, oldestHour)
		chunkContextErr := chunkCtx.Err()
		cancel()
		if chunkErr != nil {
			if errors.Is(chunkContextErr, context.DeadlineExceeded) {
				// Completed chunks already have durable progress. A later deadline requests
				// normal pause, while failure before any completed hour is recorded as an error.
				if processed > 0 {
					s.pauseHistoricalBackfill(ctx, state, started)
					return
				}
				timeoutErr := fmt.Errorf("backfill for one hour exceeded the %s work budget", budget)
				s.markAnalyticsRunFailed(ctx, time.Now().UTC(), started, timeoutErr)
				return
			}
			s.markAnalyticsRunFailed(ctx, time.Now().UTC(), started, chunkErr)
			return
		}
		cursor = chunkStart
		state.CoverageStart = timePointer(cursor)
		state.BackfillCursor = timePointer(cursor)
		state.LastSuccessAt = timePointer(time.Now().UTC())
		state.LastError = ""
		state.LastErrorAt = nil
		state.LastDurationMS = time.Since(started).Milliseconds()
		if !cursor.After(oldestHour) {
			state.Phase = "idle"
		} else {
			state.Phase = "backfill"
		}
		// Publish this completed chunk before admitting another hour. Failed persistence
		// leaves the stored cursor eligible for repeat work and ends this round's advancement.
		if err := s.updateAnalyticsState(ctx, state, AnalyticsAutomaticProgress); err != nil {
			return
		}
		previousIterationDuration = time.Since(iterationStarted)
		if !cursor.After(oldestHour) {
			return
		}
	}
}

// runManualHistoricalBackfill recomputes the observed manual range backward
// toward its UTC-hour target. Missing request fields return immediately; an
// already-completed target requests clearing its independent manual cursor.
//
// Each successful chunk finishes range writes before manual cursor
// publication and can extend automatic coverage only where its interval
// touches that coverage. Persistence compares the observed manual request
// through updateAnalyticsState, so a replaced request ends older progress.
// Round limits and deadline handling preserve completed chunks for later
// resumption, with first-chunk failure recorded separately.
func (s *DashboardAggregationService) runManualHistoricalBackfill(ctx context.Context, state *UsageAnalyticsAggregationState) {
	if state == nil || state.ManualBackfillStart == nil || state.ManualBackfillCursor == nil {
		return
	}
	// Process persisted manual targets at the UTC-hour bucket precision.
	target := state.ManualBackfillStart.UTC().Truncate(time.Hour)
	cursor := state.ManualBackfillCursor.UTC().Truncate(time.Hour)
	if !cursor.After(target) {
		state.ManualBackfillStart = nil
		state.ManualBackfillCursor = nil
		state.Phase = "idle"
		// Phase or completed-target publication is best effort here. If persistence
		// fails, the stored request remains available for a later admitted round.
		_ = s.updateAnalyticsState(ctx, state, AnalyticsManualProgress)
		return
	}

	started := time.Now()
	budget := s.resolveBackfillBudget()
	var previousIterationDuration time.Duration
	state.Phase = "backfill"
	// Phase or completed-target publication is best effort here. If persistence
	// fails, the stored request remains available for a later admitted round.
	_ = s.updateAnalyticsState(ctx, state, AnalyticsManualProgress)
	for processed := range dashboardAggregationBackfillMaxHours {
		chunkEnd := cursor
		chunkStart := cursor.Add(-time.Hour)
		if chunkStart.Before(target) {
			chunkStart = target
		}
		remaining := budget - time.Since(started)
		if !shouldStartBackfillChunk(processed, remaining, previousIterationDuration) {
			s.pauseHistoricalBackfill(ctx, state, started)
			return
		}
		iterationStarted := time.Now()
		// Give this hour the remaining round budget so context-aware SQL cannot use
		// an independent full timeout after earlier chunks have consumed part of the allowance.
		chunkCtx, cancel := context.WithTimeout(ctx, remaining)
		chunkErr := s.aggregateRange(chunkCtx, chunkStart, chunkEnd)
		chunkContextErr := chunkCtx.Err()
		cancel()
		if chunkErr != nil {
			if errors.Is(chunkContextErr, context.DeadlineExceeded) {
				// Completed chunks already have durable progress. A later deadline requests
				// normal pause, while failure before any completed hour is recorded as an error.
				if processed > 0 {
					s.pauseHistoricalBackfill(ctx, state, started)
					return
				}
				timeoutErr := fmt.Errorf("backfill for one hour exceeded the %s work budget", budget)
				s.markAnalyticsRunFailed(ctx, time.Now().UTC(), started, timeoutErr)
				return
			}
			s.markAnalyticsRunFailed(ctx, time.Now().UTC(), started, chunkErr)
			return
		}

		cursor = chunkStart
		extendUsageAnalyticsCoverage(state, chunkStart, chunkEnd)
		state.ManualBackfillCursor = timePointer(cursor)
		state.LastSuccessAt = timePointer(time.Now().UTC())
		state.LastError = ""
		state.LastErrorAt = nil
		state.LastDurationMS = time.Since(started).Milliseconds()
		if !cursor.After(target) {
			state.ManualBackfillStart = nil
			state.ManualBackfillCursor = nil
			state.Phase = "idle"
		} else {
			state.Phase = "backfill"
		}
		// Publish this completed chunk before admitting another hour. Failed persistence
		// leaves the stored cursor eligible for repeat work and ends this round's advancement.
		if err := s.updateAnalyticsState(ctx, state, AnalyticsManualProgress); err != nil {
			return
		}
		previousIterationDuration = time.Since(iterationStarted)
		if !cursor.After(target) {
			return
		}
	}
}

// resolveBackfillBudget returns a positive test override or the ten-second
// historical-round budget. Nil services and nonpositive overrides use the
// production value for chunk admission and context deadlines.
func (s *DashboardAggregationService) resolveBackfillBudget() time.Duration {
	if s != nil && s.backfillBudget > 0 {
		return s.backfillBudget
	}
	return dashboardAggregationBackfillBudget
}

// shouldStartBackfillChunk reports whether the remaining round budget permits
// another historical hour. Exhausted time rejects every chunk, while the
// first chunk or a missing positive prior duration can begin with any
// positive remainder.
//
// Later chunks use the preceding iteration as an estimate and require
// strictly more remaining time than it consumed. This comparison lets
// completed progress pause before another likely over-budget query, while the
// chunk context still supplies the actual deadline. The estimate cannot
// establish how long the next repository operation will take.
func shouldStartBackfillChunk(processed int, remaining, previousIterationDuration time.Duration) bool {
	if remaining <= 0 {
		return false
	}
	if processed == 0 || previousIterationDuration <= 0 {
		return true
	}
	return remaining > previousIterationDuration
}

// pauseHistoricalBackfill attempts to retain backfill phase and completed
// progress after budget exhaustion. It clears prior error observations and
// selects manual or automatic field ownership from the state's remembered
// manual request.
func (s *DashboardAggregationService) pauseHistoricalBackfill(ctx context.Context, state *UsageAnalyticsAggregationState, started time.Time) {
	if s == nil || s.analyticsRepo == nil || state == nil {
		return
	}
	state.Phase = "backfill"
	state.LastError = ""
	state.LastErrorAt = nil
	state.LastDurationMS = time.Since(started).Milliseconds()
	// This progress annotation is best effort. Leave the caller's data-work outcome
	// as the primary result if the state update cannot be persisted in this context.
	_ = s.updateAnalyticsState(ctx, state, progressKind(state))
}

// extendUsageAnalyticsCoverage moves an existing continuous coverage start
// when a manual chunk touches or overlaps it. Callers supply an increasing
// processed chunk, and nil state or missing coverage leaves the state
// unchanged.
//
// A chunk entirely before coverage with a gap, or starting after coverage,
// cannot establish a continuous extension. An admitted chunk supplies the
// earlier CoverageStart and moves BackfillCursor backward only if absent or
// later. Keeping both positions consistent lets automatic work resume at the
// newly covered edge while retaining an already earlier automatic cursor.
func extendUsageAnalyticsCoverage(state *UsageAnalyticsAggregationState, chunkStart, chunkEnd time.Time) {
	if state == nil || state.CoverageStart == nil {
		return
	}
	// Coverage intersections compare the same UTC-hour precision as processed
	// chunks.
	coverage := state.CoverageStart.UTC().Truncate(time.Hour)
	if chunkEnd.Before(coverage) || chunkStart.After(coverage) {
		return
	}
	// A touching manual interval keeps coverage continuous. Move its start and
	// any later automatic cursor together so subsequent automatic work resumes at that edge.
	state.CoverageStart = timePointer(chunkStart)
	if state.BackfillCursor == nil || state.BackfillCursor.After(chunkStart) {
		state.BackfillCursor = timePointer(chunkStart)
	}
}

// timePointer returns a new pointer holding value converted to UTC.
func timePointer(value time.Time) *time.Time {
	result := value.UTC()
	return &result
}

// maxInt64 returns the greater of the two supplied integer values.
func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// truncateToDayUTC returns UTC midnight for the date containing the supplied
// instant.
func truncateToDayUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// runBackground registers fn while the runtime has started and remains open,
// then dispatches it asynchronously. Calls before Start or after shutdown
// silently leave fn unstarted; accepted callbacks must be nonnil and return
// before runtime shutdown can finish.
//
// Registration holds runtimeMu through the WaitGroup admission that launches
// fn. StopContext closes admission under the same lock before waiting, so an
// accepted task cannot be missed when dispatch overlaps shutdown. A callback
// may still be running after one StopContext caller's deadline expires; the
// shared completion wait continues until registered work returns.
func (s *DashboardAggregationService) runBackground(fn func()) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if !s.runtimeStarted || s.runtimeStopped {
		return
	}
	// Register under the same admission lock checked by StopContext. Its wait can
	// then include this task even if dispatch overlaps the beginning of runtime shutdown.
	s.runtimeWG.Go(func() { ; fn() })
}

// Stop cancels runtime aggregation and waits for all accepted work to finish.
// It uses a background shutdown context, so callers needing a finite waiting
// budget use StopContext directly.
func (s *DashboardAggregationService) Stop() { _ = s.StopContext(context.Background()) }

// StopContext closes runtime task admission, cancels work and waits within
// ctx's remaining budget. Nil services return successfully; repeated calls
// share the same stop sequence and completion channel. Pass a nonnil ctx to
// select the waiting budget.
//
// The stop sequence cancels the runtime context before waiting for
// timing-wheel callbacks and registered tasks. Those waits continue in a
// completion goroutine even if one caller's deadline expires. Closing
// stopDone publishes their completion to later callers; expiration returns
// ctx.Err and reports that shutdown work remains incomplete.
func (s *DashboardAggregationService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	// Close runtime admission and cancel its context in one stop sequence. Existing
	// tasks remain registered until completion, while later callers share that sequence's signal.
	s.stopOnce.Do(func() {
		s.runtimeMu.Lock()
		s.runtimeStopped = true
		if s.runCancel != nil {
			s.runCancel()
		}
		if s.stopDone == nil {
			s.stopDone = make(chan struct{})
		}
		s.runtimeMu.Unlock()
		// Completion keeps waiting after an individual StopContext deadline expires.
		// Close stopDone only after scheduled callbacks and registered tasks have both returned.
		go func() {
			if s.timingWheel != nil {
				s.timingWheel.CancelAndWait("dashboard:aggregation")
			}
			s.runtimeWG.Wait()
			close(s.stopDone)
		}()
	})
	select {
	case <-s.stopDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// operationContext returns the aggregation runtime context when one was
// constructed. Directly assembled test instances and nil receivers use
// context.Background, leaving their caller-supplied operation contexts to
// provide any timeout.
func (s *DashboardAggregationService) operationContext() context.Context {
	if s != nil && s.runCtx != nil {
		return s.runCtx
	}
	return context.Background()
}

// acquireSingleton requests the configured distributed lease for this runtime
// instance. A nil locker returns success with a no-op release; successful
// configured acquisition requires the caller to invoke its returned release
// callback.
func (s *DashboardAggregationService) acquireSingleton(ctx context.Context) (func(), bool) {
	if s.locker == nil {
		// A runtime without a lease provider relies on the local running flag alone.
		// Supply a no-op release so admitted callers can use the same deferred cleanup sequence.
		return func() {}, true
	}
	return s.locker(ctx, dashboardAggregationLeaderLockKey, s.instanceID, dashboardAggregationLeaderLockTTL)
}

// progressKind chooses manual progress when state remembers an observed
// manual target. Callers supply a nonnil state; other observations select
// automatic progress, including a state whose manual fields were cleared
// during completion.
func progressKind(state *UsageAnalyticsAggregationState) AnalyticsChangeKind {
	if state.observedManualStart != nil {
		return AnalyticsManualProgress
	}
	return AnalyticsAutomaticProgress
}

// logMessage sends aggregation diagnostics through the configured reporter
// when present. Nil services or missing reporters use logUsage, keeping
// lifecycle, retry and retention failures visible for directly constructed
// runtimes.
func (s *DashboardAggregationService) logMessage(component, format string, args ...any) {
	if s != nil && s.reporter != nil {
		s.reporter(component, format, args...)
		return
	}
	// Directly constructed runtimes can lack the injected reporter. Use logUsage
	// so retained cleanup, progress and retry failures still produce package diagnostics.
	logUsage(component, format, args...)
}
