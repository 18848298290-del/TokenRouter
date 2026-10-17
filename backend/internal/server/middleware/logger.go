package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
)

// logger.go records completed HTTP access events and samples admission rejections.
// Access logging observes the handler's final status, selected provider, and
// access lifetime after downstream execution. Health and setup probes are
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
// system-log sink. Overall access latency starts at this middleware's entry
// and ends when handlers return. Request identifiers, provider metadata and
// errors remain available for log correlation; detailed performance belongs to usage.
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

		l := logging.FromContext(c.Request.Context()).With(fields...)
		l.Info("http request completed", zap.Time("completed_at", endTime))

		if len(c.Errors) > 0 {
			l.Warn("http request contains gin errors", zap.String("errors", c.Errors.String()))
		}
	}
}
