package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Upstream execution writes synchronous events through an HTTP response sink.
// Initial output capture obtains headers before commitment state because
// keepalive-aware Header access can stop and join a pending heartbeat; later
// state checks avoid that handoff while observing status and commitment.

// MessagesOutput supplies synchronous HTTP output and request observations.
// Executors read identity, routing, body, and funding state through execution.Request.
type MessagesOutput struct {
	Concurrency  *ConcurrencyHelper // shared HTTP capacity helper
	ResponseSink                    // embedded synchronous output sink
	HTTP         *gin.Context       // current Gin request context
	Log          *zap.Logger        // request-scoped diagnostic logger
	// StreamStarted is shared with queue and concurrency observers. Those
	// callbacks set it when wait output starts, and callers read it when choosing
	// later stream error presentation.
	StreamStarted *bool
}

// ResponseSink writes synchronous upstream events to the client and flushes when requested.
type ResponseSink struct {
	Writer http.ResponseWriter // required downstream HTTP writer
}

// Begin copies response header value slices and writes the supplied status
// synchronously. The bound writer is required; its own commitment rules decide
// whether a status write takes effect, while input header slices remain
// separate from the output map.
func (s ResponseSink) Begin(head upstream.OutputHead) error {
	for key, values := range head.Header {
		s.Writer.Header()[key] = append([]string(nil), values...)
	}
	s.Writer.WriteHeader(head.Status)
	return nil
}

// Emit writes nonempty event bytes and returns a writer error before flushing.
// It ignores the returned byte count, so a short write with nil error is not
// detected here. A requested flush requires http.Flusher; callers retain event
// storage and choose how a failed write affects upstream consumption.
func (s ResponseSink) Emit(event upstream.OutputEvent) error {
	if len(event.Data) > 0 {
		if _, err := s.Writer.Write(event.Data); err != nil {
			return err
		}
	}
	if event.Flush {
		if flusher, ok := s.Writer.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	return nil
}

// InitialOutput clones current headers before reading writer commitment and
// status. Header access can stop a waiting keepalive writer, so reading state
// afterward includes that handoff's final commitment; the returned header map
// and value slices are independent.
func (s ResponseSink) InitialOutput() upstream.OutputHead {
	// Header access can stop and join a keepalive before returning its map.
	// Read commitment afterward so the captured state includes that final heartbeat.
	headers := s.Writer.Header().Clone()
	h := s.OutputState()
	h.Header = headers
	return h
}

// OutputState reads optional Written and Status interfaces from the bound
// writer. Missing interfaces leave zero state fields, and this operation avoids
// Header access so a waiting keepalive can continue until an output handoff is
// requested.
func (s ResponseSink) OutputState() upstream.OutputHead {
	h := upstream.OutputHead{}
	if w, ok := s.Writer.(interface {
		// Written reports whether this response writer has committed HTTP output.
		Written() bool
	}); ok {
		h.Committed = w.Written()
	}
	if w, ok := s.Writer.(interface {
		// Status returns the HTTP status currently retained by this writer.
		Status() int
	}); ok {
		h.Status = w.Status()
	}
	return h
}
