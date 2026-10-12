package forward

import (
	"context"
	"fmt"
)

// InvalidJSONInput contains a failed JSON decode of an upstream 2xx response; health policy treats it as 502.
type InvalidJSONInput struct {
	ProviderID              int64
	ProviderName, RequestID string
	UpstreamStatus          int
	Headers                 map[string][]string
	Body                    []byte
	ParseError              error
	RequestedModels         []string
}
type InvalidJSONPorts interface {
	Log(string)
	Health(context.Context, int, []byte, []string) ErrorDecision
	Failover(int, map[string][]string, []byte, bool) error
}

// InvalidJSON applies health policy and returns a generic parse error or a failover signal.
func InvalidJSON(ctx context.Context, p InvalidJSONPorts, in InvalidJSONInput) error {
	const status = 502
	p.Log(fmt.Sprintf("Provider %d(%s): upstream returned non-JSON 2xx response, attempting failover: status=%d request_id=%s error=%v", in.ProviderID, in.ProviderName, in.UpstreamStatus, in.RequestID, in.ParseError))
	decision := p.Health(ctx, status, in.Body, in.RequestedModels)
	if decision.Generic {
		return fmt.Errorf("upstream returned invalid JSON (not in custom error codes): %w", in.ParseError)
	}
	return p.Failover(status, in.Headers, in.Body, decision.RetrySameProvider)
}
