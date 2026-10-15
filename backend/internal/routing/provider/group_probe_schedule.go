package provider

import (
	"context"
	"time"

	"github.com/robfig/cron/v3"
)

// group_probe_schedule.go schedules availability-probe callbacks through cron
// and exposes callback completion on shutdown.
// Availability probes run on minute boundaries in the caller's selected
// timezone. Cron owns job dispatch and completion tracking; the routing runner
// guards repeated startup and overlapping probe rounds, because adding a
// callback here registers another job.

// GroupProbeSchedule adapts minute cron dispatch and completion waiting for
// routing probes.
type GroupProbeSchedule struct {
	// cron owns the registered callbacks and tracks their completion. The routing
	// runner controls registration, cancellation, and waiting for its probe work.
	cron *cron.Cron
}

// NewGroupProbeSchedule prepares a five-field cron scheduler in location,
// leaving dispatch stopped. Supply a valid timezone location; the application
// resolves configured zones before constructing the probe runner.
func NewGroupProbeSchedule(location *time.Location) *GroupProbeSchedule {
	return &GroupProbeSchedule{cron: cron.New(cron.WithParser(cron.NewParser(cron.Minute|cron.Hour|cron.Dom|cron.Month|cron.Dow)), cron.WithLocation(location))}
}

// Start registers run for every minute and starts cron dispatch. Each call adds
// another callback, so the owning runner guards repeated calls and supplies its
// own overlap and cancellation controls.
func (s *GroupProbeSchedule) Start(run func()) error {
	if _, err := s.cron.AddFunc("* * * * *", run); err != nil {
		return err
	}
	s.cron.Start()
	return nil
}

// Stop halts future cron dispatch and returns a context that completes after
// running callbacks finish. The caller cancels probe work separately and waits
// on the returned context within its shutdown budget.
func (s *GroupProbeSchedule) Stop() context.Context { return s.cron.Stop() }
