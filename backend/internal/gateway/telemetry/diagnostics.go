package telemetry

import (
	"context"

	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
)

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

func ErrorRules(message string, args ...any) {
	logger.LegacyPrintf("service.error_passthrough", message, args...)
}

// Failover logs provider switches at warning level with request correlation and event fields.
func Failover(ctx context.Context, event string, values map[string]any) {
	fields := make([]zap.Field, 0, len(values))
	for k, v := range values {
		fields = append(fields, zap.Any(k, v))
	}
	logger.FromContext(ctx).Warn(event, fields...)
}
