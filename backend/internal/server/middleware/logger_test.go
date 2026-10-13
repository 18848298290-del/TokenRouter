package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
)

// Access events combine final response metadata with the request logger and
// context milestones. These cases inspect the accepted log sink for identity
// separation, optional stage fields, admission-rejection markers, skipped
// health probes, and severity-based suppression.

// TestLogger_AccessLogIncludesCoreFields serves a created response with
// provider and model values attached downstream. The accepted access event
// contains the final status and the same provider, platform, and model.
func TestLogger_AccessLogIncludesCoreFields(t *testing.T) {
	sink := initMiddlewareTestLogger(t)

	r := gin.New()
	r.Use(Logger())
	r.Use(func(c *gin.Context) {
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, telemetry.ProviderID, int64(101))
		ctx = context.WithValue(ctx, telemetry.Platform, "openai")
		ctx = context.WithValue(ctx, telemetry.Model, "gpt-5")
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	r.GET("/api/test", func(c *gin.Context) {
		c.Status(http.StatusCreated)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d", w.Code)
	}

	events := sink.list()
	if len(events) == 0 {
		t.Fatalf("expected at least one log event")
	}
	found := false
	for _, event := range events {
		if event == nil || event.Message != "http request completed" {
			continue
		}
		found = true
		switch v := event.Fields["status_code"].(type) {
		case int:
			if v != http.StatusCreated {
				t.Fatalf("status_code field mismatch: %v", v)
			}
		case int64:
			if v != int64(http.StatusCreated) {
				t.Fatalf("status_code field mismatch: %v", v)
			}
		default:
			t.Fatalf("status_code type mismatch: %T", v)
		}
		switch v := event.Fields["provider_id"].(type) {
		case int64:
			if v != 101 {
				t.Fatalf("provider_id field mismatch: %v", v)
			}
		case int:
			if v != 101 {
				t.Fatalf("provider_id field mismatch: %v", v)
			}
		default:
			t.Fatalf("provider_id type mismatch: %T", v)
		}
		if event.Fields["platform"] != "openai" || event.Fields["model"] != "gpt-5" {
			t.Fatalf("platform/model mismatch: %+v", event.Fields)
		}
	}
	if !found {
		t.Fatalf("access log event not found")
	}
}

// TestLogger_AccessLogSeparatesParentAndInternalRequestIDs sends an external
// correlation header through ClientRequestID and Logger. The access event
// retains the caller parent separately from its generated local diagnostic ID.
func TestLogger_AccessLogSeparatesParentAndInternalRequestIDs(t *testing.T) {
	sink := initMiddlewareTestLogger(t)

	r := gin.New()
	r.Use(Logger())
	r.Use(ClientRequestID())
	r.GET("/v1/responses", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	req.Header.Set(clientRequestIDHeader, "tokenrouter-request-123")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}

	for _, event := range sink.list() {
		if event == nil || event.Message != "http request completed" {
			continue
		}
		internalID, ok := event.Fields["request_id"].(string)
		if !ok || internalID == "" || internalID == "tokenrouter-request-123" {
			t.Fatalf("internal client request ID is not isolated: %+v", event.Fields)
		}
		if got := event.Fields["parent_client_request_id"]; got != "tokenrouter-request-123" {
			t.Fatalf("parent client request ID mismatch: %v", got)
		}
		return
	}
	t.Fatalf("access log event not found")
}

// TestLogger_AccessLogIncludesRequestStageFields supplies context timestamps
// relative to a known ingress time. The access event includes provider
// admission, upstream SSE, visible output, and downstream flush milestones.
func TestLogger_AccessLogIncludesRequestStageFields(t *testing.T) {
	sink := initMiddlewareTestLogger(t)

	r := gin.New()
	r.Use(Logger())
	r.GET("/v1/responses", func(c *gin.Context) {
		// Supply synthetic milestones after one fixed ingress reference so
		// stage-field coverage can be checked without depending on actual upload or
		// upstream timing.
		startedAt := time.Now().Add(-2 * time.Second)
		ctx := context.WithValue(c.Request.Context(), telemetry.RequestStartedAt, startedAt)
		ctx = context.WithValue(ctx, telemetry.ProviderSlotAcquiredAt, startedAt.Add(100*time.Millisecond))
		ctx = context.WithValue(ctx, telemetry.FirstSSEDataAt, startedAt.Add(500*time.Millisecond))
		ctx = context.WithValue(ctx, telemetry.FirstVisibleOutputAt, startedAt.Add(700*time.Millisecond))
		ctx = context.WithValue(ctx, telemetry.FirstDownstreamFlushAt, startedAt.Add(800*time.Millisecond))
		c.Request = c.Request.WithContext(ctx)
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}

	for _, event := range sink.list() {
		if event == nil || event.Message != "http request completed" {
			continue
		}
		for _, field := range []string{"provider_slot_acquired_ms", "upstream_first_sse_data_ms", "first_visible_output_ms", "first_downstream_flush_ms"} {
			if _, ok := event.Fields[field]; !ok {
				t.Fatalf("stage field %q missing: %+v", field, event.Fields)
			}
		}
		return
	}
	t.Fatal("access log event not found")
}

// TestLogger_IngressRejectRemainsInStandardAccessLog attaches an invalid-key
// rejection marker to one completed request. Its emitted access event includes
// the reason and requests exclusion from the Ops system-log sink.
func TestLogger_IngressRejectRemainsInStandardAccessLog(t *testing.T) {
	sink := initMiddlewareTestLogger(t)
	r := gin.New()
	r.Use(Logger())
	r.GET("/v1/messages", func(c *gin.Context) {
		MarkIngressRejected(c, IngressRejectInvalidAPIKey)
		c.Status(http.StatusUnauthorized)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", w.Code)
	}
	events := sink.list()
	if len(events) != 1 {
		t.Fatalf("events=%d, want 1", len(events))
	}
	if got := events[0].Fields["ingress_reject_reason"]; got != string(IngressRejectInvalidAPIKey) {
		t.Fatalf("ingress_reject_reason=%v", got)
	}
	if got, _ := events[0].Fields[logging.OpsSystemLogSkipField].(bool); !got {
		t.Fatalf("%s must be true", logging.OpsSystemLogSkipField)
	}
}

// TestLogger_AccessLogUsesForwardedClientIPFromTrustedProxy sends forwarding
// metadata behind a configured proxy. The emitted access event resolves the
// supplied client address; this fixture leaves GetClientIP in its
// absent-snapshot compatibility mode.
func TestLogger_AccessLogUsesForwardedClientIPFromTrustedProxy(t *testing.T) {
	sink := initMiddlewareTestLogger(t)

	r := gin.New()
	if err := r.SetTrustedProxies([]string{"104.23.251.120"}); err != nil {
		t.Fatalf("set trusted proxies: %v", err)
	}
	r.Use(Logger())
	r.GET("/api/test", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.RemoteAddr = "104.23.251.120:443"
	req.Header.Set("X-Forwarded-For", "203.0.113.42")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}

	for _, event := range sink.list() {
		if event == nil || event.Message != "http request completed" {
			continue
		}
		if got := event.Fields["client_ip"]; got != "203.0.113.42" {
			t.Fatalf("client_ip=%q, want real forwarded ip", got)
		}
		return
	}
	t.Fatalf("access log event not found")
}

// TestLogger_HealthPathSkipped serves a successful health probe through Logger
// and its capture sink. The response succeeds while no access event reaches
// that sink for this path.
func TestLogger_HealthPathSkipped(t *testing.T) {
	sink := initMiddlewareTestLogger(t)

	r := gin.New()
	r.Use(Logger())
	r.GET("/health", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	if len(sink.list()) != 0 {
		t.Fatalf("health endpoint should not write access log")
	}
}

// TestLogger_AccessLogDroppedWhenLevelWarn configures the logger at warning
// severity before a successful request. The informational access event is
// excluded from accepted sink events by severity filtering.
func TestLogger_AccessLogDroppedWhenLevelWarn(t *testing.T) {
	sink := initMiddlewareTestLoggerWithLevel(t, "warn")

	r := gin.New()
	r.Use(RequestLogger())
	r.Use(Logger())
	r.GET("/api/test", func(c *gin.Context) {
		c.Status(http.StatusCreated)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d", w.Code)
	}

	events := sink.list()
	for _, event := range events {
		if event != nil && event.Message == "http request completed" {
			t.Fatalf("access log should not be indexed when level=warn: %+v", event)
		}
	}
}
