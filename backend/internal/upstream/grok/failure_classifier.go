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

const (
	GrokFailureNone          GrokUpstreamFailureClass = ""
	GrokFailureFreeUsage     GrokUpstreamFailureClass = "subscription:free-usage-exhausted"
	GrokFailureBilling       GrokUpstreamFailureClass = "billing_quota"
	GrokFailureEmptyUpstream GrokUpstreamFailureClass = "empty_upstream"
	GrokFailureModelCapacity GrokUpstreamFailureClass = "model_capacity"
	GrokFailureRateLimit     GrokUpstreamFailureClass = "rate_limit"
	GrokFailureAuth          GrokUpstreamFailureClass = "auth_error"
	GrokFailureServer        GrokUpstreamFailureClass = "server_error"
	// GrokFailureCompatibility identifies replay or body formats the selected upstream
	// cannot process. The caller can try another provider without cooling down
	// credentials for a request compatibility failure.
	GrokFailureCompatibility GrokUpstreamFailureClass = "compatibility_error"

	GrokFreeUsageProbeCooldown = 10 * time.Minute
)

var (
	ReGrokTokenPair = regexp.MustCompile(`(?i)tokens?\s*(?:\(actual\s*/\s*limit\))?\s*[:=]?\s*(\d+)\s*/\s*(\d+)`)
	ReGrokModelFor  = regexp.MustCompile(`(?i)(?:for\s+model|model|模型)\s*[:：]?\s*([a-z0-9][a-z0-9._-]{2,80})`)
)

// GrokUpstreamFailureClass determines cooldowns and failover before response commitment.
// Body matching recognizes quota and empty-output errors even when proxies rewrite status codes.
type GrokUpstreamFailureClass string

// GrokUpstreamFailureDecision describes the state changes for the caller to apply.
type GrokUpstreamFailureDecision struct {
	Class          GrokUpstreamFailureClass
	Model          string
	Cooldown       time.Duration
	ShouldCooldown bool
	// ShouldFailover requests another provider before finalizing the response. Content policy is
	// handled separately.
	ShouldFailover bool
	// BlockModel scopes empty-output cooldowns to a known model; free-usage exhaustion cools the provider.
	BlockModel   bool
	Reason       string
	TokensActual *int64
	TokensLimit  *int64
}

// ClassifyGrokUpstreamFailure prioritizes free usage, billing, compatibility, empty output,
// capacity, rate limits, and 5xx errors. Ordinary validation failures have no cooldown.
// Callers must exclude content-policy 403 responses before classification.
func ClassifyGrokUpstreamFailure(statusCode int, responseBody []byte, requestedModel string) GrokUpstreamFailureDecision {
	text, code, low := GrokUpstreamErrorCorpus(statusCode, responseBody)
	model := ExtractGrokFailureModel(text, responseBody, requestedModel)
	actual, limit, hasTokens := ParseGrokTokenPair(text)
	if !hasTokens {
		actual, limit, hasTokens = ParseGrokTokenPair(string(responseBody))
	}

	// Free or rolling usage exhaustion takes precedence over the HTTP status.
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

	// Billing exhaustion uses a 30-minute cooldown, including HTTP 402 responses.
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

	// Replay and compaction formats may fail on a particular Grok deployment.
	// Classify recognized decoding errors before empty-output failures so retries
	// do not persist a provider cooldown for request compatibility problems.
	if IsGrokCompatibilityError(statusCode, low, code) {
		return GrokUpstreamFailureDecision{
			Class: GrokFailureCompatibility,

			Model: model,

			ShouldFailover: true,

			ShouldCooldown: false,

			Reason: firstNonEmpty(text, "grok response compatibility error"),
		}
	}

	// Empty HTTP 200 / empty model output (often rewritten to synthetic 502).
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

	// Capacity failures indicate model overload.
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

	// Handle ordinary rate limits after excluding free-usage exhaustion.
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

	// Other 5xx errors get a short cooldown; synthetic empty-output errors were handled above.
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

func GrokUpstreamErrorCorpus(statusCode int, responseBody []byte) (text, code, low string) {
	_ = statusCode // The classifier handles transport status separately from the response text.
	raw := strings.TrimSpace(string(responseBody))
	// Unwrap the status prefix before matching quota and billing messages.
	if _, unwrappedBody, ok := UnwrapGrokUpstreamErrorText(raw); ok {
		raw = unwrappedBody
	}
	text = raw
	codeFromJSON, msgFromJSON := ParseGrokUpstreamErrorJSON(raw)
	if msgFromJSON != "" {
		if text == "" || len(msgFromJSON) > len(text)/2 || LooksLikeGrokQuotaMessage(msgFromJSON) {
			text = msgFromJSON
		}
	}
	// Prefer structured fields in the original body when available.
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

func ParseGrokUpstreamErrorJSON(errText string) (code, message string) {
	text := strings.TrimSpace(errText)
	if text == "" || text[0] != '{' {
		return "", ""
	}
	var payload map[string]any
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

func LooksLikeGrokQuotaMessage(s string) bool {
	low := strings.ToLower(s)
	return strings.Contains(low, "quota") ||
		strings.Contains(low, "usage") ||
		strings.Contains(low, "credit") ||
		strings.Contains(low, "额度") ||
		strings.Contains(low, "free")
}

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

// ShouldMarkGrokTeamModelRateLimit controls the process-local sibling-provider
// overlay. Model-capacity responses are request pressure, not a team quota;
// marking them would hide healthy sibling credentials while the bounded
// same-provider retry is still in progress. Ordinary 429s and free-usage
// exhaustion mark the team quota overlay.
func ShouldMarkGrokTeamModelRateLimit(statusCode int, responseBody []byte) bool {
	decision := ClassifyGrokUpstreamFailure(statusCode, responseBody, "")
	if decision.Class == GrokFailureModelCapacity {
		return false
	}
	return statusCode == http.StatusTooManyRequests || decision.Class == GrokFailureFreeUsage
}

func IsGrokCompatibilityError(statusCode int, low, code string) bool {
	if statusCode != http.StatusBadRequest && statusCode != http.StatusUnprocessableEntity {
		return false
	}
	combined := strings.ToLower(strings.TrimSpace(low + " " + code))
	// Recognize compaction decoding failures returned as HTTP 400 or 422.
	// JSON decoder shape errors are also checked below.
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

func IsGrokModelCapacityText(low string) bool {
	return strings.Contains(low, "capacity") ||
		strings.Contains(low, "overloaded") ||
		strings.Contains(low, "server_busy") ||
		strings.Contains(low, "too many concurrent") ||
		strings.Contains(low, "engine_overloaded")
}

func IsGrokRateLimitText(low string) bool {
	return strings.Contains(low, "rate limit") ||
		strings.Contains(low, "rate_limit") ||
		strings.Contains(low, "too many requests") ||
		strings.Contains(low, "请求过于频繁") ||
		strings.Contains(low, "速率限制")
}

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

func GrokFreeUsageCooldownDuration(low string) time.Duration {
	// A rolling 24-hour usage window does not imply a 24-hour cooldown from observation.
	// Use a short probe interval when no absolute reset time is available.
	return GrokFreeUsageProbeCooldown
}

func ParseGrokTokenPair(errText string) (actual, limit int64, ok bool) {
	m := ReGrokTokenPair.FindStringSubmatch(errText)
	if len(m) != 3 {
		return 0, 0, false
	}
	a, errA := strconv.ParseInt(m[1], 10, 64)
	b, errB := strconv.ParseInt(m[2], 10, 64)
	if errA != nil || errB != nil {
		return 0, 0, false
	}
	return a, b, true
}

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

// NormalizeGrokFailureModelID removes surrounding whitespace and trailing punctuation.
func NormalizeGrokFailureModelID(model string) string {
	model = strings.TrimSpace(model)
	model = strings.TrimRight(model, ".,;:!?")
	return strings.TrimSpace(model)
}
