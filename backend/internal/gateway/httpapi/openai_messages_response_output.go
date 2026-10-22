package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaytelemetry "github.com/TokenFlux/TokenRouter/internal/gateway/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// openai_messages_response_output.go binds Messages response reading to
// request observations and stream failure policy.
// Messages clients can receive output converted from Responses events whose
// transport status remains HTTP 200 even on terminal failure. Error
// classification therefore reads event evidence and applies provider health
// before constructing Anthropic errors. Buffered cyber-policy failures record
// token observations, while stream failures use the reader's output state to
// decide whether another provider may run.
//
// MessagesOptions reuses Chat reader bindings for shared observations and
// supplies Messages-specific writes and terminal handling. A bare error event
// has its own failover predicate, so it is distinguished from a failed
// Response object. After business output starts, a failure ends the current
// Anthropic stream; combining output from another provider would misassociate
// the response content.

// messagesStreamFailure selects failover or an Anthropic terminal error from
// Responses failure evidence. Callers identify bare error events separately
// from response.failed and supply the reader's business-output state. Provider
// health classification runs before the corresponding event predicate is
// evaluated.
//
// A pre-output retryable decision returns a failover error after marking
// request failure effects. Once output has begun, the result describes an
// error for the current stream. Generic rejection keeps its own message; other
// decisions may use configured event-derived status and message overrides
// after Ops recording.
func (p *OpenAIResponseOutput) messagesStreamFailure(c *gin.Context, provider *gatewayprovider.ExecutionProvider, resp *http.Response, requestID, upstreamModel string, payloadBytes []byte, message string, clientOutputStarted, isBareErrorEvent bool) openai.ChatFailure {
	policyStatus, decision := p.ApplyStreamFailurePolicy(
		c.Request.Context(), provider, upstreamModel, resp.Header, payloadBytes, message,
	)
	policyGeneric := decision.ShouldReturnGenericError()
	// A bare error and a failed Response carry different failover evidence. Choose
	// the corresponding predicate before combining it with health and output state.
	shouldFailoverSignal := openai.OpenAIStreamFailedEventShouldFailover(payloadBytes, message)
	if isBareErrorEvent {
		shouldFailoverSignal = openai.OpenAIStreamErrorEventShouldFailover(payloadBytes, message)
	}
	// The reader permits provider switching only before business content starts;
	// subsequent failures must end this Messages stream to keep one response identity.
	if !clientOutputStarted && decision.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(provider), policyStatus, shouldFailoverSignal) {
		MarkOpenAIResponseFailureEffects(c, policyStatus, decision.StopScheduling)
		failure := p.NewStreamPolicyFailureWithModel(
			c, provider, false, requestID, resp.Header, payloadBytes, message, upstreamModel,
		)
		return openai.ChatFailure{Failover: failure}
	}
	message = p.RecordStreamError(c, provider, false, requestID, "http_error", payloadBytes, message)
	errStatus, errType, errMsg := http.StatusBadGateway, "api_error", message
	if policyGeneric {
		errStatus, errType, errMsg = http.StatusInternalServerError, "api_error", "Upstream gateway error"
	}
	// A terminal failure inside HTTP 200 needs event-derived status and normalized
	// bytes so administrator presentation rules can match the failure itself.
	if status, passthroughType, passthroughMsg, matched := ApplyOpenAIStreamFailedErrorRule(
		c, provider.Record.Platform, payloadBytes, message,
	); matched && !policyGeneric {
		if passthroughMsg == "" {
			passthroughMsg = errMsg
		}
		errStatus, errType, errMsg = status, passthroughType, passthroughMsg
		MarkResponseCommitted(c)
	}
	return openai.ChatFailure{Status: errStatus, Type: errType, Message: errMsg}
}

// MessagesOptions binds Responses-to-Messages reading to one live HTTP
// attempt. It inherits Chat observations and substitutes Anthropic output and
// failure callbacks; callers retain request and response state until reading
// completes and own closing the response body.
func (p *OpenAIResponseOutput) MessagesOptions(c *gin.Context, provider *gatewayprovider.ExecutionProvider, resp *http.Response, originalModel, billingModel, upstreamModel string) openai.MessagesResponseOptions {
	options := p.ChatOptions(c, provider, resp, originalModel, billingModel, upstreamModel)
	options.ReadBuffered = func() (*wire.ResponsesResponse, wire.ForwardUsage, *bridge.BufferedResponseAccumulator, error) {
		return p.ReadBufferedTerminal(resp, c, "openai messages buffered", resp.Header.Get("x-request-id"))
	}
	options.BufferedFailure = func(final *wire.ResponsesResponse, usage wire.ForwardUsage) error {
		return p.bufferedResponseFailure(
			c, provider, resp, resp.Header.Get("x-request-id"), upstreamModel, final, usage,
			"api_error", WriteForwardAnthropicError,
		)
	}
	options.WriteError = func(status int, kind, message string) { WriteForwardAnthropicError(c, status, kind, message) }
	options.BuildStreamError = BuildForwardAnthropicStreamError
	return openai.MessagesResponseOptions{
		ChatResponseOptions: options,
		MessagesFailure: func(body []byte, message string, started, bare bool) openai.ChatFailure {
			return p.messagesStreamFailure(c, provider, resp, resp.Header.Get("x-request-id"), upstreamModel, body, message, started, bare)
		},
		MissingUsage: func(usage *wire.ForwardUsage, event string, disconnected bool) {
			if resp == nil {
				return
			}
			var id int64
			if provider != nil {
				id = provider.Record.ID
			}
			gatewaytelemetry.SuccessMissingUsage(c.Request.Context(), id, resp.StatusCode, usage, event, disconnected)
		},
		MissingTerminal: func(message string) error {
			return p.NewStreamFailure(c, provider, false, resp.Header.Get("x-request-id"), nil, message)
		},
		RecordMissingTerminal: func(message string) {
			p.RecordMessagesStreamError(c, provider, resp.Header.Get("x-request-id"), "stream_missing_terminal", message)
		},
	}
}
