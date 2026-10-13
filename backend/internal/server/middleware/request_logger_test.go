package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
)

// HTTP request capture connects one local ID to context, response headers, and
// lifecycle snapshots. These cases distinguish caller aliases from that
// identity and check committed streaming headers after overwrites, plus storage
// normalization for an unknown route containing a decoded NUL.

// TestRequestLogger_GenerateAndPropagateRequestID serves a request without
// caller correlation metadata. The handler context and response header contain
// the same nonempty local identifier through completion.
func TestRequestLogger_GenerateAndPropagateRequestID(t *testing.T) {
	r := gin.New()
	r.Use(RequestLogger())
	r.GET("/t", func(c *gin.Context) {
		reqID, ok := c.Request.Context().Value(telemetry.RequestID).(string)
		if !ok || reqID == "" {
			t.Fatalf("request_id missing in context")
		}
		if got := c.Writer.Header().Get(requestIDHeader); got != reqID {
			t.Fatalf("response header request_id mismatch, header=%q ctx=%q", got, reqID)
		}
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	if w.Header().Get(requestIDHeader) == "" {
		t.Fatalf("X-Request-ID should be set")
	}
}

// TestRequestLogger_SeparatesIncomingRequestID supplies an external
// X-Request-ID before entry capture. The incoming value remains a parent
// context alias while the request and response use a distinct local identifier.
func TestRequestLogger_SeparatesIncomingRequestID(t *testing.T) {
	r := gin.New()
	r.Use(RequestLogger())
	r.GET("/t", func(c *gin.Context) {
		reqID, _ := c.Request.Context().Value(telemetry.RequestID).(string)
		if reqID == "rid-fixed" || reqID == "" {
			t.Fatalf("unexpected local ID: %q", reqID)
		}
		if c.Request.Context().Value(telemetry.ParentRequestID) != "rid-fixed" {
			t.Fatal("caller ID missing")
		}
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set(requestIDHeader, "rid-fixed")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	if got := w.Header().Get(requestIDHeader); got == "rid-fixed" || got == "" {
		t.Fatalf("unexpected response ID: %q", got)
	}
}

// TestRequestLoggerBoundsIncomingRequestID supplies a caller header far beyond
// the persisted correlation width. The returned request ID still uses the
// generated UUID's bounded text representation.
func TestRequestLoggerBoundsIncomingRequestID(t *testing.T) {
	r := gin.New()
	r.Use(RequestLogger())
	r.GET("/t", func(c *gin.Context) {
		reqID, _ := c.Request.Context().Value(telemetry.RequestID).(string)
		if len(reqID) != 36 {
			t.Fatalf("request_id length=%d", len(reqID))
		}
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set(requestIDHeader, strings.Repeat("r", 1024))
	r.ServeHTTP(w, req)
	if got := len(w.Header().Get(requestIDHeader)); got != 36 {
		t.Fatalf("response request_id length=%d", got)
	}
}

// TestRequestLoggerPreservesIDAcrossStreaming overwrites correlation headers
// before streaming several response statuses. Committed wire headers retain the
// capture ID and completion snapshots include the handler's selected status and
// finish time.
func TestRequestLoggerPreservesIDAcrossStreaming(t *testing.T) {
	for _, status := range []int{200, 401, 403, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			r := gin.New()
			var records []telemetry.RequestRecord
			r.Use(RequestLogger(func(record telemetry.RequestRecord) { records = append(records, record) }), ClientRequestID())
			r.GET("/stream", func(c *gin.Context) {
				id := telemetry.RequestIDValue(c.Request.Context())
				if c.Writer.Header().Get(internalRequestIDHeader) != id {
					t.Fatal("gateway generated another ID")
				}
				// Model upstream header copying after entry correlation has been
				// established. The subsequent write must restore local names before the
				// recorder captures committed response headers.
				c.Header(requestIDHeader, "upstream-id")
				c.Header(internalRequestIDHeader, "spoofed-id")
				c.Status(status)
				_, _ = c.Writer.WriteString("data: hello\n\n")
				c.Writer.Flush()
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/stream", nil))
			if len(records) < 2 {
				t.Fatal("missing lifecycle records")
			}
			id := records[0].RequestID
			for _, name := range []string{requestIDHeader, internalRequestIDHeader, legacyInternalRequestIDHeader} {
				if w.Result().Header.Get(name) != id {
					t.Fatalf("committed %s does not match request", name)
				}
			}
			last := records[len(records)-1]
			if last.Status != status || last.FinishedAt == nil {
				t.Fatalf("unexpected completion: %+v", last)
			}
		})
	}
}

// TestRequestLoggerUnknownPathCanBeStored requests an unmatched URL with a
// decoded NUL in its path. The response stays 404 and observer-side
// normalization removes that character from the path in each collected snapshot.
func TestRequestLoggerUnknownPathCanBeStored(t *testing.T) {
	var records []telemetry.RequestRecord
	router := gin.New()
	router.Use(RequestLogger(func(record telemetry.RequestRecord) {
		records = append(records, telemetry.NormalizeRequestRecord(record))
	}))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/invalid%00path", nil))
	if response.Code != http.StatusNotFound || len(records) < 2 {
		t.Fatalf("unexpected response: status=%d records=%d", response.Code, len(records))
	}
	for _, record := range records {
		if record.Path != "/invalidpath" {
			t.Fatalf("unstorable path: %q", record.Path)
		}
	}
}
