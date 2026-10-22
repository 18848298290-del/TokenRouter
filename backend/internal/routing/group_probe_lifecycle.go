package routing

import (
	"context"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// group_probe_lifecycle.go coordinates availability probe startup, round
// admission, cancellation, and shutdown waits.
// Availability probe dispatch belongs to one runner lifetime: construction is
// idle, Start consumes one registration attempt, and Stop prevents later
// restart. Scheduled ticks use a separate round lock so an instance can skip
// overlap while lifecycle operations still cancel the current round.
//
// The state mutex orders startup, admission, and shutdown. An active round owns
// completion of the idle channel; shutdown closes that channel itself when no
// round is running. Scheduler completion is tracked separately because a
// callback can still be returning after probe work becomes idle. StopContext's
// channel waits honor the caller context after synchronous lifecycle work, and
// a timeout leaves unfinished work available to later waits.

// ProbeSchedule starts probe dispatch and reports when scheduled callbacks have
// exited.
type ProbeSchedule interface {
	// Start registers the callback for probe dispatch through the scheduling
	// adapter. The runner invokes it once per lifetime; a registration error is
	// returned to startup and does not trigger automatic retry. Dispatch the
	// callback asynchronously so runDue can acquire the state mutex after Start
	// releases it.
	Start(func()) error
	// Stop prevents future dispatch and returns a context whose Done signal closes
	// after scheduled callbacks finish. The runner cancels active probe contexts
	// separately before waiting for that signal.
	Stop() context.Context
}

// ProbeExecutionResult contains one provider test's status and measured
// execution timing.
type ProbeExecutionResult struct {
	Status       string // provider test status string
	LatencyMs    int64  // provider latency in milliseconds
	ErrorMessage string // provider test failure message
	// StartedAt and FinishedAt carry the provider test's recorded interval.
	// runProbeAttempt uses these values when a test result exists, including
	// supplied zero timestamps.
	StartedAt  time.Time // provider test start time
	FinishedAt time.Time // provider test finish time
}

// GroupProbeTarget identifies a selected provider and its group-mapped
// execution model.
type GroupProbeTarget struct {
	// Attributes retains the selected attempt's catalogue and group profile for
	// provider testing. Automatic probes leave it nil and initialize testing normally.
	Attributes *ModelAttributeSession
	// ProtocolRoute carries the selected entry and resolved upstream endpoint.
	// Its zero value keeps legacy automatic tests independent of explicit routing.
	ProtocolRoute provider.TestProtocolRoute
	GroupID       int64 // selected probe group identifier
	ProviderID    int64 // selected provider row ID
	// ModelID is the selector's group-mapped model sent to the test service. That
	// service applies provider mapping; history keeps the configured request model
	// separately.
	ModelID string // group-mapped probe model ID
}

// GroupProbeExecutor selects probe targets and performs requests through
// provider testing.
type GroupProbeExecutor interface {
	// Select chooses a provider for the configured request model under the group's
	// saved rules. It returns the group-mapped model for Test, leaving the
	// original name available for history.
	Select(context.Context, GroupAvailabilityProbeDueGroup, string) (GroupProbeTarget, error)
	// Test executes the selected group, provider, and model with the probe prompt and
	// User-Agent. The runner supplies an attempt deadline, and an error makes the
	// attempt fail even when result metadata is returned.
	Test(context.Context, GroupProbeTarget, string, string) (*ProbeExecutionResult, error)
}

// GroupProbeOptions supplies runner identity, scheduling, time, and diagnostic
// callbacks.
type GroupProbeOptions struct {
	InstanceID string // database lease owner identity
	// Now supplies timestamps for leases, result construction, and next
	// scheduling. Nil uses time.Now through the runner's now method.
	Now      func() time.Time // optional probe time source
	Schedule ProbeSchedule    // probe dispatch scheduling adapter
	// Observe executes synchronously on startup or an admitted round. Its callback
	// must return without reentering lifecycle methods on this runner, because
	// startup holds mu and round diagnostics can precede the idle signal.
	Observe func(string, ...any) // optional synchronous diagnostic observer
}

// GroupAvailabilityProbeRunnerService coordinates one-shot dispatch, serialized
// probe rounds, and shutdown waiting.
type GroupAvailabilityProbeRunnerService struct {
	repo       GroupAvailabilityProbeRepository // probe lease and result store
	executor   GroupProbeExecutor               // probe selection and execution adapter
	options    GroupProbeOptions                // retained probe runtime callbacks
	instanceID string                           // copied lease owner identity
	// lastCleanupAt is used by serialized rounds to throttle maintenance attempts.
	// The timestamp advances before deletion, so a failed cleanup also delays
	// another attempt.
	lastCleanupAt time.Time // last cleanup attempt time
	// runMu admits at most one runDue round per instance. The admitted round
	// retains it while claiming groups, running workers, and updating
	// lastCleanupAt.
	runMu sync.Mutex // serialized probe round mutex
	// mu protects started, stopped, active, ctx, cancel, idle, and scheduleDone.
	// Startup is consumed once, shutdown cancels the shared context, and idle
	// closes after admitted work exits.
	mu           sync.Mutex         // probe lifetime state mutex
	started      bool               // startup registration attempt consumed
	stopped      bool               // later round admission disabled
	active       bool               // admitted round still running
	ctx          context.Context    // runner lifetime cancellation context
	cancel       context.CancelFunc // runner lifetime cancellation function
	idle         chan struct{}      // admitted round completion signal
	scheduleDone context.Context    // scheduled callbacks completion context
}

// NewGroupAvailabilityProbeRunnerService retains storage, execution, and
// scheduling dependencies without starting dispatch. InstanceID is copied from
// options; callers supply a scheduler and executor before Start, and the clock
// defaults to time.Now when queried.
func NewGroupAvailabilityProbeRunnerService(repo GroupAvailabilityProbeRepository, executor GroupProbeExecutor, options GroupProbeOptions) *GroupAvailabilityProbeRunnerService {
	return &GroupAvailabilityProbeRunnerService{repo: repo, executor: executor, options: options, instanceID: options.InstanceID}
}

// now reads the injected probe clock or the current system time. The fallback
// is selected independently on each call, so tests can provide deterministic
// timestamps while production uses time.Now.
func (s *GroupAvailabilityProbeRunnerService) now() time.Time {
	if s.options.Now != nil {
		return s.options.Now()
	}
	return time.Now()
}

// observe sends a probe diagnostic to the optional observation callback. A nil
// callback suppresses reporting, and supplied callbacks run synchronously on
// the lifecycle or round path that produced the message.
func (s *GroupAvailabilityProbeRunnerService) observe(message string, args ...any) {
	if s.options.Observe != nil {
		s.options.Observe(message, args...)
	}
}

// initializeContextLocked creates the runner cancellation context and idle
// signal when first needed. Call while holding mu; the context starts from
// Background, and an existing channel or context remains attached to the same
// runner lifetime.
func (s *GroupAvailabilityProbeRunnerService) initializeContextLocked() {
	if s.ctx == nil {
		s.ctx, s.cancel = context.WithCancel(context.Background())
	}
	if s.idle == nil {
		s.idle = make(chan struct{})
	}
}

// Start attempts to register probe dispatch once for this runner lifetime. A
// nil runner is ignored, and an attempted start is consumed even when
// dependencies are missing or the scheduler rejects registration.
//
// The state mutex serializes this decision with StopContext, so a stopped
// runner cannot register later and concurrent starts cannot add extra
// callbacks. Successful registration installs runDue; its separate round lock
// controls overlapping dispatch. Diagnostic callbacks are invoked synchronously
// while startup still holds the state mutex.
func (s *GroupAvailabilityProbeRunnerService) Start() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopped {
		return
	}
	// Consume registration under mu before dependency checks and scheduler calls,
	// making the whole startup attempt one decision shared with concurrent starts
	// and shutdown.
	s.started = true
	if s.repo == nil || s.executor == nil || s.options.Schedule == nil {
		s.observe("[GroupAvailabilityProbe] not started (missing dependencies)")
		return
	}
	s.initializeContextLocked()
	if err := s.options.Schedule.Start(s.runDue); err != nil {
		s.observe("[GroupAvailabilityProbe] not started (invalid schedule): %v", err)
		return
	}
	s.observe("[GroupAvailabilityProbe] started (tick=every minute)")
}

// beginRun admits a serialized round and returns its runner cancellation
// context. Call after acquiring runMu and pair each true result with exactly
// one endRun before releasing that round lock.
//
// The state mutex orders round admission against shutdown. A round admitted
// before StopContext becomes active work that shutdown cancels and waits for; a
// round arriving after stopped is set receives false. Lazy initialization gives
// direct test calls the same cancellation and idle state as scheduled dispatch.
func (s *GroupAvailabilityProbeRunnerService) beginRun() (context.Context, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return nil, false
	}
	s.initializeContextLocked()
	s.active = true
	return s.ctx, true
}

// endRun marks an admitted round idle and signals a shutdown waiter when work
// has stopped. Call once for each successful beginRun while the round remains
// serialized by runMu.
//
// The state mutex coordinates the final active transition with StopContext.
// Shutdown closes idle itself when no round is active; otherwise this method
// closes it after the round exits. Pairing admission and completion prevents a
// second close and keeps a nil StopContext result dependent on work having
// reached its completion signal.
func (s *GroupAvailabilityProbeRunnerService) endRun() {
	s.mu.Lock()
	defer s.mu.Unlock()
	// An admitted round owns idle while active. Publish its final transition under
	// mu so StopContext cannot also close that same channel during shutdown.
	s.active = false
	if s.stopped {
		close(s.idle)
	}
}

// Stop cancels probe work and waits for scheduled callbacks and the active
// round using a background waiting context. Call StopContext when shutdown
// needs a deadline; this convenience method discards its returned error.
func (s *GroupAvailabilityProbeRunnerService) Stop() {
	// Choose an unbounded waiting context for this convenience entry; teardown
	// callers that need a cancellation source or deadline use StopContext.
	_ = s.StopContext(context.Background())
}

// StopContext prevents future rounds, cancels the runner context, and waits for
// active and scheduled work. A nil runner succeeds, and repeated calls observe
// the same lifetime signals instead of stopping the scheduler again.
//
// The state mutex records stopped before cancellation and dispatch shutdown.
// Channel waiting occurs after releasing the mutex, allowing endRun to publish
// completion. The supplied context bounds those waits; obtaining mu and calling
// Schedule.Stop are synchronous operations that the context cannot preempt.
// If the context wins a completion wait, its error returns while unfinished
// work and its lease may remain. A ready completion can win an equally ready
// deadline; successful return observes idle and any installed scheduler
// completion signal.
func (s *GroupAvailabilityProbeRunnerService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.initializeContextLocked()
	// Close admission before canceling work and stopping dispatch so no later
	// round can start against a shutdown lifetime or register another completion
	// owner.
	if !s.stopped {
		s.stopped = true
		s.cancel()
		if s.started && s.options.Schedule != nil {
			s.scheduleDone = s.options.Schedule.Stop()
		}
		if !s.active {
			close(s.idle)
		}
	}
	// Copy both completion signals while protected, then release mu so the
	// admitted round can call endRun and publish the idle transition.
	idle, scheduled := s.idle, s.scheduleDone
	s.mu.Unlock()
	select {
	case <-idle:
	case <-ctx.Done():
		return ctx.Err()
	}
	if scheduled != nil {
		select {
		case <-scheduled.Done():
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
