package provider

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// TransportHealth records classified persistent transport failures in runtime and stored provider state.
type TransportHealth struct {
	Runtime  *provider.RuntimeBlockState
	Deferred *provider.DeferredService
	Store    interface {
		SetTempUnschedulable(context.Context, int64, time.Time, string) error
	}
}

// Attempt records Ollama Cloud activity when called as network transport begins.
func (s *TransportHealth) Attempt(value *provider.Record) {
	if s == nil || s.Deferred == nil || value == nil || !provider.IsOllamaCloudUsageProvider(value) {
		return
	}
	s.Deferred.ScheduleLastUsedUpdate(value.ID)
}

// Persistent blocks local scheduling, then uses an independent five-second budget to persist a ten-minute pause.
func (s *TransportHealth) Persistent(ctx context.Context, value *provider.Record, safeError string) {
	if s == nil || value == nil {
		return
	}
	until := time.Now().Add(10 * time.Minute)
	reason := "upstream transport error (proxy/network): " + safeError
	s.Runtime.BlockProviderScheduling(value, until, "transport_error")
	if s.Store == nil {
		logging.L().With(zap.String("component", "service.openai_gateway")).Warn("openai.provider_temp_unscheduled_transport_memory_only", zap.Int64("provider_id", value.ID), zap.String("provider_name", value.Name), zap.String("platform", value.Platform), zap.Time("until", until), zap.String("reason", reason))
		return
	}
	stateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.Store.SetTempUnschedulable(stateCtx, value.ID, until, reason); err != nil {
		logging.L().With(zap.String("component", "service.openai_gateway")).Warn("openai.provider_temp_unscheduled_transport_failed", zap.Int64("provider_id", value.ID), zap.Error(err))
		return
	}
	logging.L().With(zap.String("component", "service.openai_gateway")).Warn("openai.provider_temp_unscheduled_transport", zap.Int64("provider_id", value.ID), zap.String("provider_name", value.Name), zap.String("platform", value.Platform), zap.Time("until", until), zap.String("reason", reason))
}
