package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

const (
	opsCleanupDefaultSchedule = "0 3 * * *"

	opsCleanupDefaultBatchSize  = 1000
	OpsCleanupDefaultBatchSize  = opsCleanupDefaultBatchSize
	opsCleanupDefaultBatchPause = 200 * time.Millisecond
	OpsCleanupDefaultBatchPause = opsCleanupDefaultBatchPause

	opsCleanupJobName = "ops_cleanup"

	opsCleanupLeaderLockKeyDefault = "ops:cleanup:leader"
	opsCleanupLeaderLockTTLDefault = 30 * time.Minute
)

var (
	ErrDatabaseMaintenanceBusy = infraerrors.Conflict("MAINTENANCE_BUSY", "another database maintenance task is running")
	opsCleanupCronParser       = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
)

// CleanupBackend supplies maintenance locking and batched cleanup operations.
type CleanupBackend interface {
	AdvisoryLocker
	AcquireMaintenance(context.Context) (func(), bool, error)
	RunTarget(context.Context, bool, time.Time, string, string, bool, int, time.Duration) (CleanupTargetResult, error)
}

type CleanupTargetResult struct {
	Deleted, Batches int64
	Throttled        time.Duration
}

// OpsCleanupService periodically deletes old ops data to prevent unbounded DB growth.
//
// - Scheduling: 5-field cron spec (minute hour dom month dow).
// - Multi-instance: best-effort Redis leader lock so only one node runs cleanup.
// - Safety: deletes in batches to avoid long transactions.
type OpsCleanupService struct {
	drainingCrons []context.Context
	opsRepo       OpsRepository
	db            CleanupBackend
	redisClient   RuntimeCache
	cfg           *Options
	settingRepo   Settings

	instanceID string

	// mu protects replacement of cron and the effective configuration.
	// Reload can rebuild cron repeatedly; started and stopped track the service lifecycle.
	mu        sync.Mutex
	cron      *cron.Cron
	started   bool
	stopped   bool
	effective CleanupOptions

	warnNoRedisOnce sync.Once
}

func reportCleanupCompleted(o *Options, counts string) {
	if o != nil && o.CleanupCompleted != nil {
		o.CleanupCompleted(counts)
	}
}

func NewOpsCleanupService(
	opsRepo OpsRepository,
	db CleanupBackend,
	redisClient RuntimeCache,
	cfg *Options,
	settingRepo Settings,
) *OpsCleanupService {
	return &OpsCleanupService{
		opsRepo:     opsRepo,
		db:          db,
		redisClient: redisClient,
		cfg:         cfg,
		settingRepo: settingRepo,
		instanceID:  uuid.NewString(),
	}
}

// Start starts cron once using settings overrides ahead of deployment configuration.
func (s *OpsCleanupService) Start() {
	if s == nil {
		return
	}
	if s.cfg != nil && !s.cfg.Ops.Enabled {
		return
	}
	if s.opsRepo == nil || s.db == nil {
		reportOptions(s.cfg, "[OpsCleanup] not started (missing deps)")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopped {
		return
	}
	s.started = true
	if err := s.applyScheduleLocked(context.Background()); err != nil {
		reportOptions(s.cfg, "[OpsCleanup] not started: %v", err)
	}
}

// Stop stops scheduling and waits for active jobs. Repeated calls are safe.
// It waits outside mu for all retired cron instances, including jobs left by Reload.
func (s *OpsCleanupService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stopped = true
	if s.cron != nil {
		s.drainingCrons = append(s.drainingCrons, s.cron.Stop())
		s.cron = nil
	}
	pending := append([]context.Context(nil), s.drainingCrons...)
	s.mu.Unlock()
	for _, ctx := range pending {
		<-ctx.Done()
	}
}

// stopCronLocked retires cron and waits up to opsCleanupCronStopTimeout while holding mu.
// It retains the drain signal so Stop can finish waiting after a local timeout.
func (s *OpsCleanupService) stopCronLocked() {
	if s.cron == nil {
		return
	}
	ctx := s.cron.Stop()
	pending := s.drainingCrons[:0]
	for _, previous := range s.drainingCrons {
		select {
		case <-previous.Done():
		default:
			pending = append(pending, previous)
		}
	}
	s.drainingCrons = append(pending, ctx)
	select {
	case <-ctx.Done():
	case <-time.After(opsCleanupCronStopTimeout):
		reportOptions(s.cfg, "[OpsCleanup] cron stop timed out")
	}
	s.cron = nil
}

// applyScheduleLocked refreshes effective settings and rebuilds cron. The caller holds mu.
// When cleanup is disabled, it retires the current cron without creating a replacement.
func (s *OpsCleanupService) applyScheduleLocked(ctx context.Context) error {
	s.computeEffectiveLocked(ctx)
	s.stopCronLocked()

	if !s.effective.Enabled {
		reportOptions(s.cfg, "[OpsCleanup] cron disabled by settings")
		return nil
	}

	schedule := strings.TrimSpace(s.effective.Schedule)
	if schedule == "" {
		schedule = opsCleanupDefaultSchedule
	}

	loc := time.Local
	if s.cfg != nil && strings.TrimSpace(s.cfg.Timezone) != "" {
		if parsed, err := time.LoadLocation(strings.TrimSpace(s.cfg.Timezone)); err == nil && parsed != nil {
			loc = parsed
		}
	}

	c := cron.New(cron.WithParser(opsCleanupCronParser), cron.WithLocation(loc))
	if _, err := c.AddFunc(schedule, func() { s.runScheduled() }); err != nil {
		return fmt.Errorf("invalid schedule %q: %w", schedule, err)
	}
	c.Start()
	s.cron = c
	reportOptions(s.cfg,
		"[OpsCleanup] scheduled (schedule=%q tz=%s retention_days=err:%d/system:%d/min:%d/hour:%d batch=%d pause_ms=%d)",
		schedule, loc.String(),
		s.effective.ErrorLogRetentionDays,
		s.effective.SystemLogRetentionDays,
		s.effective.MinuteMetricsRetentionDays,
		s.effective.HourlyMetricsRetentionDays,
		s.effective.BatchSize,
		s.effective.BatchPauseMS,
	)
	return nil
}

// Reload refreshes ops_advanced_settings.data_retention and rebuilds the schedule.
// Call it after schedule or enabled changes. Retention values also refresh at each run.

// Calls before Start or after Stop do nothing.
func (s *OpsCleanupService) Reload(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started || s.stopped {
		return nil
	}
	return s.applyScheduleLocked(ctx)
}

// computeEffectiveLocked updates s.effective. The caller holds mu.
//
// Stored data_retention settings override a copy of cfg.Ops.Cleanup:
// Enabled replaces the deployment value.
// A nonempty Schedule replaces the deployment schedule.
// Nonnegative retention days override deployment values; zero selects TRUNCATE.
//
// Missing or invalid settings leave deployment cleanup values in effect.
func (s *OpsCleanupService) computeEffectiveLocked(ctx context.Context) {
	base := CleanupOptions{}
	if s.cfg != nil {
		base = s.cfg.Ops.Cleanup
	}
	if base.BatchSize <= 0 {
		base.BatchSize = opsCleanupDefaultBatchSize
	}
	if base.BatchPauseMS <= 0 {
		base.BatchPauseMS = int(opsCleanupDefaultBatchPause / time.Millisecond)
	}

	if s.settingRepo == nil {
		s.effective = base
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyOpsAdvancedSettings)
	if err != nil {
		if !errors.Is(err, ErrSettingNotFound) {
			reportOptions(s.cfg,
				"[OpsCleanup] read advanced settings failed, using cfg: %v", err)
		}
	} else if strings.TrimSpace(raw) != "" {
		var adv OpsAdvancedSettings
		if err := json.Unmarshal([]byte(raw), &adv); err != nil {
			reportOptions(s.cfg,
				"[OpsCleanup] parse advanced settings failed, using cfg: %v", err)
		} else {
			dr := adv.DataRetention
			base.Enabled = dr.CleanupEnabled
			if sched := strings.TrimSpace(dr.CleanupSchedule); sched != "" {
				base.Schedule = sched
			}
			if dr.CleanupBatchSize > 0 {
				base.BatchSize = dr.CleanupBatchSize
			}
			if dr.CleanupPauseMS > 0 {
				base.BatchPauseMS = dr.CleanupPauseMS
			}
			if dr.ErrorLogRetentionDays >= 0 {
				base.ErrorLogRetentionDays = dr.ErrorLogRetentionDays
			}
			if dr.MinuteMetricsRetentionDays >= 0 {
				base.MinuteMetricsRetentionDays = dr.MinuteMetricsRetentionDays
			}
			if dr.HourlyMetricsRetentionDays >= 0 {
				base.HourlyMetricsRetentionDays = dr.HourlyMetricsRetentionDays
			}
		}
	}

	// System log retention comes from the runtime log configuration.
	runtimeRaw, runtimeErr := s.settingRepo.GetValue(ctx, SettingKeyOpsRuntimeLogConfig)
	if runtimeErr != nil && !errors.Is(runtimeErr, ErrSettingNotFound) {
		reportOptions(s.cfg,
			"[OpsCleanup] read runtime log config failed, using cfg: %v", runtimeErr)
	} else if strings.TrimSpace(runtimeRaw) != "" {
		var runtimeCfg OpsRuntimeLogConfig
		if err := json.Unmarshal([]byte(runtimeRaw), &runtimeCfg); err != nil {
			reportOptions(s.cfg,
				"[OpsCleanup] parse runtime log config failed, using cfg: %v", err)
		} else if runtimeCfg.RetentionDays > 0 {
			base.SystemLogRetentionDays = runtimeCfg.RetentionDays
		}
	}
	s.effective = base
}

// snapshotEffective returns a copy of the current cleanup configuration under mu.
func (s *OpsCleanupService) snapshotEffective() CleanupOptions {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.effective
}

// refreshEffectiveBeforeRun applies current retention settings to this run.
// Schedule changes require Reload to replace cron's schedule.
func (s *OpsCleanupService) refreshEffectiveBeforeRun(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.computeEffectiveLocked(ctx)
}

func (s *OpsCleanupService) runScheduled() {
	if s == nil || s.db == nil || s.opsRepo == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), opsCleanupRunTimeout)
	defer cancel()

	s.refreshEffectiveBeforeRun(ctx)

	release, ok := s.tryAcquireLeaderLock(ctx)
	if !ok {
		return
	}
	if release != nil {
		defer release()
	}

	releaseMaintenance, acquired, lockErr := s.db.AcquireMaintenance(ctx)
	if lockErr != nil {
		s.recordHeartbeatError(time.Now().UTC(), 0, lockErr)
		reportOptions(s.cfg, "[OpsCleanup] acquire maintenance lock failed: %v", lockErr)
		return
	}
	if !acquired {
		s.recordHeartbeatError(time.Now().UTC(), 0, ErrDatabaseMaintenanceBusy)
		reportOptions(s.cfg, "[OpsCleanup] skipped: database maintenance task is running")
		return
	}
	defer releaseMaintenance()

	startedAt := time.Now().UTC()
	runAt := startedAt

	counts, err := s.runCleanupOnce(ctx)
	if err != nil {
		s.recordHeartbeatError(runAt, time.Since(startedAt), err)
		reportOptions(s.cfg, "[OpsCleanup] cleanup failed: %v", err)
		return
	}
	s.recordHeartbeatSuccess(runAt, time.Since(startedAt), counts)
	// Record deletion counts at info level for later inspection.
	reportCleanupCompleted(s.cfg, counts.String())
}

func (s *OpsCleanupService) runCleanupOnce(ctx context.Context) (opsCleanupDeletedCounts, error) {
	out := opsCleanupDeletedCounts{}
	if s == nil || s.db == nil || s.cfg == nil {
		return out, nil
	}

	effective := s.snapshotEffective()
	now := time.Now().UTC()

	targets := []opsCleanupTarget{
		{effective.ErrorLogRetentionDays, "ops_error_logs", "created_at", false, &out.errorLogs},
		{effective.ErrorLogRetentionDays, "ops_ingress_reject_aggregates", "bucket_start", false, &out.ingressRejects},
		{effective.ErrorLogRetentionDays, "ops_alert_events", "fired_at", false, &out.alertEvents},
		{effective.SystemLogRetentionDays, "ops_system_logs", "created_at", false, &out.systemLogs},
		{effective.ErrorLogRetentionDays, "ops_system_log_cleanup_audits", "created_at", false, &out.logAudits},
		{effective.MinuteMetricsRetentionDays, "ops_system_metrics", "created_at", false, &out.systemMetrics},
		{effective.HourlyMetricsRetentionDays, "ops_metrics_hourly", "bucket_start", false, &out.hourlyPreagg},
		{effective.HourlyMetricsRetentionDays, "ops_metrics_daily", "bucket_date", true, &out.dailyPreagg},
	}
	batchSize := effective.BatchSize
	if batchSize <= 0 {
		batchSize = opsCleanupDefaultBatchSize
	}
	batchPause := time.Duration(effective.BatchPauseMS) * time.Millisecond
	if batchPause <= 0 {
		batchPause = opsCleanupDefaultBatchPause
	}

	for _, t := range targets {
		cutoff, truncate, ok := opsCleanupPlan(now, t.retentionDays)
		if !ok {
			continue
		}
		result, err := s.db.RunTarget(ctx, truncate, cutoff, t.table, t.timeCol, t.castDate, batchSize, batchPause)
		if err != nil {
			return out, err
		}
		*t.counter = result.Deleted
		out.batches += result.Batches
		out.throttled += result.Throttled
		reportOptions(s.cfg,
			"[OpsCleanup] table complete: table=%s deleted=%d batches=%d throttled_ms=%d",
			t.table, result.Deleted, result.Batches, result.Throttled.Milliseconds())
	}

	return out, nil
}

func (s *OpsCleanupService) tryAcquireLeaderLock(ctx context.Context) (func(), bool) {
	if s == nil {
		return nil, false
	}

	key := opsCleanupLeaderLockKeyDefault
	ttl := opsCleanupLeaderLockTTLDefault

	// Prefer Redis leader lock when available, but avoid stampeding the DB when Redis is flaky by
	// falling back to a DB advisory lock.
	if s.redisClient != nil {
		ok, err := s.redisClient.Claim(ctx, key, s.instanceID, ttl)
		if err == nil {
			if !ok {
				return nil, false
			}
			return func() {
				_ = s.redisClient.Release(ctx, key, s.instanceID)
			}, true
		}
		// Redis error: fall back to DB advisory lock.
		s.warnNoRedisOnce.Do(func() {
			reportOptions(s.cfg, "[OpsCleanup] leader lock SetNX failed; falling back to DB advisory lock: %v", err)
		})
	} else {
		s.warnNoRedisOnce.Do(func() {
			reportOptions(s.cfg, "[OpsCleanup] redis not configured; using DB advisory lock")
		})
	}

	release, ok := acquireAdvisory(ctx, s.db, key)
	if !ok {
		return nil, false
	}
	return release, true
}

func (s *OpsCleanupService) recordHeartbeatSuccess(runAt time.Time, duration time.Duration, counts opsCleanupDeletedCounts) {
	if s == nil || s.opsRepo == nil {
		return
	}
	now := time.Now().UTC()
	durMs := duration.Milliseconds()
	result := truncateString(counts.String(), 2048)
	ctx, cancel := context.WithTimeout(context.Background(), opsCleanupHeartbeatTimeout)
	defer cancel()
	_ = s.opsRepo.UpsertJobHeartbeat(ctx, &OpsUpsertJobHeartbeatInput{
		JobName:        opsCleanupJobName,
		LastRunAt:      &runAt,
		LastSuccessAt:  &now,
		LastDurationMs: &durMs,
		LastResult:     &result,
	})
}

func (s *OpsCleanupService) recordHeartbeatError(runAt time.Time, duration time.Duration, err error) {
	if s == nil || s.opsRepo == nil || err == nil {
		return
	}
	now := time.Now().UTC()
	durMs := duration.Milliseconds()
	msg := truncateString(err.Error(), 2048)
	ctx, cancel := context.WithTimeout(context.Background(), opsCleanupHeartbeatTimeout)
	defer cancel()
	_ = s.opsRepo.UpsertJobHeartbeat(ctx, &OpsUpsertJobHeartbeatInput{
		JobName:        opsCleanupJobName,
		LastRunAt:      &runAt,
		LastErrorAt:    &now,
		LastError:      &msg,
		LastDurationMs: &durMs,
	})
}
