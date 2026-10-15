package httpapi

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// search_output.go writes serialized search events and JSON responses through
// the active Gin response writer.
// Search tool emulation presents serialized events through the forwarding
// request's Gin writer. Stream setup prepares status and buffering headers,
// each accepted event write flushes, and the JSON path sends an already
// encoded body under application/json.

// SearchOutput adapts the active Gin response to the searchtools.Output
// writer interface.
type SearchOutput struct {
	// Context retains the forwarding request and writer. Call output methods
	// synchronously while that request is usable; the adapter neither snapshots
	// nor synchronizes Gin state.
	Context *gin.Context // active search output request
}

// StartStream prepares a status-200 SSE response with cache and
// proxy-buffering restrictions. It requires the bound Gin writer and runs
// before event output; Gin commits the prepared status when subsequent
// writing or flushing begins.
func (o SearchOutput) StartStream() {
	// Prepare incremental search output before the first event; cache and
	// proxy-buffering directives keep compatible intermediaries from retaining
	// completed blocks until the entire message ends.
	header := o.Context.Writer.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	o.Context.Writer.WriteHeader(http.StatusOK)
}

// WriteEvent writes one named SSE event and flushes after a successful write.
// Callers supply a single-line event label and already serialized single-line
// data; a write failure returns wrapped error evidence and skips this
// method's flush.
func (o SearchOutput) WriteEvent(event string, data []byte) error {
	// The searchtools event builder has already marshaled JSON. Write those
	// bytes verbatim in the SSE data line so escaped payload characters do not
	// undergo another encoding pass.
	if _, err := fmt.Fprintf(o.Context.Writer, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	o.Context.Writer.Flush()
	return nil
}

// WriteJSON presents already encoded search output as status-200
// application/json. Callers keep the bound Gin request usable and pass the
// complete response bytes; explicit final flushing remains available through
// Flush.
func (o SearchOutput) WriteJSON(data []byte) { o.Context.Data(http.StatusOK, "application/json", data) }

// Flush asks the bound Gin writer to flush current search output. It has no
// delivery verdict or returned error, and search emulation can call it after
// an earlier event write failed.
func (o SearchOutput) Flush() { o.Context.Writer.Flush() }
