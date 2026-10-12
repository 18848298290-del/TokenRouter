package billing

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

// DateRuntime supplies the clock and calendar, defaulting to time.Now and time.Local when omitted.
type DateRuntime struct {
	Now      func() time.Time
	Calendar *timezone.Calendar
}

func (r DateRuntime) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r DateRuntime) calendar() timezone.Calendar {
	if r.Calendar != nil {
		return *r.Calendar
	}
	return timezone.NewCalendar(time.Local)
}
