package provider

import (
	"context"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Scheduled provider tests use five-field cron expressions and a per-minute
// dispatcher. A scheduler starts idle and registers its dispatcher on the first
// successful Start; stopping before that call closes the lifecycle just as
// stopping an active dispatcher does.
//
// Start and Stop serialize lifecycle changes under one mutex. The first Stop
// retains cron's completion context, and later calls wait on that same context
// with their own cancellation budgets. Job completion is checked before waiting
// on caller cancellation, so a scheduler that is already drained can report
// success even when the stop context has expired. A stopped instance cannot
// register another dispatcher.

// scheduledTestParser restricts scheduled provider expressions to minute, hour,
// day, month, and weekday fields.
var scheduledTestParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// ScheduledCron registers and drains the per-minute scheduled-provider
// dispatcher under one lifecycle lock.
type ScheduledCron struct {
	// mu protects cron registration, started, stopped, and done. Stop records the
	// completion context once, and both lifecycle methods inspect the flags under
	// this lock.
	mu               sync.Mutex // schedule lifecycle lock
	cron             *cron.Cron // per-minute job scheduler
	started, stopped bool       // started: job added; stopped: startup closed
	// done is captured by the first Stop and shared by later drain waits. It stays
	// nil until stopping begins.
	done context.Context
}

// NextScheduledTestRun parses a five-field expression and returns its next time
// after from. Parse failures return an error; the supplied time carries the
// location used by cron when the expression has no separate timezone.
func NextScheduledTestRun(expression string, from time.Time) (time.Time, error) {
	schedule, err := scheduledTestParser.Parse(expression)
	if err != nil {
		return time.Time{}, err
	}
	return schedule.Next(from), nil
}

// NewScheduledCron creates an idle provider schedule in the supplied location
// with the five-field parser. The caller starts job registration through Start
// and later waits for running jobs through Stop.
func NewScheduledCron(location *time.Location) provider.ScheduledTestSchedule {
	return &ScheduledCron{cron: cron.New(cron.WithParser(scheduledTestParser), cron.WithLocation(location))}
}

// Start registers the per-minute callback and starts cron once for this
// instance. Repeated calls after a successful start or any stop return nil;
// cancellation is checked before registration for an idle instance.
//
// The lifecycle mutex keeps registration and the start flag together, so a
// concurrent Stop cannot drain one scheduler while another dispatcher is being
// installed. ctx controls the startup check; the callback itself receives no
// context and uses its owning service's lifecycle handling for cancellation.
func (s *ScheduledCron) Start(ctx context.Context, run func()) error {
	// Keep callback registration and the started flag under one lock, so a stop
	// cannot close the lifecycle between adding the dispatcher and starting cron.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.started {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := s.cron.AddFunc("* * * * *", run); err != nil {
		return err
	}
	s.started = true
	s.cron.Start()
	return nil
}

// Stop prevents further startup and waits for cron jobs to finish. The first
// call obtains cron's completion context under the lifecycle mutex, and
// subsequent calls reuse it while applying their own ctx budget.
//
// Completion is checked before the cancellable wait, so an already drained
// schedule succeeds despite an expired caller context. Cancellation during an
// unfinished drain returns ctx.Err and leaves the saved completion signal
// intact; another call can continue waiting for those same jobs.
func (s *ScheduledCron) Stop(ctx context.Context) error {
	// Publish the stop barrier and retain one cron completion context before
	// releasing the lock; concurrent Start and Stop calls then observe the same
	// closed lifecycle.
	s.mu.Lock()
	if !s.stopped {
		s.stopped = true
		s.done = s.cron.Stop()
	}
	done := s.done
	s.mu.Unlock()
	// A completed drain takes precedence over an already cancelled caller budget.
	// Check completion first so a finished scheduler reports a stable successful
	// result.
	select {
	case <-done.Done():
		return nil
	default:
	}
	select {
	case <-done.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
