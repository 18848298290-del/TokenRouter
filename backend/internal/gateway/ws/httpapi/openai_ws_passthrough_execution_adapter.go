package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	openaicore "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
	openaiwsv2 "github.com/TokenFlux/TokenRouter/internal/upstream/openai/ws/relay"
)

// wsPassthroughAdapter holds platform credentials, handshake parameters, and execution operations.
type wsPassthroughAdapter struct {
	gatewayws.RequestUsageDecoder
	service  *OpenAIWebSocketExecutor
	request  *gin.Context
	provider *gatewayprovider.ExecutionProvider
	token    string
	hooks    *gatewayws.OpenAIIngressHooks
	decision egress.OpenAIWSProtocolDecision
	router   egress.TLSFingerprintRouterMatchResult
	headers  http.Header
	wsURL    string
	proxyURL string
}

func (p *wsPassthroughAdapter) ModelAttributes(ctx context.Context, turn int, model string, body []byte) ([]byte, error) {
	if p.hooks != nil && p.hooks.ContextForTurn != nil {
		ctx = p.hooks.ContextForTurn(turn)
	}
	var service *routing.ModelAttributeService
	if p.service.Requests != nil && p.service.Requests.Readers != nil {
		service = p.service.Requests.Readers.Attributes
	}
	return gatewayprovider.PrepareModelAttributeParameters(ctx, p.provider, model, body, service)
}

func (p *wsPassthroughAdapter) IsLite(body []byte) bool {
	return gatewayprovider.ImageIntent().IsOpenAIResponsesLiteWebSocketPayload(body)
}

func (p *wsPassthroughAdapter) NormalizeLite(body []byte) ([]byte, error) {
	value, _, err := gatewayprovider.NormalizeResponsesLiteForProvider(p.provider.View(), body)
	return value, err
}

func (p *wsPassthroughAdapter) Reasoning(body []byte, model string) ([]byte, error) {
	return gatewayws.ApplyReasoningEffortPolicy(body, p.hooks, model)
}

func (p *wsPassthroughAdapter) Models(turn int, model string, body []byte) (string, string, error) {
	return resolveOpenAIWSTurnModels(p.provider, p.hooks, turn, model, body)
}

func (p *wsPassthroughAdapter) AliasTools(body []byte) ([]byte, error) {
	out, reverse, changed, err := openai.AliasOpenAIOAuthReservedToolNamesBody(body)
	if err != nil {
		return nil, err
	}
	gatewayhttp.SetCodexToolNameReverse(p.request, reverse)
	if changed {
		return out, nil
	}
	return body, nil
}

func (p *wsPassthroughAdapter) Compatibility(body []byte, lite bool) ([]byte, bool, error) {
	return gatewayprovider.NormalizeOpenAIResponsesWebSocketCompatibilityBody(body, gatewayprovider.ExecutionProtocolRecord(p.provider), lite)
}

func (p *wsPassthroughAdapter) ScopeIdentity(body []byte) ([]byte, bool, error) {
	return openai.ApplyCodexProviderIdentityClientMetadataRaw(body, provideradapter.CodexIdentityNamespace(gatewayhttp.CodexIdentityRecord(p.request, p.provider.View())), gatewayhttp.APIKeyIDFromContext(p.request))
}

func (p *wsPassthroughAdapter) FastPolicy(ctx context.Context, turn int, model string, body []byte, scoped bool) ([]byte, *gatewayws.PolicyBlocked, error) {
	if scoped {
		ctx = openAIWSFastModePolicyContext(ctx, p.hooks, turn)
	}
	out, blocked, err := gatewayws.ApplyServiceTierFrame(body, model, p.service.FastPolicy.Input(ctx, p.provider, model))
	if blocked == nil {
		return out, nil, err
	}
	return out, &gatewayws.PolicyBlocked{Message: blocked.Message, Cause: blocked}, err
}

func (p *wsPassthroughAdapter) PromptReplace(ctx context.Context, body []byte) []byte {
	if p.service == nil {
		return body
	}
	return p.service.Prompts.ApplyUserPromptReplacementToBody(ctx, body, "openai_responses")
}

func (p *wsPassthroughAdapter) BlockedEvent(blocked *gatewayws.PolicyBlocked) []byte {
	return gatewayws.BuildFastPolicyBlockedEvent(&tierpolicy.BlockedError{Message: blocked.Message})
}

func (p *wsPassthroughAdapter) PolicyDenied() {
	gatewayhttp.MarkOpsClientBusinessLimited(p.request, gatewayhttp.OpsClientBusinessLimitedReasonLocalPolicyDenied)
}

func (p *wsPassthroughAdapter) SetUpstreamModel(model string) {
	gatewayhttp.SetOpsUpstreamModel(p.request, model)
}

func (p *wsPassthroughAdapter) PrepareDial(ctx context.Context, body []byte, promptCacheKey string) error {
	wsURL, err := p.service.buildOpenAIResponsesWSURL(p.provider)
	if err != nil {
		return fmt.Errorf("build ws url: %w", err)
	}
	wsHost := "-"
	wsPath := "-"
	if parsedURL, parseErr := url.Parse(wsURL); parseErr == nil && parsedURL != nil {
		wsHost = gatewayprovider.NormalizeOpenAIWSLogValue(parsedURL.Host)
		wsPath = gatewayprovider.NormalizeOpenAIWSLogValue(parsedURL.Path)
	}
	logOpenAIWSV2Passthrough(
		"relay_dial_start provider_id=%d ws_host=%s ws_path=%s proxy_enabled=%v",
		p.provider.Record.ID,
		wsHost,
		wsPath,
		p.provider.Record.ProxyID != nil && p.provider.Record.Proxy != nil,
	)

	isCodexCLI := false
	if p.request != nil {
		isCodexCLI = openai.IsCodexOfficialClientByHeaders(p.request.GetHeader("User-Agent"), p.request.GetHeader("originator"))
	}
	if p.service.Options != nil && p.service.Requests.Options.ForceCLI {
		isCodexCLI = true
	}
	turnState := ""
	turnMetadata := ""
	if p.request != nil {
		turnState = strings.TrimSpace(p.request.GetHeader(openAIWSTurnStateHeader))
		turnMetadata = strings.TrimSpace(p.request.GetHeader(openai.WSTurnMetadataHeader))
	}
	headers, _, buildHdrErr := p.service.buildOpenAIWSHeaders(
		ctx,
		p.request,
		p.provider,
		p.token,
		p.decision,
		isCodexCLI,
		turnState,
		turnMetadata,
		promptCacheKey,
		gjson.GetBytes(body, "model").String(),
		gjson.GetBytes(body, "service_tier").String(),
		p.router,
	)
	if buildHdrErr != nil {
		return fmt.Errorf("build ws headers: %w", buildHdrErr)
	}
	proxyURL := ""
	if p.provider.Record.ProxyID != nil && p.provider.Record.Proxy != nil {
		proxyURL = p.provider.Record.Proxy.URL()
	}

	dialer := p.service.Connections.Dialer()
	if dialer == nil {
		return errors.New("openai ws passthrough dialer is nil")
	}

	p.headers = headers
	p.wsURL = wsURL
	p.proxyURL = proxyURL
	return nil
}

func (p *wsPassthroughAdapter) DialOnce(ctx context.Context) (gatewayws.DialResult, error) {
	headers, err := p.service.Requests.Identity.RefreshHeaders(ctx, p.provider, p.headers)
	if err != nil {
		return gatewayws.DialResult{PreparationError: true}, fmt.Errorf("refresh ws authentication headers: %w", err)
	}
	p.headers = headers
	dialCtx, cancel := context.WithTimeout(ctx, p.service.openAIWSDialTimeout())
	conn, status, handshake, err := p.service.Connections.Dialer().Dial(dialCtx, p.wsURL, p.headers, p.proxyURL, p.service.Requests.TLSProfile(p.provider, p.router))
	cancel()
	result := gatewayws.DialResult{Status: status, Headers: handshake}
	if err != nil {
		var failure *openai.WSHandshakeError
		if errors.As(err, &failure) && failure != nil {
			result.Body = failure.Body
		}
		return result, err
	}
	frames, ok := conn.(openaiwsv2.FrameConn)
	if !ok {
		_ = conn.Close()
		return gatewayws.DialResult{PreparationError: true}, errors.New("openai ws passthrough upstream connection does not support frame relay")
	}
	result.Conn = openAIWSCoreFrames{frames}
	logOpenAIWSV2Passthrough("relay_dial_ok provider_id=%d status_code=%d upstream_request_id=%s", p.provider.Record.ID, status, gatewayprovider.OpenAIWSHeaderValueForLog(handshake, "x-request-id"))
	return result, nil
}

func (p *wsPassthroughAdapter) CanRecover(ctx context.Context, result gatewayws.DialResult, err error) bool {
	failure := &openai.WSDialError{StatusCode: result.Status, ResponseHeaders: result.Headers, ResponseBody: result.Body, Err: err}
	return p.service.Requests.Identity.UsesAgentIdentity(ctx, p.provider) && openai.IsAgentTaskInvalidWSDialError(failure)
}

func (p *wsPassthroughAdapter) Recover(ctx context.Context) error {
	return p.service.Requests.Identity.Recover(ctx, p.provider, p.provider.View().GetCredential("task_id"))
}

func (p *wsPassthroughAdapter) DialFailure(ctx context.Context, model string, result gatewayws.DialResult, err error) error {
	logOpenAIWSV2Passthrough("relay_dial_failed provider_id=%d status_code=%d err=%s", p.provider.Record.ID, result.Status, gatewayprovider.TruncateOpenAIWSLogValue(err.Error(), gatewayprovider.OpenAIWSLogValueMaxLen))
	failure := &openai.WSDialError{StatusCode: result.Status, ResponseHeaders: upstreamcore.CloneHeader(result.Headers), ResponseBody: result.Body, Err: err}
	decision := p.service.handleOpenAIWSDialTransientFailure(ctx, p.provider, model, failure)
	if result.Status != 0 && decision.ShouldReturnGenericError() {
		return openAIWSGenericPolicyCloseError(result.Status)
	}
	if result.Status != 0 && decision.ShouldFailoverWithDefaults(gatewayprovider.ExecutionErrorPolicy(p.provider), result.Status, result.Status == http.StatusTooManyRequests, p.service.shouldFailoverOpenAIWSError(p.provider, result.Status, result.Body)) {
		return gatewayprovider.NewOpenAIUpstreamFailure(result.Status, result.Headers, result.Body, upstreamcore.ExtractErrorMessage(result.Body), decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(p.provider), result.Status))
	}
	return p.service.mapOpenAIWSPassthroughDialError(err, result.Status, result.Headers)
}

func (p *wsPassthroughAdapter) NormalizeCompleted(body []byte) ([]byte, bool) {
	return openaicore.NormalizeCompletedImageGenerationStatus(body)
}

func (p *wsPassthroughAdapter) RestoreTools(body []byte) []byte {
	return gatewayhttp.RestoreCodexToolNamesFromContext(p.request, body)
}

func (p *wsPassthroughAdapter) EventType(body []byte) string {
	event, _, _ := openaicore.ParseWSEventEnvelope(body)
	return event
}

func (p *wsPassthroughAdapter) MayContainModel(event string) bool {
	return openaicore.WSEventMayContainModel(event)
}

func (p *wsPassthroughAdapter) ReplaceModel(body []byte, upstream, requested string) []byte {
	return openaicore.ReplaceWSMessageModel(body, upstream, requested)
}

func (p *wsPassthroughAdapter) IsTerminal(event string) bool {
	return openaicore.IsWSTerminalEvent(event)
}

func (p *wsPassthroughAdapter) NormalizeTerminal(event string) string {
	return normalizeOpenAIWSTerminalEvent(event)
}

func (p *wsPassthroughAdapter) NormalizeTier(tier string) string {
	return forwardcore.NormalizeObservedOpenAIServiceTier(tier)
}

func (p *wsPassthroughAdapter) Warning(event string, body []byte) *forwardcore.UpstreamWarning {
	warning := buildOpenAIWSUpstreamWarning(event, body)
	if warning == nil {
		return nil
	}
	return &forwardcore.UpstreamWarning{StatusCode: warning.StatusCode, Message: warning.Message, ResponseBody: warning.ResponseBody}
}

func (p *wsPassthroughAdapter) BeforeWrite(ctx context.Context, routingModel string, payload []byte, wroteDownstream bool, rawHeaders map[string][]string) error {
	handshakeHeaders := http.Header(rawHeaders)
	eventType, _, _ := openaicore.ParseWSEventEnvelope(payload)
	if (eventType == "error" || eventType == "response.failed") && markOpenAIWSV2PassthroughCyberPolicy(p.request, payload) {
		return nil
	}
	if eventType == "response.failed" {
		terminalPolicy := p.service.handleOpenAIWSTerminalTransientFailure(ctx, p.provider, routingModel, handshakeHeaders, payload)
		if terminalPolicy.Decision.ShouldReturnGenericError() {
			return openAIWSGenericPolicyCloseError(terminalPolicy.StatusCode)
		}
		if !wroteDownstream && terminalPolicy.Decision.ShouldFailoverWithDefaults(
			gatewayprovider.ExecutionErrorPolicy(p.provider),
			terminalPolicy.StatusCode,
			false,
			p.service.shouldFailoverOpenAIWSError(p.provider, terminalPolicy.StatusCode, payload),
		) {
			return gatewayprovider.NewOpenAIUpstreamFailure(
				terminalPolicy.StatusCode,
				handshakeHeaders,
				payload,
				openai.ExtractOpenAISSEErrorMessage(payload),
				terminalPolicy.Decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(p.provider), terminalPolicy.StatusCode),
			)
		}
	}
	if eventType == "error" {
		errorDecision := p.service.handleOpenAIWSErrorEventTransientFailure(ctx, p.provider, routingModel, handshakeHeaders, payload)
		if wroteDownstream {
			return nil
		}
		errCodeRaw, errTypeRaw, errMsgRaw := openaicore.ParseWSErrorEventFields(payload)
		errorStatus := openAIWSErrorPolicyStatus(payload)
		if errorDecision.ShouldReturnGenericError() {
			return openAIWSGenericPolicyCloseError(errorStatus)
		}
		defaultFailover := p.service.shouldFailoverOpenAIWSError(p.provider, errorStatus, payload)
		if errorStatus == 0 || !errorDecision.ShouldFailoverWithDefaults(
			gatewayprovider.ExecutionErrorPolicy(p.provider),
			errorStatus,
			errorStatus == http.StatusTooManyRequests,
			defaultFailover,
		) {
			return nil
		}
		logOpenAIWSV2Passthrough(
			"relay_error_failover provider_id=%d status=%d err_code=%s err_type=%s err_message=%s",
			p.provider.Record.ID,
			errorStatus, gatewayprovider.TruncateOpenAIWSLogValue(errCodeRaw, gatewayprovider.OpenAIWSLogValueMaxLen), gatewayprovider.TruncateOpenAIWSLogValue(errTypeRaw, gatewayprovider.OpenAIWSLogValueMaxLen), gatewayprovider.TruncateOpenAIWSLogValue(errMsgRaw, gatewayprovider.OpenAIWSLogValueMaxLen),
		)
		return gatewayprovider.NewOpenAIUpstreamFailure(
			errorStatus,
			handshakeHeaders,
			append([]byte(nil), payload...),
			errMsgRaw,
			errorDecision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(p.provider), errorStatus),
		)
	}
	return nil
}

func (p *wsPassthroughAdapter) RelayClose(exit gatewayws.RelayExit, completed int) (int, string, bool) {
	status, reason, ok := openAIWSPassthroughRelayClientClose(openaiwsv2.RelayExit{Stage: exit.Stage, Err: exit.Err, Graceful: exit.Graceful, WroteDownstream: exit.WroteDownstream}, completed)
	return int(status), reason, ok
}

func (p *wsPassthroughAdapter) CloseError(status int, reason string, err error) error {
	return gatewayhttp.NewOpenAIWSClientCloseError(coderws.StatusCode(status), reason, err)
}

func (p *wsPassthroughAdapter) FirstOutputFailure(ctx context.Context, d gatewayws.Deadline, headers map[string][]string) error {
	return p.service.Output.FirstOutputFailure(ctx, p.request, p.provider, d.StartedAt, d.RequestModel, d.ReasoningEffort, d.Timeout, "websocket_first_semantic_output", headers)
}

func (p *wsPassthroughAdapter) FirstOutputTimeout(effort string) time.Duration {
	return p.service.Output.FirstOutputTimeout(effort)
}
func (p *wsPassthroughAdapter) Log(message string) { logOpenAIWSV2Passthrough("%s", message) }
func (p *wsPassthroughAdapter) Truncate(message string, limit int) string {
	return gatewayprovider.TruncateOpenAIWSLogValue(message, limit)
}
func (p *wsPassthroughAdapter) ClosedError() error { return openai.ErrWSConnClosed }

func (p *wsPassthroughAdapter) TruncateReason(message string, limit int) string {
	return logredact.TruncateUTF8(message, limit)
}
