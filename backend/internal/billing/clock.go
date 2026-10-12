package billing

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

// Billing timestamps and calendar resets need separate inputs: a clock
// chooses the observation time, while a calendar chooses local date
// boundaries. Injected values support service-specific time and timezone
// selection; omitted values use the system clock and local calendar.

// DateRuntime supplies observation timestamps and date boundaries to billing services.
type DateRuntime struct {
	// Now supplies current timestamps when configured. A nil function selects
	// time.Now whenever the runtime helper is called, so service construction
	// does not freeze the observation time.
	Now func() time.Time
	// Calendar supplies date-boundary calculations from the configured value.
	// A nil pointer selects a calendar using time.Local; calendar returns
	// a value copy while retaining that calendar's location and clock inputs.
	Calendar *timezone.Calendar
}

// now returns the configured clock observation or the current system time.
// A nil Now function selects time.Now at the call, preserving independent
// observations for successive billing operations.
func (r DateRuntime) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// calendar returns the configured calendar value or a system-local
// calendar. A supplied pointer is dereferenced into a value copy; its
// location and clock inputs remain those chosen by the caller.
func (r DateRuntime) calendar() timezone.Calendar {
	if r.Calendar != nil {
		return *r.Calendar
	}
	return timezone.NewCalendar(time.Local)
}
