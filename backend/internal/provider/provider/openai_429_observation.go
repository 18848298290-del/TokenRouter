package provider

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// A 429 can represent an exhausted quota window, a reset-bearing response, or a
// transient failure. Quota classification takes precedence over retry
// admission: exhausted Codex windows are checked first, followed by other Codex
// reset headers and then the response body.
//
// Same-provider recovery is available for nonshadow OpenAI OAuth-like records
// only when runtime state permits scheduling and no quota signal has won.
// Checking retry eligibility can create the runtime recovery window on first
// observation, so it is a stateful admission check. The gateway controls actual
// retry attempts and uses the resulting window deadline to bound them.

// CanRetryOpenAI429 checks same-provider recovery eligibility for a transient
// OpenAI OAuth-like failure. Nil state, unsupported records, shadows, and
// active scheduling blocks reject recovery; an eligible check can start the
// runtime retry window.
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

// ClassifyOpenAI429 returns quota exhaustion or a reset time before classifying
// a response as transient. It first uses exhausted Codex windows, then any
// reset selected from their headers, and finally the usage-limit body.
//
// An exhausted window can return its quota disposition with a nil time when
// utilization is present but its reset is absent. Only responses without an
// exhausted window or a usable header/body reset become transient and permit
// consideration of same-provider recovery. Header-derived quota signals win
// over conflicting body information, and each delegated parser receives the
// current clock for relative reset calculations.
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
