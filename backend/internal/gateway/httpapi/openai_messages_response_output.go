package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/moderationflow"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaytelemetry "github.com/TokenFlux/TokenRouter/internal/gateway/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

func (p *OpenAIResponseOutput) messagesBufferedFailure(c *gin.Context, provider *gatewayprovider.ExecutionProvider, resp *http.Response, requestID, upstreamModel string, finalResponse *wire.ResponsesResponse, usage wire.ForwardUsage) error {
	payload, _ := json.Marshal(gin.H{"type": "response.failed", "response": finalResponse})
	if hit, code, msg := openai.DetectOpenAICyberPolicy(payload); hit {
		MarkOpsCyberPolicy(c, moderationflow.Mark{
			Code:           code,
			Message:        msg,
			Body:           logredact.TruncateUTF8(string(payload), 4096),
			UpstreamStatus: http.StatusOK,
			UpstreamInTok:  usage.InputTokens,
			UpstreamOutTok: usage.OutputTokens,
		})
		clientMsg := msg
		if clientMsg == "" {
			clientMsg = "Request blocked by upstream cyber-security policy"
		}
		WriteForwardAnthropicError(c, http.StatusBadRequest, "invalid_request_error", clientMsg)
		return fmt.Errorf("openai cyber_policy: %s", msg)
	}
	message := openAICompatFailedResponseMessage(finalResponse)
	policyStatus, decision := p.ApplyStreamFailurePolicy(
		c.Request.Context(), provider, upstreamModel, resp.Header, payload, message,
	)
	if decision.ShouldReturnGenericError() {
		WriteForwardAnthropicError(c, http.StatusInternalServerError, "api_error", "Upstream gateway error")
		return fmt.Errorf("upstream response failed: status=%d (not in custom error codes)", policyStatus)
	}
	if decision.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(provider), policyStatus, openai.OpenAIStreamFailedEventShouldFailover(payload, message)) {
		MarkOpenAIResponseFailureEffects(c, policyStatus, decision.StopScheduling)
		return p.NewStreamPolicyFailureWithModel(
			c, provider, false, requestID, resp.Header, policyStatus, payload, message,
			gatewayprovider.OpenAIStreamFailureRetryable(provider, payload, message), upstreamModel,
		)
	}
	message = p.RecordStreamError(c, provider, false, requestID, "http_error", payload, message)
	// Infer the event status and normalize its body for status-based passthrough rules.
	if status, errType, errMsg, matched := ApplyOpenAIStreamFailedErrorRule(
		c, provider.Record.Platform, payload, message,
	); matched {
		if errMsg == "" {
			errMsg = message
		}
		MarkResponseCommitted(c)
		WriteForwardAnthropicError(c, status, errType, errMsg)
		return fmt.Errorf("upstream response failed (passthrough): %s", errMsg)
	}
	WriteForwardAnthropicError(c, http.StatusBadGateway, "api_error", message)
	return fmt.Errorf("upstream response failed: %s", message)
}

func (p *OpenAIResponseOutput) messagesStreamFailure(c *gin.Context, provider *gatewayprovider.ExecutionProvider, resp *http.Response, requestID, upstreamModel string, payloadBytes []byte, message string, clientOutputStarted, isBareErrorEvent bool) openai.ChatFailure {
	policyStatus, decision := p.ApplyStreamFailurePolicy(
		c.Request.Context(), provider, upstreamModel, resp.Header, payloadBytes, message,
	)
	policyGeneric := decision.ShouldReturnGenericError()
	// After client output starts, end with an Anthropic error; failover would concatenate two model streams.
	shouldFailoverSignal := openai.OpenAIStreamFailedEventShouldFailover(payloadBytes, message)
	if isBareErrorEvent {
		shouldFailoverSignal = openai.OpenAIStreamErrorEventShouldFailover(payloadBytes, message)
	}
	if !clientOutputStarted && decision.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(provider), policyStatus, shouldFailoverSignal) {
		MarkOpenAIResponseFailureEffects(c, policyStatus, decision.StopScheduling)
		failure := p.NewStreamPolicyFailureWithModel(
			c, provider, false, requestID, resp.Header, policyStatus, payloadBytes, message,
			gatewayprovider.OpenAIStreamFailureRetryable(provider, payloadBytes, message), upstreamModel,
		)
		return openai.ChatFailure{Failover: failure}
	}
	message = p.RecordStreamError(c, provider, false, requestID, "http_error", payloadBytes, message)
	errStatus, errType, errMsg := http.StatusBadGateway, "api_error", message
	if policyGeneric {
		errStatus, errType, errMsg = http.StatusInternalServerError, "api_error", "Upstream gateway error"
	}
	// Infer the event status and normalize its body for status-based passthrough rules.
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

func (p *OpenAIResponseOutput) MessagesOptions(c *gin.Context, provider *gatewayprovider.ExecutionProvider, resp *http.Response, originalModel, billingModel, upstreamModel string) openai.MessagesResponseOptions {
	options := p.ChatOptions(c, provider, resp, originalModel, billingModel, upstreamModel)
	options.ReadBuffered = func() (*wire.ResponsesResponse, wire.ForwardUsage, *bridge.BufferedResponseAccumulator, error) {
		return p.ReadBufferedTerminal(resp, c, "openai messages buffered", resp.Header.Get("x-request-id"))
	}
	options.BufferedFailure = func(final *wire.ResponsesResponse, usage wire.ForwardUsage) error {
		return p.messagesBufferedFailure(c, provider, resp, resp.Header.Get("x-request-id"), upstreamModel, final, usage)
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
