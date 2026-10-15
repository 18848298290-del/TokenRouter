package messageforward

import (
	"context"
	"errors"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/searchtools"
)

// search.go admits and executes emulated search for Messages requests.
// Messages requests containing emulated search can complete through the search
// service before a model exchange. Provider mode and group policy decide
// admission, and a search proxy failure is converted into a gateway failover
// signal for the outer provider loop.

// shouldEmulate asks the shared search emulator whether provider mode and group
// policy enable this request. The selected target and emulator must be
// available before this decision is invoked.
func (r *Runtime) shouldEmulate(ctx context.Context, target *gatewayadapter.ExecutionProvider, group *int64, body []byte) bool {
	return r.dependencies.Search.ShouldEmulate(ctx, searchtools.PolicyInput{
		Body:     body,
		Mode:     gatewayadapter.SearchProviderMode(&searchtools.ProviderPolicy{ID: target.Record.ID, Platform: target.Record.Platform, Type: target.Record.Type, Extra: target.Record.Extra}),
		Platform: target.Record.Platform,
		GroupID:  group,
	})
}

// emulate executes search using the parsed request and selected provider proxy,
// forwarding acceptance and output callbacks. ProxyFailure becomes a gateway
// failover signal; other errors pass back to the caller.
func (r *Runtime) emulate(ctx context.Context, output HTTPBoundary, target *gatewayadapter.ExecutionProvider, parsed *requeststate.ParsedRequest) (*forward.Result, error) {
	proxyURL := ""
	if target.Record.ProxyID != nil && target.Record.Proxy != nil {
		proxyURL = target.Record.Proxy.URL()
	}
	result, err := r.dependencies.Search.Execute(ctx, searchtools.Request{
		Body: parsed.Body.Bytes(), Model: parsed.Model, Stream: parsed.Stream,
		ProviderID: target.Record.ID, ProviderName: target.Record.Name,
		ProxyURL: proxyURL, OnAccepted: parsed.OnUpstreamAccepted,
	}, output.SearchOutput())
	if err != nil {
		// A proxy failure names an unusable selected path, so expose a
		// gateway failover signal for the outer loop to choose another
		// provider.
		if _, ok := errors.AsType[*searchtools.ProxyFailure](err); ok {
			return nil, &forward.UpstreamFailoverError{StatusCode: http.StatusBadGateway, ResponseBody: []byte(err.Error())}
		}
		return nil, err
	}
	return &forward.Result{Model: result.Model, Duration: result.Duration, Usage: result.Usage}, nil
}
