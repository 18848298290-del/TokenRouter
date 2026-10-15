package ops

import (
	"strings"
)

// error_classification.go classifies request failures by phase, SLA
// eligibility, owner, source, and severity.
// An HTTP error can carry local policy markers and upstream observations
// together, so its response type alone cannot establish who caused the failure
// or whether it counts for SLA. Normalization accepts a finite type vocabulary
// and interprets selected codes and policy phrases; phase classification then
// combines that metadata with gateway facts.
//
// Explicit routing capacity and client-marked local model configuration can
// override upstream interpretations. Business-limit exclusion is calculated
// separately from severity, owner, and source, with effective upstream context
// suppressing local text-based exclusions. Provider authentication is
// provider-owned but gateway-sourced, while an upstream HTTP error is
// provider-owned and upstream-sourced. Severity first respects selected client
// types, then uses status thresholds, keeping these reporting dimensions
// distinct.

// Error classification uses persisted phase labels, exact local error codes,
// and lowercased message fragments to distinguish client policy limits from
// provider or platform failures.
const (
	// ErrorPhaseProviderAuth identifies upstream provider credential failures.
	ErrorPhaseProviderAuth = "provider_auth"

	opsErrNoAvailableProviders   = "no available providers"      // generic provider-capacity message fragment
	opsErrInsufficientBalance    = "insufficient balance"        // client funding-limit message fragment
	opsCodeInsufficientBalance   = "INSUFFICIENT_BALANCE"        // client balance rejection code
	opsCodeUsageLimitExceeded    = "USAGE_LIMIT_EXCEEDED"        // subscription usage-limit rejection code
	opsCodeSubscriptionNotFound  = "SUBSCRIPTION_NOT_FOUND"      // missing active subscription code
	opsCodeSubscriptionInvalid   = "SUBSCRIPTION_INVALID"        // invalid active subscription code
	opsCodeUserInactive          = "USER_INACTIVE"               // inactive owning user code
	opsCodeInvalidAPIKey         = "INVALID_API_KEY"             // invalid presented API-key code
	opsCodeAPIKeyRequired        = "API_KEY_REQUIRED"            // missing presented API-key code
	opsCodeAPIKeyExpired         = "API_KEY_EXPIRED"             // expired presented API-key code
	opsCodeAPIKeyDisabled        = "API_KEY_DISABLED"            // disabled presented API-key code
	opsCodeUserNotFound          = "USER_NOT_FOUND"              // missing owning user code
	opsCodeAPIKeyQuotaExhausted  = "API_KEY_QUOTA_EXHAUSTED"     // exhausted API-key quota code
	opsCodeAPIKeyQueryDeprecated = "api_key_in_query_deprecated" // query-parameter key rejection code
	opsCodeGroupDeleted          = "GROUP_DELETED"               // deleted assigned group code
	opsCodeGroupDisabled         = "GROUP_DISABLED"              // disabled assigned group code
)

// ErrorClassificationInput carries extracted response metadata and gateway
// facts for pure Ops classification.
type ErrorClassificationInput struct {
	// Type, Message, and Code come from extracted response metadata. Capture
	// callers normalize Type through NormalizeErrorType, while code and message
	// patterns still participate in local phase and business-limit selection.
	//
	// Type: caller-supplied error type. Message: observed error description text.
	// Code: extracted response classification code.
	Type, Message, Code string
	// Status carries the capture caller classification status, including intended
	// stream status. ClassifyRequestError forwards it to the business helper,
	// which currently ignores its numeric value.
	Status int
	// RoutingCapacityLimited forces routing and business-limit exclusion.
	// LocalModelConfiguration takes effect together with ClientBusinessLimited,
	// treating that local model rejection as routing and suppressing broad
	// upstream context for local exclusion.
	//
	// RoutingCapacityLimited: explicit routing capacity rejection.
	// ClientBusinessLimited: explicit client policy rejection.
	// LocalModelConfiguration: client-marked local model mismatch.
	RoutingCapacityLimited, ClientBusinessLimited, LocalModelConfiguration bool
	// ProviderAuthFailure and UpstreamClientInvalidRequest select specific initial
	// overrides ahead of broad UpstreamError. Later local policy rules can replace
	// the phase; UpstreamClientInvalidRequest also contributes business-limit
	// exclusion independently of the selected phase.
	//
	// UpstreamError reflects positive upstream status or a nonempty captured
	// attempt list. The list can contain nil elements, while ProviderAuthFailure
	// uses its last nonnil event.
	//
	// UpstreamClientInvalidRequest is populated by gateway capture for the
	// property-name-length invalid_request_error with classification and upstream
	// statuses both 400. ProviderAuthFailure is populated when the last nonnil
	// attempt Stage equals provider_auth.
	//
	// UpstreamError: observed upstream failure context.
	// UpstreamClientInvalidRequest: provider-reported invalid request condition.
	// ProviderAuthFailure: observed provider credential failure.
	UpstreamError, UpstreamClientInvalidRequest, ProviderAuthFailure bool
}

// isKnownOpsErrorType reports whether t exactly matches a recognized stored
// error-type label. It compares text as supplied, so whitespace, case changes,
// serialized nil text, and unknown labels return false for normalization
// callers.
func isKnownOpsErrorType(t string) bool {
	switch t {
	case "invalid_request_error",
		"authentication_error",
		"permission_error",
		"model_not_found",
		"service_unavailable",
		"rate_limit_error",
		"billing_error",
		"subscription_error",
		"upstream_error",
		"overloaded_error",
		"service_unavailable_error",
		"api_error",
		"not_found_error",
		"forbidden_error":
		return true
	}
	return false
}

// NormalizeErrorType returns a recognized stored type from the supplied type,
// code, and optional message. It inspects only the first message after trimming
// and lowercasing, and two local model/session policy phrases change
// permission_error to api_error before ordinary recognized-type passthrough.
//
// Other recognized types retain precedence over conflicting codes. An unknown
// or empty type uses trimmed, case-sensitive funding or subscription codes,
// then defaults to api_error. The type label itself is compared exactly, so
// unrecognized upstream text cannot become a new stored classification through
// this helper.
func NormalizeErrorType(errType string, code string, message ...string) string {
	// Handle the two local policy phrases before known-type passthrough. A
	// recognized permission_error would otherwise return immediately and keep its
	// type despite the specific policy message.
	msg := ""
	if len(message) > 0 {
		msg = strings.ToLower(strings.TrimSpace(message[0]))
	}
	if errType == "permission_error" &&
		(strings.Contains(msg, "the current group does not support the requested model") ||
			strings.Contains(msg, "this session already belongs to another group and cannot switch to the current session-isolated group")) {
		return "api_error"
	}
	if errType != "" && isKnownOpsErrorType(errType) {
		return errType
	}
	// Once exact type recognition fails, the funding/subscription code mapping
	// supplies a supported stored type; other codes use the generic api_error
	// result.
	switch strings.TrimSpace(code) {
	case opsCodeInsufficientBalance:
		return "billing_error"
	case opsCodeUsageLimitExceeded, opsCodeSubscriptionNotFound, opsCodeSubscriptionInvalid:
		return "subscription_error"
	default:
		return "api_error"
	}
}

// classifyOpsPhase returns the initial reporting phase from type, code, and
// message text. Recognized client-auth codes or patterns win first, followed by
// local business-limit patterns, before type-based selection runs.
//
// Rate-limit wording about concurrency, pending requests, or queues selects
// request; other rate-limit errors select upstream. An api_error
// provider-capacity message selects routing, while remaining unknown or generic
// types select internal. ClassifyRequestError applies gateway observation flags
// afterward, including provider credential attribution that this initial mapper
// does not produce.
func classifyOpsPhase(errType, message, code string) string {
	msg := strings.ToLower(message)
	// Structured client codes and local text are classified before the generic
	// type. Gateway upstream flags can replace this provisional phase in
	// ClassifyRequestError afterward.
	if isOpsClientAuthError(code, msg) {
		return "auth"
	}
	if isOpsLocalBusinessLimitError(code, msg) {
		return "request"
	}

	switch errType {
	case "authentication_error":
		return "auth"
	case "billing_error", "subscription_error":
		return "request"
	case "rate_limit_error":
		if strings.Contains(msg, "concurrency") || strings.Contains(msg, "pending") || strings.Contains(msg, "queue") {
			return "request"
		}
		return "upstream"
	case "invalid_request_error", "permission_error", "forbidden_error", "not_found_error", "model_not_found":
		return "request"
	case "upstream_error", "overloaded_error":
		return "upstream"
	case "api_error":
		// A capacity phrase identifies the routing phase here. The separate
		// RoutingCapacityLimited gateway fact determines forced SLA exclusion in
		// ClassifyRequestError.
		if IsNoAvailableProviderMessage(msg) {
			return "routing"
		}
		return "internal"
	default:
		return "internal"
	}
}

// ClassifySeverity returns the stored severity from the error type and
// classification status. Client validation, authentication, permission,
// missing-model, billing, and subscription types choose P3 before status
// inspection, including when their status is a server error.
//
// Other types use P1 for status at least 500 or exactly 429, P2 for other
// statuses at least 400, and P3 below that. Thus a 429 invalid_request_error
// stays P3 while a 429 api_error becomes P1. This classifier produces P1, P2,
// and P3; P0 remains a separately configured alert severity.
func ClassifySeverity(errType string, status int) string {
	switch errType {
	case "invalid_request_error", "authentication_error", "permission_error", "forbidden_error", "not_found_error", "model_not_found", "billing_error", "subscription_error":
		return "P3"
	}
	// Status-based urgency starts after the client-type P3 decision, so the same
	// HTTP status can have different severities for different observed error
	// types.
	if status >= 500 {
		return "P1"
	}
	if status == 429 {
		return "P1"
	}
	if status >= 400 {
		return "P2"
	}
	return "P3"
}

// ClassifyRequestError returns phase, business-limit exclusion, owner, and
// source from response metadata and gateway facts. LocalModelConfiguration
// takes effect with ClientBusinessLimited and selects routing;
// RoutingCapacityLimited ultimately forces routing and exclusion regardless of
// other observations.
//
// Otherwise the initial override order is provider authentication, upstream
// client-invalid request, then broad upstream error. A client-business marker
// with no broad upstream context, routing capacity, or local model mismatch
// selects auth after that sequence. Effective upstream context suppresses local
// text-based exclusion, while explicit model/capacity/client-invalid markers
// still count as business limits. Owner and source follow the final phase, so
// provider credential failures retain provider ownership with gateway source.
func ClassifyRequestError(input ErrorClassificationInput) (phase string, isBusinessLimited bool, errorOwner string, errorSource string) {
	errType, message, code, status := input.Type, input.Message, input.Code, input.Status
	phase = classifyOpsPhase(errType, message, code)
	routingCapacityLimited := input.RoutingCapacityLimited
	clientBusinessLimited := input.ClientBusinessLimited
	localModelConfiguration := clientBusinessLimited && input.LocalModelConfiguration
	upstreamError := input.UpstreamError
	upstreamClientInvalidRequest := input.UpstreamClientInvalidRequest
	providerAuthFailure := input.ProviderAuthFailure
	// The observed flags can coexist. Apply the specific local model case before
	// upstream interpretations, then let the later routing-capacity rule decide
	// the final phase.
	if localModelConfiguration {
		phase = "routing"
	} else if providerAuthFailure && !routingCapacityLimited {
		phase = "provider_auth"
	} else if upstreamClientInvalidRequest && !routingCapacityLimited {
		phase = "request"
	} else if upstreamError && !routingCapacityLimited {
		phase = "upstream"
	}
	if clientBusinessLimited && !upstreamError && !routingCapacityLimited && !localModelConfiguration {
		phase = "auth"
	}
	if routingCapacityLimited {
		phase = "routing"
	}
	msg := strings.ToLower(message)
	// A client-marked local model mismatch can carry broad upstream context.
	// Remove that context from local-exclusion evaluation so the explicit model
	// policy still controls classification.
	effectiveUpstreamError := upstreamError && !localModelConfiguration
	localClientAuthError := !effectiveUpstreamError && phase == "auth" && isOpsClientAuthError(code, msg)
	localBusinessLimited := !effectiveUpstreamError && classifyOpsIsBusinessLimited(errType, phase, code, status, message, localClientAuthError)
	isBusinessLimited = localModelConfiguration || routingCapacityLimited || upstreamClientInvalidRequest ||
		(clientBusinessLimited && !effectiveUpstreamError) || localBusinessLimited
	errorOwner = classifyOpsErrorOwner(phase, message)
	errorSource = classifyOpsErrorSource(phase, message)
	return phase, isBusinessLimited, errorOwner, errorSource
}

// classifyOpsIsBusinessLimited reports local SLA exclusion from optional
// client-auth evidence, code/message patterns, and legacy phase labels. Only
// the first optional auth value is used; a true value, a recognized local
// limit, or billing/concurrency phase returns true before upstream wording is
// inspected.
//
// The upstream rate-limit check applies after those earlier matches, and
// remaining inputs return false. Status is accepted but ignored.
// ClassifyRequestError skips this local classification when effective upstream
// context is present, keeping upstream observations from being excluded through
// local message recognition.
func classifyOpsIsBusinessLimited(errType, phase, code string, status int, message string, localClientAuthError ...bool) bool {
	if len(localClientAuthError) > 0 && localClientAuthError[0] {
		return true
	}
	if isOpsLocalBusinessLimitError(code, strings.ToLower(message)) {
		return true
	}
	if phase == "billing" || phase == "concurrency" {
		// These legacy phase labels still select local exclusion when supplied
		// directly, although classifyOpsPhase emits request for its billing and
		// concurrency observations.
		return true
	}
	// Upstream wording matters only after auth, local-limit, and legacy-phase
	// matches fail. The main classifier separately prevents effective upstream
	// context from entering local exclusion.
	if errType == "rate_limit_error" && strings.Contains(strings.ToLower(message), "upstream") {
		return false
	}
	_ = status
	return false
}

// isOpsClientAuthError reports whether an exact trimmed code or lowercase
// message identifies a local key, user, or group authentication rejection.
// Callers supply lowercase message text; this helper trims codes but does not
// normalize that message itself.
func isOpsClientAuthError(code string, msg string) bool {
	switch strings.TrimSpace(code) {
	case opsCodeInvalidAPIKey,
		opsCodeAPIKeyRequired,
		opsCodeAPIKeyExpired,
		opsCodeAPIKeyDisabled,
		opsCodeUserNotFound,
		opsCodeUserInactive,
		opsCodeGroupDeleted,
		opsCodeGroupDisabled:
		return true
	}
	return strings.Contains(msg, "invalid api key") ||
		strings.Contains(msg, "api key is required") ||
		strings.Contains(msg, "api key is disabled") ||
		strings.Contains(msg, "user associated with api key not found") ||
		strings.Contains(msg, "user account is not active") ||
		strings.Contains(msg, "api key 所属分组已删除") ||
		strings.Contains(msg, "api key 所属分组已停用") ||
		strings.Contains(msg, "api key is not assigned to any group")
}

// isOpsLocalBusinessLimitError recognizes local funding, quota, capacity,
// protocol, model, and client-policy rejections from exact trimmed codes or
// lowercase substrings. Callers supply lowercase message text; a match
// contributes SLA exclusion when effective upstream context is absent.
func isOpsLocalBusinessLimitError(code string, msg string) bool {
	switch strings.TrimSpace(code) {
	case opsCodeInsufficientBalance,
		opsCodeUsageLimitExceeded,
		opsCodeSubscriptionNotFound,
		opsCodeSubscriptionInvalid,
		opsCodeAPIKeyQuotaExhausted,
		opsCodeAPIKeyQueryDeprecated:
		return true
	}
	return strings.Contains(msg, "api key in query parameter is deprecated") ||
		strings.Contains(msg, "query parameter api_key is deprecated") ||
		strings.Contains(msg, "no active subscription found for this group") ||
		strings.Contains(msg, "subscription is invalid or expired") ||
		strings.Contains(msg, opsErrInsufficientBalance) ||
		strings.Contains(msg, "insufficient account balance") ||
		strings.Contains(msg, "api key group platform is not gemini") ||
		strings.Contains(msg, "api key 额度已用完") ||
		strings.Contains(msg, "api key 5小时限额已用完") ||
		strings.Contains(msg, "api key 日限额已用完") ||
		strings.Contains(msg, "api key 7天限额已用完") ||
		strings.Contains(msg, "daily usage limit exceeded") ||
		strings.Contains(msg, "weekly usage limit exceeded") ||
		strings.Contains(msg, "monthly usage limit exceeded") ||
		strings.Contains(msg, "usage quota exhausted for this platform") ||
		strings.Contains(msg, "requests-per-minute limit exceeded") ||
		strings.Contains(msg, "too many pending requests") ||
		strings.Contains(msg, "concurrency limit exceeded") ||
		strings.Contains(msg, "image generation concurrency limit exceeded") ||
		strings.Contains(msg, "this group is restricted to claude code clients") ||
		strings.Contains(msg, "this group does not allow /v1/messages dispatch") ||
		strings.Contains(msg, "image generation is not enabled for this group") ||
		(strings.Contains(msg, "reasoning effort") && strings.Contains(msg, "exceeds this group's limit")) ||
		strings.Contains(msg, "token counting is not supported for this platform") ||
		strings.Contains(msg, "images api is not supported for this platform") ||
		strings.Contains(msg, "the current group does not support the requested model") ||
		(strings.Contains(msg, "model ") && strings.Contains(msg, " not in whitelist")) ||
		(strings.Contains(msg, "beta feature ") && strings.Contains(msg, " is not allowed")) ||
		(strings.Contains(msg, "openai service_tier=") && strings.Contains(msg, " is not allowed for model")) ||
		// These client and session policy phrases select local business limits for
		// SLA exclusion when ClassifyRequestError has no effective upstream error
		// context.
		strings.Contains(msg, "this provider only allows codex official clients") ||
		strings.Contains(msg, "this provider only allows clients matched by the configured tls router") ||
		strings.Contains(msg, "this provider only allows configured openai oauth clients") ||
		strings.Contains(msg, "this session already belongs to another group and cannot switch to the current session-isolated group") ||
		strings.Contains(msg, "openai wsv1 is temporarily unsupported") ||
		strings.Contains(msg, "openai codex passthrough requires a non-empty instructions field")
}

// IsNoAvailableProviderMessage reports whether case-insensitive text contains a
// supported provider-capacity phrase. Its match supplies a routing-message
// hint; ClassifyRequestError still uses the explicit RoutingCapacityLimited
// marker to force capacity-related SLA exclusion.
func IsNoAvailableProviderMessage(message string) bool {
	msg := strings.ToLower(message)
	return strings.Contains(msg, opsErrNoAvailableProviders) ||
		strings.Contains(msg, "no available provider") ||
		strings.Contains(msg, "no available gemini providers") ||
		strings.Contains(msg, "no available openai providers") ||
		strings.Contains(msg, "no available compatible providers")
}

// classifyOpsErrorOwner returns provider for upstream, network, and
// provider_auth phases, client for request/auth, and platform for
// routing/internal. Phase labels are matched exactly; an unrecognized phase
// uses case-insensitive upstream wording for provider ownership, otherwise
// platform.
func classifyOpsErrorOwner(phase string, message string) string {
	switch phase {
	case "upstream", "network":
		return "provider"
	case "provider_auth":
		return "provider"
	case "request", "auth":
		return "client"
	case "routing", "internal":
		return "platform"
	default:
		// When phase has no recognized owner mapping, use message wording to select a
		// provider owner. The source classifier makes its own independent attribution
		// choice.
		if strings.Contains(strings.ToLower(message), "upstream") {
			return "provider"
		}
		return "platform"
	}
}

// classifyOpsErrorSource returns upstream_http for upstream phase,
// client_request for request/auth, and gateway for provider authentication,
// network, routing, or internal phases. Unknown phase labels use
// case-insensitive upstream wording, otherwise defaulting to gateway.
func classifyOpsErrorSource(phase string, message string) string {
	switch phase {
	case "upstream":
		return "upstream_http"
	case "provider_auth":
		return "gateway"
	case "network":
		return "gateway"
	case "request", "auth":
		return "client_request"
	case "routing", "internal":
		return "gateway"
	default:
		// When phase has no recognized source mapping, its upstream message marker
		// selects upstream_http. Provider ownership alone does not determine the
		// source label.
		if strings.Contains(strings.ToLower(message), "upstream") {
			return "upstream_http"
		}
		return "gateway"
	}
}
