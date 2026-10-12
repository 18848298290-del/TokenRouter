package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	upstreamopenai "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// openAIWSTerminalPolicyDecision contains the terminal event and provider-policy result.
// Callers check generic-error and failover decisions before writing to the client.
type openAIWSTerminalPolicyDecision struct {
	TerminalEvent string
	StatusCode    int
	Decision      providercore.UpstreamErrorDecision
}

func normalizeOpenAIWSTerminalEvent(eventType string) string {
	switch strings.TrimSpace(eventType) {
	case "response.completed":
		return "response.completed"
	case "response.done":
		return "response.done"
	case "response.failed":
		return "response.failed"
	case "response.incomplete":
		return "response.incomplete"
	case "response.cancelled", "response.canceled":
		return "response.cancelled"
	default:
		return ""
	}
}

func openAIWSPayloadTransientStatus(payload []byte) int {
	if len(payload) == 0 {
		return 0
	}
	status := int(gjson.GetBytes(payload, "response.error.status_code").Int())
	if status == 0 {
		status = int(gjson.GetBytes(payload, "response.error.status").Int())
	}
	if status == 0 {
		status = int(gjson.GetBytes(payload, "error.status_code").Int())
	}
	if status == 0 {
		status = int(gjson.GetBytes(payload, "error.status").Int())
	}
	if gatewayprovider.IsTransientProviderFailure(status, payload) {
		return status
	}
	if status != 0 {
		return 0
	}
	code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.code").String()))
	errType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.type").String()))
	if code == "" {
		code = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.code").String()))
	}
	if errType == "" {
		errType = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.type").String()))
	}
	switch {
	case code == "server_is_overloaded", code == "slow_down":
		return http.StatusServiceUnavailable
	case strings.Contains(code, "server_error"),
		strings.Contains(code, "internal_error"),
		strings.Contains(code, "upstream_error"),
		strings.Contains(errType, "server_error"),
		strings.Contains(errType, "internal_error"),
		strings.Contains(errType, "upstream_error"):
		return http.StatusInternalServerError
	default:
		return 0
	}
}

// openAIWSErrorPolicyStatus resolves the status used by provider error policies.
// It prefers a valid status in the event, then maps the WebSocket error type.
func openAIWSErrorPolicyStatus(payload []byte) int {
	if len(payload) == 0 {
		return 0
	}
	for _, path := range []string{
		"error.status_code",
		"error.status",
		"response.error.status_code",
		"response.error.status",
	} {
		status := int(gjson.GetBytes(payload, path).Int())
		if status >= http.StatusBadRequest && status <= 599 {
			return status
		}
	}
	codeRaw, errTypeRaw, _ := openai.ParseWSErrorEventFields(payload)
	if codeRaw == "" {
		codeRaw = strings.TrimSpace(gjson.GetBytes(payload, "response.error.code").String())
	}
	if errTypeRaw == "" {
		errTypeRaw = strings.TrimSpace(gjson.GetBytes(payload, "response.error.type").String())
	}
	return upstreamopenai.WSErrorHTTPStatusFromRaw(codeRaw, errTypeRaw)
}

func (s *OpenAIWebSocketExecutor) handleOpenAIWSTerminalTransientFailure(ctx context.Context, provider *gatewayprovider.ExecutionProvider, canonicalModel string, headers http.Header, payload []byte) openAIWSTerminalPolicyDecision {
	eventType, _, _ := openai.ParseWSEventEnvelope(payload)
	result := openAIWSTerminalPolicyDecision{
		TerminalEvent: normalizeOpenAIWSTerminalEvent(eventType),
		Decision:      providercore.UpstreamErrorDecision{Policy: providercore.ErrorPolicyNone},
	}
	if result.TerminalEvent != "response.failed" {
		return result
	}
	result.StatusCode = openAIWSErrorPolicyStatus(payload)
	if result.StatusCode != 0 {
		if result.StatusCode == http.StatusTooManyRequests {
			headers = gatewayprovider.OpenAISemantic429Headers(provider, canonicalModel, headers)
		}
		result.Decision = s.applyOpenAIWSEventErrorPolicy(ctx, provider, canonicalModel, result.StatusCode, headers, payload)
	}
	return result
}

func (s *OpenAIWebSocketExecutor) handleOpenAIWSErrorEventTransientFailure(ctx context.Context, provider *gatewayprovider.ExecutionProvider, canonicalModel string, headers http.Header, payload []byte) providercore.UpstreamErrorDecision {
	eventType, _, _ := openai.ParseWSEventEnvelope(payload)
	if eventType != "error" {
		return providercore.UpstreamErrorDecision{Policy: providercore.ErrorPolicyNone}
	}
	status := openAIWSErrorPolicyStatus(payload)
	if status == http.StatusTooManyRequests {
		headers = gatewayprovider.OpenAISemantic429Headers(provider, canonicalModel, headers)
	}
	return s.applyOpenAIWSEventErrorPolicy(ctx, provider, canonicalModel, status, headers, payload)
}

// markOpenAIWSClientVisibleFailure records delivered error events for Ops,
// excluding internal failures recovered by failover.
func markOpenAIWSClientVisibleFailure(c *gin.Context, eventType string, payload []byte) {
	eventType = strings.TrimSpace(eventType)
	if eventType != "error" && eventType != "response.failed" {
		return
	}
	prefix := "error"
	if eventType == "response.failed" {
		prefix = "response.error"
	}
	code := strings.TrimSpace(gjson.GetBytes(payload, prefix+".code").String())
	errType := strings.TrimSpace(gjson.GetBytes(payload, prefix+".type").String())
	message := strings.TrimSpace(gjson.GetBytes(payload, prefix+".message").String())
	if eventType == "response.failed" && code == "" && errType == "" && message == "" {
		prefix = "error"
		code = strings.TrimSpace(gjson.GetBytes(payload, prefix+".code").String())
		errType = strings.TrimSpace(gjson.GetBytes(payload, prefix+".type").String())
		message = strings.TrimSpace(gjson.GetBytes(payload, prefix+".message").String())
	}
	status := int(gjson.GetBytes(payload, prefix+".status_code").Int())
	if status == 0 {
		status = int(gjson.GetBytes(payload, prefix+".status").Int())
	}
	if status == 0 && eventType == "error" {
		status = int(gjson.GetBytes(payload, "status").Int())
	}
	if status == 0 {
		status = upstreamopenai.WSErrorHTTPStatusFromRaw(code, errType)
	}
	if errType == "" {
		errType = "upstream_error"
	}
	if code == "" {
		code = strings.ReplaceAll(eventType, ".", "_")
	}
	if message == "" {
		message = "upstream websocket request failed"
	}
	gatewayhttp.MarkOpsStreamFailure(c, errType, code, message, status)
}

// handleOpenAIWSFailureProviderSideEffects applies provider health updates for an error event.
// Its result lets the caller deduplicate paired error and response.failed events.
func (s *OpenAIWebSocketExecutor) handleOpenAIWSFailureProviderSideEffects(ctx context.Context, provider *gatewayprovider.ExecutionProvider, canonicalModel string, headers http.Header, payload []byte) bool {
	message := upstreamopenai.ExtractOpenAISSEErrorMessage(payload)
	status := upstreamopenai.OpenAIStreamFailureStatus(payload, message)
	switch status {
	case http.StatusUnauthorized, http.StatusTooManyRequests, 529:
		s.Output.TerminalProviderEffects(nil, provider, payload, message, headers, canonicalModel)
		return true
	case http.StatusForbidden:
		if !upstreamopenai.OpenAIStream403ProviderFailure(payload, message) {
			return false
		}
		s.Output.TerminalProviderEffects(nil, provider, payload, message, headers, canonicalModel)
		return true
	}
	status = openAIWSPayloadTransientStatus(payload)
	if status == 0 {
		return false
	}
	gatewayprovider.ApplyOpenAIResponseHealth(ctx, s.Output.Health, provider, status, headers, payload, false, canonicalModel)
	return true
}

func (s *OpenAIWebSocketExecutor) handleOpenAIWSDialTransientFailure(ctx context.Context, provider *gatewayprovider.ExecutionProvider, canonicalModel string, err error) providercore.UpstreamErrorDecision {
	var dialErr *upstreamopenai.WSDialError
	if !errors.As(err, &dialErr) || dialErr == nil {
		return providercore.UpstreamErrorDecision{Policy: providercore.ErrorPolicyNone}
	}
	return s.applyOpenAIWSEventErrorPolicy(ctx, provider, canonicalModel, dialErr.StatusCode, dialErr.ResponseHeaders, dialErr.ResponseBody)
}

// applyOpenAIWSEventErrorPolicy applies shared provider policy to handshake and event failures.
// Request-scoped failures receive no policy; callers can fail over before client output starts.
func (s *OpenAIWebSocketExecutor) applyOpenAIWSEventErrorPolicy(
	ctx context.Context,
	provider *gatewayprovider.ExecutionProvider,
	canonicalModel string,
	statusCode int,
	headers http.Header,
	payload []byte,
) providercore.UpstreamErrorDecision {
	if statusCode == 0 || gatewayprovider.OpenAIWSHTTPBridgeRequestScopedError(provider, statusCode, upstream.ExtractErrorMessage(payload), payload) {
		return providercore.UpstreamErrorDecision{Policy: providercore.ErrorPolicyNone}
	}
	if provider != nil && provider.Record.Platform == capability.PlatformGrok {
		return gatewayprovider.ApplyGrokExecutionHealth(ctx, s.Output.GrokHealth, provider, statusCode, headers, payload, "", canonicalModel)
	}
	return gatewayprovider.ApplyOpenAIResponseHealth(ctx, s.Output.Health, provider, statusCode, headers, payload, false, canonicalModel)
}

// shouldFailoverOpenAIWSError uses platform HTTP error classification for WS handshake and event failures.
func (s *OpenAIWebSocketExecutor) shouldFailoverOpenAIWSError(provider *gatewayprovider.ExecutionProvider, statusCode int, payload []byte) bool {
	if statusCode == 0 {
		return false
	}
	if provider != nil && provider.Record.Platform == capability.PlatformGrok {
		return gatewayprovider.ShouldFailoverGrokResponse(statusCode, payload)
	}
	upstreamMsg := logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(payload)))
	return gatewayprovider.ShouldFailoverOpenAIResponse(statusCode, upstreamMsg, payload)
}

// openAIWSGenericPolicyCloseError closes an ingress connection with the generic message before client output.
func openAIWSGenericPolicyCloseError(statusCode int) error {
	return gatewayhttp.NewOpenAIWSClientCloseError(
		coderws.StatusInternalError,
		"Upstream gateway error", ws.NewGenericPolicyError(statusCode),
	)
}

func stripCodexSparkImageGenerationToolFromRawPayload(payload []byte, model string) ([]byte, bool, error) {
	if !gatewayprovider.IsCodexSparkModel(model) {
		return payload, false, nil
	}
	return gatewayprovider.StripOpenAIImageGenerationToolsFromRawPayload(payload)
}
