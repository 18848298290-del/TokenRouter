package provider

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// CanRetryOpenAI429 checks eligibility for the same-provider recovery window; the gateway owns retries.
func CanRetryOpenAI429(state *provider.RuntimeBlockState, value *provider.Record, headers http.Header, body []byte) bool {
	if state == nil || value == nil || !value.IsOpenAIOAuthLike() || value.IsShadow() ||
		state.Blocked(value.ID, func() string { return provider.RefreshCredentialIdentity(value) }) {
		return false
	}
	disposition, _ := ClassifyOpenAI429(headers, body)
	if disposition != provider.OpenAI429Transient {
		return false
	}
	return state.RetryWindowActive(value.ID)
}

// ClassifyOpenAI429 checks exhausted quota windows, reset headers, and then the response body.
func ClassifyOpenAI429(headers http.Header, body []byte) (provider.OpenAI429Disposition, *time.Time) {
	if kind, reset := provider.OpenAIExhaustedWindow(openai.ParseCodexRateLimitHeaders(headers), time.Now); kind != provider.OpenAI429Transient {
		return kind, reset
	}
	if reset := provider.OpenAI429ResetTime(openai.ParseCodexRateLimitHeaders(headers), time.Now, slog.Info); reset != nil {
		return provider.OpenAI429QuotaReset, reset
	}
	if unix := openai.ParseUsageLimitResetTime(body, time.Now); unix != nil {
		reset := time.Unix(*unix, 0)
		return provider.OpenAI429QuotaReset, &reset
	}
	return provider.OpenAI429Transient, nil
}
