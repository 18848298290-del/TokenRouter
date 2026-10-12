package antigravity

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
)

const (
	Antigravity429Unknown        Antigravity429Category = "unknown"
	Antigravity429RateLimited    Antigravity429Category = "rate_limited"
	Antigravity429QuotaExhausted Antigravity429Category = "quota_exhausted"
)

var (
	antigravityQuotaExhaustedKeywords = []string{"quota_exhausted", "quota exhausted"}

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

type Antigravity429Category string

type CreditsOveragesRetryResult struct {
	Handled bool
	Resp    *http.Response
}

// ClassifyAntigravity429 distinguishes quota exhaustion, rate limiting, and unknown errors.
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
	if info := ParseAntigravitySmartRetryInfo(body); info != nil && !info.IsModelCapacityExhausted {
		return Antigravity429RateLimited
	}
	return Antigravity429Unknown
}

// InjectEnabledCreditTypes enables GOOGLE_ONE_AI credits in a v1internal JSON body.
func InjectEnabledCreditTypes(body []byte) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	payload["enabledCreditTypes"] = []string{"GOOGLE_ONE_AI"}
	result, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return result
}

// ShouldMarkCreditsExhausted classifies a failed request that already enabled credits.
func ShouldMarkCreditsExhausted(resp *http.Response, respBody []byte, reqErr error) bool {
	if reqErr != nil || resp == nil {
		return false
	}
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout {
		return false
	}
	// Network failures, server errors, and parsed rate limits do not prove credit exhaustion.
	// A remaining exhaustion message after enabledCreditTypes is injected marks credits spent.
	// A later successful credits request clears that state.
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

// AttemptCreditsOveragesRetry retries with AI Credits after free-quota exhaustion.
func (s *RetryAdapter) AttemptCreditsOveragesRetry(
	p RetryInput,
	baseURL string,
	modelName string,
	waitDuration time.Duration,
	originalStatusCode int,
	respBody []byte,
) *CreditsOveragesRetryResult {
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
		s.Options.ClearCredits()
		logger.LegacyPrintf("service.antigravity_gateway", "%s status=%d credit_overages_success model=%s provider=%d",
			p.Prefix, creditsResp.StatusCode, modelKey, p.ProviderID)
		return &CreditsOveragesRetryResult{Handled: true, Resp: creditsResp}
	}

	s.Options.CreditsFailure(modelKey, creditsResp, err)
	return &CreditsOveragesRetryResult{Handled: true}
}
