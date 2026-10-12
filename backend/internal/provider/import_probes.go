package provider

import (
	"context"
	"errors"
	"sync"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

const (
	grokImportProbeTimeout    = 25 * time.Second
	grokImportProbeQueueLimit = 64
)

var ErrImportProbeStopped = errors.New("provider import probes are stopped")

// GrokImportProbeResult holds an observation summary suitable for import logs.
type GrokImportProbeResult struct {
	Model           string
	StatusCode      int
	HeadersObserved bool
}
type GrokImportProbeOptions struct {
	Concurrency              int
	Timeout                  time.Duration
	Debug, Info, Warn, Error func(string, ...any)
}

type GrokImportProber interface {
	QueryQuota(ctx context.Context, providerID int64) (*GrokImportProbeResult, error)
}
type grokImportProbeTask struct {
	prober     GrokImportProber
	providerID int64
}
type GrokImportProbeScheduler struct {
	options     GrokImportProbeOptions
	stopped     bool
	activity    operationActivity
	mu          sync.Mutex
	queue       []grokImportProbeTask
	pending     map[int64]struct{}
	inFlight    map[int64]struct{}
	concurrency int
	workers     int
	maxWorkers  int
	timeout     time.Duration
}

func NewGrokImportProbeScheduler(options GrokImportProbeOptions) *GrokImportProbeScheduler {
	concurrency, timeout := options.Concurrency, options.Timeout
	if options.Debug == nil {
		options.Debug = func(string, ...any) {}
	}
	if options.Info == nil {
		options.Info = func(string, ...any) {}
	}
	if options.Warn == nil {
		options.Warn = func(string, ...any) {}
	}
	if options.Error == nil {
		options.Error = func(string, ...any) {}
	}
	if concurrency <= 0 {
		concurrency = 1
	}
	if timeout <= 0 {
		timeout = grokImportProbeTimeout
	}
	return &GrokImportProbeScheduler{
		options:     options,
		concurrency: concurrency,
		timeout:     timeout,
		pending:     make(map[int64]struct{}),
		inFlight:    make(map[int64]struct{}),
	}
}

func (s *GrokImportProbeScheduler) Schedule(prober GrokImportProber, provider *ProviderSnapshot) {
	if s == nil || prober == nil || provider == nil || provider.ID <= 0 {
		return
	}
	if provider.Platform != PlatformGrok || provider.Type != ProviderTypeOAuth {
		return
	}

	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	if _, exists := s.pending[provider.ID]; exists {
		s.mu.Unlock()
		return
	}
	if _, exists := s.inFlight[provider.ID]; exists {
		s.mu.Unlock()
		return
	}
	if len(s.queue) >= grokImportProbeQueueLimit {
		s.mu.Unlock()
		s.options.Debug("grok_import_active_probe_dropped", "provider_id", provider.ID, "reason", "queue_full")
		return
	}
	s.queue = append(s.queue, grokImportProbeTask{prober: prober, providerID: provider.ID})
	s.pending[provider.ID] = struct{}{}
	if s.workers < s.concurrency {
		s.workers++
		if s.workers > s.maxWorkers {
			s.maxWorkers = s.workers
		}
		ctx, done, err := s.activity.begin(context.Background(), ErrImportProbeStopped)
		if err != nil {
			s.workers--
			s.mu.Unlock()
			return
		}
		go func() { defer done(); s.worker(ctx) }()
	}
	s.mu.Unlock()
}

func (s *GrokImportProbeScheduler) worker(ctx context.Context) {
	for {
		task, ok := s.nextTask()
		if !ok {
			return
		}
		s.run(ctx, task.prober, task.providerID)
		s.finish(task.providerID)
	}
}

func (s *GrokImportProbeScheduler) nextTask() (grokImportProbeTask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || len(s.queue) == 0 {
		s.workers--
		return grokImportProbeTask{}, false
	}
	task := s.queue[0]
	s.queue[0] = grokImportProbeTask{}
	s.queue = s.queue[1:]
	if len(s.queue) == 0 {
		s.queue = nil
	}
	delete(s.pending, task.providerID)
	s.inFlight[task.providerID] = struct{}{}
	return task, true
}

func (s *GrokImportProbeScheduler) finish(providerID int64) {
	s.mu.Lock()
	delete(s.inFlight, providerID)
	s.mu.Unlock()
}

func (s *GrokImportProbeScheduler) run(parent context.Context, prober GrokImportProber, providerID int64) {
	if parent.Err() != nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			s.options.Error(
				"grok_import_active_probe_panic",
				"provider_id", providerID,
				"recovery_type", panicType(recovered),
			)
		}
	}()

	// The probe budget starts after dequeue and covers preparation, waiting, and the upstream request.
	ctx, cancel := context.WithTimeout(parent, s.timeout)
	defer cancel()
	result, err := prober.QueryQuota(ctx, providerID)
	if err != nil {
		s.options.Warn(
			"grok_import_active_probe_failed",
			"provider_id", providerID,
			"status", int(infraerrors.FromError(err).Code),
			"reason", infraerrors.Reason(err),
		)
		return
	}
	if result == nil {
		s.options.Warn(
			"grok_import_active_probe_failed",
			"provider_id", providerID,
			"reason", "empty_result",
		)
		return
	}

	s.options.Info(
		"grok_import_active_probe_completed",
		"provider_id", providerID,
		"model", result.Model,
		"status", result.StatusCode,
		"headers_observed", result.HeadersObserved,
	)
}

func panicType(value any) string {
	switch value.(type) {
	case string:
		return "string"
	case error:
		return "error"
	default:
		return "unknown"
	}
}

// StopContext discards queued probes, cancels in-flight workers, and waits within the caller's budget.
func (s *GrokImportProbeScheduler) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.stopped = true
	s.queue = nil
	clear(s.pending)
	s.mu.Unlock()
	return s.activity.stop(ctx, "provider import probes")
}
