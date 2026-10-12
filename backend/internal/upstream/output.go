package upstream

import (
	"encoding/json"
	"io"
	"net/http"
)

type deferredOutputWriter struct {
	sink   OutputSink
	output OutputWriter
}

// OutputHead contains response metadata. OutputContext clones headers when passing them to the sink.
type OutputHead struct {
	// Committed records whether the output adapter has committed HTTP headers.
	Committed bool
	Status    int
	Header    http.Header
}

// OutputEvent is one output chunk; sink write and flush errors return synchronously.
type OutputEvent struct {
	Data           []byte
	Flush          bool
	Semantic       bool
	CommitForRetry bool
	Terminal       bool
}

// OutputSink owns writes and flushes for HTTP or other output adapters.
type OutputSink interface {
	Begin(OutputHead) error
	Emit(OutputEvent) error
}

// OutputContext supplies byte-oriented output for one conversion.
type OutputContext struct{ Writer OutputWriter }

// OutputWriter emits through the sink in codec call order.
type OutputWriter interface {
	io.Writer
	Header() http.Header
	WriteHeader(int)
	WriteHeaderNow()
	Written() bool
	Flush()
}

type sinkWriter struct {
	sink    OutputSink
	header  http.Header
	status  int
	started bool
	written bool
	err     error
	next    *OutputEvent
}

// NewDeferredOutputContext loads initial response metadata on first writer access.
func NewDeferredOutputContext(sink OutputSink) *OutputContext {
	return &OutputContext{Writer: &deferredOutputWriter{sink: sink}}
}

func (w *deferredOutputWriter) writer() OutputWriter {
	if w.output == nil {
		w.output = NewOutputContext(w.sink).Writer
	}
	return w.output
}
func (w *deferredOutputWriter) Header() http.Header            { return w.writer().Header() }
func (w *deferredOutputWriter) Write(data []byte) (int, error) { return w.writer().Write(data) }
func (w *deferredOutputWriter) WriteHeader(status int)         { w.writer().WriteHeader(status) }
func (w *deferredOutputWriter) WriteHeaderNow()                { w.writer().WriteHeaderNow() }
func (w *deferredOutputWriter) Flush()                         { w.writer().Flush() }
func (w *deferredOutputWriter) Written() bool {
	if w.output != nil {
		return w.output.Written()
	}
	if state, ok := w.sink.(interface{ OutputState() OutputHead }); ok {
		return state.OutputState().Committed
	}
	return false
}

// CloneHeader copies the map and each value slice.
func CloneHeader(src http.Header) http.Header {
	if src == nil {
		return nil
	}
	dst := make(http.Header, len(src))
	for k, vals := range src {
		if len(vals) == 0 {
			dst[k] = nil
			continue
		}
		copied := make([]string, len(vals))
		copy(copied, vals)
		dst[k] = copied
	}
	return dst
}

// NewOutputContext creates per-conversion metadata. Callers must not reuse it across requests.
func NewOutputContext(sink OutputSink) *OutputContext {
	header := make(http.Header)
	status := http.StatusOK
	committed := false
	if source, ok := sink.(interface{ InitialOutput() OutputHead }); ok {
		head := source.InitialOutput()
		for key, values := range head.Header {
			header[key] = append([]string(nil), values...)
		}
		committed = head.Committed
		if committed && head.Status != 0 {
			status = head.Status
		}
	}
	return &OutputContext{Writer: &sinkWriter{sink: sink, header: header, status: status, written: committed}}
}

func (w *sinkWriter) Header() http.Header { return w.header }
func (w *sinkWriter) Written() bool       { return w.written }
func (w *sinkWriter) WriteHeader(status int) {
	if !w.started {
		w.status = status
	}
}

func (w *sinkWriter) begin() error {
	if w.err != nil {
		return w.err
	}
	if !w.started {
		w.started = true
		w.err = w.sink.Begin(OutputHead{Status: w.status, Header: w.header.Clone()})
		if w.err == nil {
			w.written = true
		}
	}
	return w.err
}

func (w *sinkWriter) Write(p []byte) (int, error) {
	if err := w.begin(); err != nil {
		return 0, err
	}
	w.written = true
	event := OutputEvent{CommitForRetry: true}
	if w.next != nil {
		event = *w.next
		w.next = nil
	}
	event.Data = p
	w.err = w.sink.Emit(event)
	if w.err != nil {
		return 0, w.err
	}
	return len(p), nil
}

func (w *sinkWriter) Flush() {
	if w.begin() == nil {
		w.err = w.sink.Emit(OutputEvent{Flush: true})
	}
}

// Header sets a pending header for the output sink.
func (c *OutputContext) Header(key, value string) { c.Writer.Header().Set(key, value) }

// WriteHeaderNow commits through sink.Begin; Flush requests flushing separately.
func (w *sinkWriter) WriteHeaderNow() { _ = w.begin() }

// Data sets a missing Content-Type and writes the body, or only commits headers for bodyless status codes.
func (c *OutputContext) Data(status int, contentType string, body []byte) {
	if len(c.Writer.Header()["Content-Type"]) == 0 {
		c.Writer.Header()["Content-Type"] = []string{contentType}
	}
	c.Writer.WriteHeader(status)
	if status < 200 || status == http.StatusNoContent || status == http.StatusNotModified {
		c.Writer.WriteHeaderNow()
		return
	}
	_, _ = c.Writer.Write(body)
}

// NextEvent sets semantic and terminal flags for the next synchronous write.
func (c *OutputContext) NextEvent(semantic, terminal bool) {
	if writer, ok := c.Writer.(*deferredOutputWriter); ok {
		(&OutputContext{Writer: writer.writer()}).NextEvent(semantic, terminal)
		return
	}
	if writer, ok := c.Writer.(*sinkWriter); ok {
		writer.next = &OutputEvent{Semantic: semantic, Terminal: terminal, CommitForRetry: true}
	}
}

// Status sets the pending status code; the next write commits it.
func (c *OutputContext) Status(status int) { c.Writer.WriteHeader(status) }

// JSON encodes and sends a response through Data. Encoding errors commit the status without a body.
func (c *OutputContext) JSON(status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		c.Writer.WriteHeader(status)
		c.Writer.WriteHeaderNow()
		return
	}
	c.Data(status, "application/json; charset=utf-8", body)
}

// Err returns the output error. The platform decides whether to keep reading or cancel.
func (c *OutputContext) Err() error {
	if writer, ok := c.Writer.(*deferredOutputWriter); ok {
		if writer.output == nil {
			return nil
		}
		return (&OutputContext{Writer: writer.output}).Err()
	}
	if writer, ok := c.Writer.(*sinkWriter); ok {
		return writer.err
	}
	return nil
}
