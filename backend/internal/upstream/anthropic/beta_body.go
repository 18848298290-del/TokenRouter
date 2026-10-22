package anthropic

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
)

// beta_body.go removes request fields whose required capabilities are absent
// from the selected beta header.
// A final beta Header can omit capabilities that the client used in its request
// body. Context management, fast speed, and refusal fallback fields therefore
// need their own gate after Header selection, including any provider-specific
// override.
//
// The fallback list requires the server-side token, while its credit value
// accepts any one of three listed tokens. Independent gates delete only their
// corresponding fields, so retaining context management does not retain
// fallbacks. Successful edits accumulate as the next gate's input; logged
// deletion failures keep that field and allow later checks to run. The changed
// flag identifies successful work rather than complete removal of every
// unsupported field.

// AnthropicBetaContextManagementToken names the accepted capability used to
// retain context_management during final request-body sanitization.
const AnthropicBetaContextManagementToken = "context-management-2025-06-27"

// SanitizeAnthropicBodyForBetaTokens removes extension fields whose accepted
// beta is absent from the final header. Call it after token selection and
// provider overrides; context_management, fast speed, fallbacks, and
// fallback_credit_token each use their own gate.
//
// The credit field accepts any of its three listed tokens, while fallbacks
// requires server-side fallback. Updates accumulate in the returned body, and
// the boolean reports at least one successful deletion. Failed deletions are
// logged and keep the corresponding field, so callers cannot infer complete
// sanitization from a true flag.
func SanitizeAnthropicBodyForBetaTokens(body []byte, anthropicBetaHeader string) ([]byte, bool) {
	if len(body) == 0 {
		return body, false
	}
	// Each successful gated-field deletion becomes the next field's input. Logged
	// deletion failures retain that field while the remaining independent gates
	// can still run.
	updated := body
	changed := false
	if gjson.GetBytes(updated, "context_management").Exists() &&
		!AnthropicBetaTokensContains(anthropicBetaHeader, AnthropicBetaContextManagementToken) {
		if b, err := sjson.DeleteBytes(updated, "context_management"); err == nil {
			updated = b
			changed = true
		} else {
			logger.LegacyPrintf("service.gateway", "[INFO] [CtxMgmtSanitize] Failed to remove context_management: %v (body len=%d)", err, len(body))
		}
	}
	// Final beta selection controls the fast-speed extension independently of
	// context management, so filtering that capability also removes its body selector.
	if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(updated, "speed").String()), "fast") &&
		!AnthropicBetaTokensContains(anthropicBetaHeader, BetaFastMode) {
		if b, err := sjson.DeleteBytes(updated, "speed"); err == nil {
			updated = b
			changed = true
		} else {
			logger.LegacyPrintf("service.gateway", "[INFO] [FastModeSanitize] Failed to remove speed: %v (body len=%d)", err, len(body))
		}
	}
	// The fallback list and credit value have different accepted-token sets;
	// check each field separately so a credit beta cannot retain an unsupported
	// fallback list.
	if b, deleted := StripAnthropicBodyFieldUnlessBeta(updated, "fallbacks", anthropicBetaHeader, BetaServerSideFallback); deleted {
		updated, changed = b, true
	}
	if b, deleted := StripAnthropicBodyFieldUnlessBeta(updated, "fallback_credit_token", anthropicBetaHeader, BetaServerSideFallback, BetaFallbackCredit, BetaFallbackCreditLegacy); deleted {
		updated, changed = b, true
	}
	return updated, changed
}

// StripAnthropicBodyFieldUnlessBeta deletes field when none of its accepted
// tokens remains. It returns the supplied body and false for an absent field,
// retained token, or logged deletion failure.
func StripAnthropicBodyFieldUnlessBeta(body []byte, field, anthropicBetaHeader string, requiredTokens ...string) ([]byte, bool) {
	if !gjson.GetBytes(body, field).Exists() {
		return body, false
	}
	for _, token := range requiredTokens {
		if AnthropicBetaTokensContains(anthropicBetaHeader, token) {
			return body, false
		}
	}
	// A failed field edit returns false so the sanitizer does not claim a deletion
	// or replace its working bytes for this gate.
	b, err := sjson.DeleteBytes(body, field)
	if err != nil {
		logger.LegacyPrintf("service.gateway", "[INFO] [BetaFieldSanitize] Failed to remove %s: %v (body len=%d)", field, err, len(body))
		return body, false
	}
	return b, true
}

// AnthropicBetaTokensContains reports exact, case-sensitive membership after
// trimming each header part. Empty header or token input returns false, so an
// absent final beta cannot retain a gated field.
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
