package httpapi

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	egressadapter "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/moderationflow"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// openai_chat_response_output.go binds Responses-to-Chat readers to failure
// policy, HTTP presentation, and request-local usage observations.
// Responses failures reach Chat clients through either a buffered JSON
// rejection or a terminal stream error. Provider health classification runs
// before presentation rules, so a custom error message cannot by itself reopen
// provider selection. The response reader supplies whether business output has
// begun, and only a failure before that point can return a failover signal.
//
// The callbacks retain the selected provider, response headers and
// request-local observations until reading finishes. Buffered cyber-policy
// failures also carry observed token counts into the moderation mark.
// ChatOptions delegates conversion and output to the upstream reader while
// binding these HTTP decisions, model and tier observations, optional header
// filtering, and reasoning/tool restoration to that attempt.

// chatBufferedFailure handles a failed terminal Responses object before
// buffered Chat output. It records cyber-policy evidence first; otherwise
// provider health classification chooses generic rejection, failover, or a
// client error. A failover result leaves final response writing to the outer
// attempt loop.
//
// For a final client error, event-derived status and normalized failure bytes
// feed configured error rules after Ops recording. Those rules can replace
// status and message, while the unhandled case emits a Chat upstream_error
// using the captured failure message.
func (p *OpenAIResponseOutput) chatBufferedFailure(c *gin.Context, provider *gatewayprovider.ExecutionProvider, resp *http.Response, requestID, upstreamModel string, finalResponse *wire.ResponsesResponse, usage wire.ForwardUsage) error {
	// Classification continues if event marshaling fails; the message still comes
	// from finalResponse, while byte-based cyber detection and rules receive no payload.
	payload, _ := json.Marshal(map[string]any{"type": "response.failed", "response": finalResponse})
	if hit, code, msg := openai.DetectOpenAICyberPolicy(payload); hit {
		// A buffered cyber rejection has no client content yet. Attach its observed
		// usage to the moderation mark before returning the dedicated Chat error.
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
		WriteForwardChatError(c, http.StatusBadRequest, "invalid_request_error", clientMsg)
		return fmt.Errorf("openai cyber_policy: %s", msg)
	}
	message := openAICompatFailedResponseMessage(finalResponse)
	policyStatus, decision := p.ApplyStreamFailurePolicy(
		c.Request.Context(), provider, upstreamModel, resp.Header, payload, message,
	)
	if decision.ShouldReturnGenericError() {
		WriteForwardChatError(c, http.StatusInternalServerError, "api_error", "Upstream gateway error")
		return fmt.Errorf("upstream response failed: status=%d (not in custom error codes)", policyStatus)
	}
	// Health policy can request another provider before buffered JSON is written;
	// publishing failure effects lets the outer attempt loop account for that choice.
	if decision.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(provider), policyStatus, openai.OpenAIStreamFailedEventShouldFailover(payload, message)) {
		MarkOpenAIResponseFailureEffects(c, policyStatus, decision.StopScheduling)
		return p.NewStreamPolicyFailureWithModel(
			c, provider, false, requestID, resp.Header, policyStatus, payload, message,
			gatewayprovider.OpenAIStreamFailureRetryable(provider, payload, message), upstreamModel,
		)
	}
	message = p.RecordStreamError(c, provider, false, requestID, "http_error", payload, message)
	// HTTP 200 carries no failure status for these events. Infer one from the
	// terminal object so status-based presentation rules see the upstream failure.
	if status, errType, errMsg, matched := ApplyOpenAIStreamFailedErrorRule(
		c, provider.Record.Platform, payload, message,
	); matched {
		if errMsg == "" {
			errMsg = message
		}
		MarkResponseCommitted(c)
		WriteForwardChatError(c, status, errType, errMsg)
		return fmt.Errorf("upstream response failed (passthrough): %s", errMsg)
	}
	WriteForwardChatError(c, http.StatusBadGateway, "upstream_error", message)
	return fmt.Errorf("upstream response failed: %s", message)
}

// chatStreamFailure translates a failed Responses event into a ChatFailure for
// the stream reader. Callers pass the reader's business-output state, because
// only a pre-output failure can return a provider failover error. Health
// effects are recorded before that signal is returned.
//
// Once output has begun, the returned status, type and message describe the
// terminal Chat error. Generic health-policy rejection suppresses custom
// presentation overrides; other failures may use normalized event bytes for an
// administrator rule and mark its response as committed.
func (p *OpenAIResponseOutput) chatStreamFailure(c *gin.Context, provider *gatewayprovider.ExecutionProvider, resp *http.Response, requestID, upstreamModel string, payloadBytes []byte, message string, clientOutputStarted bool) openai.ChatFailure {
	policyStatus, decision := p.ApplyStreamFailurePolicy(
		c.Request.Context(), provider, upstreamModel, resp.Header, payloadBytes, message,
	)
	policyGeneric := decision.ShouldReturnGenericError()
	// A second provider may start only while the reader still holds business
	// output; after publication, its content would be appended to an existing stream.
	if !clientOutputStarted && decision.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(provider), policyStatus, openai.OpenAIStreamFailedEventShouldFailover(payloadBytes, message)) {
		MarkOpenAIResponseFailureEffects(c, policyStatus, decision.StopScheduling)
		failure := p.NewStreamPolicyFailureWithModel(
			c, provider, false, requestID, resp.Header, policyStatus, payloadBytes, message,
			gatewayprovider.OpenAIStreamFailureRetryable(provider, payloadBytes, message), upstreamModel,
		)
		return openai.ChatFailure{Failover: failure}
	}
	message = p.RecordStreamError(c, provider, false, requestID, "http_error", payloadBytes, message)
	defaultStatus, defaultErrType, defaultMsg := http.StatusBadGateway, "upstream_error", message
	if policyGeneric {
		defaultStatus, defaultErrType, defaultMsg = http.StatusInternalServerError, "upstream_error", "Upstream gateway error"
	}
	// Derive rule input from event failure evidence, because the enclosing HTTP
	// status is successful; generic policy rejection keeps its own terminal message.
	if status, errType, errMsg, matched := ApplyOpenAIStreamFailedErrorRule(
		c, provider.Record.Platform, payloadBytes, message,
	); matched && !policyGeneric {
		if errMsg == "" {
			errMsg = defaultMsg
		}
		defaultStatus, defaultErrType, defaultMsg = status, errType, errMsg
		MarkResponseCommitted(c)
	}
	return openai.ChatFailure{Status: defaultStatus, Type: defaultErrType, Message: defaultMsg}
}

// ChatOptions binds Chat conversion, observations and error handling to one
// HTTP attempt. Keep c, provider and resp alive until the reader completes;
// response closure stays with the execution caller, and optional header
// filtering runs when output is prepared.
func (p *OpenAIResponseOutput) ChatOptions(c *gin.Context, provider *gatewayprovider.ExecutionProvider, resp *http.Response, originalModel, billingModel, upstreamModel string) openai.ChatResponseOptions {
	options := openai.ChatResponseOptions{
		Runtime:        bridge.Runtime{Now: time.Now, ReadRandom: rand.Read},
		RequestContext: c.Request.Context(),
		ReadBuffered: func() (*wire.ResponsesResponse, wire.ForwardUsage, *bridge.BufferedResponseAccumulator, error) {
			return p.ReadBufferedTerminal(resp, c, "openai chat_completions buffered", resp.Header.Get("x-request-id"))
		},
		BufferedReadFailure: func(err error) error {
			return p.BufferedReadFailure(c, provider, resp, resp.Header.Get("x-request-id"), err)
		},
		BufferedFailure: func(final *wire.ResponsesResponse, usage wire.ForwardUsage) error {
			return p.chatBufferedFailure(c, provider, resp, resp.Header.Get("x-request-id"), upstreamModel, final, usage)
		},
		ObserveFinal: func(final *wire.ResponsesResponse) {
			observer := UpstreamResponseModelObserverFromContext(c)
			if observer == nil {
				observer = BeginUpstreamResponseModelObservation(c)
			}
			observer.Observe(final.Model, true)
			observer.ObserveServiceTier(final.ServiceTier, true)
		},
		MissingUsage: func(final *wire.ResponsesResponse, usage wire.ForwardUsage) error {
			// Eligible Grok Chat models require observed billing usage even after a
			// terminal object; expose its absence as a dedicated attempt failure.
			if gatewayprovider.RequiresBillableGrokChatUsage(provider, billingModel, upstreamModel, final.Model) && !gatewayprovider.HasBillableGrokChatUsage(usage) {
				return NewGrokMissingUsageFailure(c, provider, requeststate.FirstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id")))
			}
			return nil
		},
		Headers: func(dst, src http.Header) {
			if p.Headers != nil {
				egressadapter.WriteFilteredHeaders(dst, src, p.Headers)
			}
		},
		WriteError:   func(status int, kind, message string) { WriteForwardChatError(c, status, kind, message) },
		ServiceTier:  func() string { return ObservedUpstreamResponseServiceTier(c) },
		CountSearch:  provider != nil && provider.View().IsGrok(),
		JSONSearch:   grok.CountGrokNativeSearchCallsFromJSONBytes,
		StreamSearch: grok.CountGrokNativeSearchCallsInSSEDataDedup,
		Scanner:      func(r io.Reader) *bufio.Scanner { return p.Scanner(r) },
		StreamInterval: func() time.Duration {
			if p.Options.Configured && p.Options.StreamDataIntervalTimeout > 0 {
				// Configured stream idle intervals are stored as whole seconds.
				return time.Duration(p.Options.StreamDataIntervalTimeout) * time.Second
			}
			return 0
		},
		KeepaliveInterval: func() time.Duration {
			if p.Options.Configured && p.Options.StreamKeepaliveInterval > 0 {
				// Configured heartbeat intervals are stored as whole seconds.
				return time.Duration(p.Options.StreamKeepaliveInterval) * time.Second
			}
			return 0
		},
		RestoreToolNames: func(body []byte) []byte { return RestoreCodexToolNamesFromContext(c, body) },
		Observe:          func(body []byte, event string) { ObserveOpenAIServiceTierInContext(c, body, event) },
		CyberForwarded:   forwardcore.ErrCyberPolicyForwarded,
		MarkCyber: func(value openai.CyberObservation) {
			MarkOpsCyberPolicy(c, moderationflow.Mark{Code: value.Code, Message: value.Message, Body: value.Body, UpstreamStatus: value.UpstreamStatus, UpstreamInTok: value.UpstreamInTok, UpstreamOutTok: value.UpstreamOutTok})
		},
		Truncate:         logredact.TruncateUTF8,
		BuildStreamError: BuildForwardChatStreamError,
		StreamFailure: func(body []byte, message string, started bool) openai.ChatFailure {
			return p.chatStreamFailure(c, provider, resp, resp.Header.Get("x-request-id"), upstreamModel, body, message, started)
		},
		SilentRefusal: func() error {
			return NewOpenAISilentRefusalFailoverError(c, ExecutionErrorProvider(provider), resp.Header.Get("x-request-id"))
		},
		ReadFailure: openai.NewUpstreamStreamReadError,
	}
	return options
}
