package middleware

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
)

const requestIDHeader = "X-Request-ID"

// requestIDWriter restores local request IDs before committing the HTTP response.
type requestIDWriter struct {
	gin.ResponseWriter
	id       string
	clientID string
}

func (w *requestIDWriter) protect() {
	w.Header().Set(requestIDHeader, w.id)
	clientID := w.clientID
	if clientID == "" {
		clientID = w.id
	}
	w.Header().Set(clientRequestIDHeader, clientID)
	w.Header().Set(internalRequestIDHeader, w.id)
	w.Header().Set(legacyInternalRequestIDHeader, w.id)
}

// WriteHeader restores correlation headers before recording the response status.
func (w *requestIDWriter) WriteHeader(code int) {
	w.protect()
	w.ResponseWriter.WriteHeader(code)
}

// WriteHeaderNow commits the response with restored correlation headers.
func (w *requestIDWriter) WriteHeaderNow() {
	w.protect()
	w.ResponseWriter.WriteHeaderNow()
}

func (w *requestIDWriter) Write(data []byte) (int, error) {
	w.protect()
	return w.ResponseWriter.Write(data)
}

func (w *requestIDWriter) WriteString(data string) (int, error) {
	w.protect()
	return w.ResponseWriter.WriteString(data)
}

// Flush restores local IDs before an SSE response is flushed.
func (w *requestIDWriter) Flush() {
	w.protect()
	w.ResponseWriter.Flush()
}

func (w *requestIDWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.protect()
	return w.ResponseWriter.Hijack()
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (w *requestIDWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// RequestLogger assigns a local request ID, installs a request logger, and records request completion.
// @project-doc docs/operations/request_lookup.md#request_identity
func RequestLogger(observers ...func(telemetry.RequestRecord)) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request == nil {
			c.Next()
			return
		}

		requestID := telemetry.NewRequestID()
		writer := &requestIDWriter{ResponseWriter: c.Writer, id: requestID}
		c.Writer = writer
		writer.protect()
		callerID, validCaller := normalizeCorrelationID(c.GetHeader(clientRequestIDHeader))
		if validCaller {
			writer.clientID = callerID
			c.Header(clientRequestIDHeader, callerID)
		} else {
			c.Header(clientRequestIDHeader, requestID)
		}

		ctx := context.WithValue(c.Request.Context(), telemetry.RequestID, requestID)
		parentID, _ := normalizeCorrelationID(c.GetHeader(requestIDHeader))
		if parentID != "" {
			ctx = context.WithValue(ctx, telemetry.ParentRequestID, parentID)
		}

		requestLogger := logging.With(
			zap.String("component", "http"),
			zap.String("request_id", requestID),
			zap.String("parent_request_id", parentID),
			zap.String("path", c.Request.URL.Path),
			zap.String("method", c.Request.Method),
		)

		ctx = logging.IntoContext(ctx, requestLogger)
		started := time.Now().UTC()
		ctx = context.WithValue(ctx, telemetry.RequestStartedAt, started)
		var observe func(telemetry.RequestRecord)
		if len(observers) > 0 {
			observe = observers[0]
		}
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}
		record := telemetry.RequestRecord{RequestID: requestID, StartedAt: started, Method: c.Request.Method, Path: path, State: "running"}
		if parentID != "" {
			record.Aliases = append(record.Aliases, telemetry.RequestAlias{Kind: "caller", Value: parentID})
		}
		if clientID, valid := normalizeCorrelationID(c.GetHeader(clientRequestIDHeader)); valid {
			record.Aliases = append(record.Aliases, telemetry.RequestAlias{Kind: "caller", Value: clientID})
		}
		ctx = telemetry.WithRequestCapture(ctx, record, observe)
		c.Request = c.Request.WithContext(ctx)
		defer func() {
			recovered := recover()
			writer.protect()
			telemetry.UpdateRequest(ctx, func(record *telemetry.RequestRecord) {
				finished := time.Now().UTC()
				record.FinishedAt = &finished
				record.DurationMs = finished.Sub(started).Milliseconds()
				record.Status = c.Writer.Status()
				if recovered != nil {
					if !writer.Written() {
						record.Status = http.StatusInternalServerError
					}
					record.State = "failed"
					record.ErrorCode = "panic"
				}
				if record.State == "running" {
					record.State = "completed"
					if record.Status >= 400 {
						record.State = "failed"
						if record.ErrorCode == "" {
							record.ErrorCode = "http_" + strconv.Itoa(record.Status)
						}
					}
					if c.Request.Context().Err() != nil {
						record.State = "canceled"
					}
				}
				if reason, rejected := GetIngressRejectReason(c); rejected {
					record.ErrorCode = string(reason)
				}
				if value, ok := c.Request.Context().Value(telemetry.Model).(string); ok {
					record.Model = value
				}
				if value, ok := c.Request.Context().Value(telemetry.ClientModel).(string); ok && value != "" {
					record.Model = value
				}
				if value, ok := c.Request.Context().Value(telemetry.Platform).(string); ok {
					record.Platform = value
				}
				if value, ok := c.Request.Context().Value(telemetry.ProviderID).(int64); ok {
					record.ProviderID = value
				}
			})
			if recovered != nil {
				panic(recovered)
			}
		}()
		c.Next()
	}
}
