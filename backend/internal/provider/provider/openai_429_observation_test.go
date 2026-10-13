package provider

import (
	"log/slog"
	"net/http"
	"testing"
	"time"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	openaiupstream "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// TestCalculateOpenAI429ResetTime_7dExhausted supplies an exhausted weekly
// window and lightly used short window. The selected reset must fall within the
// weekly interval measured around the call.
func TestCalculateOpenAI429ResetTime_7dExhausted(t *testing.T) {
	// Primary = 7d (10080 minutes), Secondary = 5h (300 minutes)
	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "100")
	headers.Set("x-codex-primary-reset-after-seconds", "384607") // ~4.5 days
	headers.Set("x-codex-primary-window-minutes", "10080")       // 7 days
	headers.Set("x-codex-secondary-used-percent", "3")
	headers.Set("x-codex-secondary-reset-after-seconds", "17369") // ~4.8 hours
	headers.Set("x-codex-secondary-window-minutes", "300")        // 5 hours

	before := time.Now()
	resetAt := providercore.OpenAI429ResetTime(openaiupstream.ParseCodexRateLimitHeaders(headers), time.Now, func(string, ...any) {})
	after := time.Now()

	if resetAt == nil {
		t.Fatal("expected non-nil resetAt")
	}

	expectedDuration := 384607 * time.Second
	minExpected := before.Add(expectedDuration)
	maxExpected := after.Add(expectedDuration)

	if resetAt.Before(minExpected) || resetAt.After(maxExpected) {
		t.Errorf("resetAt %v not in expected range [%v, %v]", resetAt, minExpected, maxExpected)
	}
}

// TestCalculateOpenAI429ResetTime_5hExhausted supplies an exhausted short
// window with a later nonexhausted weekly reset. The selected deadline must
// follow the exhausted five-hour window's reported reset.
func TestCalculateOpenAI429ResetTime_5hExhausted(t *testing.T) {
	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "50")
	headers.Set("x-codex-primary-reset-after-seconds", "500000")
	headers.Set("x-codex-primary-window-minutes", "10080") // 7 days
	headers.Set("x-codex-secondary-used-percent", "100")
	headers.Set("x-codex-secondary-reset-after-seconds", "3600") // 1 hour
	headers.Set("x-codex-secondary-window-minutes", "300")       // 5 hours

	before := time.Now()
	resetAt := providercore.OpenAI429ResetTime(openaiupstream.ParseCodexRateLimitHeaders(headers), time.Now, func(string, ...any) {})
	after := time.Now()

	if resetAt == nil {
		t.Fatal("expected non-nil resetAt")
	}

	expectedDuration := 3600 * time.Second
	minExpected := before.Add(expectedDuration)
	maxExpected := after.Add(expectedDuration)

	if resetAt.Before(minExpected) || resetAt.After(maxExpected) {
		t.Errorf("resetAt %v not in expected range [%v, %v]", resetAt, minExpected, maxExpected)
	}
}

// TestCalculateOpenAI429ResetTime_NeitherExhausted_UsesMax supplies reset
// intervals for two nonexhausted windows. The reset calculation must choose the
// later supplied deadline when neither utilization reaches exhaustion.
func TestCalculateOpenAI429ResetTime_NeitherExhausted_UsesMax(t *testing.T) {
	// Neither window is exhausted, so use the later reset.
	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "80")
	headers.Set("x-codex-primary-reset-after-seconds", "100000")
	headers.Set("x-codex-primary-window-minutes", "10080")
	headers.Set("x-codex-secondary-used-percent", "90")
	headers.Set("x-codex-secondary-reset-after-seconds", "5000")
	headers.Set("x-codex-secondary-window-minutes", "300")

	before := time.Now()
	resetAt := providercore.OpenAI429ResetTime(openaiupstream.ParseCodexRateLimitHeaders(headers), time.Now, func(string, ...any) {})
	after := time.Now()

	if resetAt == nil {
		t.Fatal("expected non-nil resetAt")
	}

	expectedDuration := 100000 * time.Second
	minExpected := before.Add(expectedDuration)
	maxExpected := after.Add(expectedDuration)

	if resetAt.Before(minExpected) || resetAt.After(maxExpected) {
		t.Errorf("resetAt %v not in expected range [%v, %v]", resetAt, minExpected, maxExpected)
	}
}

// TestCalculateOpenAI429ResetTime_NoCodexHeaders supplies unrelated response
// headers with no Codex measurements. The reset calculation must return nil so
// callers retain their separate no-reset handling.
func TestCalculateOpenAI429ResetTime_NoCodexHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Set("content-type", "application/json")

	resetAt := providercore.OpenAI429ResetTime(openaiupstream.ParseCodexRateLimitHeaders(headers), time.Now, func(string, ...any) {})

	if resetAt != nil {
		t.Errorf("expected nil resetAt when no codex headers, got %v", resetAt)
	}
}

// TestCalculateOpenAI429ResetTime_ReversedWindowOrder places the five-hour
// window in primary and the weekly window in secondary. Exhaustion must follow
// the duration-associated window and select its shorter reset.
func TestCalculateOpenAI429ResetTime_ReversedWindowOrder(t *testing.T) {
	// Window identity follows duration even when primary and secondary are reversed.
	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "100")
	headers.Set("x-codex-primary-reset-after-seconds", "3600") // 1 hour
	headers.Set("x-codex-primary-window-minutes", "300")       // 5 hours
	headers.Set("x-codex-secondary-used-percent", "50")
	headers.Set("x-codex-secondary-reset-after-seconds", "500000")
	headers.Set("x-codex-secondary-window-minutes", "10080") // 7 days

	before := time.Now()
	resetAt := providercore.OpenAI429ResetTime(openaiupstream.ParseCodexRateLimitHeaders(headers), time.Now, func(string, ...any) {})
	after := time.Now()

	if resetAt == nil {
		t.Fatal("expected non-nil resetAt")
	}

	expectedDuration := 3600 * time.Second
	minExpected := before.Add(expectedDuration)
	maxExpected := after.Add(expectedDuration)

	if resetAt.Before(minExpected) || resetAt.After(maxExpected) {
		t.Errorf("resetAt %v not in expected range [%v, %v]", resetAt, minExpected, maxExpected)
	}
}

// TestCalculateOpenAI429ResetTime_UserProvidedScenario reproduces exhausted
// weekly quota with three-percent short-window use. The deadline must follow
// the weekly reset and retain its approximately four-and-a-half-day duration.
func TestCalculateOpenAI429ResetTime_UserProvidedScenario(t *testing.T) {
	// The exhausted weekly window determines the reset even when five-hour usage is low.

	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "100")
	headers.Set("x-codex-primary-reset-after-seconds", "384607")
	headers.Set("x-codex-primary-window-minutes", "10080") // 7 days = 10080 minutes
	headers.Set("x-codex-secondary-used-percent", "3")
	headers.Set("x-codex-secondary-reset-after-seconds", "17369")
	headers.Set("x-codex-secondary-window-minutes", "300") // 5 hours = 300 minutes

	before := time.Now()
	resetAt := providercore.OpenAI429ResetTime(openaiupstream.ParseCodexRateLimitHeaders(headers), time.Now, func(string, ...any) {})
	after := time.Now()

	if resetAt == nil {
		t.Fatal("expected non-nil resetAt for user scenario")
	}

	expectedDuration := 384607 * time.Second
	minExpected := before.Add(expectedDuration)
	maxExpected := after.Add(expectedDuration)

	if resetAt.Before(minExpected) || resetAt.After(maxExpected) {
		t.Errorf("resetAt %v not in expected range [%v, %v]", resetAt, minExpected, maxExpected)
	}

	duration := resetAt.Sub(before)
	// Convert elapsed hours to days for the reported weekly-reset duration.
	actualDays := duration.Hours() / 24.0

	// The supplied weekly reset is about 4.45 days.
	if actualDays < 4.4 || actualDays > 4.5 {
		t.Errorf("expected ~4.45 days, got %.2f days", actualDays)
	}

	t.Logf("User scenario: reset_at=%v, duration=%.2f days", resetAt, actualDays)
}

// TestCalculateOpenAI429ResetTime_5MinFallbackWhenNoReset provides an exhausted
// utilization percentage without a reset interval. The calculation must return
// nil, leaving any fallback cooldown choice to its caller.
func TestCalculateOpenAI429ResetTime_5MinFallbackWhenNoReset(t *testing.T) {
	// Usage percentages alone cannot provide a reset time.

	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "100")

	resetAt := providercore.OpenAI429ResetTime(openaiupstream.ParseCodexRateLimitHeaders(headers), time.Now, func(string, ...any) {})

	if resetAt != nil {
		t.Errorf("expected nil when no reset_after_seconds, got %v", resetAt)
	}
}

// TestHandle429_AnthropicPlatformUnaffected provides an Anthropic reset header
// to the Codex parser. The OpenAI reset calculation must return nil because no
// Codex window was observed.
func TestHandle429_AnthropicPlatformUnaffected(t *testing.T) {
	// Anthropic reset headers are ignored by the Codex header parser.

	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-reset", "1737820800") // Unix seconds.

	resetAt := providercore.OpenAI429ResetTime(openaiupstream.ParseCodexRateLimitHeaders(headers), time.Now, slog.Info)

	if resetAt != nil {
		t.Errorf("expected nil for Anthropic headers, got %v", resetAt)
	}
}
