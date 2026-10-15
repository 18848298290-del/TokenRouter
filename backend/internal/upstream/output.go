package upstream

import (
	"encoding/json"
	"io"
	"net/http"
)

// output.go adapts synchronous output sinks to response writers with deferred
// headers and per-event commitment markers.
// A codec can prepare response metadata before it writes bytes, while a
// waiting HTTP heartbeat can already commit the underlying response. Output
// contexts keep their own header map and sample optional initial sink state so
// codec output can continue from that earlier commitment. Deferred
// construction postpones that sampling until writer access, allowing waiting
// heartbeats to continue while the adapter is being created.
//
// Beginning output copies headers into the sink once, and subsequent writes
// carry synchronous byte events with separate semantic, retry, and terminal
// markers. A successful begin records commitment even if a later emit fails.
// Pending next-event flags are consumed by one write before its emit result is
// known; errors remain available through Err and block later output attempts.
// Callers serialize access to each mutable context during one conversion.

// deferredOutputWriter delays metadata initialization until the codec accesses
// its output writer.
type deferredOutputWriter struct {
	sink OutputSink // active conversion output sink
	// output is created on first writer access from sink metadata at that time.
	// Callers serialize access so initialization and later writer state stay ordered.
	output OutputWriter // lazily initialized output writer
}

// OutputHead carries status, headers, and commitment metadata supplied by
// output adapters.
type OutputHead struct {
	// Committed records an adapter's existing HTTP commitment. NewOutputContext
	// retains a supplied Status only when Committed is true and Status is nonzero.
	Committed bool // observed adapter commitment state
	Status    int  // pending HTTP response status
	// Header carries adapter metadata. NewOutputContext copies the map values,
	// and sinkWriter.begin supplies another copy when passing headers to Begin.
	Header http.Header // supplied response header values
}

// OutputEvent carries synchronous response bytes and the observations attached
// to their emission.
type OutputEvent struct {
	// Data can borrow a codec buffer for synchronous Emit. A sink retaining
	// these bytes after Emit returns needs to copy their storage first.
	Data []byte // synchronous response payload bytes
	// Flush, Semantic, CommitForRetry, and Terminal describe independent event
	// properties. Gateway tracking can commit HTTP output for a flush while
	// keeping the retry window open until retry-marked payload bytes are emitted.
	Flush    bool // request output buffer flush
	Semantic bool // event contains semantic content
	// CommitForRetry asks tracking to close the attempt retry window.
	// gateway.OutputTracker applies this marker when Data contains payload bytes.
	CommitForRetry bool // requested retry commitment marker
	Terminal       bool // event carries terminal evidence
}

// OutputSink receives response metadata and synchronous payload events from
// platform codecs.
type OutputSink interface {
	// Begin applies response metadata before payload emission and reports a sink
	// failure synchronously. Concrete sinks decide how existing commitment affects
	// a supplied status; callers account for that adapter state.
	Begin(OutputHead) error
	// Emit consumes one payload or flush request and reports its write failure
	// synchronously. Sinks copy borrowed data before retaining it beyond return;
	// callers interpret observations alongside the error.
	Emit(OutputEvent) error
}

// OutputContext exposes one conversion writer and its codec output convenience
// methods.
type OutputContext struct {
	// Writer holds mutable per-conversion metadata and error state. Callers
	// serialize its use and keep the same writer throughout that conversion.
	Writer OutputWriter // active codec output writer
}

// OutputWriter supplies response metadata and ordered synchronous byte output
// to codecs.
type OutputWriter interface {
	io.Writer // synchronous payload write methods
	// Header returns the mutable metadata map used for the pending response.
	// Callers set values before beginning output and follow the concrete writer's
	// synchronization rules while preparing those headers.
	Header() http.Header
	// WriteHeader sets a pending response status before output begins. A concrete
	// writer may retain an already-started status; callers use WriteHeaderNow,
	// Write, or Flush to begin emission.
	WriteHeader(int)
	// WriteHeaderNow begins output with pending metadata before any payload is
	// written. The method has no error result, so callers inspect the owning
	// OutputContext error after use.
	WriteHeaderNow()
	// Written reports recorded output commitment for codec response decisions.
	// A writer can record header commitment before an emit fails, so this marker
	// alone does not establish successful payload delivery.
	Written() bool
	// Flush requests a synchronous flush through the output sink after beginning
	// pending metadata. It has no error result; callers inspect the owning
	// OutputContext error for sink failures.
	Flush()
}

// sinkWriter translates codec writes into sink events with per-conversion
// mutable metadata.
type sinkWriter struct {
	sink OutputSink // active synchronous output sink
	// header and status retain pending metadata until started records the first
	// Begin attempt. written also retains initial adapter commitment, and becomes
	// true after a successful Begin even before payload emission succeeds.
	header  http.Header // mutable pending response headers
	status  int         // pending HTTP response status
	started bool        // sink Begin already attempted
	written bool        // recorded output commitment state
	// err retains the latest Begin or Emit failure. Once nonnil, begin returns
	// it before further sink calls, and OutputContext.Err exposes it to the platform.
	err error // retained output sink error
	// next supplies flags for one future Write. That write consumes the marker
	// before Emit, so an emit failure still leaves this slot empty.
	next *OutputEvent // pending next-write event flags
}

// NewDeferredOutputContext creates a context that samples optional sink
// metadata on first writer access. The sink is borrowed for one conversion;
// callers serialize writer use and inspect Err after operations with no error
// return.
func NewDeferredOutputContext(sink OutputSink) *OutputContext {
	return &OutputContext{Writer: &deferredOutputWriter{sink: sink}}
}

// writer initializes and returns the conversion writer using sink state at
// first access. Later calls reuse that mutable writer; the caller serializes
// access to its metadata and error state.
func (w *deferredOutputWriter) writer() OutputWriter {
	if w.output == nil {
		w.output = NewOutputContext(w.sink).Writer
	}
	return w.output
}

// Header initializes output when needed and returns its mutable response
// header map. Callers edit that map before beginning output and serialize
// access with the conversion writer.
func (w *deferredOutputWriter) Header() http.Header { return w.writer().Header() }

// Write initializes output when needed and sends one byte slice through the
// sink writer. Its synchronous count and error come from that writer, whose
// stored failure can block later writes.
func (w *deferredOutputWriter) Write(data []byte) (int, error) { return w.writer().Write(data) }

// WriteHeader initializes output when needed and delegates a pending status
// update. The underlying writer accepts status changes before its first Begin
// attempt; commitment already recorded by the sink is separate.
func (w *deferredOutputWriter) WriteHeader(status int) { w.writer().WriteHeader(status) }

// WriteHeaderNow initializes output when needed and delegates immediate
// metadata commitment. It reports no error directly; callers inspect
// OutputContext.Err to observe a failure stored by the underlying writer.
func (w *deferredOutputWriter) WriteHeaderNow() { w.writer().WriteHeaderNow() }

// Flush initializes output when needed and delegates sink flushing after
// metadata begins. It reports no error directly; callers inspect
// OutputContext.Err for a stored begin or flush failure.
func (w *deferredOutputWriter) Flush() { w.writer().Flush() }

// Written reports retained writer commitment after initialization, or reads
// optional sink state before that point. This pre-initialization query avoids
// sampling headers; missing sink state returns false while deferred output
// remains uninitialized.
func (w *deferredOutputWriter) Written() bool {
	if w.output != nil {
		return w.output.Written()
	}
	if state, ok := w.sink.(interface {
		// OutputState reports commitment metadata before writer initialization.
		// Implementers return the current Committed flag so this query can inspect
		// output state without requesting headers or initializing codec metadata.
		OutputState() OutputHead
	}); ok {
		return state.OutputState().Committed
	}
	return false
}

// CloneHeader copies a header map and each nonempty value slice into separate
// storage. Nil maps remain nil, while empty value slices become nil; callers
// can modify the returned map and slices independently.
func CloneHeader(src http.Header) http.Header {
	if src == nil {
		return nil
	}
	dst := make(http.Header, len(src))
	for k, vals := range src {
		// Header copying retains the key but normalizes empty value slices to
		// nil. Callers still receive an independently mutable map for their metadata.
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

// NewOutputContext creates a mutable conversion writer with its own header map
// and optional initial sink metadata. Callers provide a usable sink and
// serialize the returned context during one conversion.
//
// Initial header values are copied, and initial commitment is retained
// separately from the first Begin attempt. A nonzero initial status is used
// when that sink is already committed; otherwise status starts at HTTP 200.
// Later begin copies pending headers again before passing them to the sink.
func NewOutputContext(sink OutputSink) *OutputContext {
	header := make(http.Header)
	status := http.StatusOK
	committed := false
	if source, ok := sink.(interface {
		// InitialOutput supplies existing commitment and headers when conversion
		// output initializes. The caller copies header values, so implementers can
		// return current metadata while keeping the source map available to the active
		// adapter.
		InitialOutput() OutputHead
	}); ok {
		head := source.InitialOutput()
		for key, values := range head.Header {
			header[key] = append([]string(nil), values...)
		}
		// Heartbeats may already have committed the response before codec output
		// is constructed. Carry that observation and its nonzero status into this writer.
		committed = head.Committed
		if committed && head.Status != 0 {
			status = head.Status
		}
	}
	return &OutputContext{Writer: &sinkWriter{sink: sink, header: header, status: status, written: committed}}
}

// Header returns the writer-owned mutable response header map for codec
// preparation. Callers set values before begin copies it into the sink and
// serialize access with writes or flushes.
func (w *sinkWriter) Header() http.Header { return w.header }

// Written reports initial adapter commitment or commitment recorded by a
// successful Begin. A later emit error leaves this flag set, so callers
// inspect Err separately when deciding whether output succeeded.
func (w *sinkWriter) Written() bool { return w.written }

// WriteHeader sets the pending status until the first Begin attempt, including
// a failed attempt. Status changes afterward are ignored; writing bytes,
// flushing, or WriteHeaderNow invokes that begin path.
func (w *sinkWriter) WriteHeader(status int) {
	if !w.started {
		w.status = status
	}
}

// begin applies pending metadata to the sink at most once and returns any
// retained output error. A previous Begin or Emit failure prevents another
// sink call.
//
// The started marker is set before invoking Begin, so a failed begin remains
// an attempted start rather than being retried with different metadata. Header
// values are cloned for that call. A successful begin sets written even before
// any payload is emitted; an existing written marker from initial sink state
// remains available throughout.
func (w *sinkWriter) begin() error {
	if w.err != nil {
		return w.err
	}
	if !w.started {
		// Mark the Begin attempt before invoking the sink so later calls retain
		// one metadata start even when that first sink call reports a failure.
		w.started = true
		w.err = w.sink.Begin(OutputHead{Status: w.status, Header: w.header.Clone()})
		if w.err == nil {
			w.written = true
		}
	}
	return w.err
}

// Write begins metadata and emits the supplied bytes with retry commitment
// enabled by default. A pending next-event marker replaces the default flags
// and is consumed before emission.
//
// Begin failure leaves that marker pending; Emit failure returns zero and
// stores the error even if a concrete sink performed partial output. A
// successful emit returns the full slice length. The bytes are borrowed for
// synchronous emission, and callers serialize this write with metadata and
// flag updates.
func (w *sinkWriter) Write(p []byte) (int, error) {
	if err := w.begin(); err != nil {
		return 0, err
	}
	w.written = true
	event := OutputEvent{CommitForRetry: true}
	if w.next != nil {
		// Flags belong to one attempted byte emission. Consume them before Emit
		// so a failed write cannot reuse that classification for a later payload.
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

// Flush begins pending metadata and emits a flush request when begin succeeds.
// It retains a sink error for OutputContext.Err and leaves any pending
// next-write marker available for a later Write.
func (w *sinkWriter) Flush() {
	if w.begin() == nil {
		w.err = w.sink.Emit(OutputEvent{Flush: true})
	}
}

// Header sets one pending response header through the current writer map. It
// can initialize deferred output; callers set headers before begin copies them
// into the sink and serialize metadata access.
func (c *OutputContext) Header(key, value string) { c.Writer.Header().Set(key, value) }

// WriteHeaderNow invokes begin with the pending response metadata and retains
// any sink error. It emits no flush event; callers inspect OutputContext.Err
// because this method has no error result.
func (w *sinkWriter) WriteHeaderNow() { _ = w.begin() }

// Data supplies a missing canonical Content-Type, sets pending status, and
// sends the body through the writer. Statuses below 200, 204, and 304 begin
// headers with no body; output failures are retained for Err.
func (c *OutputContext) Data(status int, contentType string, body []byte) {
	if len(c.Writer.Header()["Content-Type"]) == 0 {
		c.Writer.Header()["Content-Type"] = []string{contentType}
	}
	c.Writer.WriteHeader(status)
	// These HTTP statuses carry no response body through this codec path.
	// Begin headers now so a bodyless result still reaches the sink with its status.
	if status < 200 || status == http.StatusNoContent || status == http.StatusNotModified {
		c.Writer.WriteHeaderNow()
		return
	}
	// Convenience output discards the synchronous count and error result.
	// The sink writer retains failures for Err so platform readers can decide cleanup.
	_, _ = c.Writer.Write(body)
}

// NextEvent sets semantic and terminal flags on the next sinkWriter Write,
// also enabling retry commitment. It initializes a deferred writer when
// needed; other writer implementations ignore the request, and later calls
// replace pending flags.
func (c *OutputContext) NextEvent(semantic, terminal bool) {
	if writer, ok := c.Writer.(*deferredOutputWriter); ok {
		(&OutputContext{Writer: writer.writer()}).NextEvent(semantic, terminal)
		return
	}
	if writer, ok := c.Writer.(*sinkWriter); ok {
		writer.next = &OutputEvent{Semantic: semantic, Terminal: terminal, CommitForRetry: true}
	}
}

// Status delegates a pending response status update to the conversion writer.
// It can initialize deferred output; sinkWriter accepts the value before its
// first Begin attempt and ignores changes afterward.
func (c *OutputContext) Status(status int) { c.Writer.WriteHeader(status) }

// JSON marshals value and sends it through Data with the JSON content type.
// Marshaling failure requests header output for the supplied status; an
// already-started sinkWriter retains its current status. The encoding error
// is discarded, and Err exposes sink failures.
func (c *OutputContext) JSON(status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		// Encoding failure leaves no JSON payload to emit. Request metadata
		// output so a pending status can still reach the sink through its Begin
		// method.
		c.Writer.WriteHeader(status)
		c.Writer.WriteHeaderNow()
		return
	}
	c.Data(status, "application/json; charset=utf-8", body)
}

// Err returns the stored Begin or Emit failure from this context's sink
// writer. Uninitialized deferred output and other writer implementations
// return nil; platforms decide whether to continue reading or cancel after an
// error.
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
