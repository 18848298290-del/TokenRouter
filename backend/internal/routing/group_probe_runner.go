package routing

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// group_probe_runner.go claims due groups, runs bounded probe attempts, and
// saves their final outcomes.
// A due-group lease must cover selection, every configured retry, and final
// result storage. Each instance claims at most its worker count and skips
// overlapping cron ticks. PostgreSQL leases defer competing claims by other
// instances until expiry; result saving depends on work finishing within that
// lease.
//
// Maintenance, claiming, and group execution receive distinct time budgets so a
// slow cleanup cannot consume retries before a group begins. A group stores one
// final sample after success or exhaustion, retaining the configured request
// model alongside the selected provider. Shutdown cancels the shared parent and
// blocks new rounds; a canceled or failed final save can leave a claim to
// expire rather than publish an incomplete round.

// Probe budgets bound workers, retention maintenance, and database leases;
// lease duration includes the maximum configured attempts plus claim and
// storage headroom.
const (
	groupAvailabilityProbeDefaultMaxWorkers = 5
	// The execution budget covers all configured attempts and one minute of
	// selection and result-storage headroom.
	groupAvailabilityProbeMaxRunDuration     = time.Duration(MaxGroupAvailabilityProbeMaxRetries+1)*time.Duration(MaxGroupAvailabilityProbeTimeoutSeconds)*time.Second + time.Minute
	groupAvailabilityProbeMaintenanceTimeout = time.Minute
	groupAvailabilityProbeClaimTimeout       = time.Minute
	groupAvailabilityProbeLockDuration       = groupAvailabilityProbeMaxRunDuration + groupAvailabilityProbeClaimTimeout + time.Minute
	groupAvailabilityProbeCleanupRetention   = 90 * 24 * time.Hour
	groupAvailabilityProbeCleanupMinInterval = 12 * time.Hour
)

// runDue executes one due-group round while skipping ticks that overlap an
// admitted round. It acquires runMu before lifetime admission and keeps the
// round active until all launched workers exit.
//
// Maintenance and claiming use independent deadlines before each group receives
// its own retry budget. The repository is asked for at most the worker count,
// avoiding locally queued leases. Selecting parent cancellation stops further
// dispatch and waits for launched workers; semaphore admission can win when
// both branches are ready, so that worker inherits the canceled parent. Storage
// and execution adapters must cooperate for prompt shutdown.
// @project-doc docs/interfaces/model_catalog_and_marketplace.md#group_availability_probe
func (s *GroupAvailabilityProbeRunnerService) runDue() {
	// A slow admitted round keeps runMu until workers finish, so a later cron tick
	// skips claiming rather than occupying another set of leases.
	if !s.runMu.TryLock() {
		return
	}
	defer s.runMu.Unlock()
	parent, ok := s.beginRun()
	if !ok {
		return
	}
	defer s.endRun()

	// Give retention deletion its own deadline before claiming; each claimed
	// group's retry budget begins later and remains available after slow
	// maintenance.
	maintenanceCtx, maintenanceCancel := context.WithTimeout(parent, groupAvailabilityProbeMaintenanceTimeout)
	s.cleanupIfNeeded(maintenanceCtx, s.now())
	maintenanceCancel()
	if parent.Err() != nil {
		return
	}

	// Claim only work that this instance can launch immediately, keeping a
	// database lease from aging while its group waits behind another local probe.
	claimCtx, claimCancel := context.WithTimeout(parent, groupAvailabilityProbeClaimTimeout)
	claimNow := s.now()
	dueGroups, err := s.repo.ClaimDue(claimCtx, claimNow, claimNow.Add(groupAvailabilityProbeLockDuration), s.instanceID, groupAvailabilityProbeDefaultMaxWorkers)
	claimCancel()
	if err != nil {
		s.observe("[GroupAvailabilityProbe] ClaimDue error: %v", err)
		return
	}
	if parent.Err() != nil || len(dueGroups) == 0 {
		return
	}

	sem := make(chan struct{}, groupAvailabilityProbeDefaultMaxWorkers)
	var wg sync.WaitGroup
	// When the cancellation branch wins, stop dispatch and wait for launched
	// workers before signaling idle. Admission can also win with a canceled
	// parent, which the worker inherits.
	for i := range dueGroups {
		group := dueGroups[i]
		select {
		case sem <- struct{}{}:
		case <-parent.Done():
			wg.Wait()
			return
		}
		wg.Go(func() {
			defer func() { <-sem }()
			// Start the group deadline only after its worker is admitted so shared
			// maintenance and claiming cannot consume that group's allowed attempt time.
			probeCtx, probeCancel := context.WithTimeout(parent, groupAvailabilityProbeMaxRunDuration)
			defer probeCancel()
			s.runOne(probeCtx, group)
		})
	}
	wg.Wait()
}

// cleanupIfNeeded attempts retention cleanup when the last attempt is old
// enough. Call from a serialized round with a maintenance context; the
// timestamp advances before deletion, and a storage error is observed but not
// returned.
func (s *GroupAvailabilityProbeRunnerService) cleanupIfNeeded(ctx context.Context, now time.Time) {
	// The retention interval counts attempts, including failed deletion runs.
	if now.Sub(s.lastCleanupAt) < groupAvailabilityProbeCleanupMinInterval {
		return
	}
	// Record the round clock before issuing the delete so a failed database
	// request still consumes this maintenance interval while probe claiming can
	// continue.
	s.lastCleanupAt = now
	if err := s.repo.CleanupOldResults(ctx, now.Add(-groupAvailabilityProbeCleanupRetention)); err != nil {
		s.observe("[GroupAvailabilityProbe] cleanup error: %v", err)
	}
}

// runOne normalizes one claimed group's probe settings, executes its attempts,
// and saves one final result. Disabled settings produce no write; invalid
// settings or a missing attempt result produce a synthetic failure.
//
// Every attempt receives a fresh timeout within the group's round budget. A
// successful attempt ends retries, and earlier failures remain internal to the
// round rather than becoming separate availability samples. Saving uses the
// same round context, so shutdown or an exhausted budget can prevent storage
// and leave recovery to the lease.
func (s *GroupAvailabilityProbeRunnerService) runOne(ctx context.Context, due GroupAvailabilityProbeDueGroup) {
	probeConfig, err := NormalizeGroupAvailabilityProbeConfig(due.Config)
	// Rejected settings cannot produce an upstream test. A synthetic sample keeps
	// the claimed group and request name available for administrative diagnosis
	// after lease scheduling.
	if err != nil {
		s.saveFailure(ctx, due, probeConfig, fmt.Sprintf("invalid probe config: %v", err), nil)
		return
	}
	if !probeConfig.Enabled {
		return
	}

	maxRetries := DefaultGroupAvailabilityProbeMaxRetries
	if probeConfig.MaxRetries != nil {
		maxRetries = *probeConfig.MaxRetries
	}
	// Convert saved seconds for the attempt helper; the enclosing worker deadline
	// still caps the total time spent across retries and final result storage.
	probeResult := runGroupAvailabilityProbeAttempts(
		ctx,
		maxRetries,
		time.Duration(probeConfig.TimeoutSeconds)*time.Second,
		func(attemptCtx context.Context) *GroupAvailabilityProbeResult {
			return s.runProbeAttempt(attemptCtx, due, probeConfig)
		},
	)
	if probeResult == nil {
		s.saveFailure(ctx, due, probeConfig, "probe did not produce a result", nil)
		return
	}

	s.saveResult(ctx, due, probeConfig, probeResult)
}

// runGroupAvailabilityProbeAttempts runs the initial attempt plus at most
// maxRetries additional attempts and returns the last result. Negative retry
// counts become zero, nonpositive timeout uses the parent context, and
// preexisting cancellation starts no attempt.
//
// Positive timeout creates a new child for each call, so an expired attempt
// does not cancel the next one. The helper checks Success before the post-call
// parent cancellation check; a callback that returns success while canceling
// its parent still yields that success. Callbacks own result classification,
// and nil failed results can be retried.
func runGroupAvailabilityProbeAttempts(
	ctx context.Context,
	maxRetries int,
	timeout time.Duration,
	runAttempt func(context.Context) *GroupAvailabilityProbeResult,
) *GroupAvailabilityProbeResult {
	if maxRetries < 0 {
		maxRetries = 0
	}

	var lastResult *GroupAvailabilityProbeResult
	for retryCount := 0; retryCount <= maxRetries; retryCount++ {
		if ctx.Err() != nil {
			break
		}
		// Retain the shared round deadline while replacing each attempt's child
		// deadline, allowing a timeout failure to be retried within the remaining
		// parent budget.
		attemptCtx := ctx
		cancel := func() {}
		if timeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		lastResult = runAttempt(attemptCtx)
		cancel()

		// With a positive timeout, cancel releases the child timer above. Classify
		// returned success independently of that child cancellation so cleanup cannot
		// turn a completed request into a failure.
		if lastResult != nil && lastResult.Success {
			return lastResult
		}
		if ctx.Err() != nil {
			break
		}
	}
	return lastResult
}

// runProbeAttempt selects and tests a provider while recording the configured
// request model in history. Selection receives that original name, and testing
// uses the selector's group-mapped model so provider mapping can occur in the
// test service.
//
// Selection failure returns a timed failure without a provider ID. A returned
// test result supplies status, timing, and diagnostics; nil metadata with no
// error keeps locally measured success. Test errors override success while
// retaining an existing diagnostic, and an already-canceled attempt context
// converts a remaining success into failure before this method returns.
func (s *GroupAvailabilityProbeRunnerService) runProbeAttempt(ctx context.Context, due GroupAvailabilityProbeDueGroup, probeConfig GroupAvailabilityProbeConfig) *GroupAvailabilityProbeResult {
	// Locally measured elapsed values are truncated to whole milliseconds for
	// history; returned test metadata can replace those wall-clock measurements
	// with its own recorded timing.
	startedAt := s.now()
	due.Config = probeConfig
	target, err := s.executor.Select(ctx, due, probeConfig.ModelID)
	if err != nil {
		finishedAt := s.now()
		return &GroupAvailabilityProbeResult{
			GroupID:      due.GroupID,
			ModelID:      probeConfig.ModelID,
			Status:       GroupAvailabilityProbeStatusFailed,
			Success:      false,
			LatencyMs:    finishedAt.Sub(startedAt).Milliseconds(),
			ErrorMessage: err.Error(),
			StartedAt:    startedAt,
			FinishedAt:   finishedAt,
		}
	}

	// Execute the selected model but retain probeConfig.ModelID in history,
	// keeping the administrator's requested model distinct from group and provider
	// mapping results.
	result, err := s.executor.Test(ctx, target, probeConfig.Prompt, probeConfig.UserAgent)
	finishedAt := s.now()
	probeResult := &GroupAvailabilityProbeResult{
		GroupID:    due.GroupID,
		ProviderID: &target.ProviderID,
		ModelID:    probeConfig.ModelID,
		Status:     GroupAvailabilityProbeStatusSuccess,
		Success:    true,
		LatencyMs:  finishedAt.Sub(startedAt).Milliseconds(),
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
	}
	if result != nil {
		probeResult.Status = result.Status
		probeResult.Success = result.Status == GroupAvailabilityProbeStatusSuccess
		probeResult.LatencyMs = result.LatencyMs
		probeResult.ErrorMessage = result.ErrorMessage
		probeResult.StartedAt = result.StartedAt
		probeResult.FinishedAt = result.FinishedAt
	}
	// Preserve a provider diagnostic already returned with test metadata; use the
	// execution error text only when that metadata supplied no explanation for the
	// failed attempt.
	if err != nil {
		probeResult.Status = GroupAvailabilityProbeStatusFailed
		probeResult.Success = false
		if probeResult.ErrorMessage == "" {
			probeResult.ErrorMessage = err.Error()
		}
	}
	// A completed-looking result is still a failed attempt when its deadline or
	// shutdown context has already expired before classification reaches this
	// check.
	if ctx.Err() != nil && probeResult.Success {
		probeResult.Status = GroupAvailabilityProbeStatusFailed
		probeResult.Success = false
		probeResult.ErrorMessage = ctx.Err().Error()
	}

	return probeResult
}

// saveFailure constructs a zero-latency observation and sends it through normal
// result scheduling. The supplied provider ID is retained, and one clock read
// supplies both timestamps because this sample represents a synthetic failure.
//
// Rejected normalization returns an empty configuration, which can erase its
// submitted model name. Recover that name from the claimed configuration when
// the normalized name is blank, keeping history tied to the administrator's
// selection. saveResult supplies a default next interval when invalid settings
// also leave the interval unset.
func (s *GroupAvailabilityProbeRunnerService) saveFailure(ctx context.Context, due GroupAvailabilityProbeDueGroup, cfg GroupAvailabilityProbeConfig, message string, providerID *int64) {
	now := s.now()
	modelID := strings.TrimSpace(cfg.ModelID)
	// Invalid configuration can return a zero normalized value. Recover its
	// submitted model name so the synthetic failure still identifies the
	// administrator's probe selection.
	if modelID == "" {
		modelID = strings.TrimSpace(due.Config.ModelID)
	}
	s.saveResult(ctx, due, cfg, &GroupAvailabilityProbeResult{
		GroupID:      due.GroupID,
		ProviderID:   providerID,
		ModelID:      modelID,
		Status:       GroupAvailabilityProbeStatusFailed,
		Success:      false,
		ErrorMessage: message,
		StartedAt:    now,
		FinishedAt:   now,
	})
}

// saveResult asks the repository to store the final result and schedule the
// next attempt interval from the current clock. Nonpositive intervals use the
// default; a save error is observed and leaves the repository's lease recovery
// behavior in effect.
func (s *GroupAvailabilityProbeRunnerService) saveResult(ctx context.Context, due GroupAvailabilityProbeDueGroup, cfg GroupAvailabilityProbeConfig, result *GroupAvailabilityProbeResult) {
	interval := time.Duration(cfg.IntervalMinutes) * time.Minute
	// Synthetic failures from invalid settings still receive a positive schedule
	// interval, preventing an unusable configuration from being reclaimed
	// immediately on every tick.
	if interval <= 0 {
		interval = time.Duration(DefaultGroupAvailabilityProbeIntervalMinutes) * time.Minute
	}
	// Start the next interval at saving, keeping this round's execution time
	// outside the configured delay before another round becomes due.
	nextRunAt := s.now().Add(interval)
	// Persist one final round outcome with its next schedule through the
	// repository's atomic operation; a failed save is observed and the claim
	// follows lease expiry.
	if err := s.repo.SaveResultAndScheduleNext(ctx, result, nextRunAt); err != nil {
		s.observe("[GroupAvailabilityProbe] group=%d save result error: %v", due.GroupID, err)
	}
}
