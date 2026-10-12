package telemetry

import (
	"context"

	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
)

// Completion pool diagnostics and provider-switch observations carry different
// correlation data. Pool events choose their severity from Event.Level and attach
// supplied fields, while failover messages use the request context so the logger
// can retain the request's identity.

// Completion emits a worker event with its supplied structured fields.
// Exact "error" and "warn" levels select those severities; other levels use info.
// The callback reads Fields synchronously, so callers serialize map mutation.
func Completion(event completion.Event) {
	fields := make([]zap.Field, 0, len(event.Fields))
	for k, v := range event.Fields {
		fields = append(fields, zap.Any(k, v))
	}
	log := logger.L().With(fields...)
	switch event.Level {
	case "error":
		log.Error(event.Message)
	case "warn":
		log.Warn(event.Message)
	default:
		log.Info(event.Message)
	}
}

// ErrorRules formats error-passthrough diagnostics through the named service
// component, passing formatting arguments to the shared legacy logging adapter.
func ErrorRules(message string, args ...any) {
	logger.LegacyPrintf("service.error_passthrough", message, args...)
}

// Failover emits a provider-switch warning through the request-context logger.
// It reads the supplied values synchronously and attaches each as a structured
// field; callers keep the map stable during emission.
func Failover(ctx context.Context, event string, values map[string]any) {
	fields := make([]zap.Field, 0, len(values))
	for k, v := range values {
		fields = append(fields, zap.Any(k, v))
	}
	logger.FromContext(ctx).Warn(event, fields...)
}
