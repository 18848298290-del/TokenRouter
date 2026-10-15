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

// server_timing.go collects UI request timings and gates their disclosure in
// response headers.
// Timing collection and timing disclosure answer different request questions.
// Administrator or user UI paths and marker headers can install a collector,
// but a nonempty role value is still required before a timing header can be
// produced. Non-administrator roles also need a user path accepted by the
// disclosure predicate.
//
// The response wrapper takes its header snapshot at the first ordinary
// commitment operation, or after downstream handling returns for a status-only
// response. Selecting Gin's status alone leaves time for handlers to supply
// role and cache outcome. A once guard prevents later wrapper calls from
// rebuilding that snapshot, so observations after a stream begins do not update
// the header. WebSocket handlers obtain an explicit header map for their own
// handshake serialization.

const (
	// Cache outcome header names let timing rendering read handler observations,
	// preferring a recognized snapshot status before the usage-statistics status.
	snapshotCacheHeader = "X-Snapshot-Cache"
	usageCacheHeader    = "X-Usage-Stats-Cache"
)

// serverTimingResponseWriter adds one timing snapshot before ordinary Gin
// commitment operations. It retains the active request context; callers use it
// during that request's response handling and serialize underlying Gin writes.
type serverTimingResponseWriter struct {
	// ResponseWriter, context, and once belong to one response. The first finalize
	// call reads context authorization and cache headers from ResponseWriter, then
	// once suppresses subsequent snapshot attempts.
	gin.ResponseWriter // wrapped Gin response writer
	// context retains the active Gin request for role and path disclosure checks.
	// It is used during response handling before Gin recycles that request
	// context.
	context *gin.Context // current request disclosure context
	// once consumes the first finalization attempt even when it produces no
	// header. Later role, cache, or timing changes do not trigger another wrapper
	// snapshot.
	once sync.Once // first snapshot attempt guard
}

// Unwrap exposes the wrapped response writer for response-controller capability
// traversal. Direct commitment through the returned writer bypasses this
// wrapper's timing finalization, so ordinary response output uses the wrapper's
// write and flush methods.
func (w *serverTimingResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// ServerTiming returns optional UI timing collection middleware, with disabled
// collection passing straight through. Eligible paths or marker values receive
// a fresh request collector and writer; authentication still determines whether
// later header rendering is permitted.
//
// Ordinary writes finalize before commitment, and normal handler return
// finalizes status-only responses. The first finalization attempt is decisive
// even when authorization prevents a header, so role and handler cache metadata
// need to be established before response output.
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

// WriteHeader delegates Gin's status selection while leaving timing
// finalization pending. A status-only handler can therefore finish setting role
// and cache metadata before normal middleware return or an actual commitment
// operation takes its snapshot.
func (w *serverTimingResponseWriter) WriteHeader(statusCode int) {
	w.ResponseWriter.WriteHeader(statusCode)
}

// WriteHeaderNow takes the first permitted timing snapshot before asking Gin to
// commit its selected status. Once finalization has been attempted, later calls
// delegate commitment without recalculating timings or authorization.
func (w *serverTimingResponseWriter) WriteHeaderNow() {
	w.finalize()
	w.ResponseWriter.WriteHeaderNow()
}

// Write finalizes the timing snapshot before delegating body bytes to Gin's
// writer. The wrapped write commits the selected status when needed and returns
// its own byte count and error.
func (w *serverTimingResponseWriter) Write(data []byte) (int, error) {
	w.finalize()
	return w.ResponseWriter.Write(data)
}

// WriteString finalizes timing before delegating response text to Gin's writer.
// Its delegated write commits the selected status when needed, and the wrapper
// returns the underlying count and error.
func (w *serverTimingResponseWriter) WriteString(data string) (int, error) {
	w.finalize()
	return w.ResponseWriter.WriteString(data)
}

// Flush finalizes timing before delegating the stream flush to Gin's writer.
// That first flush may commit headers, and subsequent wrapper flushes retain
// the outcome of the first finalization attempt.
func (w *serverTimingResponseWriter) Flush() {
	w.finalize()
	w.ResponseWriter.Flush()
}

// finalize attempts to add one authorized timing snapshot, treating a nil
// writer as a no-op. Its once guard consumes the attempt even when the result
// is empty, so later role and cache changes do not produce another snapshot.
func (w *serverTimingResponseWriter) finalize() {
	if w == nil {
		return
	}
	// The first finalization attempt fixes the wrapper's snapshot. Consume even an
	// empty result so subsequent writes and normal handler return do not resample
	// the response.
	w.once.Do(func() {
		if value := ServerTimingHeaderValue(w.context); value != "" {
			w.ResponseWriter.Header().Set(timing.HeaderName, value)
		}
	})
}

// ServerTimingHeaderValue renders the active collector when its role and path
// gates permit disclosure. It requires a nonempty role from authctx; the exact
// admin role permits collected scopes, and every other role also needs
// isUserTimingPath to accept the request path.
//
// UI marker headers select collection only. Missing request or role data, a
// denied user path, or absent collector yields an empty value. The cache label
// comes from current response headers at this rendering call.
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

// ServerTimingResponseHeader returns a newly allocated authorized timing header
// map, or nil for an empty rendered value. Pass it to an upgrader's explicit
// responseHeader parameter because hijacked WebSocket handshakes serialize
// their own headers.
func ServerTimingResponseHeader(c *gin.Context) http.Header {
	value := ServerTimingHeaderValue(c)
	if value == "" {
		return nil
	}
	return http.Header{timing.HeaderName: []string{value}}
}

// shouldCollectServerTiming reports whether administrator or user UI scope
// selects collection. It delegates path and marker checks, treating missing
// requests as outside scope; disclosure is separately checked when headers are
// rendered.
func shouldCollectServerTiming(c *gin.Context) bool {
	return isAdminUIRequest(c) || isUserUIRequest(c)
}

// isAdminUIRequest reports whether a request selects administrator timing
// collection. A trimmed marker equal to 1 qualifies any path; otherwise the
// exact administrator path or its slash-delimited descendants qualify. Missing
// request data returns false.
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

// isUserUIRequest reports whether a request selects user timing collection. A
// trimmed marker equal to 1 qualifies independently of the path; otherwise the
// user timing predicate decides. Missing request data returns false.
func isUserUIRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	if strings.TrimSpace(c.GetHeader(timing.UserUIHeader)) == "1" {
		return true
	}
	return isUserTimingPath(c.Request.URL.Path)
}

// isUserTimingPath checks a trimmed path against the user timing categories
// after removing /api/v1. It adds a slash when the remaining text lacks one,
// then accepts listed user resources and selected auth or payment paths.
// Payment public and webhook prefixes are excluded.
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
	// A remainder lacking a slash is compared in slash-prefixed form. Consequently
	// /api/v1keys and /api/v1/keys select the same user timing category here.
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
		// These payment prefixes serve public recovery or provider callbacks. Exclude
		// their descendants from user disclosure even when a UI marker caused
		// collection.
		if strings.HasPrefix(rest, "/payment/public") || strings.HasPrefix(rest, "/payment/webhook") {
			return false
		}
		return true
	default:
		return false
	}
}

// responseCacheStatus returns the first recognized cache label from snapshot
// and usage headers in that order. Values are trimmed and lowercased; hit,
// miss, and bypass qualify, while absent or invalid observations default to
// bypass.
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
	// An active timing header still includes its cache metric when no handler
	// supplied a recognized outcome. Use the default label for that absent
	// observation.
	return "bypass"
}
