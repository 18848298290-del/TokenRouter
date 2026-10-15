package antigravity

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
)

// retry_credits.go classifies quota failures and retries eligible requests
// with enabled credits.
// Quota exhaustion can permit an enabled-credit retry, while a structured
// model rate limit follows provider wait or switch policy. Textual quota
// markers take precedence over structured rate-limit decoding, and capacity
// observations without a quota marker select the unknown category.
//
// The enabled-credit attempt changes only the retry body and delegates
// provider state updates to callbacks. Successful responses clear
// exhausted-credit state and transfer body ownership to the caller; failed
// responses pass to CreditsFailure for reading, closing, and classification.
// Exhaustion marking rejects transport/server failures and recognized
// structured limits before broad message matching, so callers apply it after
// an attempt that enabled credits.

// The category values direct 429 handling toward quota overages, structured
// model limiting, or ordinary failure processing.
const (
	Antigravity429Unknown Antigravity429Category = "unknown" // unclassified 429 failure body
	// parsed noncapacity model limit.
	Antigravity429RateLimited Antigravity429Category = "rate_limited"
	// textual free-quota exhaustion marker.
	Antigravity429QuotaExhausted Antigravity429Category = "quota_exhausted"
)

var (
	// Quota marker fragments select enabled-credit handling before structured
	// parsing; case folding allows uppercase supplier messages to match the same
	// configured fragments.
	antigravityQuotaExhaustedKeywords = []string{"quota_exhausted", "quota exhausted"}

	// Credit exhaustion fragments apply after transport/status and
	// structured-limit exclusions, including broad resource-exhausted wording
	// retained by the provider credit classifier.
	creditsExhaustedKeywords = []string{
		"google_one_ai",
		"insufficient credit",
		"insufficient credits",
		"not enough credit",
		"not enough credits",
		"credit exhausted",
		"credits exhausted",
		"credit balance",
		"minimumcreditamountforusage",
		"minimum credit amount for usage",
		"minimum credit",
		"resource has been exhausted",
	}
)

// Antigravity429Category names the local quota, rate-limit, or unknown
// classification of a 429 body.
type Antigravity429Category string

// CreditsOveragesRetryResult reports credit-path handling and an optional
// successful response for continued execution.
type CreditsOveragesRetryResult struct {
	// Handled reports whether the credit path took responsibility; true can
	// accompany a nil response after build or upstream failure.
	Handled bool
	// Resp transfers a successful response body to the caller for reading and
	// closing; nil leaves ordinary recovery to its owner.
	Resp *http.Response
}

// ClassifyAntigravity429 selects quota exhaustion, parsed model limiting, or
// unknown from body text. Empty bodies are unknown, and callers supply the
// relevant HTTP-status context separately.
//
// Case-folded quota keywords win before structured parsing, including when
// another detail describes a limit. Structured decoding selects rate_limited
// only for a recognized noncapacity result with required model metadata.
// Unrecognized, incomplete, or capacity-only RPC observations return unknown
// so this classifier does not turn shared capacity recovery into
// enabled-credit handling.
func ClassifyAntigravity429(body []byte) Antigravity429Category {
	if len(body) == 0 {
		return Antigravity429Unknown
	}
	lowerBody := strings.ToLower(string(body))
	for _, keyword := range antigravityQuotaExhaustedKeywords {
		if strings.Contains(lowerBody, keyword) {
			return Antigravity429QuotaExhausted
		}
	}
	// Use recognized noncapacity model limits for rate-limit handling; incomplete
	// metadata and capacity observations leave credit retry selection unknown.
	if info := ParseAntigravitySmartRetryInfo(body); info != nil && !info.IsModelCapacityExhausted {
		return Antigravity429RateLimited
	}
	return Antigravity429Unknown
}

// InjectEnabledCreditTypes replaces the enabledCreditTypes array with
// GOOGLE_ONE_AI in decoded JSON. Callers supply a nonnull object; invalid or
// unencodable input returns nil, and changed numbers use ordinary float64
// decoding.
func InjectEnabledCreditTypes(body []byte) []byte {
	// Reject malformed input as a nil conversion result so the retry adapter
	// keeps ordinary error handling instead of sending partially decoded data.
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	payload["enabledCreditTypes"] = []string{"GOOGLE_ONE_AI"}
	result, err := json.Marshal(payload)
	// A failed complete encoding also selects nil, leaving the ordinary recovery
	// path responsible for the already captured upstream failure.
	if err != nil {
		return nil
	}
	return result
}

// ShouldMarkCreditsExhausted reports whether an enabled-credit failure matches
// local exhaustion wording. Callers apply it after requesting credits; this
// helper supplies a classification and leaves provider state mutation to the
// caller.
//
// A transport error, absent response, server failure, or request timeout
// rejects marking before text inspection. Any recognized structured
// smart-retry observation also rejects marking, while incomplete metadata can
// leave parsing unrecognized. Remaining case-folded exhaustion fragments
// include broad resource-exhausted text; messages lacking those fragments
// return false.
func ShouldMarkCreditsExhausted(resp *http.Response, respBody []byte, reqErr error) bool {
	if reqErr != nil || resp == nil {
		return false
	}
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout {
		return false
	}
	// Reject recognized structured waits before broad exhaustion wording so a
	// model cooldown does not alone mark credits spent; incomplete RPC metadata
	// can still leave the body to the textual classifier.
	if info := ParseAntigravitySmartRetryInfo(respBody); info != nil {
		return false
	}
	bodyLower := strings.ToLower(string(respBody))
	for _, keyword := range creditsExhaustedKeywords {
		if strings.Contains(bodyLower, keyword) {
			return true
		}
	}
	return false
}

// AttemptCreditsOveragesRetry sends p.Body with enabled credits through the
// selected base URL. Callers supply the adapter credit/header/transport
// callbacks; waitDuration, originalStatusCode, and respBody are retained
// inputs unused by this implementation.
//
// Failed injection returns Handled=false so ordinary handling can continue.
// Once request construction starts, a build or upstream failure returns
// Handled=true without a response. CreditsFailure owns a failed response for
// reading and closing, while success clears credit state and returns an open
// body for the caller to consume and close.
func (s *RetryAdapter) AttemptCreditsOveragesRetry(
	p RetryInput,
	baseURL string,
	modelName string,
	waitDuration time.Duration,
	originalStatusCode int,
	respBody []byte,
) *CreditsOveragesRetryResult {
	// The credit attempt needs one complete converted body. A failed conversion
	// returns responsibility to ordinary retry handling before any credit request
	// starts.
	creditsBody := InjectEnabledCreditTypes(p.Body)
	if creditsBody == nil {
		return &CreditsOveragesRetryResult{Handled: false}
	}
	modelKey := s.Options.CreditsModel(modelName)
	logger.LegacyPrintf("service.antigravity_gateway", "%s status=429 credit_overages_retry model=%s provider=%d (injecting enabledCreditTypes)",
		p.Prefix, modelKey, p.ProviderID)

	creditsReq, err := NewAPIRequestWithURL(p.Ctx, baseURL, p.Action, p.AccessToken, creditsBody)
	if err != nil {
		logger.LegacyPrintf("service.antigravity_gateway", "%s credit_overages_failed model=%s provider=%d build_request_err=%v",
			p.Prefix, modelKey, p.ProviderID, err)
		return &CreditsOveragesRetryResult{Handled: true}
	}
	s.Options.ApplyHeaders(creditsReq)

	creditsResp, err := s.Options.Do(creditsReq)
	if err == nil && creditsResp != nil && creditsResp.StatusCode < 400 {
		// Clear exhausted-credit state after this accepted credit response, then
		// transfer its open body to the caller for protocol output and closing.
		s.Options.ClearCredits()
		logger.LegacyPrintf("service.antigravity_gateway", "%s status=%d credit_overages_success model=%s provider=%d",
			p.Prefix, creditsResp.StatusCode, modelKey, p.ProviderID)
		return &CreditsOveragesRetryResult{Handled: true, Resp: creditsResp}
	}

	// Transfer failed-response handling to the provider callback because it
	// captures bounded failure text, closes the response body, and decides
	// exhausted-credit state.
	s.Options.CreditsFailure(modelKey, creditsResp, err)
	return &CreditsOveragesRetryResult{Handled: true}
}
