package messageforward

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
)

// Execute creates state for one Messages attempt; the outer gateway selects any failover provider.
// @project-doc docs/architecture/gateway_request_lifecycle.md#protocol_conversion_boundary
func (r *Runtime) Execute(ctx context.Context, output HTTPBoundary, target *gatewayadapter.ExecutionProvider, parsed *requeststate.ParsedRequest) (*forward.MessagesResult, error) {
	adapter := newAttempt(r, output, target)
	result, err := forward.Messages(ctx, adapter, adapter.input(), parsed)
	return messagesResult(result), err
}

// Count runs the counting flow with its own model preparation and error output.
func (r *Runtime) Count(ctx context.Context, output HTTPBoundary, target *gatewayadapter.ExecutionProvider, parsed *requeststate.ParsedRequest) error {
	adapter := &countAttempt{attempt: newAttempt(r, output, target)}
	return forward.CountTokens(ctx, adapter, adapter.input(), parsed)
}

// Chat converts Messages responses to Chat using tool restoration state local to the call.
func (r *Runtime) Chat(ctx context.Context, output HTTPBoundary, target *gatewayadapter.ExecutionProvider, body []byte) (*forward.MessagesResult, error) {
	adapter := &conversionAttempt{s: r, c: output, provider: target, state: &AttemptState{}}
	result, err := forward.AsChat(ctx, adapter, forward.ConversionInput{OAuth: target.View().IsOAuth()}, body)
	return messagesResult(result), err
}

func (r *Runtime) Responses(ctx context.Context, output HTTPBoundary, target *gatewayadapter.ExecutionProvider, body []byte) (*forward.MessagesResult, error) {
	adapter := &conversionAttempt{s: r, c: output, provider: target, responses: true, state: &AttemptState{}}
	result, err := forward.AsResponses(ctx, adapter, forward.ConversionInput{OAuth: target.View().IsOAuth()}, body)
	return messagesResult(result), err
}

// messagesResult returns a shallow copy of the forwarding result.
func messagesResult(result *forward.Result) *forward.MessagesResult {
	if result == nil {
		return nil
	}
	copy := forward.MessagesResult(*result)
	return &copy
}
