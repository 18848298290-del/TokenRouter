package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// MessagesOutput supplies synchronous HTTP output and request observations.
// Executors read identity, routing, body, and funding state through execution.Request.
type MessagesOutput struct {
	Concurrency *ConcurrencyHelper
	ResponseSink
	HTTP          *gin.Context
	Log           *zap.Logger
	StreamStarted *bool
}

// ResponseSink writes synchronous upstream events to the client and flushes when requested.
type ResponseSink struct{ Writer http.ResponseWriter }

func (s ResponseSink) Begin(head upstream.OutputHead) error {
	for key, values := range head.Header {
		s.Writer.Header()[key] = append([]string(nil), values...)
	}
	s.Writer.WriteHeader(head.Status)
	return nil
}

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

// InitialOutput snapshots response state and headers, including values set by heartbeats or middleware.
func (s ResponseSink) InitialOutput() upstream.OutputHead {
	// Read Header before state because acquiring headers can pause the waiting heartbeat.
	headers := s.Writer.Header().Clone()
	h := s.OutputState()
	h.Header = headers
	return h
}

// OutputState reads commitment state while allowing waiting keepalives to continue.
func (s ResponseSink) OutputState() upstream.OutputHead {
	h := upstream.OutputHead{}
	if w, ok := s.Writer.(interface{ Written() bool }); ok {
		h.Committed = w.Written()
	}
	if w, ok := s.Writer.(interface{ Status() int }); ok {
		h.Status = w.Status()
	}
	return h
}
