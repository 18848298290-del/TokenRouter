package grok

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// failure_classifier.go classifies Grok upstream failures and recommends
// failover, cooldown, and model scope to callers.
// Grok quota messages can survive a proxy's rewritten HTTP status, so failure
// classification combines message text, structured codes and the observed
// status. Free usage exhaustion wins before billing, compatibility, empty
// output, capacity, rate limits and server errors, with each class supplying its
// own failover and cooldown recommendation.
//
// The classifier returns recommendations for caller policy to apply after
// content refusal has been excluded. Model identity comes from message text,
// structured fields or the requested model; token pairs are retained for Free
// usage diagnostics when parsing succeeds. Compatibility failures permit another
// provider without a cooldown, while capacity takes precedence over ordinary 429
// handling and does not mark the team-model quota overlay. The fixed Free
// cooldown schedules another probe rather than estimating a reset from
// rolling-window language.

// These failure labels identify caller policy outcomes, and the Free probe
// interval schedules re-observation after included usage is exhausted.
const (
	GrokFailureNone          GrokUpstreamFailureClass = ""                                  // no selected failure
	GrokFailureFreeUsage     GrokUpstreamFailureClass = "subscription:free-usage-exhausted" // included usage exhausted
	GrokFailureBilling       GrokUpstreamFailureClass = "billing_quota"                     // billing or credit exhaustion
	GrokFailureEmptyUpstream GrokUpstreamFailureClass = "empty_upstream"                    // missing usable upstream output
	GrokFailureModelCapacity GrokUpstreamFailureClass = "model_capacity"                    // temporary model request pressure
	GrokFailureRateLimit     GrokUpstreamFailureClass = "rate_limit"                        // ordinary provider rate limit
	GrokFailureAuth          GrokUpstreamFailureClass = "auth_error"                        // authentication failure class label
	GrokFailureServer        GrokUpstreamFailureClass = "server_error"                      // generic upstream server failure
	// GrokFailureCompatibility identifies replay or body formats the selected
	// upstream cannot process. The caller can try another provider without cooling
	// down credentials for a request compatibility failure.
	GrokFailureCompatibility GrokUpstreamFailureClass = "compatibility_error" // unsupported replay-state representation

	GrokFreeUsageProbeCooldown = 10 * time.Minute // Free quota probe interval
)

var (
	// ReGrokTokenPair accepts decimal actual/limit token evidence used to retain
	// Free quota diagnostics and evaluate a reached limit.
	ReGrokTokenPair = regexp.MustCompile(`(?i)tokens?\s*(?:\(actual\s*/\s*limit\))?\s*[:=]?\s*(\d+)\s*/\s*(\d+)`)
	// ReGrokModelFor extracts a model mentioned by the error text before structured
	// fields or the requested model are considered.
	ReGrokModelFor = regexp.MustCompile(`(?i)(?:for\s+model|model|模型)\s*[:：]?\s*([a-z0-9][a-z0-9._-]{2,80})`)
)

// GrokUpstreamFailureClass names the failure category used by Grok caller
// policy.
type GrokUpstreamFailureClass string

// GrokUpstreamFailureDecision carries pure recommendations and evidence for
// caller-applied provider changes.
type GrokUpstreamFailureDecision struct {
	Class GrokUpstreamFailureClass // selected failure category
	// Model records the selected error identity; GrokHealth uses it in
	// model-specific Free quota and capacity handling before applying provider
	// temporary state.
	Model string // diagnostic failure model
	// Cooldown and ShouldCooldown recommend whether and for how long caller policy
	// should cool a target. Consumers choose persistence and scope; GrokHealth
	// handles quota/capacity classes before provider temporary state.
	Cooldown       time.Duration // recommended cooldown duration
	ShouldCooldown bool          // cooldown application flag
	// ShouldFailover recommends another provider; the caller still checks response
	// commitment and excludes content-policy refusals before classification.
	ShouldFailover bool // provider-switch recommendation
	// BlockModel recommends model scope for billing or empty-output decisions with
	// a nonblank Model. The current GrokHealth chooses its actual state-write scope
	// from Class and Model.
	BlockModel bool   // model scope recommendation
	Reason     string // selected diagnostic message
	// TokensActual and TokensLimit are populated together for classified Free usage
	// when decimal pair parsing succeeds; nil means no pair was retained.
	TokensActual *int64 // observed token count
	TokensLimit  *int64 // observed token limit
}

// ClassifyGrokUpstreamFailure combines status and body evidence into a
// caller-applied failure decision. Callers exclude content-policy responses
// first. The function reads evidence without updating provider state, and an
// unmatched failure returns its selected message as Reason.
//
// Free quota precedes billing, then compatibility, empty output, capacity,
// ordinary rate limits and 5xx. That order prevents a rewritten status from
// overriding a more specific body classification. Model scope and cooldown are
// class-dependent; token actual/limit pointers appear only for parsed Free quota
// evidence. Callers separately determine whether current output commitment
// permits the recommended failover.
func ClassifyGrokUpstreamFailure(statusCode int, responseBody []byte, requestedModel string) GrokUpstreamFailureDecision {
	text, code, low := GrokUpstreamErrorCorpus(statusCode, responseBody)
	model := ExtractGrokFailureModel(text, responseBody, requestedModel)
	actual, limit, hasTokens := ParseGrokTokenPair(text)
	// Selected messages can omit token details retained by the raw body. Retry pair
	// parsing there before losing Free quota diagnostics.
	if !hasTokens {
		actual, limit, hasTokens = ParseGrokTokenPair(string(responseBody))
	}

	// Body evidence for Free quota can survive a rewritten transport status. Apply
	// that class first so later billing, rate-limit or server branches cannot mask
	// it.
	if IsGrokFreeUsageExhaustedText(low) || IsGrokFreeUsageCode(code) || IsGrokFreeUsageCode(text) {
		d := GrokUpstreamFailureDecision{
			Class: GrokFailureFreeUsage,

			Model: model,

			Cooldown: GrokFreeUsageCooldownDuration(low),

			ShouldCooldown: true,

			ShouldFailover: true,

			BlockModel: false,

			Reason: firstNonEmpty(text, code, "free usage exhausted"),
		}
		if hasTokens {
			a, b := actual, limit
			d.TokensActual = &a
			d.TokensLimit = &b
		}
		return d
	}

	// Billing text or HTTP 402 selects the billing cooldown before generic failure
	// handling; a missing message still needs a diagnostic reason for caller
	// policy.
	if IsGrokBillingQuotaText(low) || statusCode == http.StatusPaymentRequired {
		reason := firstNonEmpty(text, "billing quota")
		if statusCode == http.StatusPaymentRequired && text == "" {
			reason = "payment required"
		}
		return GrokUpstreamFailureDecision{
			Class: GrokFailureBilling,

			Model: model,

			Cooldown: 30 * time.Minute,

			ShouldCooldown: true,

			ShouldFailover: true,

			BlockModel: model != "",

			Reason: reason,
		}
	}

	// Compaction compatibility is a request-format failure for this deployment.
	// Select failover before empty-output handling so it receives no provider
	// cooldown.
	if IsGrokCompatibilityError(statusCode, low, code) {
		return GrokUpstreamFailureDecision{
			Class: GrokFailureCompatibility,

			Model: model,

			ShouldFailover: true,

			ShouldCooldown: false,

			Reason: firstNonEmpty(text, "grok response compatibility error"),
		}
	}

	// A specific empty-output marker determines this class even when transport
	// status was rewritten, keeping its model-scoped cooldown ahead of generic
	// server errors.
	if IsGrokEmptyModelOutputText(low) || IsGrokEmptyModelOutputCode(code) {
		return GrokUpstreamFailureDecision{
			Class: GrokFailureEmptyUpstream,

			Model: model,

			Cooldown: 4 * time.Minute,

			ShouldCooldown: true,

			ShouldFailover: true,

			BlockModel: model != "",

			Reason: firstNonEmpty(text, "empty model output"),
		}
	}

	// Capacity evidence precedes generic rate limiting so caller policy can retry
	// HTTP 429 capacity failures with its bounded allowance while keeping sibling
	// credentials outside the quota overlay.
	if IsGrokModelCapacityText(low) {
		return GrokUpstreamFailureDecision{
			Class: GrokFailureModelCapacity,

			Model: model,

			Cooldown: time.Minute,

			ShouldCooldown: true,

			ShouldFailover: true,

			BlockModel: false,

			Reason: firstNonEmpty(text, "model capacity"),
		}
	}

	// Earlier quota and capacity branches already selected their own scopes. Apply
	// the ordinary provider rate-limit cooldown to the remaining 429 or text
	// evidence.
	if statusCode == http.StatusTooManyRequests || IsGrokRateLimitText(low) {
		return GrokUpstreamFailureDecision{
			Class: GrokFailureRateLimit,

			Model: model,

			Cooldown: 10 * time.Minute,

			ShouldCooldown: true,

			ShouldFailover: true,

			BlockModel: false,

			Reason: firstNonEmpty(text, "rate limit"),
		}
	}

	// Specific body classifications have already chosen their policy. Remaining 5xx
	// failures receive the short provider cooldown associated with generic server
	// errors.
	if statusCode >= 500 && statusCode <= 599 {
		return GrokUpstreamFailureDecision{
			Class: GrokFailureServer,

			Cooldown: 2 * time.Minute,

			ShouldCooldown: true,

			ShouldFailover: true,

			Reason: firstNonEmpty(text, "server error"),
		}
	}

	return GrokUpstreamFailureDecision{Reason: text}
}

// GrokUpstreamErrorCorpus returns selected text, code and lowercase matching
// text from a response body. Status is handled by the classifier rather than
// this text helper. A recognized status-prefix wrapper is removed before flat or
// nested JSON parsing.
//
// A parsed message replaces raw text when it is sufficiently long or contains
// quota language. Original-body fields can further supply a quota message and
// take precedence for code. The lowercase corpus prefixes a code absent from the
// selected text, keeping code evidence available when a message was chosen for
// readable diagnostics.
func GrokUpstreamErrorCorpus(statusCode int, responseBody []byte) (text, code, low string) {
	_ = statusCode // The classifier handles transport status separately from the response text.
	raw := strings.TrimSpace(string(responseBody))
	// Wrapper prefixes can surround the upstream JSON body. Remove that wrapper so
	// structured quota messages and codes remain available to later classification.
	if _, unwrappedBody, ok := UnwrapGrokUpstreamErrorText(raw); ok {
		raw = unwrappedBody
	}
	text = raw
	codeFromJSON, msgFromJSON := ParseGrokUpstreamErrorJSON(raw)
	if msgFromJSON != "" {
		// A substantial message or quota wording replaces serialized envelope text,
		// keeping readable diagnostics while retaining quota evidence for class
		// selection.
		if text == "" || len(msgFromJSON) > len(text)/2 || LooksLikeGrokQuotaMessage(msgFromJSON) {
			text = msgFromJSON
		}
	}
	// The supplied body may carry direct structured fields even after wrapper
	// parsing. Let its code and quota message contribute to the final diagnostic
	// corpus.
	if len(responseBody) > 0 {
		if m := strings.TrimSpace(firstNonEmpty(
			gjson.GetBytes(responseBody, "error.message").String(),
			gjson.GetBytes(responseBody, "message").String(),
			gjson.GetBytes(responseBody, "error").String(),
		)); m != "" && (text == "" || LooksLikeGrokQuotaMessage(m)) {
			text = m
		}
		if c := strings.TrimSpace(firstNonEmpty(
			gjson.GetBytes(responseBody, "error.code").String(),
			gjson.GetBytes(responseBody, "code").String(),
		)); c != "" {
			codeFromJSON = c
		}
	}
	code = codeFromJSON
	low = strings.ToLower(strings.TrimSpace(text))
	if code != "" && !strings.Contains(low, strings.ToLower(code)) {
		low = strings.ToLower(code) + " " + low
	}
	return text, code, low
}

// UnwrapGrokUpstreamErrorText parses the recognized upstream status or status
// prefix followed by decimal digits. A positive parsed status returns the
// trimmed remainder after an optional colon. Unmatched or malformed prefixes
// return false for ordinary text handling.
func UnwrapGrokUpstreamErrorText(errText string) (status int, body string, ok bool) {
	text := strings.TrimSpace(errText)
	if text == "" {
		return 0, "", false
	}
	lower := strings.ToLower(text)
	for _, p := range []string{"upstream status ", "status "} {
		if !strings.HasPrefix(lower, p) {
			continue
		}
		rest := strings.TrimSpace(text[len(p):])
		i := 0
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			status = status*10 + int(rest[i]-'0')
			i++
		}
		if status <= 0 || i == 0 {
			return 0, "", false
		}
		rest = strings.TrimSpace(rest[i:])
		if strings.HasPrefix(rest, ":") {
			rest = strings.TrimSpace(rest[1:])
		}
		return status, rest, true
	}
	return 0, "", false
}

// ParseGrokUpstreamErrorJSON extracts trimmed code and message from a JSON
// object string. Top-level fields precede nested error fields, and a string
// error supplies a missing message. Invalid or non-object input returns two
// empty strings.
func ParseGrokUpstreamErrorJSON(errText string) (code, message string) {
	text := strings.TrimSpace(errText)
	if text == "" || text[0] != '{' {
		return "", ""
	}
	var payload map[string]any
	// Failed object decoding yields no extracted fields here, leaving the owning
	// error corpus's raw text available for its unstructured classification.
	if json.Unmarshal([]byte(text), &payload) != nil {
		return "", ""
	}
	if v, ok := payload["code"].(string); ok {
		code = v
	}
	if v, ok := payload["message"].(string); ok {
		message = v
	}
	if errObj, ok := payload["error"].(map[string]any); ok {
		if v, ok := errObj["code"].(string); ok && code == "" {
			code = v
		}
		if v, ok := errObj["message"].(string); ok && message == "" {
			message = v
		}
	}
	if errStr, ok := payload["error"].(string); ok && message == "" {
		message = errStr
	}
	return strings.TrimSpace(code), strings.TrimSpace(message)
}

// LooksLikeGrokQuotaMessage finds broad quota wording used when selecting
// diagnostic messages.
func LooksLikeGrokQuotaMessage(s string) bool {
	low := strings.ToLower(s)
	return strings.Contains(low, "quota") ||
		strings.Contains(low, "usage") ||
		strings.Contains(low, "credit") ||
		strings.Contains(low, "额度") ||
		strings.Contains(low, "free")
}

// IsGrokFreeUsageCode recognizes normalized Free or usage-limit exhaustion code
// markers.
func IsGrokFreeUsageCode(code string) bool {
	c := strings.ToLower(strings.TrimSpace(code))
	if c == "" {
		return false
	}
	if strings.Contains(c, "subscription:free-usage-exhausted") ||
		strings.Contains(c, "free-usage-exhausted") ||
		strings.Contains(c, "free_usage_exhausted") ||
		strings.Contains(c, "usage-limit-exceeded") ||
		strings.Contains(c, "usage_limit_exceeded") {
		return true
	}
	return (strings.Contains(c, "free-usage") || strings.Contains(c, "free_usage")) &&
		(strings.Contains(c, "exhaust") || strings.Contains(c, "exceed") || strings.Contains(c, "limit"))
}

// IsGrokFreeUsageExhaustedText recognizes selected quota phrases and exhausted
// token pairs in lowercase text. Callers provide the lowercase error corpus;
// direct Free markers and the selected Chinese quota phrases can establish this
// classification without a numeric pair.
//
// Broader quota or usage language needs its accompanying exhaustion and quota
// context. The numeric path additionally requires a positive limit reached by
// actual tokens and one of the selected quota-context words. A zero limit or
// failed signed-integer parse cannot establish exhaustion through that path,
// while independent phrase evidence can still match.
func IsGrokFreeUsageExhaustedText(low string) bool {
	if low == "" {
		return false
	}
	if strings.Contains(low, "free-usage-exhausted") ||
		strings.Contains(low, "free_usage_exhausted") ||
		strings.Contains(low, "subscription:free-usage") ||
		strings.Contains(low, "usage-limit-exceeded") ||
		strings.Contains(low, "usage_limit_exceeded") ||
		strings.Contains(low, "free-tier-limit") ||
		strings.Contains(low, "free_tier_limit") {
		return true
	}
	if strings.Contains(low, "free usage") ||
		strings.Contains(low, "included free usage") ||
		strings.Contains(low, "used all the included free") ||
		strings.Contains(low, "you've used all the included free") ||
		strings.Contains(low, "you have used all the included free") ||
		strings.Contains(low, "free quota") ||
		strings.Contains(low, "no remaining free") ||
		strings.Contains(low, "out of free") ||
		strings.Contains(low, "usage resets over a rolling") ||
		(strings.Contains(low, "free tier") && (strings.Contains(low, "exhaust") || strings.Contains(low, "limit") || strings.Contains(low, "exceed"))) {
		return true
	}
	for _, p := range []string{
		"额度耗尽", "额度用完", "额度不足", "额度已用尽", "额度已耗尽",
		"免费额度", "免费用量", "用量用完", "用量耗尽", "用量超限", "用量已用尽",
		"配额耗尽", "配额已用尽", "配额不足", "配额超限", "配额用完",
		"没有额度", "没额度", "无额度", "可用额度不足", "模型额度",
		"临时额度", "额度已满", "额度超限", "额度达到上限",
		"模型额度用完", "模型额度耗尽", "提供商额度用完", "提供商额度耗尽",
		"额度不够", "没额度了", "额度没了", "用完额度", "耗尽额度",
	} {
		if strings.Contains(low, p) {
			return true
		}
	}
	if (strings.Contains(low, "quota") && (strings.Contains(low, "exhaust") || strings.Contains(low, "exceed") || strings.Contains(low, "limit"))) ||
		(strings.Contains(low, "usage") && (strings.Contains(low, "exhaust") || strings.Contains(low, "exceed")) && (strings.Contains(low, "limit") || strings.Contains(low, "free") || strings.Contains(low, "model"))) {
		if strings.Contains(low, "free") || strings.Contains(low, "rolling") ||
			strings.Contains(low, "24-hour") || strings.Contains(low, "24 hour") ||
			strings.Contains(low, "model") || strings.Contains(low, "subscription") ||
			strings.Contains(low, "included") || strings.Contains(low, "tokens") {
			return true
		}
	}
	if a, b, ok := ParseGrokTokenPair(low); ok && b > 0 && a >= b {
		if strings.Contains(low, "free") || strings.Contains(low, "subscription") ||
			strings.Contains(low, "included") || strings.Contains(low, "model") ||
			strings.Contains(low, "usage") || strings.Contains(low, "quota") ||
			strings.Contains(low, "rolling") {
			return true
		}
	}
	return false
}

// IsGrokBillingQuotaText recognizes billing, payment and exhausted-credit
// phrases in lowercase error text. Callers test Free usage evidence first
// because messages can contain language relevant to more than one quota class.
func IsGrokBillingQuotaText(low string) bool {
	if low == "" {
		return false
	}
	if strings.Contains(low, "insufficient_quota") {
		return true
	}
	if strings.Contains(low, "billing") && strings.Contains(low, "quota") {
		return true
	}
	if strings.Contains(low, "payment") && (strings.Contains(low, "required") || strings.Contains(low, "fail")) {
		return true
	}
	if strings.Contains(low, "spending limit") || strings.Contains(low, "run out of credits") || strings.Contains(low, "out of credits") ||
		(strings.Contains(low, "need a grok subscription") || strings.Contains(low, "need grok subscription")) {
		return true
	}
	if strings.Contains(low, "余额不足") || strings.Contains(low, "欠费") || strings.Contains(low, "需要付费") {
		return true
	}
	return false
}

// ShouldMarkGrokTeamModelRateLimit decides whether an error can mark the
// sibling-provider quota overlay. Callers apply that process-local mark
// separately; this predicate performs no state update. HTTP 429 or classified
// Free quota ordinarily permits marking.
//
// Classified model capacity takes precedence over the 429 status and returns
// false. HTTP 429 capacity has a bounded same-provider retry policy, and
// marking the team overlay would exclude sibling credentials during that
// request-pressure condition. Other non-429 classes leave the overlay unchanged
// unless their body establishes Free usage exhaustion.
func ShouldMarkGrokTeamModelRateLimit(statusCode int, responseBody []byte) bool {
	decision := ClassifyGrokUpstreamFailure(statusCode, responseBody, "")
	if decision.Class == GrokFailureModelCapacity {
		return false
	}
	return statusCode == http.StatusTooManyRequests || decision.Class == GrokFailureFreeUsage
}

// IsGrokCompatibilityError recognizes selected compaction markers in HTTP 400 or
// 422 text and code. Other statuses return false. Generic decoder-shape failures
// are handled by IsGrokDecoderCompatibilityError at the gateway's separate
// compatibility decision.
func IsGrokCompatibilityError(statusCode int, low, code string) bool {
	if statusCode != http.StatusBadRequest && statusCode != http.StatusUnprocessableEntity {
		return false
	}
	combined := strings.ToLower(strings.TrimSpace(low + " " + code))
	// Compaction phrases identify replay-state incompatibility on the allowed
	// client error statuses; generic shape decoding has its separate gateway
	// predicate.
	for _, phrase := range []string{
		"could not decode the compaction blob",
		"cannot decode the compaction blob",
		"decode the compaction blob",
		"ensure it is unmodified from the compact response",
		"compaction blob",
	} {
		if strings.Contains(combined, phrase) {
			return true
		}
	}
	for _, marker := range []string{
		"invalid_compaction",
		"compaction_decode_error",
	} {
		if strings.Contains(combined, marker) {
			return true
		}
	}
	return false
}

// IsGrokModelCapacityText recognizes the selected capacity and
// concurrent-request phrases in lowercase text. The classifier tests it before
// ordinary rate-limit evidence so an overloaded model does not become a team
// quota condition.
func IsGrokModelCapacityText(low string) bool {
	return strings.Contains(low, "capacity") ||
		strings.Contains(low, "overloaded") ||
		strings.Contains(low, "server_busy") ||
		strings.Contains(low, "too many concurrent") ||
		strings.Contains(low, "engine_overloaded")
}

// IsGrokRateLimitText recognizes selected English and Chinese lowercase
// rate-limit phrases.
func IsGrokRateLimitText(low string) bool {
	return strings.Contains(low, "rate limit") ||
		strings.Contains(low, "rate_limit") ||
		strings.Contains(low, "too many requests") ||
		strings.Contains(low, "请求过于频繁") ||
		strings.Contains(low, "速率限制")
}

// IsGrokEmptyModelOutputText recognizes selected lowercase empty-output and
// client-content failure phrases.
func IsGrokEmptyModelOutputText(low string) bool {
	if low == "" {
		return false
	}
	return strings.Contains(low, "empty model output") ||
		strings.Contains(low, "no content/tool_calls") ||
		strings.Contains(low, "no client-visible content") ||
		strings.Contains(low, "empty_upstream") ||
		strings.Contains(low, "empty upstream")
}

// IsGrokEmptyModelOutputCode recognizes normalized empty-output codes and
// supported embedded markers.
func IsGrokEmptyModelOutputCode(code string) bool {
	c := strings.ToLower(strings.TrimSpace(code))
	if c == "" {
		return false
	}
	return c == "empty_upstream" ||
		c == "empty-model-output" ||
		c == "empty_model_output" ||
		strings.Contains(c, "empty_upstream") ||
		strings.Contains(c, "empty-model-output")
}

// GrokFreeUsageCooldownDuration returns the fixed interval before another Free
// quota probe. The text argument currently does not affect the duration;
// rolling-window wording provides no absolute reset time for this selection.
func GrokFreeUsageCooldownDuration(low string) time.Duration {
	// Rolling-window language gives no absolute reset timestamp, so another probe
	// uses the fixed short interval before caller policy observes quota again.
	return GrokFreeUsageProbeCooldown
}

// ParseGrokTokenPair parses decimal actual/limit values matched by
// ReGrokTokenPair. Missing pairs or values outside signed int64 return false. A
// successfully parsed zero limit remains zero for the classifier's separate
// exhaustion check.
func ParseGrokTokenPair(errText string) (actual, limit int64, ok bool) {
	m := ReGrokTokenPair.FindStringSubmatch(errText)
	if len(m) != 3 {
		return 0, 0, false
	}
	a, errA := strconv.ParseInt(m[1], 10, 64)
	b, errB := strconv.ParseInt(m[2], 10, 64)
	// Out-of-range decimal values cannot supply signed token diagnostics; report no
	// pair so classification keeps its text evidence without numeric usage fields.
	if errA != nil || errB != nil {
		return 0, 0, false
	}
	return a, b, true
}

// ExtractGrokFailureModel selects a diagnostic model token from text, structured
// fields or fallback. Text matching has precedence, followed by error.model and
// model, then the caller's requested identity. Selected strings are trimmed and
// stripped of trailing punctuation.
//
// A matched earlier source prevents selection of a later one. Text uses
// ReGrokModelFor's token shape, which excludes slash qualifiers; structured and
// fallback values can retain those qualifiers. The result can be empty, and
// callers then apply their class-specific missing-model handling.
func ExtractGrokFailureModel(text string, responseBody []byte, fallback string) string {
	if m := ReGrokModelFor.FindStringSubmatch(text); len(m) == 2 {
		return NormalizeGrokFailureModelID(m[1])
	}
	if len(responseBody) > 0 {
		if m := strings.TrimSpace(firstNonEmpty(
			gjson.GetBytes(responseBody, "error.model").String(),
			gjson.GetBytes(responseBody, "model").String(),
		)); m != "" {
			return NormalizeGrokFailureModelID(m)
		}
	}
	return NormalizeGrokFailureModelID(fallback)
}

// NormalizeGrokFailureModelID trims surrounding whitespace and trailing message
// punctuation from model text.
func NormalizeGrokFailureModelID(model string) string {
	model = strings.TrimSpace(model)
	model = strings.TrimRight(model, ".,;:!?")
	return strings.TrimSpace(model)
}
