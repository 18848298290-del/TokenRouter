package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

// Network failures can arrive before an upstream HTTP response supplies a
// status or body. Ops keeps sanitized diagnostic evidence, while failover
// receives a fixed safe 502 error body; cancellation returns its cause and
// skips provider activity and scheduling penalties.

// transportFailureBody is shared client-safe transport evidence reused by
// failover errors. Consumers treat its bytes as immutable while presenting failures.
var transportFailureBody = []byte(`{"error":{"type":"upstream_error","message":"Upstream request failed"}}`)

// UpstreamTransportFailure associates transport diagnostics with provider
// scheduling health updates.
type UpstreamTransportFailure struct {
	// Health records attempt activity and persistent scheduling blocks when
	// supplied. Its nil-safe methods leave failover classification available when
	// health tracking is absent.
	Health *provideradapter.TransportHealth
}

// Handle records sanitized transport evidence and returns a generic provider
// failover error. Nil errors return nil; wrapped client cancellation retains
// its cause after observation and skips health updates. Other failures record
// activity and apply persistent transport health when configured; persistent
// writes require a nonnil context, and this helper writes no client body.
func (p *UpstreamTransportFailure) Handle(ctx context.Context, c *gin.Context, target *gatewayadapter.ExecutionProvider, err error, passthrough bool) error {
	if err == nil {
		return nil
	}
	safe := logredact.SanitizeUpstreamQueries(err.Error())
	platform, name := "", ""
	var id int64
	if target != nil {
		platform, name, id = target.Record.Platform, target.Record.Name, target.Record.ID
	}
	SetOpsUpstreamError(c, 0, safe, "")
	AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{Platform: platform, ProviderID: id, ProviderName: name, UpstreamStatusCode: 0, Passthrough: passthrough, Kind: "request_error", Message: safe})
	// Client cancellation already has request evidence recorded; return its cause
	// before activity or provider penalties so disconnection does not trigger
	// transport failover.
	if errors.Is(err, context.Canceled) {
		return err
	}
	if p != nil {
		p.Health.Attempt(gatewayadapter.ExecutionRecord(target))
		if httpclient.ClassifyTransportFailure(err).Persistent {
			p.Health.Persistent(ctx, gatewayadapter.ExecutionRecord(target), safe)
		}
	}
	// Replace diagnostic transport text with fixed safe failure evidence for
	// switching providers; final client presentation should not receive raw
	// network or proxy details.
	return &forward.UpstreamFailoverError{StatusCode: http.StatusBadGateway, ResponseBody: transportFailureBody}
}
