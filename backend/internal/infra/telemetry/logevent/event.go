package logevent

import "time"

// event.go defines structured events forwarded to the Ops sink after logging
// accepts them. That sink reads event fields asynchronously, prefers a
// nonempty component attribute, and skips events carrying the boolean skip
// marker; producers leave published mutable values unchanged.

// OpsSystemLogSkipField names the boolean attribute that asks the Ops system
// log sink to skip indexing this accepted event while other outputs still receive it.
const OpsSystemLogSkipField = "ops_system_log_skip"

// LogEvent carries an accepted structured log entry and its attributes to a sink.
type LogEvent struct {
	Time  time.Time // log entry observation time
	Level string    // lowercase log severity text
	// Component identifies the logger source; the Ops sink prefers a nonempty
	// Fields component attribute and defaults an empty final component to app.
	Component  string // component or logger name
	Message    string // rendered log message text
	LoggerName string // structured logger name text
	// Fields and mutable values remain unchanged after publication because the
	// Ops sink can retain this event for later asynchronous batch processing.
	Fields map[string]any // published structured attribute values
}
