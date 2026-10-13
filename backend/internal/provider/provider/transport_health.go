package provider

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// A classified persistent transport failure can pause OpenAI and Grok
// scheduling before its durable cooldown is written. Those platforms receive an
// immediate ten-minute local block, and a separate five-second context permits
// durable cooldown writes after the originating request is cancelled.
//
// The local block survives a missing store or failed persistence attempt, with
// logs distinguishing those outcomes. The caller supplies a safe error string
// for the stored reason and logs. Transport attempts also enqueue last-used
// activity for Ollama Cloud providers, so their usage maintenance can identify
// active accounts through the shared deferred updater.

// TransportHealth records transport activity and classified persistent failures
// for provider scheduling.
type TransportHealth struct {
	// Runtime installs local blocks for OpenAI and Grok. A nil state skips that
	// block while durable storage can still run.
	Runtime *provider.RuntimeBlockState
	// Deferred optionally records Ollama Cloud activity. Attempt skips activity
	// updates when this service is absent.
	Deferred *provider.DeferredService
	// Store optionally persists transport cooldowns. A missing store leaves the
	// runtime block active and produces the memory-only warning.
	Store interface {
		// SetTempUnschedulable persists a provider's cooldown deadline and safe
		// reason. Persistent logs errors from this write while retaining the runtime
		// block installed before the call.
		SetTempUnschedulable(context.Context, int64, time.Time, string) error
	}
}

// Attempt schedules last-used activity when network transport begins for an
// Ollama Cloud usage provider. Nil receivers, records, and deferred services
// are skipped, as are records outside that maintenance category.
func (s *TransportHealth) Attempt(value *provider.Record) {
	if s == nil || s.Deferred == nil || value == nil || !provider.IsOllamaCloudUsageProvider(value) {
		return
	}
	s.Deferred.ScheduleLastUsedUpdate(value.ID)
}

// Persistent records a ten-minute cooldown for a classified transport failure.
// Nil receivers or records are skipped. Callers supply a nonnil context and
// safeError already redacted for storage and logging.
//
// For OpenAI and Grok, the runtime block precedes durable state so subsequent
// requests can avoid the provider immediately. Storage uses a five-second
// context detached from request cancellation; a missing store or write failure
// is logged and leaves any installed local block in place. Persistence success
// records the recovery time and reason for other readers.
func (s *TransportHealth) Persistent(ctx context.Context, value *provider.Record, safeError string) {
	if s == nil || value == nil {
		return
	}
	// Use one recovery deadline for local and durable state, so a successful write
	// records the same ten-minute pause installed for immediate scheduling.
	until := time.Now().Add(10 * time.Minute)
	reason := "upstream transport error (proxy/network): " + safeError
	// Install the eligible platform's process-local block before storage. OpenAI
	// and Grok scheduling can then see it while the durable write is delayed or
	// fails.
	s.Runtime.BlockProviderScheduling(value, until, "transport_error")
	if s.Store == nil {
		logging.L().With(zap.String("component", "service.openai_gateway")).Warn("openai.provider_temp_unscheduled_transport_memory_only", zap.Int64("provider_id", value.ID), zap.String("provider_name", value.Name), zap.String("platform", value.Platform), zap.Time("until", until), zap.String("reason", reason))
		return
	}
	// Detach persistence from the failed request's cancellation while bounding it
	// to five seconds, allowing cooldown publication during client disconnect
	// cleanup.
	stateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	// A failed durable write leaves the local block installed. Log that difference
	// so diagnostics can distinguish in-process protection from persisted provider
	// state.
	if err := s.Store.SetTempUnschedulable(stateCtx, value.ID, until, reason); err != nil {
		logging.L().With(zap.String("component", "service.openai_gateway")).Warn("openai.provider_temp_unscheduled_transport_failed", zap.Int64("provider_id", value.ID), zap.Error(err))
		return
	}
	logging.L().With(zap.String("component", "service.openai_gateway")).Warn("openai.provider_temp_unscheduled_transport", zap.Int64("provider_id", value.ID), zap.String("provider_name", value.Name), zap.String("platform", value.Platform), zap.Time("until", until), zap.String("reason", reason))
}
