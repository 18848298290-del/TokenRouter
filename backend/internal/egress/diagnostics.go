package egress

// Diagnostic messages carry a component name and printf arguments to the logger
// supplied by the application. A zero Diagnostics value discards messages,
// allowing policy and service helpers to use the same logging call when no sink
// is configured.

// Diagnostics forwards formatted component messages to an optional application
// logger.
type Diagnostics struct {
	// Logf runs synchronously in the calling goroutine and receives the supplied
	// argument values. The application chooses formatting, concurrency support,
	// and handling of mutable arguments.
	Logf func(component, format string, args ...any) // optional formatted message sink
}

// Log sends one formatted message to the configured component logger. When Logf
// is nil, the call has no effect; a configured sink runs synchronously and
// controls formatting and argument handling.
func (d Diagnostics) Log(component, format string, args ...any) {
	if d.Logf != nil {
		d.Logf(component, format, args...)
	}
}

// diagnosticsOption selects the first diagnostics option supplied to a
// constructor. An empty slice selects an unset logger, and later options do not
// contribute to the returned value.
func diagnosticsOption(options []Diagnostics) Diagnostics {
	if len(options) > 0 {
		return options[0]
	}
	return Diagnostics{}
}
