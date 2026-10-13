package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// HTTP text failures can describe client input, provider credentials, capacity
// or transient transport behavior. The classifier excludes request-scoped
// rejections before applying platform health, then combines that decision with
// protocol defaults and provider retry settings to decide whether to return a
// failover signal.
//
// A nil Gin context follows the default non-Grok rejection gate before health
// handling, while Grok uses its own health policy. Selected failover decisions
// record the upstream request and optional bounded body detail in Ops. Final
// protocol errors remain with the calling Chat or Messages writer, and the
// outer text loop owns output commitment and provider switching. This keeps
// health classification separate from final client presentation.

// httpFailover applies text HTTP failure classification and returns a provider
// recovery signal when permitted by policy. Callers supply the selected
// provider, response metadata and captured body; the final protocol writer
// handles nil results. Cyber, parameter, context-window and Grok policy
// evidence bypass provider-health updates.
//
// Without Gin, a default nonfailover non-Grok status also ends before health
// policy. Remaining failures use their platform health adapter, and generic
// rejection or a nonfailover decision returns nil. An accepted decision
// records Ops evidence and returns same-provider retry metadata, while the
// outer caller independently enforces its output window before switching
// providers.
func (s *OpenAITextExecutor) httpFailover(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	resp *http.Response,
	respBody []byte,
	upstreamMsg string,
	upstreamModel string,
) *forwardcore.UpstreamFailoverError {
	shouldFailover := gatewayprovider.ShouldFailoverOpenAIResponse(resp.StatusCode, upstreamMsg, respBody)
	if provider != nil && provider.Record.Platform == capability.PlatformGrok {
		shouldFailover = gatewayprovider.ShouldFailoverGrokResponse(resp.StatusCode, respBody)
	}
	// These payloads describe input or content rejection for the current request;
	// provider cooldown or pool recovery would attribute that rejection to another scope.
	if detectHit, _, _ := openai.DetectOpenAICyberPolicy(respBody); detectHit || gatewayprovider.IsOpenAICyberWarningPayload(respBody, upstreamMsg) ||
		openai.IsOpenAIClientInvalidRequestError(resp.StatusCode, upstreamMsg, respBody) ||
		openai.IsOpenAIContextWindowError(upstreamMsg, respBody) ||
		(provider != nil && provider.Record.Platform == capability.PlatformGrok && grok.IsGrokContentPolicyRejection(resp.StatusCode, respBody)) {
		return nil
	}
	// Direct non-Grok calls without HTTP state follow the default rejection gate;
	// only default failover reaches health, while Grok still uses its platform policy.
	if c == nil && !shouldFailover && (provider == nil || provider.Record.Platform != capability.PlatformGrok) {
		return nil
	}
	var decision providercore.UpstreamErrorDecision
	if provider != nil && provider.Record.Platform == capability.PlatformGrok {
		decision = gatewayprovider.ApplyGrokExecutionHealth(ctx, s.Grok.Health, provider, resp.StatusCode, resp.Header, respBody, "", upstreamModel)
	} else {
		decision = gatewayprovider.ApplyOpenAIResponseHealth(ctx, s.Output.Health, provider, resp.StatusCode, resp.Header, respBody, false, upstreamModel)
	}
	if decision.ShouldReturnGenericError() || !decision.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode, shouldFailover) {
		return nil
	}
	upstreamDetail := ""
	if s.Output.Options.LogUpstreamErrorBody {
		maxBytes := s.Output.Options.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		// Bound optional logged body detail while health and classification keep
		// the captured response bytes; truncation retains complete UTF-8 characters in Ops.
		upstreamDetail = logredact.TruncateUTF8(string(respBody), maxBytes)
	}
	AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
		Platform:           provider.Record.Platform,
		ProviderID:         provider.Record.ID,
		ProviderName:       provider.Record.Name,
		UpstreamStatusCode: resp.StatusCode,
		UpstreamRequestID:  resp.Header.Get("x-request-id"),
		Kind:               "failover",
		Message:            upstreamMsg,
		Detail:             upstreamDetail,
	})
	return gatewayprovider.NewOpenAIUpstreamFailure(
		resp.StatusCode,
		resp.Header,
		respBody,
		upstreamMsg,
		decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode),
	)
}
