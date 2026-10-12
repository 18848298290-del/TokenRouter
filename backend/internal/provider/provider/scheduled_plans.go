package provider

import (
	"context"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// scheduledTestParser accepts minute, hour, day, month, and weekday fields.
var scheduledTestParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// ScheduledCron registers a per-minute job on Start; Stop permanently prevents another start.
type ScheduledCron struct {
	mu               sync.Mutex
	cron             *cron.Cron
	started, stopped bool
	done             context.Context
}

func NextScheduledTestRun(expression string, from time.Time) (time.Time, error) {
	schedule, err := scheduledTestParser.Parse(expression)
	if err != nil {
		return time.Time{}, err
	}
	return schedule.Next(from), nil
}

func NewScheduledCron(location *time.Location) provider.ScheduledTestSchedule {
	return &ScheduledCron{cron: cron.New(cron.WithParser(scheduledTestParser), cron.WithLocation(location))}
}

func (s *ScheduledCron) Start(ctx context.Context, run func()) error {
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

func (s *ScheduledCron) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.stopped {
		s.stopped = true
		s.done = s.cron.Stop()
	}
	done := s.done
	s.mu.Unlock()
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
