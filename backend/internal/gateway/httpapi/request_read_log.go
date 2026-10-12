package httpapi

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
)

// LogRequestBodyReadFailure records the error category, declared length, and content encoding.
func LogRequestBodyReadFailure(reqLog *zap.Logger, req *http.Request, err error) {
	if reqLog == nil || err == nil {
		return
	}

	contentLength := int64(-1)
	contentEncoding := "identity"
	if req != nil {
		contentLength = req.ContentLength
		contentEncoding = requestContentEncodingCategory(req.Header.Get("Content-Encoding"))
	}

	reqLog.Warn("read request body failed",
		zap.String("error_kind", requestBodyReadErrorKind(err)),
		zap.String("content_encoding", contentEncoding),
		zap.Int64("content_length", contentLength),
	)
}

func requestContentEncodingCategory(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "identity":
		return "identity"
	case "gzip", "x-gzip":
		return "gzip"
	case "zstd":
		return "zstd"
	case "deflate":
		return "deflate"
	default:
		return "other"
	}
}

func requestBodyReadErrorKind(err error) string {
	if err == nil {
		return "none"
	}
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return "max_bytes"
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "decode content-encoding") {
		if strings.Contains(lower, "unsupported content-encoding") {
			return "unsupported_content_encoding"
		}
		return "decode_content_encoding"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return "client_disconnect"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return "truncated_body"
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return "transport"
	}
	return "io_read"
}

// RequestLogger derives a logger from the request context and component name.
func RequestLogger(c *gin.Context, component string, fields ...zap.Field) *zap.Logger {
	base := logger.L()
	if c != nil && c.Request != nil {
		base = logger.FromContext(c.Request.Context())
	}

	if component != "" {
		fields = append([]zap.Field{zap.String("component", component)}, fields...)
	}
	return base.With(fields...)
}
