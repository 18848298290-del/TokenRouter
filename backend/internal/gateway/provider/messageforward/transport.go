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

// requestTLS resolves the provider's configured TLS profile through the egress cache.
func (r *Runtime) requestTLS(target *gatewayadapter.ExecutionProvider) *tlsfingerprint.Profile {
	selection := egress.TLSSelection{}
	if target != nil {
		selection.Enabled = target.View().IsTLSFingerprintEnabled()
		selection.DirectProfileID = target.View().GetTLSFingerprintProfileID()
	}
	return r.dependencies.TLS.ResolveRequestTLS(selection)
}

func (r *Runtime) scheduleActivity(target *gatewayadapter.ExecutionProvider) {
	if r.dependencies.Deferred != nil && target != nil && provider.IsOllamaCloudUsageProvider(gatewayadapter.ExecutionRecord(target)) {
		r.dependencies.Deferred.ScheduleLastUsedUpdate(target.Record.ID)
	}
}

// transportError observes failures before handling them. Cancellation stops here; persistent failures also unschedule the provider.
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

func (r *Runtime) tempUnscheduleTransport(ctx context.Context, target *gatewayadapter.ExecutionProvider, safe string) {
	if target == nil || r.dependencies.ProviderState == nil {
		return
	}
	until := time.Now().Add(10 * time.Minute)
	reason := "upstream transport error (proxy/network): " + safe
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
