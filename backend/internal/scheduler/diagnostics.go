package scheduler

// diagnostics.go forwards scheduler events and formatted messages to optional
// application callbacks.
// Scheduler diagnostics are delivered through callbacks supplied by
// application assembly. Optional callbacks let the same scheduling operations
// run in tests or instances without a logging sink, while retaining the event
// names and argument values chosen by callers.

// Diagnostics holds optional application callbacks for scheduler events and
// formatted messages.
type Diagnostics struct {
	// Event receives structured fields synchronously when configured.
	// Callers and the injected sink determine field ownership and any
	// required synchronization.
	Event func(level, event string, fields ...any)
	// Logf formats scoped messages when configured; a nil callback
	// discards them without altering the scheduler operation that produced
	// the message.
	Logf func(scope, format string, args ...any)
}

// printf sends a scoped formatted message to the optional Logf callback. A nil
// callback discards the message, and a configured callback executes
// synchronously with the supplied arguments.
func (d Diagnostics) printf(scope, format string, args ...any) {
	if d.Logf != nil {
		d.Logf(scope, format, args...)
	}
}

// event sends a structured observation to the optional Event callback. A nil
// callback discards the observation, and a configured callback receives the
// level, name, and fields synchronously.
func (d Diagnostics) event(level, event string, fields ...any) {
	if d.Event != nil {
		d.Event(level, event, fields...)
	}
}
