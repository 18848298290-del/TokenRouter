package qoder

import (
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// output_observation.go classifies Qoder output frames and records downstream
// write failures around the execution sink. Frame inspection supplements the
// converter's semantic and terminal flags before forwarding. Failed Begin or
// Emit calls retain disconnect evidence while the converter can continue
// collecting upstream usage for completion.

// executionOutput supplements frame classification and disconnect evidence around
// one serialized output sink.
type executionOutput struct {
	sink upstream.OutputSink // downstream output sink

	// disconnected records a failed Begin or Emit even when upstream collection
	// continues. Execute copies this observation into completion's
	// client-disconnect evidence.
	disconnected bool // failed downstream write
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

// Emit supplements frame classification before forwarding the event to the sink.
// Existing Semantic and Terminal flags remain set when inspection adds evidence.
// A failed write retains disconnect state for the execution result and returns
// the sink error to the stream converter.
func (o *executionOutput) Emit(event upstream.OutputEvent) error {
	if len(event.Data) > 0 {
		semantic, terminal := qoderOutputMeaning(event.Data)
		event.Semantic = event.Semantic || semantic
		event.Terminal = event.Terminal || terminal
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
