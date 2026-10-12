package middleware

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
)

const (
	snapshotCacheHeader = "X-Snapshot-Cache"
	usageCacheHeader    = "X-Usage-Stats-Cache"
)

type serverTimingResponseWriter struct {
	gin.ResponseWriter
	context *gin.Context
	once    sync.Once
}

func (w *serverTimingResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// ServerTiming collects request durations for admin and user UI paths or markers when enabled.
func ServerTiming(enabled bool) gin.HandlerFunc {
	if !enabled {
		return func(c *gin.Context) {
			c.Next()
		}
	}
	return func(c *gin.Context) {
		if !shouldCollectServerTiming(c) || c.Request == nil {
			c.Next()
			return
		}

		collector := timing.New(time.Now())
		c.Request = c.Request.WithContext(timing.WithCollector(c.Request.Context(), collector))
		writer := &serverTimingResponseWriter{
			ResponseWriter: c.Writer,
			context:        c,
		}
		c.Writer = writer
		c.Next()
		writer.finalize()
	}
}

func (w *serverTimingResponseWriter) WriteHeader(statusCode int) {
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *serverTimingResponseWriter) WriteHeaderNow() {
	w.finalize()
	w.ResponseWriter.WriteHeaderNow()
}

func (w *serverTimingResponseWriter) Write(data []byte) (int, error) {
	w.finalize()
	return w.ResponseWriter.Write(data)
}

func (w *serverTimingResponseWriter) WriteString(data string) (int, error) {
	w.finalize()
	return w.ResponseWriter.WriteString(data)
}

func (w *serverTimingResponseWriter) Flush() {
	w.finalize()
	w.ResponseWriter.Flush()
}

func (w *serverTimingResponseWriter) finalize() {
	if w == nil {
		return
	}
	w.once.Do(func() {
		if value := ServerTimingHeaderValue(w.context); value != "" {
			w.ResponseWriter.Header().Set(timing.HeaderName, value)
		}
	})
}

// ServerTimingHeaderValue returns collected timing data when the caller's role permits it.
// Administrators can receive all collected timings; other authenticated users need an allowlisted user API path.
// X-User-UI-Request selects collection scope and does not grant access to timing data.
func ServerTimingHeaderValue(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	role, ok := authctx.GetUserRoleFromContext(c)
	if !ok || role == "" {
		return ""
	}
	if role != "admin" && !isUserTimingPath(c.Request.URL.Path) {
		return ""
	}
	return timing.HeaderValue(c.Request.Context(), time.Now(), responseCacheStatus(c.Writer.Header()))
}

// ServerTimingResponseHeader builds timing headers for a WebSocket upgrade.
func ServerTimingResponseHeader(c *gin.Context) http.Header {
	value := ServerTimingHeaderValue(c)
	if value == "" {
		return nil
	}
	return http.Header{timing.HeaderName: []string{value}}
}

func shouldCollectServerTiming(c *gin.Context) bool {
	return isAdminUIRequest(c) || isUserUIRequest(c)
}

func isAdminUIRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	if strings.TrimSpace(c.GetHeader(timing.AdminUIHeader)) == "1" {
		return true
	}
	path := strings.TrimSpace(c.Request.URL.Path)
	return path == "/api/v1/admin" || strings.HasPrefix(path, "/api/v1/admin/")
}

func isUserUIRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	if strings.TrimSpace(c.GetHeader(timing.UserUIHeader)) == "1" {
		return true
	}
	return isUserTimingPath(c.Request.URL.Path)
}

// isUserTimingPath reports whether a user API path permits timing data for authenticated callers.
func isUserTimingPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	const prefix = "/api/v1"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	rest := strings.TrimPrefix(path, prefix)
	if rest == "" {
		return false
	}
	if !strings.HasPrefix(rest, "/") {
		rest = "/" + rest
	}

	switch {
	case rest == "/auth/me",
		rest == "/auth/revoke-all-sessions",
		rest == "/auth/oauth/bind-token":
		return true
	case rest == "/user", strings.HasPrefix(rest, "/user/"):
		return true
	case rest == "/keys", strings.HasPrefix(rest, "/keys/"):
		return true
	case rest == "/groups/available", rest == "/groups/rates":
		return true
	case rest == "/channels/available":
		return true
	case rest == "/usage", strings.HasPrefix(rest, "/usage/"):
		return true
	case rest == "/announcements", strings.HasPrefix(rest, "/announcements/"):
		return true
	case rest == "/redeem", strings.HasPrefix(rest, "/redeem/"):
		return true
	case rest == "/subscriptions", strings.HasPrefix(rest, "/subscriptions/"):
		return true
	case rest == "/channel-monitors", strings.HasPrefix(rest, "/channel-monitors/"):
		return true
	case strings.HasPrefix(rest, "/payment/"):
		// Public payment and webhook routes are outside the authenticated user scope.
		if strings.HasPrefix(rest, "/payment/public") || strings.HasPrefix(rest, "/payment/webhook") {
			return false
		}
		return true
	default:
		return false
	}
}

func responseCacheStatus(header http.Header) string {
	for _, name := range []string{snapshotCacheHeader, usageCacheHeader} {
		switch strings.ToLower(strings.TrimSpace(header.Get(name))) {
		case "hit":
			return "hit"
		case "miss":
			return "miss"
		case "bypass":
			return "bypass"
		}
	}
	return "bypass"
}
