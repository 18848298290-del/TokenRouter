package routing

import (
	"context"
	"sync"
	"time"
)

// ProbeSchedule runs the supplied probe callback on a cron schedule. Stop returns a context canceled when
// scheduled work exits.
type ProbeSchedule interface {
	Start(func()) error
	Stop() context.Context
}

// ProbeExecutionResult contains probe status, latency in milliseconds, timestamps, and an error message.
type ProbeExecutionResult struct {
	Status       string
	LatencyMs    int64
	ErrorMessage string
	StartedAt    time.Time
	FinishedAt   time.Time
}

// GroupProbeTarget contains the selected provider and group-mapped model. The test service applies provider
// mapping.
type GroupProbeTarget struct {
	ProviderID int64
	ModelID    string
}

type GroupProbeExecutor interface {
	Select(context.Context, GroupAvailabilityProbeDueGroup, string) (GroupProbeTarget, error)
	Test(context.Context, int64, string, string, string) (*ProbeExecutionResult, error)
}
type GroupProbeOptions struct {
	InstanceID string
	Now        func() time.Time
	Schedule   ProbeSchedule
	Observe    func(string, ...any)
}

// GroupAvailabilityProbeRunnerService claims groups, retries probes, and saves results. Shutdown blocks new
// rounds and waits for in-flight work.
type GroupAvailabilityProbeRunnerService struct {
	repo          GroupAvailabilityProbeRepository
	executor      GroupProbeExecutor
	options       GroupProbeOptions
	instanceID    string
	lastCleanupAt time.Time
	runMu         sync.Mutex
	mu            sync.Mutex
	started       bool
	stopped       bool
	active        bool
	ctx           context.Context
	cancel        context.CancelFunc
	idle          chan struct{}
	scheduleDone  context.Context
}

func NewGroupAvailabilityProbeRunnerService(repo GroupAvailabilityProbeRepository, executor GroupProbeExecutor, options GroupProbeOptions) *GroupAvailabilityProbeRunnerService {
	return &GroupAvailabilityProbeRunnerService{repo: repo, executor: executor, options: options, instanceID: options.InstanceID}
}

func (s *GroupAvailabilityProbeRunnerService) now() time.Time {
	if s.options.Now != nil {
		return s.options.Now()
	}
	return time.Now()
}

func (s *GroupAvailabilityProbeRunnerService) observe(message string, args ...any) {
	if s.options.Observe != nil {
		s.options.Observe(message, args...)
	}
}

func (s *GroupAvailabilityProbeRunnerService) initializeContextLocked() {
	if s.ctx == nil {
		s.ctx, s.cancel = context.WithCancel(context.Background())
	}
	if s.idle == nil {
		s.idle = make(chan struct{})
	}
}

func (s *GroupAvailabilityProbeRunnerService) Start() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopped {
		return
	}
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

func (s *GroupAvailabilityProbeRunnerService) endRun() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = false
	if s.stopped {
		close(s.idle)
	}
}
func (s *GroupAvailabilityProbeRunnerService) Stop() { _ = s.StopContext(context.Background()) }

// StopContext bounds waiting for cron and in-flight work by ctx. A timeout does not mean the runner has drained.
func (s *GroupAvailabilityProbeRunnerService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.initializeContextLocked()
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
