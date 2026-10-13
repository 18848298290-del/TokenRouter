package openai

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Responses lifecycle progress, client output, and retryable failure are
// separate observations. Empty known added-item structures can remain staged,
// while unknown structures count as output so unsupported content is not
// silently retained. A bare retryable error can precede response.failed;
// classifying and holding that frame keeps a later authoritative failure
// eligible for caller recovery.
//
// Error policy combines explicit statuses, structured codes, selected messages,
// and event type with distinct precedence. Credential/access-state evidence
// excludes arbitrary 403 text, and structured JSON is inspected through known
// error fields to limit echoed-request influence. Client sanitization runs on
// delivery copies after accounting and monitoring have seen upstream evidence.
// It rewrites selected capacity codes and trims failed-response echoes while
// keeping caller retry and provider-health decisions outside these helpers.

// These codes distinguish the known client parameter rejection and the
// retryable client-facing capacity code used after gateway recovery is
// unavailable.
const (
	OpenAIPropertyNameAboveMaxLengthCode  = "property_name_above_max_length"
	OpenAICapacityShedRetryableClientCode = "server_error"
)

// OpenAIStreamEventIsPreamble recognizes trimmed created and in-progress
// Responses lifecycle event types.
func OpenAIStreamEventIsPreamble(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "response.created", "response.in_progress":
		return true
	default:
		return false
	}
}

// OpenAIStreamAddedEventStartsClientOutput distinguishes populated
// added-item/part data from known empty protocol containers. Empty or invalid
// payloads, missing expected objects, and unfamiliar shapes return true.
// Nonempty text checks use the supplied string value, including whitespace.
//
// Known reasoning, message, function/custom call, and compaction forms check
// their visible or encrypted fields before opening the output window. Empty
// supported containers return false so later failure handling can still recover
// before client content. Unknown parts count as output conservatively; this
// probe does not claim that every accepted shape is a token delta.
func OpenAIStreamAddedEventStartsClientOutput(payload []byte, eventType string) bool {
	// Unknown or malformed added content counts as output, keeping the staging
	// decision conservative when this parser cannot prove a known empty container.
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return true
	}

	switch strings.TrimSpace(eventType) {
	case "response.output_item.added":
		item := gjson.GetBytes(payload, "item")
		if !item.Exists() || !item.IsObject() {
			return true
		}
		switch strings.TrimSpace(item.Get("type").String()) {
		case "reasoning":
			if item.Get("encrypted_content").String() != "" {
				return true
			}
			summary := item.Get("summary")
			if !summary.IsArray() {
				return false
			}
			for _, part := range summary.Array() {
				if strings.TrimSpace(part.Get("type").String()) != "summary_text" || part.Get("text").String() != "" {
					return true
				}
			}
			return false
		case "message":
			content := item.Get("content")
			if !content.IsArray() {
				return false
			}
			for _, part := range content.Array() {
				switch strings.TrimSpace(part.Get("type").String()) {
				case "output_text":
					if part.Get("text").String() != "" {
						return true
					}
				case "refusal":
					if part.Get("refusal").String() != "" {
						return true
					}
				default:
					return true
				}
			}
			return false
		case "function_call":
			return item.Get("arguments").String() != ""
		case "custom_tool_call":
			return item.Get("input").String() != ""
		case "compaction":
			return item.Get("encrypted_content").String() != ""
		default:
			return true
		}
	case "response.content_part.added":
		part := gjson.GetBytes(payload, "part")
		if !part.Exists() || !part.IsObject() {
			return true
		}
		switch strings.TrimSpace(part.Get("type").String()) {
		case "output_text":
			return part.Get("text").String() != ""
		case "refusal":
			return part.Get("refusal").String() != ""
		default:
			return true
		}
	case "response.reasoning_summary_part.added":
		part := gjson.GetBytes(payload, "part")
		if !part.Exists() || !part.IsObject() || strings.TrimSpace(part.Get("type").String()) != "summary_text" {
			return true
		}
		return part.Get("text").String() != ""
	default:
		return true
	}
}

// OpenAIStreamDataStartsClientOutput decides whether nonblank data ends
// pre-output staging under the supplied event type. Failed terminals return
// false, known added structures use their content predicate, and other events
// are accepted unless they are preamble. Callers handle DONE markers
// separately.
//
// A bare error is accepted only when the failed-event classifier rejects
// failover. Retryable bare errors may precede response.failed, so keeping them
// staged leaves the recovery decision with that later terminal. The helper
// reads classification evidence but does not inspect writer commitment or
// initiate a retry itself.
func OpenAIStreamDataStartsClientOutput(data, eventType string) bool {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return false
	}
	switch strings.TrimSpace(eventType) {
	case "response.failed":
		return false
	case "error":
		// Retryable bare errors can precede their failed terminal; retain them in
		// attempt staging so output commitment does not close recovery before terminal policy runs.
		payload := []byte(trimmed)
		return !OpenAIStreamFailedEventShouldFailover(payload, ExtractOpenAISSEErrorMessage(payload))
	case "response.output_item.added", "response.content_part.added", "response.reasoning_summary_part.added":
		return OpenAIStreamAddedEventStartsClientOutput([]byte(trimmed), eventType)
	}
	return !OpenAIStreamEventIsPreamble(eventType)
}

// OpenAIStreamDataStartsSemanticTTFT applies the HTTP reader's first-output
// timing predicate to data and event type. Blank data, DONE, and failed
// terminals return false; missing event types can be inferred from valid JSON.
//
// Successful terminal types need the wire visible-output predicate, while bare
// errors use failover classification and other types reuse client-output
// classification. Consequently unfamiliar added shapes and other accepted
// events can count; this predicate is broader than a nonempty text-delta check.
// The caller decides when to record a timestamp and keeps that observation
// separate from physical HTTP commitment.
func OpenAIStreamDataStartsSemanticTTFT(data, eventType string) bool {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" || trimmed == "[DONE]" {
		return false
	}
	eventType = strings.TrimSpace(eventType)
	if eventType == "" && gjson.Valid(trimmed) {
		eventType = strings.TrimSpace(gjson.Get(trimmed, "type").String())
	}
	switch eventType {
	case "response.failed":
		return false
	case "response.completed", "response.done":
		// Terminal usage alone supplies billing evidence but no visible output;
		// use the wire content predicate before recording first-output timing for that event.
		return wire.StreamDataStartsVisibleOutput(trimmed, eventType)
	case "error":
		payload := []byte(trimmed)
		return !OpenAIStreamFailedEventShouldFailover(payload, ExtractOpenAISSEErrorMessage(payload))
	default:
		return OpenAIStreamDataStartsClientOutput(trimmed, eventType)
	}
}

// OpenAIStreamFailedEventErrorCode returns a trimmed lowercase
// response.error.code, then error.code when blank. Missing codes yield empty
// text. It performs raw gjson extraction rather than validating the whole
// payload for classification.
func OpenAIStreamFailedEventErrorCode(payload []byte) string {
	code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.code").String()))
	if code == "" {
		code = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.code").String()))
	}
	return code
}

// IsOpenAIUpstreamCapacityShedEvent recognizes overload codes or selected
// error-message phrases in raw payload fields. Such evidence can arrive in HTTP
// 200 stream frames before a failure terminal. Payload validation and provider
// policy belong to callers.
func IsOpenAIUpstreamCapacityShedEvent(payload []byte) bool {
	switch OpenAIStreamFailedEventErrorCode(payload) {
	case "server_is_overloaded", "slow_down":
		return true
	}
	for _, path := range []string{"response.error.message", "error.message", "message"} {
		if IsOpenAICapacityShedMessage(gjson.GetBytes(payload, path).String()) {
			return true
		}
	}
	return false
}

// SanitizeOpenAICapacityShedErrorCodeForClient rewrites blank,
// server_is_overloaded, or slow_down error codes on a valid capacity-shed
// payload to server_error. Other codes and messages remain supplied. Invalid or
// unmatched payloads return unchanged.
//
// Both response.error and bare error locations are considered before a changed
// copy is returned. Any patch failure returns the supplied payload and false,
// discarding earlier successful patches. Callers retain upstream bytes for
// accounting/health and use this delivery representation only after recovery
// can no longer keep the error inside the attempt.
func SanitizeOpenAICapacityShedErrorCodeForClient(payload []byte) ([]byte, bool) {
	if len(payload) == 0 || !gjson.ValidBytes(payload) || !IsOpenAIUpstreamCapacityShedEvent(payload) {
		return payload, false
	}
	// Accumulate both error-node rewrites privately; a patch error returns the
	// input so callers cannot publish only one half of the delivery-code policy.
	updated := payload
	changed := false
	for _, path := range []string{"response.error.code", "error.code"} {
		parent := strings.TrimSuffix(path, ".code")
		if !gjson.GetBytes(updated, parent).Exists() {
			continue
		}
		code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(updated, path).String()))
		if code != "" && code != "server_is_overloaded" && code != "slow_down" {
			continue
		}
		next, err := sjson.SetBytes(updated, path, OpenAICapacityShedRetryableClientCode)
		if err != nil {
			return payload, false
		}
		updated = next
		changed = true
	}
	return updated, changed
}

// OpenAIStreamFailedEventSemanticStatus derives an HTTP category from
// context-window evidence, nested explicit statuses, and error
// type/code/message. Context-window evidence takes precedence here; then nested
// status values in the 400-599 range remain authoritative.
//
// Selected root status codes and rate-limit evidence precede generic
// invalid-request type and authentication/permission inference. Structured
// access-state failure becomes 403, capacity shedding becomes 503, and unknown
// evidence uses 502. This semantic mapping differs from
// OpenAIStreamFailureStatus, whose valid explicit statuses are checked again
// and whose allowed inferred statuses are narrower.
func OpenAIStreamFailedEventSemanticStatus(payload []byte, message string) int {
	if IsOpenAIContextWindowError(message, payload) {
		return http.StatusBadRequest
	}
	// Nested HTTP-style error statuses can ride an HTTP 200 stream; preserve their
	// selected value after context classification so caller policy can match codes such as 422.
	for _, path := range []string{
		"response.error.status_code",
		"response.error.status",
		"error.status_code",
		"error.status",
	} {
		status := int(gjson.GetBytes(payload, path).Int())
		if status >= http.StatusBadRequest && status <= 599 {
			return status
		}
	}

	code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.code").String()))
	if code == "" {
		code = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.code").String()))
	}
	errType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.type").String()))
	if errType == "" {
		errType = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.type").String()))
	}
	combined := strings.TrimSpace(errType + " " + code + " " + strings.ToLower(strings.TrimSpace(message)))
	for _, path := range []string{"response.error.status_code", "error.status_code", "status_code"} {
		if status := int(gjson.GetBytes(payload, path).Int()); status == http.StatusUnauthorized ||
			status == http.StatusForbidden || status == http.StatusTooManyRequests || status == 529 {
			return status
		}
	}
	switch {
	// Rate-limit evidence can accompany a generic invalid-request type; classify
	// it first so caller recovery sees the quota failure instead of a client-input verdict.
	case strings.Contains(combined, "rate_limit"):
		return http.StatusTooManyRequests
	case strings.Contains(errType, "invalid_request"):
		return http.StatusBadRequest
	case strings.Contains(combined, "authentication") || strings.Contains(combined, "unauthorized") || strings.Contains(combined, "invalid_api_key"):
		return http.StatusUnauthorized
	case strings.Contains(combined, "permission") || strings.Contains(combined, "forbidden") || strings.Contains(combined, "access denied"):
		return http.StatusForbidden
	case IsOpenAIUpstreamAccessStateError(message, payload):
		return http.StatusForbidden
	case IsOpenAIUpstreamCapacityShedEvent(payload):
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadGateway
	}
}

// OpenAIStreamFailureStatus selects the status used by stream failure/retry
// policy. Blank or invalid JSON returns 502. A valid explicit nested or root
// status in the 400-599 range wins, including when semantic classification
// found a context-window failure.
//
// Without such a status, inferred authentication, permission, rate-limit, and
// 529 categories remain available. Inferred 503 is retained only for recognized
// capacity shedding; other semantic categories reduce to 502. Callers use their
// separate context-window and client-error checks before deciding presentation
// or failover from this status.
func OpenAIStreamFailureStatus(payload []byte, message string) int {
	if len(bytes.TrimSpace(payload)) == 0 || !gjson.ValidBytes(payload) {
		return http.StatusBadGateway
	}
	semanticStatus := OpenAIStreamFailedEventSemanticStatus(payload, message)
	// Actual valid error statuses outrank inferred semantic categories here, so
	// provider policy can match a supplied custom status even when earlier inference differs.
	for _, path := range []string{"response.error.status_code", "response.error.status", "error.status_code", "error.status", "status_code"} {
		status := int(gjson.GetBytes(payload, path).Int())
		if status >= http.StatusBadRequest && status <= 599 {
			return status
		}
	}
	switch semanticStatus {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, 529:
		return semanticStatus
	case http.StatusServiceUnavailable:
		if IsOpenAIUpstreamCapacityShedEvent(payload) {
			return semanticStatus
		}
	}
	return http.StatusBadGateway
}

// OpenAIStreamCredentialAuthFailure recognizes credential evidence in valid
// JSON through explicit 401 values and configured authentication type/code
// spellings. Arbitrary 403 message text does not qualify. The predicate
// classifies evidence only; the caller applies credential snapshot and
// provider-health policy.
func OpenAIStreamCredentialAuthFailure(payload []byte) bool {
	if len(bytes.TrimSpace(payload)) == 0 || !gjson.ValidBytes(payload) {
		return false
	}
	for _, path := range []string{"response.error.status_code", "error.status_code", "status_code"} {
		if int(gjson.GetBytes(payload, path).Int()) == http.StatusUnauthorized {
			return true
		}
	}
	for _, path := range []string{"response.error.type", "error.type", "type"} {
		errType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, path).String()))
		if errType == "authentication_error" || errType == "authentication_failed" || errType == "unauthorized_error" {
			return true
		}
	}
	for _, path := range []string{"response.error.code", "error.code", "code"} {
		switch strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, path).String())) {
		case "invalid_api_key", "api_key_disabled", "unauthorized", "authentication_error",
			"invalid_token", "access_token_invalid", "token_revoked", "token_invalidated",
			"invalid_credentials", "credential_invalid":
			return true
		}
	}
	return false
}

// OpenAIStream403ProviderFailure combines structured account/workspace
// access-state evidence and credential authentication evidence for a stream
// 403. Free-form permission text is insufficient. The caller still owns
// output-state gating and provider-health mutation.
func OpenAIStream403ProviderFailure(payload []byte, message string) bool {
	return IsOpenAIUpstreamAccessStateError(message, payload) || OpenAIStreamCredentialAuthFailure(payload)
}

// OpenAIStreamFailedEventShouldFailover classifies failure evidence for caller
// recovery before output. Cyber policy and context-window errors stop recovery
// first; structured access-state errors permit it. A 403 needs
// credential/provider evidence, while rate-limit and recognized transient
// processing failures permit retry.
//
// Cyber warning text and remaining request/policy markers reject recovery.
// Otherwise unknown or empty classification evidence is retryable. Message,
// code, and type follow their own nested precedence, and the cyber-warning
// check also scans payload text. This predicate neither inspects writer state
// nor consumes retry budget; the response reader enforces those conditions.
func OpenAIStreamFailedEventShouldFailover(payload []byte, message string) bool {
	if hit, _, _ := DetectOpenAICyberPolicy(payload); hit {
		return false
	}
	if IsOpenAIContextWindowError(message, payload) {
		return false
	}
	if IsOpenAIUpstreamAccessStateError(message, payload) {
		return true
	}
	semanticStatus := OpenAIStreamFailureStatus(payload, message)
	if semanticStatus == http.StatusForbidden {
		return OpenAIStream403ProviderFailure(payload, message)
	}
	// Stream HTTP 200 can carry a quota failure; the classified rate status
	// admits caller retry policy before the later generic nonretryable-marker screen.
	if semanticStatus == http.StatusTooManyRequests {
		return true
	}
	if IsOpenAITransientProcessingError(http.StatusBadRequest, message, payload) {
		return true
	}
	if IsOpenAICyberWarningText(message) || IsOpenAICyberWarningText(string(payload)) {
		return false
	}
	code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.code").String()))
	if code == "" {
		code = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.code").String()))
	}
	errType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.type").String()))
	if errType == "" {
		errType = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.type").String()))
	}
	combined := strings.ToLower(strings.TrimSpace(message + " " + code + " " + errType))
	if combined == "" {
		return true
	}
	nonRetryableMarkers := []string{
		"invalid_request",
		"content_policy",
		"policy",
		"safety",
		"high-risk cyber",
		"not allowed",
		"violat",
	}
	for _, marker := range nonRetryableMarkers {
		if strings.Contains(combined, marker) {
			return false
		}
	}
	return true
}

// OpenAIStreamErrorEventShouldFailover classifies a bare error before semantic
// output. Cyber/context-window failures reject recovery; structured access loss
// and selected authentication/rate statuses permit it. A 403 needs provider
// evidence.
//
// Known transient processing or temporary/retry message phrases also qualify.
// Unclassified bare errors return false, unlike the broader failed-terminal
// classifier, because a later terminal can still determine the attempt outcome.
// Callers enforce output-state eligibility and decide whether to hold, retry,
// or present the error; this helper reads the evidence only.
func OpenAIStreamErrorEventShouldFailover(payload []byte, message string) bool {
	if hit, _, _ := DetectOpenAICyberPolicy(payload); hit {
		return false
	}
	if IsOpenAIContextWindowError(message, payload) {
		return false
	}
	if IsOpenAIUpstreamAccessStateError(message, payload) {
		return true
	}
	switch OpenAIStreamFailedEventSemanticStatus(payload, message) {
	case http.StatusForbidden:
		return OpenAIStream403ProviderFailure(payload, message)
	case http.StatusUnauthorized, http.StatusTooManyRequests, 529:
		return true
	}
	if IsOpenAITransientProcessingError(http.StatusBadRequest, message, payload) {
		return true
	}
	combined := strings.ToLower(strings.TrimSpace(message + " " +
		gjson.GetBytes(payload, "error.message").String() + " " +
		gjson.GetBytes(payload, "response.error.message").String()))
	return strings.Contains(combined, "temporary") ||
		strings.Contains(combined, "try again") ||
		strings.Contains(combined, "please retry")
}

// IsOpenAITransientProcessingError recognizes selected request-processing
// failures for an error status of at least 400. Overload code/message evidence
// applies across those error statuses. Other generic processing/retry phrases
// qualify only for HTTP 400.
//
// Known JSON error message fields are examined before considering a whole body.
// Whole-body phrase scanning is used only when JSON validation fails, keeping
// echoed request content from influencing the structured error case. The
// supplied message is separate caller-selected evidence. This predicate
// supplies recovery classification and does not itself cool a provider or
// commit client output.
func IsOpenAITransientProcessingError(upstreamStatusCode int, upstreamMsg string, upstreamBody []byte) bool {
	if upstreamStatusCode < http.StatusBadRequest {
		return false
	}

	hasOpenAIServerOverloadedCode := func(payload []byte) bool {
		code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.code").String()))
		if code == "" {
			code = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.code").String()))
		}
		return code == "server_is_overloaded" || code == "slow_down"
	}

	if len(upstreamBody) > 0 && hasOpenAIServerOverloadedCode(upstreamBody) {
		return true
	}
	if IsOpenAICapacityShedMessage(upstreamMsg) ||
		IsOpenAICapacityShedMessage(gjson.GetBytes(upstreamBody, "error.message").String()) ||
		IsOpenAICapacityShedMessage(gjson.GetBytes(upstreamBody, "response.error.message").String()) ||
		(!gjson.ValidBytes(upstreamBody) && IsOpenAICapacityShedMessage(string(upstreamBody))) {
		return true
	}
	if upstreamStatusCode != http.StatusBadRequest && upstreamStatusCode != http.StatusServiceUnavailable {
		return false
	}
	if upstreamStatusCode != http.StatusBadRequest {
		return false
	}

	match := func(text string) bool {
		lower := strings.ToLower(strings.TrimSpace(text))
		if lower == "" {
			return false
		}
		if strings.Contains(lower, "an error occurred while processing your request") {
			return true
		}
		if strings.Contains(lower, "selected model is at capacity") {
			return true
		}
		return strings.Contains(lower, "you can retry your request") &&
			strings.Contains(lower, "help.openai.com") &&
			strings.Contains(lower, "request id")
	}

	if match(upstreamMsg) {
		return true
	}
	if len(upstreamBody) == 0 {
		return false
	}
	if match(gjson.GetBytes(upstreamBody, "error.message").String()) {
		return true
	}
	if match(gjson.GetBytes(upstreamBody, "response.error.message").String()) ||
		match(gjson.GetBytes(upstreamBody, "message").String()) {
		return true
	}
	// Structured errors may echo user content outside their selected error fields;
	// whole-body phrase matching is reserved for non-JSON responses to avoid that influence.
	return !gjson.ValidBytes(upstreamBody) && match(string(upstreamBody))
}

// IsOpenAIClientInvalidRequestError recognizes HTTP 400 invalid_request_error
// with property_name_above_max_length code after transient processing has been
// excluded. Other invalid-request types/codes return false. The narrow match
// lets callers distinguish known client parameter rejection from conversion or
// upstream failures.
func IsOpenAIClientInvalidRequestError(upstreamStatusCode int, upstreamMsg string, upstreamBody []byte) bool {
	if upstreamStatusCode != http.StatusBadRequest || len(upstreamBody) == 0 {
		return false
	}
	if IsOpenAITransientProcessingError(upstreamStatusCode, upstreamMsg, upstreamBody) {
		return false
	}
	errType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(upstreamBody, "error.type").String()))
	errCode := strings.ToLower(strings.TrimSpace(gjson.GetBytes(upstreamBody, "error.code").String()))
	return errType == "invalid_request_error" && errCode == OpenAIPropertyNameAboveMaxLengthCode
}

// IsOpenAICapacityShedMessage recognizes configured overload phrases in
// trimmed, case-folded text for error classification.
func IsOpenAICapacityShedMessage(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	return strings.Contains(lower, "server is overloaded") ||
		strings.Contains(lower, "servers are overloaded") ||
		strings.Contains(lower, "servers are currently overloaded")
}

// IsOpenAIRequestScopedCapacityShed recognizes capacity shedding from error
// fields, supplied message, or a non-JSON body. Whole-body text is excluded for
// valid JSON. The result supplies request-level policy; callers decide recovery
// and any provider observations.
func IsOpenAIRequestScopedCapacityShed(upstreamMsg string, upstreamBody []byte) bool {
	return IsOpenAIUpstreamCapacityShedEvent(upstreamBody) ||
		IsOpenAICapacityShedMessage(upstreamMsg) ||
		(!gjson.ValidBytes(upstreamBody) && IsOpenAICapacityShedMessage(string(upstreamBody)))
}

// IsOpenAIContextWindowError recognizes context/token-limit evidence in the
// supplied message and known JSON error message/code fields. Empty or unmatched
// evidence returns false. Matching combines context terminology with over-limit
// language where needed.
//
// A valid JSON body can contain echoed user content, so its arbitrary fields
// are not scanned as error evidence. Whole-body matching is reserved for
// non-JSON responses. Callers use this classification before retry or
// client-error presentation; it does not alter the input or infer the request's
// actual token count.
func IsOpenAIContextWindowError(upstreamMsg string, upstreamBody []byte) bool {
	match := func(text string) bool {
		lower := strings.ToLower(strings.TrimSpace(text))
		if lower == "" {
			return false
		}
		if strings.Contains(lower, "context_too_large") || strings.Contains(lower, "context_length_exceeded") {
			return true
		}
		if strings.Contains(lower, "maximum context length") || strings.Contains(lower, "max context length") {
			return true
		}
		hasExceeded := strings.Contains(lower, "exceed") || strings.Contains(lower, "too large") || strings.Contains(lower, "too long")
		if strings.Contains(lower, "context window") && hasExceeded {
			return true
		}
		if strings.Contains(lower, "context length") && hasExceeded {
			return true
		}
		return strings.Contains(lower, "token limit") &&
			strings.Contains(lower, "context") &&
			hasExceeded
	}

	if match(upstreamMsg) {
		return true
	}
	if len(upstreamBody) == 0 {
		return false
	}
	for _, path := range []string{
		"error.message",
		"response.error.message",
		"message",
		"error.code",
		"response.error.code",
		"code",
	} {
		if match(gjson.GetBytes(upstreamBody, path).String()) {
			return true
		}
	}
	// Restrict structured context-limit evidence to named error fields; arbitrary
	// echoed input can contain the same phrases, while non-JSON errors need whole-body matching.
	return !gjson.ValidBytes(upstreamBody) && match(string(upstreamBody))
}

// IsOpenAIUpstreamAccessStateError recognizes configured provider
// account/workspace state codes in valid JSON error, response-error, detail, or
// root code fields. Its message parameter is unused. Free-form and invalid-JSON
// bodies do not establish this access-state evidence.
func IsOpenAIUpstreamAccessStateError(_ string, body []byte) bool {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	for _, path := range []string{"error.code", "response.error.code", "detail.code", "code"} {
		if IsOpenAIUpstreamAccessStateCode(gjson.GetBytes(body, path).String()) {
			return true
		}
	}
	return false
}

// IsOpenAIUpstreamAccessStateCode recognizes trimmed, case-folded
// workspace/account/organization state spellings with deactivated, disabled, or
// suspended in either order. Unmatched values return false. The caller
// determines whether the code came from an authoritative error field.
func IsOpenAIUpstreamAccessStateCode(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "deactivated_workspace" {
		return true
	}
	for _, subject := range []string{"workspace", "account", "organization", "org"} {
		for _, state := range []string{"deactivated", "disabled", "suspended"} {
			if value == subject+"_"+state || value == state+"_"+subject {
				return true
			}
		}
	}
	return false
}

// IsOpenAIHTTPUpstreamAccessStateError delegates structured access-state
// recognition independently of its HTTP status and message arguments. Known
// codes provide the evidence here. Other authentication or permission failures
// continue through caller policy rather than this access-state predicate.
func IsOpenAIHTTPUpstreamAccessStateError(_ int, _ string, body []byte) bool {
	return IsOpenAIUpstreamAccessStateError("", body)
}

// ExtractOpenAISSEErrorMessage selects the first nonblank nested response, bare
// error, or root message and sanitizes upstream query text. If those paths are
// empty, the shared upstream extractor supplies the candidate. Blank payloads
// return empty text.
func ExtractOpenAISSEErrorMessage(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	for _, path := range []string{"response.error.message", "error.message", "message"} {
		if msg := strings.TrimSpace(gjson.GetBytes(payload, path).String()); msg != "" {
			return logredact.SanitizeUpstreamQueries(msg)
		}
	}
	return logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(payload)))
}

// SanitizeOpenAIResponseFailedEventForClient prepares a valid failed or
// bare-error payload for delivery after upstream evidence has been observed.
// Other types and invalid JSON return unchanged. Capacity codes are rewritten
// on both representations.
//
// For response.failed after client output, context-window evidence supplies the
// selected client type/code. Response echoes, including output and usage, are
// then removed from the delivered copy. Any later required patch failure
// returns the supplied payload and false. Callers therefore keep upstream bytes
// for billing/alerts and apply changes only to the returned client
// representation, with retry commitment handled by the response reader.
func SanitizeOpenAIResponseFailedEventForClient(payload []byte, eventType string, clientOutputStarted bool) ([]byte, bool) {
	eventType = strings.TrimSpace(eventType)
	isFailedEvent := eventType == "response.failed"
	if (!isFailedEvent && eventType != "error") || len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload, false
	}
	updated := payload
	// Apply capacity-code delivery policy to both error representations before
	// trimming failed-response echoes, since upstream can send both frames for the same failure.
	if rewritten, changed := SanitizeOpenAICapacityShedErrorCodeForClient(updated); changed {
		updated = rewritten
	}
	if !isFailedEvent {
		return updated, !bytes.Equal(updated, payload)
	}
	// Once output has started, expose context-limit failure through its recognized
	// client code/type while retaining the upstream payload for caller diagnostics.
	if clientOutputStarted && IsOpenAIContextWindowError(ExtractOpenAISSEErrorMessage(payload), payload) {
		errorPath := ""
		switch {
		case gjson.GetBytes(updated, "response.error").Exists():
			errorPath = "response.error"
		case gjson.GetBytes(updated, "error").Exists():
			errorPath = "error"
		}
		if errorPath != "" {
			next, err := sjson.SetBytes(updated, errorPath+".type", "invalid_request_error")
			if err != nil {
				return payload, false
			}
			updated = next
			next, err = sjson.SetBytes(updated, errorPath+".code", "context_length_exceeded")
			if err != nil {
				return payload, false
			}
			updated = next
		}
	}
	if !gjson.GetBytes(updated, "response").Exists() {
		return updated, !bytes.Equal(updated, payload)
	}
	// Remove echoed request/output and usage from the delivery copy after caller
	// observation, keeping billing and alert evidence available in its separate upstream payload.
	for _, path := range []string{
		"response.instructions",
		"response.output",
		"response.usage",
		"response.metadata",
		"response.reasoning",
		"response.tools",
		"response.tool_choice",
		"response.parallel_tool_calls",
		"response.text",
		"response.truncation",
		"response.max_output_tokens",
		"response.incomplete_details",
	} {
		next, err := sjson.DeleteBytes(updated, path)
		if err != nil {
			return payload, false
		}
		updated = next
	}
	return updated, !bytes.Equal(updated, payload)
}

// DetectOpenAICyberPolicy matches the trimmed case-folded cyber_policy code,
// preferring bare error.code over response.error.code. Message extraction
// follows the same bare/nested precedence but returns trimmed text without
// query sanitization. Unmatched codes return empty companion values.
func DetectOpenAICyberPolicy(payload []byte) (bool, string, string) {
	code := gjson.GetBytes(payload, "error.code").String()
	if code == "" {
		code = gjson.GetBytes(payload, "response.error.code").String()
	}
	if !strings.EqualFold(strings.TrimSpace(code), "cyber_policy") {
		return false, "", ""
	}
	msg := gjson.GetBytes(payload, "error.message").String()
	if msg == "" {
		msg = gjson.GetBytes(payload, "response.error.message").String()
	}
	return true, "cyber_policy", strings.TrimSpace(msg)
}

// IsOpenAICyberWarningText recognizes selected cyber-specific phrases or cyber
// combined with risk/abuse/security-work wording. Blank text returns false.
// This substring heuristic supplies warning/refusal policy and is broader than
// an exact structured cyber_policy code.
func IsOpenAICyberWarningText(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	// Cyber-specific phrases distinguish this warning heuristic from ordinary
	// policy language; the final combined-word check also accepts less structured cyber wording.
	if strings.Contains(lower, "cybersecurity risk") ||
		strings.Contains(lower, "chatgpt.com/cyber") ||
		strings.Contains(lower, "cyber abuse") ||
		strings.Contains(lower, "trusted access for cyber") {
		return true
	}
	return strings.Contains(lower, "cyber") &&
		(strings.Contains(lower, "risk") ||
			strings.Contains(lower, "abuse") ||
			strings.Contains(lower, "security work"))
}

// IsCodexPlanGatedModelError recognizes the configured Codex model-plan
// rejection phrase after shared body normalization on HTTP 400. Other statuses
// and blank normalized bodies return false. The caller decides endpoint
// capability cooldown and request recovery.
func IsCodexPlanGatedModelError(status int, body []byte) bool {
	if status != http.StatusBadRequest {
		return false
	}
	normalized := upstream.NormalizeModelErrorBody(body)
	return normalized != "" && strings.Contains(normalized, "model is not supported when using codex")
}
