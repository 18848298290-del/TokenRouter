package middleware

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
)

// logger.go records completed HTTP requests and their gateway timing
// observations.
// Access logging observes the handler's final status, selected provider, and
// request stages after downstream execution. Health and setup probes are
// skipped. Expected admission rejections reach the aggregate recorder first,
// then a shared sampler decides whether to emit their access event and
// occasional dropped-count summary.

// Logger emits access metadata after downstream handlers return, skipping
// /health and /setup/status. Install the request's forwarding-policy snapshot
// beforehand when access logging should use the same client-address policy as
// security consumers.
//
// Marked admission rejections are recorded before their access-event sampling
// decision; sampled events and dropped summaries request exclusion from the Ops
// system-log sink. Emitted events also merge the request capture's stage
// timings with available millisecond fields. Overall latency starts at this
// middleware's entry, while stage offsets use their recorded timing references.
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		startTime := time.Now()

		path := c.Request.URL.Path

		c.Next()

		// Health and setup status responses describe service readiness. Skipping
		// these known probe paths keeps routine checks out of request access output.
		if path == "/health" || path == "/setup/status" {
			return
		}

		endTime := time.Now()
		latency := endTime.Sub(startTime)

		method := c.Request.Method
		statusCode := c.Writer.Status()
		clientIP := clientip.GetClientIP(c)
		protocol := c.Request.Proto
		providerID, hasProviderID := c.Request.Context().Value(telemetry.ProviderID).(int64)
		platform, _ := c.Request.Context().Value(telemetry.Platform).(string)
		model, _ := c.Request.Context().Value(telemetry.Model).(string)
		reason, rejected := GetIngressRejectReason(c)
		if rejected {
			// Record the marked rejection before spending its access-event budget.
			// Aggregate admission counts therefore remain available even when the access
			// log is sampled away.
			recordIngressReject(c, reason)
			allowed, droppedSummary := globalIngressRejectAccessSampler.allow(endTime)
			// The sampler returns a completed skipped-event count for occasional
			// reporting. Mark the summary to skip the Ops sink so rejection volume does
			// not reappear there.
			if droppedSummary > 0 {
				logging.FromContext(c.Request.Context()).Info("ingress rejection access logs dropped",
					zap.String("component", "http.access"),
					zap.Uint64("dropped_count", droppedSummary),
					zap.Bool(logging.OpsSystemLogSkipField, true),
				)
			}
			if !allowed {
				return
			}
		}

		// Access latency uses whole milliseconds; submillisecond portions are
		// truncated.
		fields := []zap.Field{
			zap.String("component", "http.access"),
			zap.Int("status_code", statusCode),
			zap.Int64("latency_ms", latency.Milliseconds()),
			zap.String("client_ip", clientIP),
			zap.String("protocol", protocol),
			zap.String("method", method),
			zap.String("path", path),
		}
		if rejected {
			fields = append(fields,
				zap.String("ingress_reject_reason", string(reason)),
				zap.Bool(logging.OpsSystemLogSkipField, true),
			)
		}
		if hasProviderID && providerID > 0 {
			fields = append(fields, zap.Int64("provider_id", providerID))
		}
		if platform != "" {
			fields = append(fields, zap.String("platform", platform))
		}
		if model != "" {
			fields = append(fields, zap.String("model", model))
		}
		if c.Request.ContentLength >= 0 {
			fields = append(fields, zap.Int64("request_content_length", c.Request.ContentLength))
		}
		fields = appendRequestStageFields(fields, c)
		// Copy the emitted stage fields into the shared request capture under its
		// update lock. Exclude this middleware's latency so RequestLogger keeps
		// ownership of total request duration.
		telemetry.UpdateRequest(c.Request.Context(), func(record *telemetry.RequestRecord) {
			if record.Timings == nil {
				record.Timings = make(map[string]int64)
			}
			for _, field := range fields {
				if (strings.HasSuffix(field.Key, "_ms") && field.Key != "latency_ms") || strings.HasPrefix(field.Key, "timing_") {
					record.Timings[field.Key] = field.Integer
				}
			}
		})

		l := logging.FromContext(c.Request.Context()).With(fields...)
		l.Info("http request completed", zap.Time("completed_at", endTime))

		if len(c.Errors) > 0 {
			l.Warn("http request contains gin errors", zap.String("errors", c.Errors.String()))
		}
	}
}

// appendRequestStageFields appends available request milestone and outbound
// trace fields, returning the supplied slice for a missing request. Context
// milestones use RequestStartedAt, while HTTP trace offsets use the trace
// snapshot's own installation time. Invalid milestone timestamps are omitted;
// trace counts retain repeated attempts. HTTP text collectors provide
// admission durations and applicability markers alongside event offsets; their
// delivery observations replace legacy stream marks for that request only.
func appendRequestStageFields(fields []zap.Field, c *gin.Context) []zap.Field {
	if c == nil || c.Request == nil {
		return fields
	}
	ctx := c.Request.Context()
	startedAt, _ := ctx.Value(telemetry.RequestStartedAt).(time.Time)
	appendTimestamp := func(key telemetry.ContextKey, name string) {
		at, ok := ctx.Value(key).(time.Time)
		// An absent reference or a stage predating ingress cannot supply a valid
		// request milestone. Omit that field rather than encoding a misleading
		// negative offset.
		if !ok || startedAt.IsZero() || at.Before(startedAt) {
			return
		}
		// Store whole milliseconds to match request-record stage fields. Fractional
		// milliseconds are truncated while a stage exactly at ingress remains zero.
		fields = append(fields, zap.Int64(name, at.Sub(startedAt).Milliseconds()))
	}
	if stages := timing.StagesFromContext(ctx); stages != nil {
		for name, value := range stages.Snapshot() {
			fields = append(fields, zap.Int64(name, value))
		}
	} else {
		appendTimestamp(telemetry.ProviderSlotAcquiredAt, "provider_slot_acquired_ms")
		appendTimestamp(telemetry.FirstSSEDataAt, "upstream_first_sse_data_ms")
		appendTimestamp(telemetry.FirstVisibleOutputAt, "first_visible_output_ms")
		appendTimestamp(telemetry.FirstDownstreamFlushAt, "first_downstream_flush_ms")
	}

	if snapshot, ok := timing.HTTPTraceSnapshotFromContext(ctx); ok {
		if elapsed, valid := timing.HTTPTraceElapsedMs(snapshot, snapshot.GetConnAt); valid {
			fields = append(fields, zap.Int64("upstream_get_conn_ms", elapsed))
		}
		if elapsed, valid := timing.HTTPTraceElapsedMs(snapshot, snapshot.GotConnAt); valid {
			fields = append(fields, zap.Int64("upstream_got_conn_ms", elapsed))
		}
		if elapsed, valid := timing.HTTPTraceElapsedMs(snapshot, snapshot.WroteRequestAt); valid {
			fields = append(fields, zap.Int64("upstream_wrote_request_ms", elapsed))
		}
		if elapsed, valid := timing.HTTPTraceElapsedMs(snapshot, snapshot.GotFirstResponseByteAt); valid {
			fields = append(fields, zap.Int64("upstream_first_response_byte_ms", elapsed))
		}
		if snapshot.GotConnCount > 0 {
			fields = append(fields, zap.Int("upstream_got_conn_count", snapshot.GotConnCount))
		}
		if snapshot.GetConnCount > 0 {
			fields = append(fields, zap.Int("upstream_get_conn_count", snapshot.GetConnCount))
		}
		if snapshot.WroteRequestCount > 0 {
			fields = append(fields, zap.Int("upstream_attempt_count", snapshot.WroteRequestCount))
		}
		if snapshot.FirstResponseByteCount > 0 {
			fields = append(fields, zap.Int("upstream_first_response_byte_count", snapshot.FirstResponseByteCount))
		}
		if snapshot.ConnectionReused {
			fields = append(fields, zap.Bool("upstream_connection_reused", true))
		}
		if snapshot.WroteRequestErrorObserved {
			fields = append(fields, zap.Bool("upstream_wrote_request_error", true))
		}
	}
	return fields
}
