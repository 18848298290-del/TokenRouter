package anthropic

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
)

// AnthropicBetaContextManagementToken enables the context_management body field.
const AnthropicBetaContextManagementToken = "context-management-2025-06-27"

// SanitizeAnthropicBodyForBetaTokens removes fields unsupported by the final beta header.
// Call it before building the upstream request, after selecting and filtering betas.
//
// context_management requires the context-management beta; speed=fast requires
// the fast-mode beta. Client fallbacks require the server-side-fallback beta, while
// fallback_credit_token also accepts either credit beta. OAuth mimic can replace
// client betas, leaving body fields that the upstream schema would reject.
//
// The boolean reports a successful deletion. Failed deletions are logged and leave
// the affected field unchanged.
func SanitizeAnthropicBodyForBetaTokens(body []byte, anthropicBetaHeader string) ([]byte, bool) {
	if len(body) == 0 {
		return body, false
	}
	updated := body
	changed := false
	if gjson.GetBytes(updated, "context_management").Exists() &&
		!AnthropicBetaTokensContains(anthropicBetaHeader, AnthropicBetaContextManagementToken) {
		if b, err := sjson.DeleteBytes(updated, "context_management"); err == nil {
			updated = b
			changed = true
		} else {
			logger.LegacyPrintf("service.gateway", "[CtxMgmtSanitize] 删除 context_management 失败: %v (body len=%d)", err, len(body))
		}
	}
	// Remove fast speed when its required beta was filtered out.
	if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(updated, "speed").String()), "fast") &&
		!AnthropicBetaTokensContains(anthropicBetaHeader, BetaFastMode) {
		if b, err := sjson.DeleteBytes(updated, "speed"); err == nil {
			updated = b
			changed = true
		} else {
			logger.LegacyPrintf("service.gateway", "[FastModeSanitize] 删除 speed 失败: %v (body len=%d)", err, len(body))
		}
	}
	// Remove refusal fallback fields unless an accepted beta remains.
	if b, deleted := StripAnthropicBodyFieldUnlessBeta(updated, "fallbacks", anthropicBetaHeader, BetaServerSideFallback); deleted {
		updated, changed = b, true
	}
	if b, deleted := StripAnthropicBodyFieldUnlessBeta(updated, "fallback_credit_token", anthropicBetaHeader, BetaServerSideFallback, BetaFallbackCredit, BetaFallbackCreditLegacy); deleted {
		updated, changed = b, true
	}
	return updated, changed
}

// StripAnthropicBodyFieldUnlessBeta removes field when none of requiredTokens is present.
func StripAnthropicBodyFieldUnlessBeta(body []byte, field, anthropicBetaHeader string, requiredTokens ...string) ([]byte, bool) {
	if !gjson.GetBytes(body, field).Exists() {
		return body, false
	}
	for _, token := range requiredTokens {
		if AnthropicBetaTokensContains(anthropicBetaHeader, token) {
			return body, false
		}
	}
	b, err := sjson.DeleteBytes(body, field)
	if err != nil {
		logger.LegacyPrintf("service.gateway", "[BetaFieldSanitize] 删除 %s 失败: %v (body len=%d)", field, err, len(body))
		return body, false
	}
	return b, true
}

// AnthropicBetaTokensContains matches a complete, case-sensitive token after trimming whitespace.
func AnthropicBetaTokensContains(header, token string) bool {
	if header == "" || token == "" {
		return false
	}
	for part := range strings.SplitSeq(header, ",") {
		if strings.TrimSpace(part) == token {
			return true
		}
	}
	return false
}
