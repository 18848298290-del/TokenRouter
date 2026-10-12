package provider

import (
	"log/slog"
	"net/http"
	"testing"
	"time"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	openaiupstream "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

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

func TestCalculateOpenAI429ResetTime_NoCodexHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Set("content-type", "application/json")

	resetAt := providercore.OpenAI429ResetTime(openaiupstream.ParseCodexRateLimitHeaders(headers), time.Now, func(string, ...any) {})

	if resetAt != nil {
		t.Errorf("expected nil resetAt when no codex headers, got %v", resetAt)
	}
}

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
	actualDays := duration.Hours() / 24.0

	if actualDays < 4.4 || actualDays > 4.5 {
		t.Errorf("expected ~4.45 days, got %.2f days", actualDays)
	}

	t.Logf("User scenario: reset_at=%v, duration=%.2f days", resetAt, actualDays)
}

func TestCalculateOpenAI429ResetTime_5MinFallbackWhenNoReset(t *testing.T) {
	// Usage percentages alone cannot provide a reset time.

	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "100")

	resetAt := providercore.OpenAI429ResetTime(openaiupstream.ParseCodexRateLimitHeaders(headers), time.Now, func(string, ...any) {})

	if resetAt != nil {
		t.Errorf("expected nil when no reset_after_seconds, got %v", resetAt)
	}
}

func TestHandle429_AnthropicPlatformUnaffected(t *testing.T) {
	// Anthropic reset headers are ignored by the Codex header parser.

	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-reset", "1737820800") // Unix seconds.

	resetAt := providercore.OpenAI429ResetTime(openaiupstream.ParseCodexRateLimitHeaders(headers), time.Now, slog.Info)

	if resetAt != nil {
		t.Errorf("expected nil for Anthropic headers, got %v", resetAt)
	}
}
