package egress

// Diagnostics holds a caller-supplied logging function.
type Diagnostics struct {
	Logf func(component, format string, args ...any)
}

// Log forwards the component and formatting arguments when a logger is configured.
func (d Diagnostics) Log(component, format string, args ...any) {
	if d.Logf != nil {
		d.Logf(component, format, args...)
	}
}

// diagnosticsOption returns the first option, or a zero value when none is supplied.
func diagnosticsOption(options []Diagnostics) Diagnostics {
	if len(options) > 0 {
		return options[0]
	}
	return Diagnostics{}
}
