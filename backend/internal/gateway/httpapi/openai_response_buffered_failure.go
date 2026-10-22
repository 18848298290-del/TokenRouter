package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/moderationflow"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// openai_response_buffered_failure.go handles failed Responses terminals before
// Chat or Messages JSON conversion. The terminal supplies failure evidence
// inside a successful HTTP transport response, so provider health and final
// presentation derive their statuses from that evidence. Cyber rejection keeps
// observed usage with its moderation mark and ends processing before provider
// health or administrator presentation rules run.
//
// Health can select a generic rejection or provider failover. Failover publishes
// applied effects for the error builder to consume, preventing another health
// update for the same failure. Remaining failures record Ops before matching
// presentation rules. The supplied protocol writer controls its usual response
// commitment, while a matching rule marks commitment before either writer runs.

// bufferedResponseFailure handles one failed terminal before buffered output
// and returns the failure for the attempt loop. Callers supply the protocol
// writer and its default error type, and keep the request, provider and response
// available throughout this synchronous operation.
//
// Cyber evidence and health decisions precede final presentation. A failover
// result carries the already-applied health effects and leaves client output
// unwritten. Final rule matches mark commitment before writing; other final
// branches rely on the supplied writer's commitment behavior.
func (p *OpenAIResponseOutput) bufferedResponseFailure(
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	resp *http.Response,
	requestID, upstreamModel string,
	finalResponse *wire.ResponsesResponse,
	usage wire.ForwardUsage,
	defaultErrorType string,
	writeError func(*gin.Context, int, string, string),
) error {
	// Classification continues if event marshaling fails; the message still comes
	// from finalResponse, while byte-based cyber detection and rules receive no payload.
	payload, _ := json.Marshal(map[string]any{"type": "response.failed", "response": finalResponse})
	if hit, code, msg := openai.DetectOpenAICyberPolicy(payload); hit {
		// Buffered cyber rejection precedes converted client content. Attach observed
		// usage to its moderation mark before the protocol writer publishes the error.
		// Retained moderation evidence is capped at 4096 UTF-8-safe bytes.
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
		writeError(c, http.StatusBadRequest, "invalid_request_error", clientMsg)
		return fmt.Errorf("openai cyber_policy: %s", msg)
	}
	message := openAICompatFailedResponseMessage(finalResponse)
	policyStatus, decision := p.ApplyStreamFailurePolicy(
		c.Request.Context(), provider, upstreamModel, resp.Header, payload, message,
	)
	if decision.ShouldReturnGenericError() {
		writeError(c, http.StatusInternalServerError, "api_error", "Upstream gateway error")
		return fmt.Errorf("upstream response failed: status=%d (not in custom error codes)", policyStatus)
	}
	// Health policy can request another provider before buffered JSON is written;
	// publishing failure effects lets the builder reuse that health decision once.
	if decision.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(provider), policyStatus, openai.OpenAIStreamFailedEventShouldFailover(payload, message)) {
		MarkOpenAIResponseFailureEffects(c, policyStatus, decision.StopScheduling)
		return p.NewStreamPolicyFailureWithModel(
			c, provider, false, requestID, resp.Header, payload, message, upstreamModel,
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
		// Rule-selected output is final for both protocols. Mark it before calling
		// the writer so outer attempt handling observes the dedicated presentation.
		MarkResponseCommitted(c)
		writeError(c, status, errType, errMsg)
		return fmt.Errorf("upstream response failed (passthrough): %s", errMsg)
	}
	writeError(c, http.StatusBadGateway, defaultErrorType, message)
	return fmt.Errorf("upstream response failed: %s", message)
}
