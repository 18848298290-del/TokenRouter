package qoder

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Output observation measures the first semantic event before the downstream
// write is attempted. Failed Begin or Emit calls mark a client disconnect while
// the stream converter can continue reading upstream usage, so completion
// receives both timing and delivery evidence for the same attempt.

// executionOutput observes timing and disconnect evidence around one serialized
// output sink.
type executionOutput struct {
	sink    upstream.OutputSink // downstream output sink
	started time.Time           // attempt start time
	// disconnected records a failed Begin or Emit even when upstream collection
	// continues. Execute copies this observation into completion's
	// client-disconnect evidence.
	disconnected bool // failed downstream write
	// firstSemantic is recorded before the first semantic Emit reaches the sink.
	// Nil means no semantic output has been observed for this attempt.
	firstSemantic *time.Duration // first semantic elapsed time
}

// Begin forwards output headers and records whether the downstream sink rejects
// them. A failed header write marks disconnect before returning its error,
// allowing Execute to retain that observation during completion handling.
func (o *executionOutput) Begin(h upstream.OutputHead) error {
	err := o.sink.Begin(h)
	if err != nil {
		o.disconnected = true
	}
	return err
}

// Emit enriches frame meaning, records first semantic timing, and forwards the
// event. Existing Semantic and Terminal flags remain set when frame inspection
// adds evidence. Timing is recorded before sink emission so failed writes still
// report when upstream content became available.
func (o *executionOutput) Emit(event upstream.OutputEvent) error {
	if len(event.Data) > 0 {
		semantic, terminal := qoderOutputMeaning(event.Data)
		event.Semantic = event.Semantic || semantic
		event.Terminal = event.Terminal || terminal
	}
	if event.Semantic && o.firstSemantic == nil {
		elapsed := time.Since(o.started)
		o.firstSemantic = &elapsed
	}
	err := o.sink.Emit(event)
	if err != nil {
		o.disconnected = true
	}
	return err
}

// qoderOutputMeaning delegates compatibility-frame content and terminal
// classification to the protocol bridge.
func qoderOutputMeaning(frame []byte) (bool, bool) { return bridge.CompatOutputMeaning(frame) }

// InitialOutput returns existing sink commitment metadata when the sink exposes
// it. Sinks lacking the optional source interface produce a zero head, so the
// conversion writer starts from its normal defaults.
func (o *executionOutput) InitialOutput() upstream.OutputHead {
	if source, ok := o.sink.(interface {
		// InitialOutput reports response commitment and headers already selected by
		// the sink. The conversion writer reads that metadata at initialization and
		// retains the status when the response is committed.
		InitialOutput() upstream.OutputHead
	}); ok {
		return source.InitialOutput()
	}
	return upstream.OutputHead{}
}
