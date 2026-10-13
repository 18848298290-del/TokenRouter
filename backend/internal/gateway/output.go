package gateway

import "github.com/TokenFlux/TokenRouter/internal/upstream"

// HTTP commitment, retry commitment, and semantic output can occur at
// different times during a request. An emitted keepalive can commit HTTP while
// leaving provider recovery open; an output event closes that window only when
// its nonempty data requests retry commitment.
//
// The tracker retains HTTP and semantic observations across attempts but
// clears the per-attempt commitment flag at each new execution. Event flags
// are recorded before the sink call, so even a failed downstream write retains
// the attempted commitment and disconnect evidence. The request loop
// serializes tracker access and uses the per-attempt flag for replay
// decisions, while sink-provided initial output can report earlier HTTP
// commitment.

// OutputTracker tracks HTTP commitment, attempt retry eligibility, and
// semantic output separately; keepalives can leave retries open.
type OutputTracker struct {
	Sink upstream.OutputSink // synchronous downstream event receiver
	// HTTPCommitted and AttemptCommitted track different recovery decisions. Data
	// or Flush sets the former, while nonempty CommitForRetry data sets the latter;
	// BeginAttempt clears only the attempt flag before another execution begins.
	HTTPCommitted    bool // accumulated HTTP output commitment
	AttemptCommitted bool // current attempt retry commitment
	Semantic         bool // accumulated semantic event observation
	Disconnected     bool // downstream sink returned an error
}

// BeginAttempt clears retry commitment for the new provider attempt and
// retains known HTTP commitment. A sink implementing InitialOutput contributes
// its earlier committed state; callers serialize this tracker across attempts.
func (o *OutputTracker) BeginAttempt() {
	o.AttemptCommitted = false
	o.HTTPCommitted = o.HTTPCommitted || o.InitialOutput().Committed
}

// Begin passes response headers to the sink and records a disconnect when that
// call fails. Header delivery alone does not update commitment flags; callers
// use Emit or initial-output evidence for those observations.
func (o *OutputTracker) Begin(h upstream.OutputHead) error {
	err := o.Sink.Begin(h)
	if err != nil {
		o.Disconnected = true
	}
	return err
}

// Emit forwards one event after accumulating its commitment and semantic
// flags. Callers supply a usable sink and serialize emission with
// BeginAttempt; an error records disconnect evidence while retaining the
// event's flags.
//
// Data or a flush marks HTTP committed, but retry commitment additionally
// requires nonempty data with CommitForRetry. Semantic is an independent event
// observation and can be set for an empty event. Updating before the sink call
// keeps a failed write from reopening the attempt for replay merely because
// downstream delivery returned an error.
func (o *OutputTracker) Emit(e upstream.OutputEvent) error {
	// Record the event's commitment before downstream delivery, so a sink error
	// cannot make a retryable replay ignore output that was already attempted.
	if len(e.Data) > 0 || e.Flush {
		o.HTTPCommitted = true
	}
	if len(e.Data) > 0 && e.CommitForRetry {
		o.AttemptCommitted = true
	}
	if e.Semantic {
		o.Semantic = true
	}
	err := o.Sink.Emit(e)
	if err != nil {
		o.Disconnected = true
	}
	return err
}

// InitialOutput returns initial response evidence supplied by a supporting
// sink. Other sinks receive a head containing the tracker's current HTTP
// commitment; callers should treat sink-provided evidence as authoritative for
// that query.
func (o *OutputTracker) InitialOutput() upstream.OutputHead {
	if source, ok := o.Sink.(interface {
		// InitialOutput reports commitment from output emitted before this tracker.
		InitialOutput() upstream.OutputHead
	}); ok {
		return source.InitialOutput()
	}
	return upstream.OutputHead{Committed: o.HTTPCommitted}
}
