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

// openai_ws_forwarder_support.go classifies Responses WebSocket failures and
// applies provider-health and client-reporting decisions. Their selected status
// controls provider policy, while the event shape controls warning observation
// and client close presentation. Explicit numeric statuses retain priority over
// inferred code or type labels.
//
// Provider health and client reporting run at different points. Request-scoped
// rejections bypass provider policy, paired error and failed events can share a
// single health update, and Ops failure data is marked when an error is
// selected for client delivery. Callers decide whether output progress still
// permits provider failover; these helpers return decisions and close
// instructions for that execution path.

// openAIWSTerminalPolicyDecision contains the terminal event and provider-policy result.
// Callers check generic-error and failover decisions before writing to the client.
type openAIWSTerminalPolicyDecision struct {
	TerminalEvent string // canonical response terminal label
	// StatusCode remains zero for terminals other than response.failed. Failure
	// status is resolved from the payload before applying the provider policy.
	StatusCode int
	Decision   providercore.UpstreamErrorDecision // applied provider policy result
}

// normalizeOpenAIWSTerminalEvent returns canonical terminal labels, folding
// canceled into cancelled. Unsupported or nonterminal trimmed labels return
// empty, letting callers retain their current turn rather than complete it
// from an unrecognized event.
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

// openAIWSPayloadTransientStatus selects a transient-health status from
// numeric fields before code and type labels. Numeric precedence is
// response.error.status_code, response.error.status, error.status_code, then
// error.status. A recognized transient status is returned; another nonzero
// value stops inference and returns zero, including explicit 529.
//
// When no numeric status exists, overloaded or slow-down codes select 503 and
// server, internal, or upstream error labels select 500. This distinction
// keeps provider-wide statuses from being recast as model-transient failures
// merely because their code or type also says server error.
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
	// An explicit nontransient status wins over textual server-error labels, so
	// provider-wide overload and policy statuses cannot become model cooldowns here.
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

// openAIWSErrorPolicyStatus resolves a provider-policy status from an event
// payload. It checks valid 400 through 599 numeric fields in top-level error
// before response.error, using status_code before status at each location.
// Empty payloads return zero; absent or invalid numeric fields continue to
// protocol classification.
//
// Code and type come from ParseWSErrorEventFields, with missing values filled
// individually from response.error. WSErrorHTTPStatusFromRaw supplies the
// final mapping. Retaining an explicit status keeps custom provider rules, 429
// handling, and close presentation tied to the event's supplied failure rather
// than its broader error label.
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

// handleOpenAIWSTerminalTransientFailure returns terminal identity and applied
// provider policy. Only response.failed resolves policy status; other labels
// retain zero and ErrorPolicyNone. For 429, handshake headers are retained
// only when canonicalModel is Spark and the provider is OAuth-like. Other
// semantic 429 cases discard handshake headers before the health helper runs.
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
			// Reuse handshake quota headers when the requested model is Spark and the
			// provider is OAuth-like; other semantic 429 cases lack those matching windows.
			headers = gatewayprovider.OpenAISemantic429Headers(provider, canonicalModel, headers)
		}
		result.Decision = s.applyOpenAIWSEventErrorPolicy(ctx, provider, canonicalModel, result.StatusCode, headers, payload)
	}
	return result
}

// handleOpenAIWSErrorEventTransientFailure applies provider policy for
// standalone error frames. Other labels return ErrorPolicyNone. A 429 retains
// handshake headers only when canonicalModel is Spark and the provider is
// OAuth-like; the shared policy helper then returns the health decision used
// by downstream presentation.
func (s *OpenAIWebSocketExecutor) handleOpenAIWSErrorEventTransientFailure(ctx context.Context, provider *gatewayprovider.ExecutionProvider, canonicalModel string, headers http.Header, payload []byte) providercore.UpstreamErrorDecision {
	eventType, _, _ := openai.ParseWSEventEnvelope(payload)
	if eventType != "error" {
		return providercore.UpstreamErrorDecision{Policy: providercore.ErrorPolicyNone}
	}
	status := openAIWSErrorPolicyStatus(payload)
	if status == http.StatusTooManyRequests {
		// Handshake quota headers describe Spark OAuth-like rate windows; retain
		// them for that model and drop them for other semantic 429 provider cases.
		headers = gatewayprovider.OpenAISemantic429Headers(provider, canonicalModel, headers)
	}
	return s.applyOpenAIWSEventErrorPolicy(ctx, provider, canonicalModel, status, headers, payload)
}

// markOpenAIWSClientVisibleFailure stores Ops error attributes for a delivered
// error or response.failed event. Other labels leave observation unchanged.
// Failed responses prefer response.error; if all three descriptive values are
// empty they use the top-level error object, after which status follows the
// selected prefix.
//
// An error frame can also supply a root status. Missing status falls back to
// code and type classification, and missing descriptive fields receive
// default diagnostic labels and a generic message. Calling this after the
// execution path accepts client delivery prevents an internally recovered
// provider error from becoming the session's visible Ops failure.
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
	// Some failed frames place diagnostics in the standalone error object; use
	// that object only when the nested descriptive fields supply no identity.
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

// handleOpenAIWSFailureProviderSideEffects applies health effects and reports
// whether a recognized provider failure was handled. Authentication, rate-
// limit, overload, and structured provider 403 failures use terminal effects;
// other transient failures use response-health policy. Callers retain the true
// result to suppress a paired event's duplicate effect.
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
	// Terminal-specific provider effects did not apply; classify a model-transient
	// observation while preserving explicit nontransient statuses from the event.
	status = openAIWSPayloadTransientStatus(payload)
	if status == 0 {
		return false
	}
	gatewayprovider.ApplyOpenAIResponseHealth(ctx, s.Output.Health, provider, status, headers, payload, false, canonicalModel)
	return true
}

// handleOpenAIWSDialTransientFailure applies provider policy from a
// WSDialError's handshake status, headers, and body. Other errors return
// ErrorPolicyNone, leaving connection-capacity and cancellation failures to
// the acquisition caller's close rules.
func (s *OpenAIWebSocketExecutor) handleOpenAIWSDialTransientFailure(ctx context.Context, provider *gatewayprovider.ExecutionProvider, canonicalModel string, err error) providercore.UpstreamErrorDecision {
	var dialErr *upstreamopenai.WSDialError
	if !errors.As(err, &dialErr) || dialErr == nil {
		return providercore.UpstreamErrorDecision{Policy: providercore.ErrorPolicyNone}
	}
	return s.applyOpenAIWSEventErrorPolicy(ctx, provider, canonicalModel, dialErr.StatusCode, dialErr.ResponseHeaders, dialErr.ResponseBody)
}

// applyOpenAIWSEventErrorPolicy returns provider health policy for a
// classified handshake or event failure. A zero status or
// OpenAIWSHTTPBridgeRequestScopedError match returns ErrorPolicyNone;
// otherwise the selected platform chooses Grok or OpenAI health handling. The
// caller separately gates failover using output progress and the returned
// decision.
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

// shouldFailoverOpenAIWSError reports the platform's default failover
// eligibility for a WS failure. Zero status returns false; Grok uses its
// Responses classifier and OpenAI receives the query-sanitized upstream
// message with the status and body.
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

// openAIWSGenericPolicyCloseError returns an internal-error close instruction
// retaining the provider status in GenericPolicyError. It constructs no
// transport action; the entry or relay controller presents the generic reason
// and closes the client when it receives this error.
func openAIWSGenericPolicyCloseError(statusCode int) error {
	return gatewayhttp.NewOpenAIWSClientCloseError(
		coderws.StatusInternalError,
		"Upstream gateway error", ws.NewGenericPolicyError(statusCode),
	)
}

// stripCodexSparkImageGenerationToolFromRawPayload removes image-generation
// declarations for a Spark model. Other models return the supplied bytes with
// a false changed flag; callers replace their body only after a successful
// changed result.
func stripCodexSparkImageGenerationToolFromRawPayload(payload []byte, model string) ([]byte, bool, error) {
	if !gatewayprovider.IsCodexSparkModel(model) {
		return payload, false, nil
	}
	return gatewayprovider.StripOpenAIImageGenerationToolsFromRawPayload(payload)
}
