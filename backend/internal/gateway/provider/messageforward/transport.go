package messageforward

import (
	"context"
	"errors"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// An upstream transport error is recorded before forwarding decides whether the
// caller should retry. Client cancellation returns its source error, while an
// upstream deadline with a live request context remains eligible for provider
// switching. Persistent transport failures can also temporarily remove the
// selected provider from scheduling.
//
// Last-used updates apply to eligible Ollama Cloud targets after cancellation
// has been ruled out. The scheduling update uses a bounded context detached
// from request cancellation, allowing the provider state write to finish after
// the HTTP request ends. A failed update is logged, and the caller still
// receives the failover error.

// requestTLS resolves the selected provider eligibility and profile through the
// shared egress reader. Nil targets use disabled eligibility, and a missing TLS
// wrapper follows the delegated resolver handling.
func (r *Runtime) requestTLS(target *gatewayadapter.ExecutionProvider) *tlsfingerprint.Profile {
	selection := egress.TLSSelection{}
	if target != nil {
		selection.Enabled = target.View().IsTLSFingerprintEnabled()
		selection.DirectProfileID = target.View().GetTLSFingerprintProfileID()
	}
	return r.dependencies.TLS.ResolveRequestTLS(selection)
}

// scheduleActivity queues a last-used update for eligible Ollama Cloud targets.
// A missing deferred service or target skips the update; the application owns
// draining the shared deferred service.
func (r *Runtime) scheduleActivity(target *gatewayadapter.ExecutionProvider) {
	if r.dependencies.Deferred != nil && target != nil && provider.IsOllamaCloudUsageProvider(gatewayadapter.ExecutionRecord(target)) {
		r.dependencies.Deferred.ScheduleLastUsedUpdate(target.Record.ID)
	}
}

// transportError records sanitized request failure evidence and selects
// cancellation or provider failover for this attempt. A cancellation error, or
// a deadline matching an expired request context, returns the supplied error.
//
// Other failures queue eligible provider activity before classification.
// Persistent failures attempt a temporary scheduling update, while transient
// failures leave scheduling to later attempts. The returned failover body
// contains a generic client message, and synchronous HTTP output remains with
// the outer handler.
func (r *Runtime) transportError(ctx context.Context, output HTTPBoundary, target *gatewayadapter.ExecutionProvider, err error, notice forward.Notice) error {
	safe := logredact.SanitizeUpstreamQueries(err.Error())
	output.SetError(0, safe, "")
	notice.Platform = target.Record.Platform
	notice.ProviderID = target.Record.ID
	notice.ProviderName = target.Record.Name
	notice.UpstreamStatusCode = 0
	notice.Kind = "request_error"
	notice.Message = safe
	output.Observe(notice)
	if errors.Is(err, context.Canceled) || (errors.Is(err, context.DeadlineExceeded) && errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		return err
	}
	r.scheduleActivity(target)
	if httpclient.ClassifyTransportFailure(err).Persistent {
		r.tempUnscheduleTransport(ctx, target, safe)
	}
	return &forward.UpstreamFailoverError{
		StatusCode:   http.StatusBadGateway,
		ResponseBody: []byte(`{"type":"error","error":{"type":"upstream_error","message":"Upstream request failed"}}`),
	}
}

// tempUnscheduleTransport attempts a ten-minute provider cooldown after a
// persistent transport failure. Missing targets or persistence adapters skip
// the write, which uses a five-second cancellation-independent budget and logs
// failures.
func (r *Runtime) tempUnscheduleTransport(ctx context.Context, target *gatewayadapter.ExecutionProvider, safe string) {
	if target == nil || r.dependencies.ProviderState == nil {
		return
	}
	// The transport cooldown lasts ten minutes from observation.
	until := time.Now().Add(10 * time.Minute)
	reason := "upstream transport error (proxy/network): " + safe
	// Detach request cancellation and cap the state write at five seconds
	// so request teardown does not immediately discard the persistent
	// transport cooldown.
	updateContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.dependencies.ProviderState.SetTempUnschedulable(updateContext, target.Record.ID, until, reason); err != nil {
		logging.L().With(zap.String("component", "service.gateway")).Warn("gateway.provider_temp_unschedule_transport_failed", zap.Int64("provider_id", target.Record.ID), zap.Error(err))
		return
	}
	logging.L().With(zap.String("component", "service.gateway")).Warn("gateway.provider_temp_unscheduled_transport",
		zap.Int64("provider_id", target.Record.ID), zap.String("provider_name", target.Record.Name),
		zap.String("platform", target.Record.Platform), zap.Time("until", until), zap.String("reason", reason))
}
