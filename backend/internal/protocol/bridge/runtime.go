package bridge

import "time"

// runtime.go defines the clock and random-byte sources used to synthesize
// conversion metadata.
// Response conversion can synthesize timestamps and IDs that the incoming
// protocol did not supply. Callers inject their clock and random reader so
// those generated values follow the attempt's chosen sources; request-only
// conversions do not automatically consume either callback.

// Runtime supplies clock and random callbacks for conversions that synthesize metadata.
// @project-doc docs/architecture/gateway_request_lifecycle.md#protocol_conversion_boundary
type Runtime struct {
	// Now is called by timestamp-producing response/state constructors.
	// Callers supply it for those conversions; invoking a nil callback panics.
	Now func() time.Time
	// ReadRandom fills generated-ID bytes. Some ID helpers discard its count
	// and error, so callers choose a reader that fills the requested buffer
	// when they rely on fresh random identity values.
	ReadRandom func([]byte) (int, error)
}
