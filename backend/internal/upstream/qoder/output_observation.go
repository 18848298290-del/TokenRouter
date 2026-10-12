package qoder

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// executionOutput tracks semantic output and disconnects for completion handling, including failed writes.
type executionOutput struct {
	sink          upstream.OutputSink
	started       time.Time
	disconnected  bool
	firstSemantic *time.Duration
}

func (o *executionOutput) Begin(h upstream.OutputHead) error {
	err := o.sink.Begin(h)
	if err != nil {
		o.disconnected = true
	}
	return err
}

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

func qoderOutputMeaning(frame []byte) (bool, bool) { return bridge.CompatOutputMeaning(frame) }

func (o *executionOutput) InitialOutput() upstream.OutputHead {
	if source, ok := o.sink.(interface{ InitialOutput() upstream.OutputHead }); ok {
		return source.InitialOutput()
	}
	return upstream.OutputHead{}
}
