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

// transportFailureBody is the generic upstream error body used for transport failover.
var transportFailureBody = []byte(`{"error":{"type":"upstream_error","message":"Upstream request failed"}}`)

type UpstreamTransportFailure struct {
	Health *provideradapter.TransportHealth
}

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
	if errors.Is(err, context.Canceled) {
		return err
	}
	if p != nil {
		p.Health.Attempt(gatewayadapter.ExecutionRecord(target))
		if httpclient.ClassifyTransportFailure(err).Persistent {
			p.Health.Persistent(ctx, gatewayadapter.ExecutionRecord(target), safe)
		}
	}
	return &forward.UpstreamFailoverError{StatusCode: http.StatusBadGateway, ResponseBody: transportFailureBody}
}
